// Package filelock is tycswap's own cross-process advisory lock.
//
// Implements spec 03§7 (locking.py). POSIX uses flock(LOCK_EX|LOCK_NB) with a
// 0.1s poll and a monotonic timeout (default 10s); Windows uses LockFileEx on a
// single byte. The lock is NON-REENTRANT — callers never nest it: a goroutine
// asking for a file it holds is refused at once, and code that runs under a
// held lock takes the lock it lends (Lend) instead (DESIGN A118). A failed
// With acquisition raises cerr.Lock (Python LockError).
package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/locknest"
)

const pollInterval = 100 * time.Millisecond

// DefaultTimeout matches Python FileLock(timeout=10.0).
const DefaultTimeout = 10 * time.Second

type FileLock struct {
	path    string
	timeout time.Duration
	hold    sync.Mutex
	mu      sync.Mutex
	file    *os.File
	locked  bool
	unhold  func()
	// lender holds the file for this lock while it lends it (Lend).
	lender atomic.Pointer[FileLock]
}

// New returns a FileLock for path. A non-positive timeout uses DefaultTimeout.
func New(path string, timeout time.Duration) *FileLock {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &FileLock{path: path, timeout: timeout}
}

// Acquire tries to take the lock, waiting up to timeout (its own timeout when
// <= 0). It returns false on timeout rather than an error, matching Python,
// and at once when the calling goroutine holds the file: that goroutine is
// the holder the wait would wait for (DESIGN A118).
func (l *FileLock) Acquire(timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		timeout = l.timeout
	}
	site := locknest.Here()
	if l.lender.Load() != nil {
		if ok, lent := l.acquireLent(site); lent {
			return ok, nil
		}
	}
	if locknest.Nested(l, l.path, nil, site) {
		return false, nil
	}
	// Serialize in-process holders: hold stays locked until Release, so a
	// concurrent caller waits here instead of clobbering the file descriptor.
	// It is unlocked here on any failure and by Release on success.
	l.hold.Lock()

	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		l.hold.Unlock()
		return false, err
	}
	// No O_TRUNC: the lock file carries no data, and truncating would let a
	// symlink planted at the lock path truncate whatever it points to.
	// noFollow refuses such a symlink outright (POSIX).
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|noFollow, 0o600)
	if err != nil {
		l.hold.Unlock()
		return false, err
	}
	info, _ := f.Stat()
	if locknest.Nested(nil, "", info, site) {
		f.Close()
		l.hold.Unlock()
		return false, nil
	}

	start := time.Now()
	for {
		ok, err := tryLock(f)
		if err != nil {
			f.Close()
			l.hold.Unlock()
			return false, err
		}
		if ok {
			l.mu.Lock()
			l.file = f
			l.locked = true
			l.unhold = locknest.Hold(l, l.path, info, site)
			l.mu.Unlock()
			return true, nil
		}
		if time.Since(start) > timeout {
			f.Close()
			l.hold.Unlock()
			return false, nil
		}
		time.Sleep(pollInterval)
	}
}

// acquireLent takes l while it is lent: at once, one holder at a time, asking
// the file nothing. lent is false once the lending has ended.
func (l *FileLock) acquireLent(site locknest.Site) (ok, lent bool) {
	if locknest.Nested(l, "", nil, site) {
		return false, true
	}
	l.hold.Lock()
	if l.lender.Load() == nil {
		l.hold.Unlock()
		return false, false
	}
	l.mu.Lock()
	l.locked = true
	l.unhold = locknest.Hold(l, "", nil, site)
	l.mu.Unlock()
	return true, true
}

// Lend runs fn with a lock for l's file that what fn calls takes in place of
// the file while l, which the caller holds, holds it: its Acquire succeeds at
// once, one holder at a time, and asks the file nothing. Once fn has returned
// it is an ordinary lock again, which takes the file (DESIGN A118).
func (l *FileLock) Lend(fn func(lent *FileLock) error) error {
	if !l.Held() {
		return errors.New("filelock: only a held lock can be lent")
	}
	lent := New(l.path, l.timeout)
	lent.lender.Store(l)
	defer func() {
		// A holder of the lent lock finishes before l stops lending it.
		lent.hold.Lock()
		lent.lender.Store(nil)
		lent.hold.Unlock()
	}()
	return fn(lent)
}

// Release drops the lock. It is safe to call more than once.
func (l *FileLock) Release() error {
	l.mu.Lock()
	f := l.file
	locked := l.locked
	unhold := l.unhold
	l.file = nil
	l.locked = false
	l.unhold = nil
	l.mu.Unlock()
	if !locked {
		// Nothing held: either never acquired or already released. Do not
		// touch hold (a failed Acquire / prior Release already unlocked it).
		return nil
	}
	unhold()
	var err error
	if f != nil { // a lent lock has no file: its lender holds it
		unlock(f)
		err = f.Close()
	}
	l.hold.Unlock()
	return err
}

// Held reports whether this process holds the lock, to keep network calls out from under it.
func (l *FileLock) Held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.locked
}

// With acquires the lock (default timeout), runs fn, then releases. If the lock
// cannot be acquired it returns cerr.Lock without running fn.
func (l *FileLock) With(fn func() error) error { return l.WithWait(l.timeout, fn) }

// WithWait is With waiting up to timeout (the lock's own when <= 0).
func (l *FileLock) WithWait(timeout time.Duration, fn func() error) error {
	ok, err := l.Acquire(timeout)
	if err != nil {
		return err
	}
	if !ok {
		return cerr.Lock("Failed to acquire lock - another instance may be running")
	}
	defer l.Release()
	return fn()
}
