//go:build unix

package browser

import (
	"os/exec"
	"runtime"
	"syscall"
)

func openURL(url string) error { return openChain(url, launcherOpeners(runtime.GOOS)) }

// setsid, like Node detached spawn: the browser outlives the CLI and never gets the terminal SIGHUP/SIGINT.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
