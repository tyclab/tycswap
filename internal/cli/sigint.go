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

// sigintCancelToStderr forces the cancel note to stderr for `tycswap env`, whose stdout is an eval stream; separate from JSON mode.
var sigintCancelToStderr atomic.Bool

// setSigintJSON records JSON mode and clears the stderr override so it never leaks across commands; env re-asserts it after.
func setSigintJSON(v bool) {
	sigintJSON.Store(v)
	sigintCancelToStderr.Store(false)
}

func setSigintCancelToStderr() { sigintCancelToStderr.Store(true) }

func setSigintNote(note string) { sigintNote.Store(note) }

var sigintCh chan os.Signal

// claimSigint stops the notifier; the caller must already have its own signal context, or a Ctrl-C in between takes the default action.
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

// Default SIGINT kills before the cancel note runs, so cli reproduces Python's note and exit 130 (DESIGN A7).
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
