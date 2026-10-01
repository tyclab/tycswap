//go:build windows

package web

import "os"

// stopSignalName is what handleStop reports it sent.
const stopSignalName = "terminate"

// terminate ends the process. Windows has no SIGTERM for another process:
// os.Process.Signal supports only Kill there and answers anything else with
// "not supported by windows", so TerminateProcess (os.Process.Kill) is the
// stop that exists. Claude Code writes its transcript as it goes, so
// `claude --continue` in that directory resumes the session.
func terminate(p *os.Process) error { return p.Kill() }
