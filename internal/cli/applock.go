// applock.go — one tray app per machine, and a purge that refuses while it
// runs (DESIGN A38).
//
// `tycswap app` holds an exclusive lock on <backup_root>/app.lock for its
// whole lifetime. A second `app` finds it held and exits with a clear message
// instead of putting a second icon in the menu bar, and `purge` finds it held
// and refuses instead of deleting the accounts under a running dashboard. The
// lock is advisory and process-scoped: the OS drops it when the process dies,
// so a crash leaves nothing to clean up.
package cli

import (
	"path/filepath"
	"time"

	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/paths"
)

// appLockPath is the lock file the running app holds.
func appLockPath() string { return filepath.Join(paths.GetBackupRoot(), "app.lock") }

// startGrace is how long a starting app waits for the lock. It is not zero on
// purpose: after installing an update the app respawns itself as a DETACHED
// process and exits, so for a moment both exist. The child needs a window to
// pick the lock up, and two seconds is far longer than the parent's remaining
// teardown while still being instant to a human who started a second copy.
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

// appIsRunning reports whether another process holds the app lock. Its
// callers are `purge` and the background start, so an unreadable lock
// answers TRUE: refusing to delete every account is the safe direction when
// we cannot tell.
func appIsRunning() bool { return lockHeld(appLockPath()) }

// lockHeld reports whether another process holds the lock at path; an
// unreadable lock counts as held.
func lockHeld(path string) bool {
	l := filelock.New(path, 50*time.Millisecond)
	ok, err := l.Acquire(50 * time.Millisecond)
	if err != nil {
		return true
	}
	if !ok {
		return true
	}
	_ = l.Release()
	return false
}
