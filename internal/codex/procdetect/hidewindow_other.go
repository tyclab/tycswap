//go:build !windows

package procdetect

import "os/exec"

// No console window to hide off Windows; see hidewindow_windows.go.
func hideWindow(*exec.Cmd) {}
