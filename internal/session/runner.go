package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/clock"
)

const probeWaitDelay = 2 * time.Second

type Runner interface {
	// LookPath resolves a binary on PATH (shutil.which). Returns a non-nil
	// error when the binary is not found. On Windows this must consult PATHEXT
	// so a `claude.cmd` shim resolves.
	LookPath(name string) (string, error)
	// Probe runs argv with env, capturing stdout, up to timeout. It returns
	// (stdout, exitCode, err); err is non-nil only on a spawn failure or
	// timeout (Python OSError / TimeoutExpired), never for a non-zero exit.
	Probe(argv, env []string, timeout time.Duration) (stdout string, rc int, err error)
	Exec(bin string, argv, env []string) error
}

type osRunner struct{}

func (osRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (osRunner) Probe(argv, env []string, timeout time.Duration) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := CLICommand(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.WaitDelay = probeWaitDelay // don't let a leaked pipe outlive the deadline
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return classifyProbe(out.String(), err, ctx.Err())
}

func classifyProbe(out string, runErr error, ctxErr error) (string, int, error) {
	if errors.Is(runErr, exec.ErrWaitDelay) {
		return out, 0, nil
	}
	if runErr == nil {
		// Success is honored regardless of ctx state: a probe that completed at
		// the deadline produced a full result that must not be discarded.
		return out, 0, nil
	}
	if ee, ok := runErr.(*exec.ExitError); ok {
		// A process the deadline killed dies via signal (never Exited): surface
		// TimeoutExpired parity → validation fails. A process that exited on its
		// own — any rc — is honored even if the deadline then fired.
		if ctxErr == context.DeadlineExceeded && !ee.Exited() {
			return "", 0, context.DeadlineExceeded
		}
		return out, ee.ExitCode(), nil // non-zero rc is not an error
	}
	if ctxErr == context.DeadlineExceeded {
		return "", 0, context.DeadlineExceeded // killed before start → timeout parity
	}
	return "", 0, runErr // OSError parity (could not spawn)
}

// clockLockConfig acquires Claude Code's <config>.lock via cclock and returns a
// release closure. A ClaudeCodeLockTimeout (or mkdir OSError) propagates so the
// MCP mirror can fail open.
func clockLockConfig(lockDir string, clk clock.Clock) (func(), error) {
	h, err := cclock.Acquire(lockDir, 0, clk) // 0 → DefaultTimeoutS (9s)
	if err != nil {
		return nil, err
	}
	return h.Release, nil
}
