// browser_other.go — no process detachment on platforms without setsid or
// Windows creation flags; the child is still started without waiting.

//go:build !unix && !windows

package browser

import (
	"os/exec"
	"runtime"
)

// openURL hands url to the platform launcher.
func openURL(url string) error { return startLauncher(runtime.GOOS, url) }

func detach(*exec.Cmd) {}
