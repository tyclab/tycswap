//go:build !linux && !darwin && !windows

package web

import (
	"fmt"
	"runtime"
	"time"
)

// stopSignalName is what handleStop reports it sent.
const stopSignalName = "SIGTERM"

func terminateVerified(int, time.Time) error {
	return fmt.Errorf("%w: process start times cannot be read on %s", ErrNotTheProcess, runtime.GOOS)
}
