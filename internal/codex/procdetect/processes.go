// processes.go — detect running codex processes. Implements claude-swap PR
// #252 codex/processes.py.
//
// A Codex switch rewrites auth.json, but a codex session already running has
// its tokens in memory — it keeps using the old account until restarted.
// Silently switching under a live session is this feature's biggest trap, so a
// switch that finds running processes says so and names their PIDs.
//
// Unlike internal/procdetect on the Claude side, this cannot read session PID
// files: Claude Code writes ~/.claude/sessions/{pid}.json and codex writes no
// equivalent liveness record (~/.codex/sessions/ holds rollout transcripts,
// which outlive the process that wrote them). So the process table is the only
// honest source.
//
// Matching is on the executable's NAME, never a substring of the whole path: a
// directory called codex-notes would otherwise produce a restart warning on
// every switch, and a warning that fires wrongly is one users learn to ignore.
// Detection is advisory — a failed listing loses a warning, never a switch —
// so RunningCodexPIDs never returns an error and never panics.

// Package procdetect finds running codex CLI sessions from the process table.
package procdetect

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/platform"
)

// Proc is one visible process: its PID and its command (a path or bare name).
type Proc struct {
	PID     int
	Command string
}

// codexExecutables are the executable names that mean "a Codex session is
// running". codext is the seamless-switching fork; it is still a session
// worth reporting.
var codexExecutables = map[string]bool{"codex": true, "codext": true}

// listTimeout bounds the process listing: it is advisory, and a switch must
// not stall on it.
const listTimeout = 5 * time.Second

// Log is the package logger seam; when nil, the debug line recording a failed
// listing is dropped.
var Log *logging.Logger

// ListProcesses is the injectable process lister (ps -axo pid=,comm= on POSIX,
// tasklist /FO CSV /NH on Windows); tests replace it.
var ListProcesses = func() ([]Proc, error) {
	return listProcesses(platform.IsWindows())
}

// runCommand runs argv with the listing timeout and returns its stdout. It is
// a seam so the ps/tasklist parsers are tested against real captured output
// without shelling out.
var runCommand = func(argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	hideWindow(cmd)
	out, err := cmd.Output()
	return string(out), err
}

// listProcesses lists processes with the platform's tool. The OS is a
// parameter so both branches are testable on either host.
func listProcesses(windows bool) ([]Proc, error) {
	if windows {
		out, err := runCommand("tasklist", "/FO", "CSV", "/NH")
		if err != nil {
			return nil, err
		}
		return parseTasklist(out), nil
	}
	out, err := runCommand("ps", "-axo", "pid=,comm=")
	if err != nil {
		return nil, err
	}
	return parsePS(out), nil
}

// parseTasklist reads `tasklist /FO CSV /NH` rows: "image","pid",... . Rows
// whose second field is not all digits are skipped.
func parseTasklist(out string) []Proc {
	var rows []Proc
	for _, line := range splitLines(out) {
		parts := strings.Split(line, `","`)
		for i, p := range parts {
			parts[i] = strings.Trim(p, `"`)
		}
		if len(parts) >= 2 && isDigits(parts[1]) {
			if pid, err := strconv.Atoi(parts[1]); err == nil {
				rows = append(rows, Proc{PID: pid, Command: parts[0]})
			}
		}
	}
	return rows
}

// parsePS reads `ps -axo pid=,comm=` rows: a right-aligned PID, one space,
// then the command (which may itself contain spaces).
func parsePS(out string) []Proc {
	var rows []Proc
	for _, line := range splitLines(out) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, comm, _ := strings.Cut(line, " ")
		if !isDigits(pid) {
			continue
		}
		if n, err := strconv.Atoi(pid); err == nil {
			rows = append(rows, Proc{PID: n, Command: strings.TrimSpace(comm)})
		}
	}
	return rows
}

// splitLines splits on \n, \r\n or \r and drops the final empty line, like
// Python's str.splitlines for the separators these tools emit.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// isDigits reports whether s is non-empty and all ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// executableName is the bare executable name of a command path, with either
// slash style accepted and a case-insensitive .exe suffix stripped.
func executableName(command string) string {
	name := strings.ReplaceAll(command, `\`, "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if len(name) >= 4 && strings.EqualFold(name[len(name)-4:], ".exe") {
		name = name[:len(name)-4]
	}
	return name
}

// RunningCodexPIDs returns the PIDs of running codex sessions, in listing
// order. It never panics and returns an empty slice on any failure: if the
// listing fails we lose a warning, not a switch.
func RunningCodexPIDs() (pids []int) {
	pids = []int{}
	defer func() {
		if r := recover(); r != nil {
			debugf("codex process detection failed: panic: %v", r)
			pids = []int{}
		}
	}()
	rows, err := ListProcesses()
	if err != nil {
		debugf("codex process detection failed: %T", err)
		return []int{}
	}
	for _, p := range rows {
		if codexExecutables[executableName(p.Command)] {
			pids = append(pids, p.PID)
		}
	}
	return pids
}

func debugf(format string, a ...any) {
	if Log != nil {
		Log.Debugf(format, a...)
	}
}
