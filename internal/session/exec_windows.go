//go:build windows

// Windows terminal handoff: os.exec* detaches from the console confusingly, so
// tycswap stays resident as a thin wrapper subprocess and exits with claude's own
// return code. Ctrl+C while waiting mirrors to exit code 130.
//
// Implements spec 06§1.8 (_exec Windows branch). Not compiled/vetted on the
// Linux build host; provided for parity (mirrors WP0's Windows-tagged files).
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

// CLICommand is exec.CommandContext(ctx, bin, args...), except that a .cmd or
// .bat file (npm's claude.cmd or codex.cmd) runs as cmd.exe /s /c "<command
// line>", its path always quoted. Started directly, a batch file gets cmd.exe
// /c with Go's command line as it is, which quotes the path only when it holds
// a space, and cmd.exe strips the line's first and last quote when it holds
// more than two quotes, or one of &<>()@^| between them. A shim on a path with
// a space, given an argument that holds one, or on a path with an ampersand,
// would have that path cut at the space or the ampersand. /s strips just the
// pair added here. An argument cmd.exe would act on is refused through
// cmd.Err (CheckCmdShimArgs).
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
