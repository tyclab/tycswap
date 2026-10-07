//go:build windows

package cli

import (
	"os"
	"syscall"
)

// Process-creation flags (winbase.h). DETACHED_PROCESS: no console at all, so
// neither a window flashes up nor does closing the terminal end the app;
// CREATE_NEW_PROCESS_GROUP keeps the terminal's Ctrl-C away from it.
const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// spawnDetachedApp starts `<exe> app` without a console, stdout and stderr
// appended to logPath.
func spawnDetachedApp(exe, logPath string) (backgroundApp, error) {
	p, err := startDetached(exe, []string{exe, "app"}, os.Environ(), logPath)
	if err != nil {
		return nil, err
	}
	return watchProcess(p), nil
}

func startDetached(exe string, argv, env []string, logPath string) (*os.Process, error) {
	null, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer null.Close()
	log, err := openAppLog(logPath)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	return os.StartProcess(exe, argv, &os.ProcAttr{
		Env:   env,
		Files: []*os.File{null, log, log},
		Sys: &syscall.SysProcAttr{
			CreationFlags: detachedProcess | createNewProcessGroup,
			HideWindow:    true,
		},
	})
}
