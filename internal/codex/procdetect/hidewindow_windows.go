//go:build windows

package procdetect

import (
	"os/exec"
	"syscall"
)

// createNoWindow is Win32 CREATE_NO_WINDOW: it keeps tasklist from flashing a console window.
const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
