package procdetect

import (
	"errors"
	"io/fs"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// TestIgnoreStatError: on Windows, the stat errors that mean the path cannot
// name a directory read as absent; a denial, or an error with no code,
// surfaces so the Err variants fail closed.
func TestIgnoreStatError(t *testing.T) {
	stat := func(err error) error {
		return &fs.PathError{Op: "GetFileAttributesEx", Path: `C:\x\sessions`, Err: err}
	}
	for _, errno := range []syscall.Errno{
		windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND, // syscall.ENOENT and ENOTDIR on Windows
		windows.ERROR_NOT_READY, windows.ERROR_INVALID_NAME, windows.ERROR_CANT_RESOLVE_FILENAME,
		syscall.EBADF, syscall.ELOOP,
	} {
		if !ignoreStatError(stat(errno)) {
			t.Errorf("%v (%d) surfaced; want it read as absent", errno, uint32(errno))
		}
	}
	for _, err := range []error{stat(windows.ERROR_ACCESS_DENIED), stat(errors.New("no code")), errors.New("no code")} {
		if ignoreStatError(err) {
			t.Errorf("%v read as absent; want it surfaced", err)
		}
	}
}
