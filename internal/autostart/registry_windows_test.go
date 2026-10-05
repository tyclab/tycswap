package autostart

import (
	"errors"
	"testing"
)

// TestRegistryRefusesInTests: inside a test binary the Run-key functions
// answer errInTests before opening the key.
func TestRegistryRefusesInTests(t *testing.T) {
	c := Config{Home: t.TempDir(), Exe: `C:\tycswap.exe`, GOOS: "windows"}
	if err := Enable(c); !errors.Is(err, errInTests) {
		t.Errorf("Enable = %v, want errInTests", err)
	}
	if err := Disable(c); !errors.Is(err, errInTests) {
		t.Errorf("Disable = %v, want errInTests", err)
	}
	if _, err := Enabled(c); !errors.Is(err, errInTests) {
		t.Errorf("Enabled = %v, want errInTests", err)
	}
}
