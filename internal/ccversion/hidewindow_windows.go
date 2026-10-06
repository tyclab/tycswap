package ccversion

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideWindow keeps a background child from opening a console window, keeping
// any CmdLine CLICommand set for a .cmd shim.
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
