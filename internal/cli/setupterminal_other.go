//go:build !windows

package cli

import "os/exec"

func windowsSetupCommand(exe string, args ...string) *exec.Cmd { return exec.Command(exe, args...) }
