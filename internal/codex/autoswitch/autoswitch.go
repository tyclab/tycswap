// autoswitch.go — threshold rotation for Codex accounts, run alongside the
// Claude engine by `cswap auto`. Port of claude-swap PR #252
// codex/autoswitch.py (binding_pct, CodexTick, CodexAutoSwitcher).
//
// Deliberately not a genericized Claude engine: that engine is built around
// Claude specifics (per-model scoped windows, setup tokens, credential
// quarantine, the consume gate), and abstracting it for a provider that needs
// almost none of it would be a rewrite of the most load-bearing code in the
// project. What Codex needs is small: read every account's usage, and if the
// active one is at or over the threshold, move to whichever candidate has the
// most headroom by at least the hysteresis margin. Cadence, backoff and
// freshness are already the usage cache's job.
//
// The honest caveat, surfaced rather than hidden: a Codex switch rewrites
// ~/.codex/auth.json, but a codex session already running holds its tokens in
// memory and keeps using the old account until restarted. So a tick that
// switches while codex processes run reports their PIDs, and Human says so.
package autoswitch

import (
	"context"
	"fmt"
	"strings"

	"git.dpemmons.com/dpemmons/cswap/internal/codex/switcher"
	"git.dpemmons.com/dpemmons/cswap/internal/reporting"
)

// Tick outcomes (codex/autoswitch.py CodexTick.outcome).
const (
	OutcomeOK         = "ok"
	OutcomeSwitched   = "switched"
	OutcomeBlocked    = "blocked"
	OutcomeNoAccounts = "no-accounts"
	OutcomeError      = "error"
)

// BindingPct is the utilization that decides an account's fate: the worst of
// its five_hour and seven_day windows, or nil without a measurement. Codex has
// no per-model windows. Values read back from the usage store are
// json.Number; every numeric form is accepted. The rule itself lives in
// switcher (switch best shares it); this is the autoswitch.py entry point.
func BindingPct(usage map[string]any) *float64 { return switcher.BindingPct(usage) }

// Tick is what one auto-switch pass decided, and why.
type Tick struct {
	Outcome     string
	Detail      string
	SwitchedTo  string // "" unless Outcome is OutcomeSwitched
	RunningPIDs []int
}

// Human is the event-stream line. It always names the provider — two engines
// share one stream — and, when codex sessions were running at switch time,
// tells the user to restart them.
func (t Tick) Human() string {
	base := "codex: " + t.Outcome
	if t.Detail != "" {
		base = "codex: " + t.Detail
	}
	if len(t.RunningPIDs) > 0 {
		pids := make([]string, len(t.RunningPIDs))
		for i, p := range t.RunningPIDs {
			pids[i] = fmt.Sprint(p)
		}
		base += " — restart codex (pid " + strings.Join(pids, ", ") + ") for it to take effect"
	}
	return base
}

// Source is what the engine needs from a switcher; *switcher.Switcher
// satisfies it, and tests substitute a fake as the Python tests do.
type Source interface {
	AccountsSnapshot(ctx context.Context, fetch map[string]bool) reporting.AccountsSnapshot
	SwitchableAccountNumbers() []string
	SwitchTo(ctx context.Context, identifier string) (switcher.SwitchResult, error)
}

var _ Source = (*switcher.Switcher)(nil)

// AutoSwitcher is threshold rotation for Codex accounts.
type AutoSwitcher struct {
	Switcher   Source
	Threshold  float64
	Hysteresis float64
}

// New returns an engine over sw; a nil sw is a default switcher.
// codex/autoswitch.py defaults are threshold 90 and hysteresis 10.
func New(sw Source, threshold, hysteresis float64) *AutoSwitcher {
	if sw == nil {
		sw = switcher.New(switcher.Options{})
	}
	return &AutoSwitcher{Switcher: sw, Threshold: threshold, Hysteresis: hysteresis}
}

// snapshot takes one pass, turning a panic into an error: a tick never raises.
func (a *AutoSwitcher) snapshot(ctx context.Context) (snap reporting.AccountsSnapshot, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return a.Switcher.AccountsSnapshot(ctx, nil), nil
}

// Tick runs one decision pass. It never panics or returns an error; failures
// are OutcomeError ticks.
func (a *AutoSwitcher) Tick(ctx context.Context, dryRun bool) Tick {
	// fetch=nil: every account eligible; the usage cache's poll plan is what
	// keeps a tick per interval from becoming a request per account.
	snap, err := a.snapshot(ctx)
	if err != nil {
		return Tick{Outcome: OutcomeError, Detail: fmt.Sprintf("snapshot failed (%v)", err)}
	}

	rotatable := map[string]bool{}
	for _, n := range a.Switcher.SwitchableAccountNumbers() {
		rotatable[n] = true
	}
	var accounts []reporting.AccountSnapshot
	for _, acc := range snap.Accounts {
		if rotatable[acc.Number] {
			accounts = append(accounts, acc)
		}
	}
	if len(accounts) == 0 {
		return Tick{Outcome: OutcomeNoAccounts, Detail: "no rotatable accounts"}
	}

	var active *reporting.AccountSnapshot
	for i := range accounts {
		if accounts[i].IsActive {
			active = &accounts[i]
			break
		}
	}
	if active == nil {
		return Tick{Outcome: OutcomeOK, Detail: "no managed account active"}
	}

	activePct := BindingPct(active.Usage.LastGood)
	if activePct == nil {
		// No measurement is not the same as no usage: switching on unknown data
		// would move the user for no established reason.
		return Tick{Outcome: OutcomeOK, Detail: fmt.Sprintf("account %s usage unknown", active.Number)}
	}
	if *activePct < a.Threshold {
		return Tick{Outcome: OutcomeOK, Detail: fmt.Sprintf("account %s at %.0f%% (below threshold)", active.Number, *activePct)}
	}

	best := ""
	var bestPct *float64
	for _, c := range accounts {
		if c.Number == active.Number {
			continue
		}
		pct := BindingPct(c.Usage.LastGood)
		if pct == nil || *pct >= a.Threshold {
			continue
		}
		// Must beat the active account by the hysteresis margin, or two
		// accounts hovering at the line would ping-pong every tick.
		if *pct > *activePct-a.Hysteresis {
			continue
		}
		if bestPct == nil || *pct < *bestPct {
			best, bestPct = c.Number, pct
		}
	}
	if best == "" {
		return Tick{Outcome: OutcomeBlocked, Detail: fmt.Sprintf("account %s at %.0f%% and no better candidate", active.Number, *activePct)}
	}

	move := fmt.Sprintf("%s (%.0f%%) -> %s (%.0f%%)", active.Number, *activePct, best, *bestPct)
	if dryRun {
		return Tick{Outcome: OutcomeOK, Detail: "would switch " + move}
	}

	res, err := a.Switcher.SwitchTo(ctx, best)
	if err != nil {
		return Tick{Outcome: OutcomeError, Detail: fmt.Sprintf("switch failed: %v", err)}
	}
	// The switch already detected running sessions; an empty list means
	// nothing was running, not that nobody looked, so it is never re-probed.
	return Tick{Outcome: OutcomeSwitched, Detail: "switched " + move, SwitchedTo: best, RunningPIDs: res.RunningPIDs}
}
