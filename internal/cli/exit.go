package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/store"
)

const (
	ansiRed   = "\033[31m"
	ansiReset = "\033[0m"
)

func defaultGeteuid() int { return os.Geteuid() }

// errorTo prints a red "Error" line to w (printer.Error parity, spec 08§10.3).
func errorTo(w io.Writer, msg string) {
	if printer.ColorsEnabled() {
		msg = ansiRed + msg + ansiReset
	}
	fmt.Fprintln(w, msg)
}

// warningTo prints a yellow warning line to w (printer.Warning parity → stdout).
func warningTo(w io.Writer, msg string) {
	fmt.Fprintln(w, printer.Yellowed(msg))
}

// RunTUI is the indirection through which cli hands control to internal/tui
// (--tui/--watch). internal/tui is built concurrently and MUST NOT be imported
// here; the integrator wires this variable from a tiny file in cmd/tycswap. When
// nil (a build without the TUI), the dispatch prints a notice and exits 1.
//
// The first parameter is the façade (an *core.Switcher, passed as any so this
// package need not name tui.Facade); the second is the start screen
// ("" for the dashboard, "watch" for the live watch page).
var RunTUI func(f any, start string) int

var geteuid = defaultGeteuid

var newSwitcher = func(opts store.Options) (*core.Switcher, error) {
	return core.New(opts)
}

func constructSwitcher(debug bool, stderr io.Writer) (*core.Switcher, error) {
	return newSwitcher(store.Options{
		Debug:    debug,
		OAuth:    oauth.NewHTTPClient(),
		Keychain: keychain.Security{},
		Stderr:   stderr,
	})
}

// guardRoot refuses to run as root outside a container (spec 08§5/§7.
// _guard_root). On a violation it prints the exact message to stderr and
// returns (1, true); otherwise (0, false). Windows (geteuid < 0) never trips.
func guardRoot(stderr io.Writer) (int, bool) {
	if platform.IsWindows() {
		return 0, false
	}
	if geteuid() == 0 && !platform.RunningInContainer() {
		errorTo(stderr, "Error: Do not run this script as root (unless running in a container)")
		return 1, true
	}
	return 0, false
}

func renderDomainError(err error, jsonMode bool, stdout, stderr io.Writer) int {
	if jsonMode {
		writeJSONIndent(stdout, jsonout.ErrorEnvelope(err))
	} else {
		errorTo(stderr, "Error: "+err.Error())
	}
	return 1
}

// isDomainError reports whether err is a handled ClaudeSwitchError.
func isDomainError(err error) bool { return cerr.IsClaudeSwitchError(err) }

// writeJSONIndent emits exactly one indent-2 JSON document plus a newline
// (DESIGN §3.2 single-JSON-document stdout discipline).
func writeJSONIndent(out io.Writer, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	out.Write(b)
	fmt.Fprintln(out)
}

// writeJSONCompact emits one compact JSON document plus a newline (spec 08§7.7
// auto: JSONL events + compact error envelope).
func writeJSONCompact(out io.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	out.Write(b)
	fmt.Fprintln(out)
}
