//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

func windowsSetupCommand(exe string) *exec.Cmd {
	cmd := exec.Command(exe, "menu", "add")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010}
	return cmd
}
