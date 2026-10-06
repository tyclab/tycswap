// sigint.go — SIGINT (Ctrl-C) reproduction (DESIGN A7, spec 08§5/§7).
//
// Implements DESIGN A7: Python has no SIGINT handler and relies on
// KeyboardInterrupt unwinding to the exit-130 paths; in Go, default SIGINT
// delivery kills the process before the cancel note / JSON-vs-plain routing can
// run, so cli installs a notifier that REPRODUCES (never extends) Python's
// semantics — print the cancelled note, route stderr-vs-stdout by JSON mode,
// exit 130. Installed only from Main() (the real entry), so run()-driven tests
// never trip it. `tycswap web` and `tycswap app` claim the signal while they
// serve (claimSigint): their own signal context ends the serve loop, their
// deferred cleanup runs and they return 0 (DESIGN A48).
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

// sigintJSON reports whether the active command is in JSON mode (routes the
// cancel note to stderr instead of stdout, spec 08§5).
var sigintJSON atomic.Bool

// sigintNote is the cancellation text; "Operation cancelled" for the main path,
// swapped to "Auto-switch stopped" by the auto loop (spec 08§5/§7.7).
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

// setSigintCancelToStderr forces the SIGINT cancel note to stderr for the active
// command (see sigintCancelToStderr).
func setSigintCancelToStderr() { sigintCancelToStderr.Store(true) }

func setSigintNote(note string) { sigintNote.Store(note) }

// sigintCh is the notifier's channel: nil until installSigint ran, so in a
// run()-driven test a claim is a no-op.
var sigintCh chan os.Signal

// claimSigint hands SIGINT to the calling command, which must already have
// registered its own signal context (os.Interrupt), or a Ctrl-C in between
// would take the default action. The claim stops the notifier's delivery
// (signal.Stop), so a Ctrl-C while it stands reaches the command's context
// alone, whenever the notifier's goroutine happens to run; the returned func
// registers the notifier again. A command takes the claim before it serves
// and defers the release before its cleanup defers, so a Ctrl-C during the
// cleanup is still its own.
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

// installSigint wires SIGINT to the reproduced cancel-note + exit-130 path.
func installSigint(s ioStreams) {
	ch := make(chan os.Signal, 1)
	sigintCh = ch
	signal.Notify(ch, syscall.SIGINT)
	go func() {
		// A loop: a server's claim stops delivery for a while and its release
		// resumes it (claimSigint).
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

// writeSigintNote writes the cancel note to the stream the active command's
// routing selects: stderr in JSON mode (spec 08§5) or when a command forced it
// there (env, FINDING 9), stdout otherwise. Extracted from installSigint's
// handler so the routing is unit-testable without the os.Exit that follows it.
func writeSigintNote(s ioStreams) {
	note := "\n" + printer.Dimmed(currentSigintNote())
	if sigintJSON.Load() || sigintCancelToStderr.Load() {
		fmt.Fprintln(s.err, note)
	} else {
		fmt.Fprintln(s.out, note)
	}
}
