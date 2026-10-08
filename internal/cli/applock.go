package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/paths"
)

// appLockPath is the lock file the running app holds.
func appLockPath() string { return filepath.Join(paths.GetBackupRoot(), "app.lock") }

// Not zero: after an update the app respawns itself detached and exits, so both briefly exist.
const startGrace = 2 * time.Second

// acquireAppLock takes the lock for this process. held is false when another
// app already owns it; err is a real filesystem problem, which callers treat
// as "assume free" rather than refusing to start.
func acquireAppLock() (lock *filelock.FileLock, held bool, err error) {
	return acquireLockAt(appLockPath())
}

// acquireLockAt is acquireAppLock's contract at any path: the remote tray's
// remote.lock (remote.go, A45) is the same rule beside app.lock.
func acquireLockAt(path string) (lock *filelock.FileLock, held bool, err error) {
	l := filelock.New(path, startGrace)
	ok, err := l.Acquire(startGrace)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return l, true, nil
}

// appIsRunning answers true for an unreadable lock: refusing is the safe direction for purge and a second start.
func appIsRunning() bool {
	held, err := lockHeldAt(appLockPath(), 50*time.Millisecond)
	return held || err != nil
}

// lockHeldAt waits up to wait for the lock at path; it creates nothing, so asking leaves no lock file or backup root behind.
func lockHeldAt(path string, wait time.Duration) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	l := filelock.New(path, wait)
	ok, err := l.Acquire(wait)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	_ = l.Release()
	return false, nil
}
