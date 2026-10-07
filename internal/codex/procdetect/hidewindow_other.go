//go:build !windows

package procdetect

import "os/exec"

func hideWindow(*exec.Cmd) {}
