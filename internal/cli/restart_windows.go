//go:build windows

package cli

import (
	"os"
)

// restartSelf starts the replaced .exe as a new detached process and lets the
// caller exit; Windows has no exec-in-place. Detached like the background
// start (A41): without DETACHED_PROCESS a tray app that has no console — the
// usual case, start at login or started in the background — would get a
// fresh console window after every update, and nil handles would give the
// child its parent's process handle as stdio. A var so a test can take the
// hand-over without starting anything.
var restartSelf = func(installedTag string) error {
	exe := exePath()
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return err
		}
	}
	p, err := startDetachedProcess(exe, os.Args, append(os.Environ(), justInstalledEnv()+"="+installedTag), appLogPath())
	if err != nil {
		return err
	}
	return p.Release()
}
