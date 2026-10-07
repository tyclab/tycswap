package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func (osRunner) ExecInDir(bin string, argv, env []string, dir string) error {
	cmd := CLICommand(context.Background(), bin, argv[1:]...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var status *exec.ExitError
		if errors.As(err, &status) {
			os.Exit(status.ExitCode())
		}
		return err
	}
	return nil
}
