package cli

import (
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/printer"
)

var sigintJSON atomic.Bool

var sigintNote atomic.Value // string

// sigintCancelToStderr, when true, forces the cancel note to stderr regardless
// of JSON mode — the per-command stream selector `tycswap env` sets because its
// stdout is a pure eval stream (a cancel note on stdout would corrupt the
// `eval "$(tycswap env)"` the user runs). It is a separate flag from JSON mode so
// the JSON-vs-plain routing every other command relies on is untouched.
var sigintCancelToStderr atomic.Bool

// setSigintJSON records JSON mode AND clears the per-command stderr override, so
// the override never leaks across commands (run() drives many commands per
// process in tests). Every command calls this; `tycswap env` re-asserts the
// override with setSigintCancelToStderr immediately after.
func setSigintJSON(v bool) {
	sigintJSON.Store(v)
	sigintCancelToStderr.Store(false)
}

func setSigintCancelToStderr() { sigintCancelToStderr.Store(true) }

func setSigintNote(note string) { sigintNote.Store(note) }

var sigintCh chan os.Signal

func claimSigint() (release func()) {
	ch := sigintCh
	if ch == nil {
		return func() {}
	}
	signal.Stop(ch)
	return func() { signal.Notify(ch, syscall.SIGINT) }
}

func currentSigintNote() string {
	if v, ok := sigintNote.Load().(string); ok && v != "" {
		return v
	}
	return "Operation cancelled"
}

func installSigint(s ioStreams) {
	ch := make(chan os.Signal, 1)
	sigintCh = ch
	signal.Notify(ch, syscall.SIGINT)
	go func() {
		for range ch {
			sigintCleanup()
			writeSigintNote(s)
			os.Exit(130)
		}
	}()
}

// sigintCleanup runs what exiting from the SIGINT goroutine skips. The
// registered cleanups restore any terminal state a live prompt left off (echo
// disabled by a no-echo Secret prompt), whose deferred restore never runs
// (spec 08§5). Then, last, the Claude Code locks the process holds are
// released, whose deferred releases never run either (DESIGN A55).
func sigintCleanup() {
	lifecycle.RunCleanups()
	cclock.ReleaseAll()
}

func writeSigintNote(s ioStreams) {
	note := "\n" + printer.Dimmed(currentSigintNote())
	if sigintJSON.Load() || sigintCancelToStderr.Load() {
		fmt.Fprintln(s.err, note)
	} else {
		fmt.Fprintln(s.out, note)
	}
}
