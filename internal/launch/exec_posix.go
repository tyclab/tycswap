//go:build !windows

package launch

import (
	"context"
	"os/exec"
	"syscall"
)

func CLICommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}

func Exec(bin string, argv, env []string) (int, error) {
	err := syscall.Exec(bin, argv, env)
	return ExitCode(err), err
}
