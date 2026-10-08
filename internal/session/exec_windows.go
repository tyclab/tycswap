//go:build windows

// Windows handoff: tycswap stays resident as a thin wrapper and exits with claude's return code; Ctrl+C maps to 130 (spec 06§1.8).
package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// CLICommand runs a .cmd/.bat shim as cmd.exe /s /c "<line>" with its path always quoted: started directly, cmd.exe strips the
// first and last quote and cuts a path at a space or &. /s strips only the added pair; risky arguments fail via CheckCmdShimArgs.
func CLICommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	if !isCmdShim(bin) {
		return exec.CommandContext(ctx, bin, args...)
	}
	line := `"` + bin + `"`
	for _, a := range args {
		line += " " + syscall.EscapeArg(a)
	}
	system, err := windows.GetSystemDirectory()
	comspec := filepath.Join(system, "cmd.exe")
	cmd := exec.CommandContext(ctx, comspec)
	if aerr := CheckCmdShimArgs(bin, args); aerr != nil {
		err = aerr
	}
	if err != nil {
		cmd.Err = err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(comspec) + ` /s /c "` + line + `"`}
	return cmd
}

// Exec spawns claude, waits, and exits with its return code. It never returns
// on success.
func (osRunner) Exec(bin string, argv, env []string) error {
	cmd := CLICommand(context.Background(), bin, argv[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		// Ctrl+C (interrupt) or a spawn failure: mirror claude's exit as 130,
		// matching the Python KeyboardInterrupt path.
		os.Exit(130)
	}
	os.Exit(0)
	return nil // unreachable
}
