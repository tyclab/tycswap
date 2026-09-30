//go:build windows

// hidewindow_windows.go — keep tasklist from flashing a console window.
// Implements the creationflags=CREATE_NO_WINDOW argument of claude-swap PR
// #252 codex/processes.py's win32 branch.

package procdetect

import (
	"os/exec"
	"syscall"
)

// createNoWindow is the Win32 CREATE_NO_WINDOW process-creation flag.
const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
