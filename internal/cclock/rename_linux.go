//go:build linux

package cclock

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames oldpath to newpath and fails when newpath exists,
// in one call (renameat2 with RENAME_NOREPLACE), so a lock another waiter
// took between a check and the rename is never replaced. A kernel or
// filesystem that does not know the flag (EINVAL, ENOSYS) gets the checked
// rename instead.
func renameNoReplace(oldpath, newpath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		return renameIfAbsent(oldpath, newpath)
	}
	return err
}
