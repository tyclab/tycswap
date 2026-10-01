// Package cclock is the proper-lockfile interop layer: tycswap holds Claude
// Code's OWN advisory locks while mutating its files, closing the token-refresh
// race with a running Claude Code.
//
// Implements spec 03§6 (claude_locks.py) and DESIGN §2.7, §4 row 1. The protocol
// (npm proper-lockfile) is external contract and must match byte-for-byte: the
// lock is a directory at <target>.lock whose mkdir atomicity is the mutex; a
// lock is stale when its mtime is older than 10s (compared against the WALL
// clock); a live holder touches the mtime every 3s to prove liveness; the
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
)

// CredentialsLockDir returns Claude Code's credential-refresh lock directory,
// <config_home>.lock (default ~/.claude.lock, honoring CLAUDE_CONFIG_DIR).
func CredentialsLockDir() string {
	home := paths.GetClaudeConfigHome()
	return filepath.Join(filepath.Dir(home), filepath.Base(home)+".lock")
}

// ConfigLockDir returns Claude Code's global-config write lock directory,
// <global_config>.lock (default ~/.claude.json.lock, honoring CLAUDE_CONFIG_DIR
// and the legacy .config.json resolution).
func ConfigLockDir() string {
	path := paths.GetGlobalConfigPath()
	return filepath.Join(filepath.Dir(path), filepath.Base(path)+".lock")
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
		if !errors.Is(err, fs.ErrExist) {
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
			return nil, statErr
		}
		if clk.Now().Sub(fi.ModTime()) > StalenessS {
			// Dead holder per the protocol: break it and retake.
			breakStale(lockDir, clk)
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
	go h.touchLoop()
	return h, nil
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
// it is put back where it was (unless someone has taken the name since, in
// which case it is dropped; the protocol has then moved on without it).
func breakStale(lockDir string, clk clock.Clock) {
	aside := fmt.Sprintf("%s.stale-%d-%d", lockDir, os.Getpid(), staleSeq.Add(1))
	if err := os.Rename(lockDir, aside); err != nil {
		// Gone already (another waiter broke it) or not renamable: loop.
		time.Sleep(50 * time.Millisecond)
		return
	}
	fi, err := os.Stat(aside)
	if err == nil && clk.Now().Sub(fi.ModTime()) <= StalenessS {
		if _, lerr := os.Lstat(lockDir); errors.Is(lerr, fs.ErrNotExist) {
			if os.Rename(aside, lockDir) == nil {
				return
			}
		}
	}
	_ = os.Remove(aside)
}

// staleSeq makes each breakStale attempt's aside name unique in the process.
var staleSeq atomic.Int64
