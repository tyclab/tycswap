package cli

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/providers"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/tui"
)

// sentinelOAuthClient is a distinct oauth.Client used only for identity
// comparison. Its methods are never called; embedding the interface satisfies
// oauth.Client without implementing them.
type sentinelOAuthClient struct{ oauth.Client }

// TestEngineFactoryForwardsSwitcherOAuthClient pins finding 8: the TUI
// auto-engine factory must forward the switcher's injected OAuth client
// (sw.OAuth) — matching autoCommand — not a fresh oauth.NewHTTPClient(). Were
// the factory to manufacture its own client, the sentinel wired into the
// switcher below would never reach the engine.
func TestEngineFactoryForwardsSwitcherOAuthClient(t *testing.T) {
	sentinel := &sentinelOAuthClient{}
	sw := &core.Switcher{Store: &store.Store{OAuth: sentinel}}

	var got oauth.Client
	orig := newAutoEngine
	t.Cleanup(func() { newAutoEngine = orig })
	newAutoEngine = func(_ *core.Switcher, _ settings.AutoSwitchSettings, _ func(autoswitch.Event), _ bool, oc oauth.Client) *autoswitch.Engine {
		got = oc
		return nil
	}

	factory := engineFactoryFor(sw)
	_ = factory(settings.AutoSwitchSettings{}, func(autoswitch.Event) {}, false)

	if got != oauth.Client(sentinel) {
		t.Fatalf("factory forwarded oauth client %#v, want the switcher's injected client %#v", got, sentinel)
	}
}

// tuiProvidersCapture records what tuiOptions handed tui.WithProviders.
type tuiProvidersCapture struct {
	built, calls int
	src          tui.ProviderSource
	codex        tui.CodexActions
}

func stubTUIProviders(t *testing.T, present bool, codexSw *switcher.Switcher) *tuiProvidersCapture {
	t.Helper()
	c := &tuiProvidersCapture{}
	origPresent, origNew, origWith := codexIsPresent, newQuietCodexSwitcher, withTUIProviders
	t.Cleanup(func() { codexIsPresent, newQuietCodexSwitcher, withTUIProviders = origPresent, origNew, origWith })
	codexIsPresent = func() bool { return present }
	newQuietCodexSwitcher = func() *switcher.Switcher { c.built++; return codexSw }
	withTUIProviders = func(ctx context.Context, src tui.ProviderSource, codex tui.CodexActions) tui.Option {
		c.calls++
		c.src, c.codex = src, codex
		return origWith(ctx, src, codex)
	}
	return c
}

// TestTUIWiringIsClaudeOnlyWithoutCodex pins that an install with no Codex
// store keeps the exact Claude-only option set: no Codex switcher is built and
// the dashboard reads the Facade as before.
func TestTUIWiringIsClaudeOnlyWithoutCodex(t *testing.T) {
	c := stubTUIProviders(t, false, nil)
	opts := tuiOptions(context.Background(), &core.Switcher{Store: &store.Store{}})
	if len(opts) != 1 || c.built != 0 || c.calls != 0 {
		t.Fatalf("opts=%d built=%d providers=%d, want the engine factory alone", len(opts), c.built, c.calls)
	}
}

// TestTUIWiringAddsCodexRowsWhenCodexIsPresent pins the multi-provider wiring:
// a merged Claude + Codex source over the Claude switcher and the Codex one,
// and the same Codex switcher as the action router.
func TestTUIWiringAddsCodexRowsWhenCodexIsPresent(t *testing.T) {
	dir := testutil.IsolateHome(t)
	t.Setenv("CODEX_HOME", dir+"/.codex")
	t.Setenv("XDG_DATA_HOME", dir+"/data")
	codexSw := switcher.New(switcher.Options{Stdout: io.Discard})
	c := stubTUIProviders(t, true, codexSw)

	opts := tuiOptions(context.Background(), &core.Switcher{Store: &store.Store{}})
	if len(opts) != 2 || c.built != 1 || c.calls != 1 {
		t.Fatalf("opts=%d built=%d providers=%d, want engine factory + providers", len(opts), c.built, c.calls)
	}
	if c.codex != tui.CodexActions(codexSw) {
		t.Errorf("action router = %#v, want the Codex switcher", c.codex)
	}
	multi, ok := c.src.(*providers.MultiSnapshotSource)
	if !ok {
		t.Fatalf("source = %T, want *providers.MultiSnapshotSource", c.src)
	}
	ps := multi.Providers()
	var ids []string
	for _, p := range ps {
		ids = append(ids, p.ID())
	}
	if !reflect.DeepEqual(ids, []string{"claude", "codex"}) || providers.Switcher(ps[1]) != codexSw {
		t.Errorf("providers = %v, want claude then the wired codex switcher", ids)
	}
}
