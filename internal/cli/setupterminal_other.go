//go:build !windows

package cli

import "os/exec"

func windowsSetupCommand(exe string) *exec.Cmd { return exec.Command(exe, "menu", "add") }
