//go:build !windows

package web

import (
	"os"
	"syscall"
)

// stopSignalName is what handleStop reports it sent.
const stopSignalName = "SIGTERM"

// terminate asks the process to end; Claude Code exits cleanly on SIGTERM.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
