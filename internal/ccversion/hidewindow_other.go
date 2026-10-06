//go:build !windows

package ccversion

import "os/exec"

func hideWindow(*exec.Cmd) {}
