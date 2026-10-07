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

// Exec replaces the current process image with claude. It returns only if the
// exec syscall itself fails; on success control never comes back.
func (osRunner) Exec(bin string, argv, env []string) error {
	return syscall.Exec(bin, argv, env)
}
