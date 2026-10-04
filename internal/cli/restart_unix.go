//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// restartSelf starts the binary now at the executable's path as a fresh,
// detached process (own session, so it survives this one ending) and lets
// the caller exit (DESIGN A36). execve() in place does not work for a Cocoa
// status item: LaunchServices/WindowServer still associate the PID with the
// image that just ended, and the relaunched process never gets its icon. A
// new PID is what macOS expects for an app relaunch. A var so a test can
// take the hand-over without starting anything.
var restartSelf = func(installedTag string) error {
	exe := exePath()
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return err
		}
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	files := []*os.File{devnull, os.Stdout, os.Stderr}
	p, err := os.StartProcess(exe, os.Args, &os.ProcAttr{
		Env:   append(os.Environ(), justInstalledEnv()+"="+installedTag),
		Files: files,
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return err
	}
	return p.Release()
}
