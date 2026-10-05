//go:build windows

// Windows terminal handoff: os.exec* detaches from the console confusingly, so
// tycswap stays resident as a thin wrapper subprocess and exits with claude's own
// return code. Ctrl+C while waiting mirrors to exit code 130.
//
// Implements spec 06§1.8 (_exec Windows branch). Not compiled/vetted on the
// Linux build host; provided for parity (mirrors WP0's Windows-tagged files).
package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// ClaudeCommand is exec.Command(bin, args...), except that a .cmd or .bat file
// (npm's claude.cmd) runs as cmd.exe /s /c "<command line>". Started directly,
// a batch file gets cmd.exe /c with Go's command line as it is, and cmd.exe
// strips the first and the last quote of a line that holds more than two: a
// shim on a path with a space, given an argument that holds one, would run its
// path only up to that space. /s strips just the pair added here. The
// arguments hold no quote or other cmd.exe metacharacter (CheckCmdShimArgs).
func ClaudeCommand(bin string, args ...string) *exec.Cmd {
	if !isCmdShim(bin) {
		return exec.Command(bin, args...)
	}
	line := `"` + bin + `"`
	for _, a := range args {
		line += " " + syscall.EscapeArg(a)
	}
	system, err := windows.GetSystemDirectory()
	comspec := filepath.Join(system, "cmd.exe")
	cmd := exec.Command(comspec)
	if err != nil {
		cmd.Err = err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(comspec) + ` /s /c "` + line + `"`}
	return cmd
}

// Exec spawns claude, waits, and exits with its return code. It never returns
// on success.
func (osRunner) Exec(bin string, argv, env []string) error {
	cmd := ClaudeCommand(bin, argv[1:]...)
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
