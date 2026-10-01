// tuiwire.go — the integration seam that binds internal/tui into the CLI.
//
// Implements DESIGN §2.20/A13: it wires the RunTUI indirection (declared in
// exit.go) to tui.Run and carries the frozen compile assertion
// var _ tui.Facade = (*core.Switcher)(nil). The engine factory captures the
// autoswitchAdapter (the ReadAccountCredentials no-error shadow) plus the
// switcher's OWN OAuth client, the store logger, and the store clock — exactly
// the seams the autoswitch engine needs (spec 05, 09§4.3), matching autoCommand
// (spec 08§7.7). tui does not import cli, so this direct cli→tui edge (per the
// DESIGN dependency graph) introduces no cycle.
//
// When Codex is present (providers.CodexIsPresent: a tycswap Codex store or a
// codex-auth registry), the dashboard is fed a merged Claude + Codex snapshot
// and routes Codex rows to a Codex switcher — the Go twin of claude-swap PR
// #252 tui/app.py's available_providers + MultiSnapshotSource. Without Codex the
// wiring is exactly the Claude-only one, so the dashboard renders as before.
package cli

import (
	"context"
	"io"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/providers"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/tui"
)

// *core.Switcher satisfies the frozen tui.Facade method set (DESIGN A13). This
// is the assertion DESIGN §2.20 places in cli.
var _ tui.Facade = (*core.Switcher)(nil)

func init() {
	RunTUI = runTUI
}

// runTUI is the concrete RunTUI implementation. cli hands it the switcher as an
// `any` (so exit.go need not name tui.Facade); we recover the concrete type to
// build the engine factory over autoswitchAdapter, then launch tui.Run.
func runTUI(f any, start string) int {
	sw := f.(*core.Switcher)
	return tui.Run(sw, start, tuiOptions(context.Background(), sw)...)
}

// tuiOptions is the TUI's option set for sw: the engine factory always, and the
// Codex rows only when Codex is present.
func tuiOptions(ctx context.Context, sw *core.Switcher) []tui.Option {
	opts := []tui.Option{tui.WithEngineFactory(engineFactoryFor(sw))}
	if !codexIsPresent() {
		return opts
	}
	codexSw := newTUICodexSwitcher()
	src := providers.NewMultiSnapshotSource(ctx, sw, codexSw)
	return append(opts, withTUIProviders(ctx, src, codexSw))
}

// codexIsPresent, newTUICodexSwitcher and withTUIProviders are the wiring seams
// tests replace.
var (
	codexIsPresent = providers.CodexIsPresent

	// newTUICodexSwitcher discards the switcher's stdout: Remove warns there
	// about an active slot even when assumeYes is set, and a write to stdout
	// under the alt screen corrupts the display. The confirmation modal has
	// already said what the warning would.
	newTUICodexSwitcher = func() *switcher.Switcher {
		return switcher.New(switcher.Options{Stdout: io.Discard})
	}

	withTUIProviders = tui.WithProviders
)

// engineFactoryFor builds the Auto-screen engine factory for sw. It forwards the
// switcher's OWN injected OAuth client (sw.OAuth) — exactly as autoCommand does
// (spec 08§7.7) — rather than manufacturing a fresh oauth.NewHTTPClient(), which
// would silently ignore whatever client the switcher was constructed with.
func engineFactoryFor(sw *core.Switcher) tui.EngineFactory {
	return func(s settings.AutoSwitchSettings, onEvent func(autoswitch.Event), dryRun bool) tui.AutoEngine {
		return newAutoEngine(sw, s, onEvent, dryRun, sw.OAuth)
	}
}

// newAutoEngine is the engine-construction seam (default builds the real
// autoswitch.Engine). The OAuth client is passed explicitly (oc) so the factory
// wiring is observable in tests without reaching into the engine's unexported
// state; tests override this var to capture the forwarded client.
var newAutoEngine = func(sw *core.Switcher, s settings.AutoSwitchSettings, onEvent func(autoswitch.Event), dryRun bool, oc oauth.Client) tui.AutoEngine {
	return autoswitch.NewEngine(autoswitchAdapter{sw}, s, onEvent, dryRun,
		autoswitch.WithOAuthClient(oc),
		autoswitch.WithLogger(sw.Log),
		autoswitch.WithClock(sw.Clk),
	)
}
