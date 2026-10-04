// console_windows.go — the tray app gives back a console window Windows
// opened only for it (DESIGN A41).
//
// tycswap.exe is a console program: it is also the CLI, and a GUI-
// subsystem build would lose every command's output. So when Explorer starts
// it — the start-at-login Run entry, a double-click, a shortcut — Windows
// creates a console window for it, and `app` then sits in the taskbar as an
// empty terminal whose close button ends the app. That console is recognisable:
// this process is the only one attached to it. A shell's console (a terminal
// the user typed `tycswap app` into) has the shell attached as well and
// is left alone, and a process started detached (the bare command's
// background start, A40) has no console at all.

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
