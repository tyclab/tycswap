// autoswitch.go — threshold rotation for Codex accounts, run alongside the
// Claude engine by `tycswap auto`. Port of claude-swap PR #252
// codex/autoswitch.py (binding_pct, CodexTick, CodexAutoSwitcher).
//
// Deliberately not a genericized Claude engine: that engine is built around
// Claude specifics (per-model scoped windows, setup tokens, credential
// quarantine, the consume gate), and abstracting it for a provider that needs
// almost none of it would be a rewrite of the most load-bearing code in the
// project. What Codex needs is small: read every account's usage, and if one
// of the active account's windows is at or over its bar, move to a candidate
// that is under that bar and better by the hysteresis margin, preferring the
// most weekly room. The windows are judged the way the Claude engine judges
// its 5h and 7d windows (DESIGN A34). Cadence, backoff and freshness are
// already the usage cache's job.
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

	"github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/usage"
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

// Bars is the switch threshold per Codex window, as for Claude (DESIGN A34):
// FiveHour for the 5h window, which bursts, SevenDay for the weekly one, which
// creeps. A non-zero autoswitch.codexThreshold is one bar for both
// (SingleBar).
type Bars struct {
	FiveHour float64
	SevenDay float64
}

// SingleBar is one threshold for both Codex windows.
func SingleBar(threshold float64) Bars { return Bars{FiveHour: threshold, SevenDay: threshold} }

// of is the bar that governs one window class.
func (b Bars) of(c usage.Class) float64 {
	if c == usage.ClassSession {
		return b.FiveHour
	}
	return b.SevenDay
}

// AutoSwitcher is threshold rotation for Codex accounts.
type AutoSwitcher struct {
	Switcher   Source
	Bars       Bars
	Hysteresis float64
}

// New returns an engine over sw; a nil sw is a default switcher.
// codex/autoswitch.py defaults are threshold 90 and hysteresis 10.
func New(sw Source, bars Bars, hysteresis float64) *AutoSwitcher {
	if sw == nil {
		sw = switcher.New(switcher.Options{})
	}
	return &AutoSwitcher{Switcher: sw, Bars: bars, Hysteresis: hysteresis}
}

// codexAxes is the order the two windows are considered in: the week first,
// because losing it costs days; the 5h window costs a wait. The costlier
// window over its bar names the move.
var codexAxes = []usage.Class{usage.ClassWeek, usage.ClassSession}

// pctOf is one window's utilization, nil when the account reports none.
func pctOf(h usage.Headroom, c usage.Class) *float64 {
	room := h.Axis(c)
	if room == nil {
		return nil
	}
	v := 100.0 - *room
	return &v
}

// label renders "5h 40%" or "7d 96%".
func label(c usage.Class, pct float64) string {
	return fmt.Sprintf("%s %.0f%%", c.Name(), pct)
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

	activeH := usage.AccountHeadroomByClass(active.Usage.LastGood, nil)
	if !activeH.Known() {
		// No measurement is not the same as no usage: switching on unknown data
		// would move the user for no established reason.
		return Tick{Outcome: OutcomeOK, Detail: fmt.Sprintf("account %s usage unknown", active.Number)}
	}
	// The costliest window that has reached its own bar decides; none has,
	// the line names the one closest to its bar.
	axis, hot := usage.ClassWeek, false
	for _, c := range codexAxes {
		if p := pctOf(activeH, c); p != nil && *p >= a.Bars.of(c) {
			axis, hot = c, true
			break
		}
	}
	if !hot {
		near, gap := usage.ClassWeek, 0.0
		found := false
		for _, c := range codexAxes {
			if p := pctOf(activeH, c); p != nil && (!found || a.Bars.of(c)-*p < gap) {
				near, gap, found = c, a.Bars.of(c)-*p, true
			}
		}
		return Tick{Outcome: OutcomeOK, Detail: fmt.Sprintf("account %s at %s (below its %.0f%% bar)",
			active.Number, label(near, *pctOf(activeH, near)), a.Bars.of(near))}
	}
	activePct := *pctOf(activeH, axis)

	best, bestLabel := "", ""
	var bestWeekly *float64
	bestBinding := 0.0
	for _, c := range accounts {
		if c.Number == active.Number {
			continue
		}
		h := usage.AccountHeadroomByClass(c.Usage.LastGood, nil)
		// Judge the candidate on the window that made the tick move; one that
		// reports no such window is judged on its week rather than ruled out
		// for an unknown.
		judged := axis
		pct := pctOf(h, axis)
		if pct == nil {
			judged, pct = usage.ClassWeek, pctOf(h, usage.ClassWeek)
		}
		if pct == nil || *pct >= a.Bars.of(axis) {
			continue
		}
		// Must beat the active account by the hysteresis margin, or two
		// accounts hovering at the line would ping-pong every tick.
		if *pct > activePct-a.Hysteresis {
			continue
		}
		// Never land on an account whose week is spent.
		if h.WeeklyExhausted() {
			continue
		}
		// The most weekly room wins, not the most 5h room; an account with no
		// weekly window falls back to its binding figure.
		weekly := pctOf(h, usage.ClassWeek)
		binding := 100.0 - *h.Binding()
		better := best == ""
		switch {
		case better:
		case weekly != nil && bestWeekly != nil:
			better = *weekly < *bestWeekly || (*weekly == *bestWeekly && binding < bestBinding)
		case (weekly != nil) != (bestWeekly != nil):
			better = weekly != nil
		default:
			better = binding < bestBinding
		}
		if better {
			best, bestLabel, bestWeekly, bestBinding = c.Number, label(judged, *pct), weekly, binding
		}
	}
	if best == "" {
		return Tick{Outcome: OutcomeBlocked, Detail: fmt.Sprintf("account %s at %s and no better candidate", active.Number, label(axis, activePct))}
	}

	move := fmt.Sprintf("%s (%s) -> %s (%s)", active.Number, label(axis, activePct), best, bestLabel)
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
