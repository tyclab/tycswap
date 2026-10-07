//go:build unix

package browser

import (
	"os/exec"
	"runtime"
	"syscall"
)

func openURL(url string) error { return openChain(url, launcherOpeners(runtime.GOOS)) }

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
