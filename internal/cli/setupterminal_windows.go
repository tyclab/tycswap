//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

func windowsSetupCommand(exe string, args ...string) *exec.Cmd {
	cmd := powershellSetupCommand("powershell.exe", exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
