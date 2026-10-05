// Package autostart registers `tycswap app` to start at login (DESIGN A35):
// a LaunchAgent on macOS, an XDG autostart entry on Linux, a HKCU Run value
// on Windows. Stdlib plus golang.org/x/sys/windows/registry.
package autostart

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/tyclab/tycswap/internal/brand"
)

// Label is the LaunchAgent label / desktop-entry and registry value name:
// the build's reverse-DNS identifier (brand.Identifier).
var Label = brand.Sanitized().Identifier

// ErrUnsupported means this OS has no autostart integration here.
var ErrUnsupported = errors.New("autostart: unsupported platform")

// Config locates the user's profile and the binary to start. Zero values are
// filled from the process (home dir, os.Executable, exec).
type Config struct {
	Home string
	Exe  string
	// Args are appended to Exe; default {"app"}.
	Args []string
	// Label names the entry (the LaunchAgent label, the .desktop file, the
	// Run value); default Label. A remote tray (A45) uses its own, so a
	// machine that also runs the local app keeps both entries.
	Label string
	// Run executes launchctl on macOS; tests replace it. nil → exec.Command,
	// which refuses to run in a test binary.
	Run func(name string, args ...string) error
	// GOOS overrides runtime.GOOS (tests only).
	GOOS string
}

func (c Config) withDefaults() Config {
	if c.Home == "" {
		c.Home, _ = os.UserHomeDir()
	}
	if c.Exe == "" {
		c.Exe, _ = os.Executable()
	}
	if c.Args == nil {
		c.Args = []string{"app"}
	}
	if c.Label == "" {
		c.Label = Label
	}
	if c.Run == nil {
		c.Run = func(name string, args ...string) error {
			if testing.Testing() {
				return errors.New("autostart: the real launchctl is not reachable from tests; give the test a Config.Run")
			}
			cmd := exec.Command(name, args...)
			cmd.Stdout, cmd.Stderr = nil, nil
			return cmd.Run()
		}
	}
	if c.GOOS == "" {
		c.GOOS = runtime.GOOS
	}
	return c
}

// Enable registers the start-at-login entry, replacing an existing one.
func Enable(c Config) error {
	c = c.withDefaults()
	switch c.GOOS {
	case "darwin":
		return launchAgentEnable(c)
	case "linux":
		return xdgEnable(c)
	case "windows":
		return registryEnable(c)
	}
	return ErrUnsupported
}

// Disable removes the entry; a missing entry is not an error.
func Disable(c Config) error {
	c = c.withDefaults()
	switch c.GOOS {
	case "darwin":
		return launchAgentDisable(c)
	case "linux":
		return xdgDisable(c)
	case "windows":
		return registryDisable(c)
	}
	return ErrUnsupported
}

// Enabled reports whether an entry exists.
func Enabled(c Config) (bool, error) {
	c = c.withDefaults()
	switch c.GOOS {
	case "darwin":
		return fileExists(launchAgentPath(c))
	case "linux":
		return fileExists(xdgPath(c))
	case "windows":
		return registryEnabled(c)
	}
	return false, ErrUnsupported
}

func fileExists(p string) (bool, error) {
	_, err := os.Stat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
