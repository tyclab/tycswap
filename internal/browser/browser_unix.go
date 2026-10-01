// browser_unix.go — detach the browser child into its own session (unix).
//
// Mirrors Node's `spawn(..., { detached: true })`, which calls setsid so the
// browser outlives the CLI and never receives the terminal's SIGHUP/SIGINT.

//go:build unix

package browser

import (
	"os/exec"
	"runtime"
	"syscall"
)

// openURL hands url to the platform launcher (`open`, or the xdg chain).
func openURL(url string) error { return startLauncher(runtime.GOOS, url) }

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
