//go:build linux

package cclock

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameat2 RENAME_NOREPLACE never replaces a lock retaken since the check; EINVAL/ENOSYS fall back to the checked rename.
func renameNoReplace(oldpath, newpath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		return renameIfAbsent(oldpath, newpath)
	}
	return err
}
