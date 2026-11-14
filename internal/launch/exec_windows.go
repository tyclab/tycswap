//go:build windows

package launch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

func CLICommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	if !isCmdShim(bin) {
		return exec.CommandContext(ctx, bin, args...)
	}
	line := `"` + bin + `"`
	for _, arg := range args {
		line += " " + syscall.EscapeArg(arg)
	}
	system, err := windows.GetSystemDirectory()
	comspec := filepath.Join(system, "cmd.exe")
	cmd := exec.CommandContext(ctx, comspec)
	if checkErr := CheckCmdShimArgs(bin, args); checkErr != nil {
		err = checkErr
	}
	cmd.Err = err
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(comspec) + ` /s /c "` + line + `"`}
	return cmd
}

func Exec(bin string, argv, env []string) (int, error) {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	cmd := CLICommand(context.Background(), bin, argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return ExitCode(err), err
}
