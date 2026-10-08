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

// releaseOwnConsole frees a console only this process is attached to (Explorer started the console-subsystem exe); a shell's console stays.
// Output moves to the log; if the log cannot be opened the window stays.
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
