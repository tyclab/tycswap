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

func runTUI(f any, start string) int {
	sw := f.(*core.Switcher)
	return tui.Run(sw, start, tuiOptions(context.Background(), sw)...)
}

func tuiOptions(ctx context.Context, sw *core.Switcher) []tui.Option {
	opts := []tui.Option{tui.WithEngineFactory(engineFactoryFor(sw))}
	if !codexIsPresent() {
		return opts
	}
	codexSw := newQuietCodexSwitcher()
	src := providers.NewMultiSnapshotSource(ctx, sw, codexSw)
	return append(opts, withTUIProviders(ctx, src, codexSw))
}

var (
	codexIsPresent = providers.CodexIsPresent

	// newQuietCodexSwitcher is the Codex switcher of a surface that draws its
	// own screen, the TUI and the dashboard. It discards the switcher's
	// stdout: Remove warns there about an active slot even when assumeYes is
	// set, a write to stdout under the alt screen corrupts the display, and
	// the dashboard's terminal is not where its user looks. The confirmation
	// modal has already said what the warning would.
	newQuietCodexSwitcher = func() *switcher.Switcher {
		return switcher.New(switcher.Options{Stdout: io.Discard})
	}

	withTUIProviders = tui.WithProviders
)

func engineFactoryFor(sw *core.Switcher) tui.EngineFactory {
	return func(s settings.AutoSwitchSettings, onEvent func(autoswitch.Event), dryRun bool) tui.AutoEngine {
		return newAutoEngine(sw, s, onEvent, dryRun, sw.OAuth)
	}
}

var newAutoEngine = func(sw *core.Switcher, s settings.AutoSwitchSettings, onEvent func(autoswitch.Event), dryRun bool, oc oauth.Client) *autoswitch.Engine {
	return autoswitch.NewEngine(autoswitchAdapter{sw}, s, onEvent, dryRun,
		autoswitch.WithOAuthClient(oc),
		autoswitch.WithLogger(sw.Log),
		autoswitch.WithClock(sw.Clk),
		autoswitch.WithStatePath(sw.StatePath()),
	)
}
