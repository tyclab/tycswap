//go:build !windows

package session

import (
	"context"
	"os/exec"
	"syscall"
)

func CLICommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}

// Exec replaces the process image (execvpe); the FileLock is already released, so claude never inherits a held flock.
func (osRunner) Exec(bin string, argv, env []string) error {
	return syscall.Exec(bin, argv, env)
}
