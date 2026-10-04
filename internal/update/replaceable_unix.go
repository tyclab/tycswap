//go:build unix

package update

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// replaceable reports whether this process may rename a new build over exe:
// it can write the directory and the file, and it owns the file (in a
// directory without the sticky bit, writing the directory is enough to
// replace a binary someone else owns). Access checks only: nothing is
// created.
func replaceable(exe string) bool {
	if unix.Access(filepath.Dir(exe), unix.W_OK) != nil || unix.Access(exe, unix.W_OK) != nil {
		return false
	}
	fi, err := os.Stat(exe)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
