//go:build windows

package cli

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
)

// consoleProcessCount is how many processes share this process's console:
// 0 without a console, 1 when it was created for this process alone.
var consoleProcessCount = func() int {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return int(n)
}

// releaseOwnConsole moves the app's output to its log and frees a console
// that exists only for this process; any other case returns s unchanged. If
// the log cannot be opened the window stays: a visible console beats output
// that goes nowhere.
func releaseOwnConsole(s ioStreams) ioStreams {
	if consoleProcessCount() != 1 {
		return s
	}
	f, err := openAppLog(appLogPath())
	if err != nil {
		return s
	}
	os.Stdout, os.Stderr = f, f
	_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(f.Fd()))
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
	procFreeConsole.Call()
	return ioStreams{in: s.in, out: f, err: f}
}
