//go:build windows

package launch

import (
	"context"
	"errors"
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

// TestCLICommandRefusesCmdMetacharacters: an argument cmd.exe would act on
// never reaches a batch file; the Cmd carries the refusal and does not start.
func TestCLICommandRefusesCmdMetacharacters(t *testing.T) {
	cmd := CLICommand(context.Background(), `C:\npm\claude.cmd`, "x & calc")
	if cmd.Err == nil {
		t.Fatal("CLICommand accepted an argument cmd.exe would act on")
	}
	if err := cmd.Start(); !errors.Is(err, cmd.Err) {
		t.Errorf("Start = %v, want the refusal %v", err, cmd.Err)
	}
}
