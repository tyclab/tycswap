//go:build windows

package autostart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// execSchtasks runs schtasks.exe from the system directory, not whichever
// PATH finds first, without a window: the tray is a GUI process, so a console
// child would flash a console window of its own at every read.
func execSchtasks(args ...string) ([]byte, uint32, error) {
	ctx, cancel := context.WithTimeout(context.Background(), schtasksTimeout)
	defer cancel()
	exe := "schtasks.exe"
	if dir, err := windows.GetSystemDirectory(); err == nil && dir != "" {
		exe = filepath.Join(dir, exe)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	// Kept apart so the definition on stdout is whole.
	out := append(stdout.Bytes(), stderr.Bytes()...)
	if ctx.Err() != nil {
		return out, 0, fmt.Errorf("no answer within %s", schtasksTimeout)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok {
			return out, ws.ExitCode, nil
		}
	}
	return out, 0, err
}
