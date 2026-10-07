//go:build !windows

// Implements spec 06§4.3 POSIX backend: kill(pid, 0), with EPERM (process
// exists but owned by another user) counting as alive.

package procdetect

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// killProbe is a seam over unix.Kill so tests can force specific errno
// results (EPERM, ESRCH, ...) without depending on real process-ownership
// state on the test host.
var killProbe = unix.Kill

func isPIDAliveNative(pid int) bool {
	err := killProbe(pid, syscall.Signal(0))
	if err == nil {
		return true
	}
	if errors.Is(err, unix.EPERM) {
		return true
	}
	return false
}

func ignoreStatError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case syscall.ENOENT, syscall.ENOTDIR, syscall.EBADF, syscall.ELOOP:
		return true
	}
	return false
}
