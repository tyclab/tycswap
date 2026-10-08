//go:build unix

package update

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// Owning the file is checked too: without a sticky bit, a writable directory alone replaces someone else's binary.
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
