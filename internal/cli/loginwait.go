package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/session"
)

// loginPollInterval is how often a running login is checked for a complete
// result; loginStopGrace is how long an ended login may take to exit before
// it is killed.
var loginPollInterval = 500 * time.Millisecond
var loginStopGrace = 5 * time.Second

type loginExit struct {
	code int
	err  error
}

// loginProcess is a running `claude auth login`; done delivers its exit once.
type loginProcess struct {
	cmd  *exec.Cmd
	done chan loginExit
}

// startClaudeLogin runs the resolved claude binary directly — never through a
// shell, so a shell function or alias named `claude` cannot add flags tycswap
// never chose — with env as its whole environment and the terminal inherited,
// so the user can finish the browser flow.
var startClaudeLogin = func(binary string, args, env []string, s ioStreams) (*loginProcess, error) {
	cmd := session.CLICommand(context.Background(), binary, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.in, s.out, s.err
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &loginProcess{cmd: cmd, done: make(chan loginExit, 1)}
	go func() {
		err := cmd.Wait()
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			p.done <- loginExit{}
		case errors.As(err, &exitErr):
			code := exitErr.ExitCode()
			if code <= 0 {
				code = 1
			}
			p.done <- loginExit{code: code}
		default:
			p.done <- loginExit{code: 1, err: err}
		}
	}()
	return p, nil
}

// stop asks the login to end: SIGTERM, which Claude Code leaves on cleanly.
// False where that is unsupported (Windows), where the login ends on Enter.
func (p *loginProcess) stop() bool {
	return p.cmd.Process != nil && p.cmd.Process.Signal(syscall.SIGTERM) == nil
}

// end waits for the exit, killing a login that outstays loginStopGrace.
func (p *loginProcess) end() loginExit {
	select {
	case exit := <-p.done:
		return exit
	case <-time.After(loginStopGrace):
		_ = p.cmd.Process.Kill()
		return <-p.done
	}
}

// storeLoginWhenComplete stores the login the moment the scratch profile
// holds a complete one, while Claude Code may still be at its "Press Enter to
// continue" prompt, then ends that process (DESIGN A82); a login Claude Code
// ends first is stored from what it left. It returns the slot stored.
func storeLoginWhenComplete(src lifecycle.AddSource, login *loginProcess, store func() (string, error), s ioStreams) (string, error) {
	tick := time.NewTicker(loginPollInterval)
	defer tick.Stop()
	for {
		select {
		case exit := <-login.done:
			if exit.err != nil || exit.code != 0 {
				return "", lifecycle.ErrLoginIncomplete()
			}
			if err := lifecycle.CheckLogin(src); err != nil {
				return "", err
			}
			return store()
		case <-tick.C:
			if lifecycle.CheckLogin(src) != nil {
				continue
			}
			num, err := store()
			if err != nil {
				login.stop()
				login.end()
				return "", err
			}
			select {
			case <-login.done:
				return num, nil
			default:
			}
			if login.stop() {
				login.end()
			} else {
				fmt.Fprintln(s.out, printer.Dimmed("Press Enter in the sign-in prompt to finish."))
				<-login.done
			}
			return num, nil
		}
	}
}
