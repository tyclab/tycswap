//go:build windows

package launch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsExecChildExit(t *testing.T) {
	const childKey = "TYCSWAP_TEST_LAUNCH_CHILD"
	if os.Getenv(childKey) == "exit-23" {
		os.Exit(23)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(childKey, "exit-23")
	code, err := Exec(bin, []string{bin, "-test.run=^TestWindowsExecChildExit$"}, os.Environ())
	if err != nil || code != 23 {
		t.Fatalf("child status=%d error=%v", code, err)
	}
}

func TestWindowsExecSpawnFailure(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "missing-command.exe")
	code, err := Exec(bin, []string{bin}, os.Environ())
	if err == nil || code != 127 {
		t.Fatalf("spawn status=%d error=%v", code, err)
	}
}
