//go:build !windows

package session

import (
	"context"
	"os/exec"

	"github.com/tyclab/tycswap/internal/launch"
)

func CLICommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	return launch.CLICommand(ctx, bin, args...)
}

// Exec replaces the process image (execvpe); the FileLock is already released, so claude never inherits a held flock.
func (osRunner) Exec(bin string, argv, env []string) error {
	_, err := launch.Exec(bin, argv, env)
	return err
}
