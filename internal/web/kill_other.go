//go:build !linux && !darwin && !windows

package web

import (
	"fmt"
	"runtime"
	"time"
)

const stopSignalName = "SIGTERM"

// Refuses: without a start time the PID cannot be matched to the session file, and an unverified PID is never signalled.
func terminateVerified(int, time.Time) error {
	return fmt.Errorf("%w: process start times cannot be read on %s", ErrNotTheProcess, runtime.GOOS)
}
