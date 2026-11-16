//go:build !windows

package session

import "github.com/tyclab/tycswap/internal/launch"

// Exec replaces the process image (execvpe); the FileLock is already released, so claude never inherits a held flock.
func (osRunner) Exec(bin string, argv, env []string) error {
	_, err := launch.Exec(bin, argv, env)
	return err
}
