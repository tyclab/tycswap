//go:build !unix && !windows

package browser

import (
	"os/exec"
	"runtime"
)

// openURL hands url to the first launcher on PATH that starts.
func openURL(url string) error { return openChain(url, launcherOpeners(runtime.GOOS)) }

func detach(*exec.Cmd) {}
