//go:build !windows

// hidewindow_other.go — no console window to hide off Windows; see
// hidewindow_windows.go.

package procdetect

import "os/exec"

func hideWindow(*exec.Cmd) {}
