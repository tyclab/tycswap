//go:build windows

package session

import (
	"context"
	"os"
	"os/exec"

	"github.com/tyclab/tycswap/internal/launch"
)

func CLICommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	return launch.CLICommand(ctx, bin, args...)
}

func (osRunner) Exec(bin string, argv, env []string) error {
	code, err := launch.Exec(bin, argv, env)
	if err != nil {
		return err
	}
	os.Exit(code)
	return nil
}
