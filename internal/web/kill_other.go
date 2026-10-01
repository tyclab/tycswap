//go:build !linux && !darwin && !windows

package web

import (
	"fmt"
	"runtime"
	"time"
)

// stopSignalName is what handleStop reports it sent.
const stopSignalName = "SIGTERM"

// terminateVerified refuses: without a way to read a process's start time
// the PID cannot be matched to the session file, and signalling an
// unverified PID is what the check exists to prevent.
func terminateVerified(int, time.Time) error {
	return fmt.Errorf("%w: process start times cannot be read on %s", ErrNotTheProcess, runtime.GOOS)
}
