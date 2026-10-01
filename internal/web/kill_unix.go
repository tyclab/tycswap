//go:build linux || darwin

package web

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// stopSignalName is what handleStop reports it sent.
const stopSignalName = "SIGTERM"

// terminateVerified checks that the process holding pid started when the
// session file says (processStart, per platform), then asks it to end;
// Claude Code exits cleanly on SIGTERM. A disagreement is ErrNotTheProcess
// and no signal.
func terminateVerified(pid int, recorded time.Time) error {
	if err := checkStart(pid, recorded); err != nil {
		return err
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

// checkStart is the identity decision: the process's start time from the
// system against the session file's, within startTolerance.
func checkStart(pid int, recorded time.Time) error {
	started, err := processStart(pid)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotTheProcess, err)
	}
	if !startMatches(recorded, started) {
		return fmt.Errorf("%w: it started at %s, the session file says %s",
			ErrNotTheProcess, started.UTC().Format(time.RFC3339), recorded.UTC().Format(time.RFC3339))
	}
	return nil
}
