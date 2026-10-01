// browser_unix.go — the unix launcher chain, each child detached into its own
// session.
//
// detach mirrors Node's `spawn(..., { detached: true })`, which calls setsid so
// the browser outlives the CLI and never receives the terminal's SIGHUP/SIGINT.

//go:build unix

package browser

import (
	"os/exec"
	"runtime"
	"syscall"
)

// openURL hands url to the first launcher on PATH that starts (`open` on
// macOS; wslview, xdg-open, sensible-browser elsewhere).
func openURL(url string) error { return openChain(url, launcherOpeners(runtime.GOOS)) }

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
