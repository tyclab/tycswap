package session

import (
	"context"
	"errors"
	"testing"
)

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
