//go:build windows

package session

import (
	"os"

	"github.com/tyclab/tycswap/internal/launch"
)

func (osRunner) Exec(bin string, argv, env []string) error {
	code, err := launch.Exec(bin, argv, env)
	if err != nil {
		return err
	}
	os.Exit(code)
	return nil
}
