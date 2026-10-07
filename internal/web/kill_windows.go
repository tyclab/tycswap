//go:build windows

package web

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// stopSignalName is what handleStop reports it sent.
const stopSignalName = "terminate"

func terminateVerified(pid int, recorded time.Time) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotTheProcess, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return fmt.Errorf("%w: %v", ErrNotTheProcess, err)
	}
	started := time.Unix(0, creation.Nanoseconds())
	if !startMatches(recorded, started) {
		return fmt.Errorf("%w: it started at %s, the session file says %s",
			ErrNotTheProcess, started.UTC().Format(time.RFC3339), recorded.UTC().Format(time.RFC3339))
	}
	return windows.TerminateProcess(h, 1)
}
