// Package cclock is the proper-lockfile interop layer: tycswap holds Claude
// Code's OWN advisory locks while mutating its files, closing the token-refresh
// race with a running Claude Code.
//
// Implements spec 03§6 (claude_locks.py) and DESIGN §2.7, §4 row 1. The protocol
// (npm proper-lockfile) is external contract and must match byte-for-byte: the
// lock is a directory at <target>.lock whose mkdir atomicity is the mutex; a
// lock is stale when its mtime is older than the staleness Claude Code gives
// that lock, 10s unless it says otherwise (compared against the WALL clock,
// DESIGN A55); a live holder touches the mtime every 3s to prove liveness; the
// acquire timeout is measured MONOTONICALLY; a stale lock is removed and taken
// over; and the acquire backoff is jittered uniformly in [0.25, 0.50)s.
package cclock

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
)

// Timing constants, matching claude_locks.py verbatim. proper-lockfile defaults
// Claude Code runs with: stale after 10s, holder touches every stale/2 = 5s;
// tycswap touches faster (3s) for margin. 9s of bounded waiting comfortably
// outlasts a sub-second-to-few-second credential/config hold.
const (
	// StalenessS is the age past which a held lock is considered stale.
	StalenessS = 10 * time.Second
	// TouchIntervalS is how often the holder bumps the lock dir's mtime.
	TouchIntervalS = 3 * time.Second
	// DefaultTimeoutS is the default maximum wait to acquire.
	DefaultTimeoutS = 9 * time.Second
	// CredentialsStalenessS and StorageWriteStalenessS are the staleness Claude
	// Code gives its credential-refresh lock (60s) and its storage-write lock
	// (15s). Taking either over after StalenessS would take a lock Claude Code
	// may still hold (DESIGN A55).
	CredentialsStalenessS  = 60 * time.Second
	StorageWriteStalenessS = 15 * time.Second
)

// CredentialsLockDir returns Claude Code's credential-refresh lock directory,
// <secure storage home>.lock (default ~/.claude.lock, honoring
// CLAUDE_CONFIG_DIR and CLAUDE_SECURESTORAGE_CONFIG_DIR like
// StorageWriteLockDir, DESIGN A55).
func CredentialsLockDir() string {
	home := paths.GetSecureStorageHome()
	return filepath.Join(filepath.Dir(home), filepath.Base(home)+".lock")
}

// ConfigLockDir returns Claude Code's global-config write lock directory,
// <global_config>.lock (default ~/.claude.json.lock, honoring CLAUDE_CONFIG_DIR
// and the legacy .config.json resolution).
func ConfigLockDir() string {
	path := paths.GetGlobalConfigPath()
	return filepath.Join(filepath.Dir(path), filepath.Base(path)+".lock")
}

// StorageWriteLockDir returns the lock directory under which Claude Code
// read-modify-writes its credential store (the credentials file or the macOS
// Keychain item): <secure storage home>/.storage-write.lock (DESIGN A55).
func StorageWriteLockDir() string {
	return filepath.Join(paths.GetSecureStorageHome(), ".storage-write.lock")
}

// Handle is a held proper-lockfile lock. Release stops its toucher goroutine and
// removes the lock directory.
type Handle struct {
	dir  string
	clk  clock.Clock
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Acquire takes the proper-lockfile-compatible directory lock at lockDir,
// blocking up to timeout (DefaultTimeoutS when non-positive), taking over locks
// whose mtime is older than StalenessS, and starting a toucher goroutine that
// keeps the mtime fresh while held. The staleness check uses clk (the wall
// clock); the timeout is measured monotonically via time.Since.
//
// It returns a *cerr.Error of kind ClaudeCodeLockTimeout if the lock stays held
// past timeout; nothing is mutated in that case, so the operation is safe to
// retry.
func Acquire(lockDir string, timeout time.Duration, clk clock.Clock) (*Handle, error) {
	return acquire(lockDir, StalenessS, timeout, clk)
}

// AcquireCredentials takes Claude Code's credential-refresh lock,
// CredentialsLockDir, stale only after CredentialsStalenessS.
func AcquireCredentials(timeout time.Duration, clk clock.Clock) (*Handle, error) {
	return acquire(CredentialsLockDir(), CredentialsStalenessS, timeout, clk)
}

// AcquireStorageWrite takes Claude Code's storage-write lock,
// StorageWriteLockDir, stale only after StorageWriteStalenessS.
func AcquireStorageWrite(timeout time.Duration, clk clock.Clock) (*Handle, error) {
	return acquire(StorageWriteLockDir(), StorageWriteStalenessS, timeout, clk)
}

// acquire is Acquire with the lock's own staleness.
func acquire(lockDir string, staleness, timeout time.Duration, clk clock.Clock) (*Handle, error) {
	if timeout <= 0 {
		timeout = DefaultTimeoutS
	}
	// proper-lockfile: lock_dir.parent.mkdir(parents=True, exist_ok=True).
	if err := os.MkdirAll(filepath.Dir(lockDir), 0o700); err != nil {
		return nil, err
	}

	start := time.Now() // monotonic reading embedded; time.Since below is monotonic.
	for {
		err := os.Mkdir(lockDir, 0o700)
		if err == nil {
			break // acquired
		}
		if !errors.Is(err, fs.ErrExist) && !pendingDelete(err) {
			// Python catches only FileExistsError; any other mkdir error propagates.
			return nil, err
		}
		if time.Since(start) > timeout {
			return nil, cerr.ClaudeCodeLockTimeout(
				"Could not acquire %s — Claude Code appears to be refreshing credentials. "+
					"Retry in a few seconds.", filepath.Base(lockDir))
		}
		fi, statErr := os.Stat(lockDir)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				continue // holder released between mkdir and stat; retry now
			}
			if pendingDelete(statErr) {
				time.Sleep(jitterBackoff())
				continue
			}
			return nil, statErr
		}
		if clk.Now().Sub(fi.ModTime()) > staleness {
			// Dead holder per the protocol: break it and retake.
			breakStale(lockDir, staleness, clk)
			continue
		}
		time.Sleep(jitterBackoff())
	}

	h := &Handle{
		dir:  lockDir,
		clk:  clk,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	heldMu.Lock()
	held[h] = struct{}{}
	heldMu.Unlock()
	go h.touchLoop()
	return h, nil
}

// held is every lock this process holds, for ReleaseAll.
var (
	heldMu sync.Mutex
	held   = map[*Handle]struct{}{}
)

// ReleaseAll releases every lock this process holds. Ctrl-C exits from the
// SIGINT goroutine past the holders' deferred releases, and Claude Code would
// wait out a directory left behind until its staleness (DESIGN A55).
func ReleaseAll() {
	heldMu.Lock()
	hs := make([]*Handle, 0, len(held))
	for h := range held {
		hs = append(hs, h)
	}
	heldMu.Unlock()
	for _, h := range hs {
		h.Release()
	}
}

// touchLoop bumps the lock dir's mtime every TouchIntervalS while held, stopping
// on the first Chtimes error (the lock was stolen/removed) or when stop closes.
func (h *Handle) touchLoop() {
	defer close(h.done)
	ticker := time.NewTicker(TouchIntervalS)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			now := h.clk.Now()
			if err := os.Chtimes(h.dir, now, now); err != nil {
				return // lock stolen/removed; nothing left to keep alive
			}
		}
	}
}

// Release stops the toucher (joining with a 1s bound) and removes the lock
// directory. It tolerates a lock that was taken over as stale (already removed
// or replaced): no error escapes.
func (h *Handle) Release() {
	h.once.Do(func() {
		heldMu.Lock()
		delete(held, h)
		heldMu.Unlock()
		close(h.stop)
		select {
		case <-h.done:
		case <-time.After(1 * time.Second):
		}
		// os.Remove of a vanished (FileNotFoundError) or unremovable (OSError)
		// lock is tolerated — Python logs a warning and swallows; we swallow.
		_ = os.Remove(h.dir)
	})
}

// jitterBackoff returns a uniform random duration in [0.25, 0.50)s, matching
// Python's 0.25 + random.random()*0.25.
func jitterBackoff() time.Duration {
	return time.Duration((0.25 + rand.Float64()*0.25) * float64(time.Second))
}

// breakStale removes a lock dir judged stale without ever removing a fresh one.
//
// A plain os.Remove is not safe when two waiters judge the same lock stale:
// the first removes it and takes a fresh lock, and the second's Remove then
// deletes that fresh lock, so both believe they hold it. Instead the stale dir
// is renamed to a name unique to this attempt, which only one waiter can do,
// and its mtime is checked again after the rename. Still stale: it is removed.
// Fresh: another waiter retook the lock between our stat and our rename, so
// it is put back where it was, by a rename that fails when the name has been
// taken since (renameNoReplace: rename(2) alone would replace an empty
// directory, that is, the lock a third waiter just made). Taken: it is
// dropped; the protocol has then moved on without it. staleness is the lock's
// own, the one the caller judged it by.
func breakStale(lockDir string, staleness time.Duration, clk clock.Clock) {
	aside := fmt.Sprintf("%s.stale-%d-%d", lockDir, os.Getpid(), staleSeq.Add(1))
	if err := os.Rename(lockDir, aside); err != nil {
		// Gone already (another waiter broke it) or not renamable: loop.
		time.Sleep(50 * time.Millisecond)
		return
	}
	fi, err := os.Stat(aside)
	if err == nil && clk.Now().Sub(fi.ModTime()) <= staleness {
		if renameNoReplace(aside, lockDir) == nil {
			return
		}
	}
	_ = os.Remove(aside)
}

// renameIfAbsent renames oldpath to newpath after checking that newpath does
// not exist. The two calls are not one operation: a name taken in between is
// replaced. It is the put-back where renameNoReplace has nothing better.
func renameIfAbsent(oldpath, newpath string) error {
	if _, err := os.Lstat(newpath); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(oldpath, newpath)
}

// staleSeq makes each breakStale attempt's aside name unique in the process.
var staleSeq atomic.Int64

// pendingDelete reports Windows' answer for a lock directory another process
// has just removed but whose delete is still pending: access denied, until the
// last handle closes. The lock is effectively held; retry like ErrExist.
func pendingDelete(err error) bool {
	return platform.IsWindows() && errors.Is(err, fs.ErrPermission)
}
