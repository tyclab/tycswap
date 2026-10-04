//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// spawnDetachedApp starts `<exe> app` in its own session, so closing the
// terminal (SIGHUP to its process group) does not take the app down, with
// stdin on /dev/null and both output streams appended to logPath. A new
// session is also what gives a Cocoa status item its icon on macOS; the
// update restart does the same (restartSelf, A36).
func spawnDetachedApp(exe, logPath string) (backgroundApp, error) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer devnull.Close()
	log, err := openAppLog(logPath)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	p, err := os.StartProcess(exe, []string{exe, "app"}, &os.ProcAttr{
		Env:   os.Environ(),
		Files: []*os.File{devnull, log, log},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return nil, err
	}
	return watchProcess(p), nil
}
