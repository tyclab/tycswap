package autoswitch

import (
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
)

func withFable(u map[string]any, pct float64) map[string]any {
	u["scoped"] = []any{scopedWin("Fable", pct, "")}
	return u
}

func fableSettings() settings.AutoSwitchSettings {
	s := settings.Default()
	s.Model = strp("Fable")
	return s
}

// The live login reports no window for the counted model (a Pro account
// without Fable, 2026-11-04): it hands over to an account that reports one
// below every bar, naming no window (DESIGN A73).
func TestModelMissingActiveMovesToCountedModel(t *testing.T) {
	ultra := map[string]any{"seven_day": win(10, "")}
	for name, target := range map[string]map[string]any{
		"five-hour-target": withFable(usageOf(10, 10), 20),
		"ultra-target":     withFable(ultra, 20),
	} {
		t.Run(name, func(t *testing.T) {
			clk := newClk()
			f := threeAccounts(clk, dictEntry(usageOf(42, 20)), dictEntry(usageOf(5, 5)), dictEntry(target))
			rec := &recorder{}
			e := build(t, f, fableSettings(), rec, clk, false)
			if got := e.Tick(); got != Switched {
				t.Fatalf("outcome = %v, want Switched (kinds=%v)", got, rec.kinds())
			}
			ev := rec.last("switch").(SwitchEvent)
			if ev.Trigger != "model-missing" || ev.Axis != "" || deref(f.current) != "3" {
				t.Fatalf("trigger %q axis %q current %q", ev.Trigger, ev.Axis, deref(f.current))
			}
		})
	}
}

func TestModelMissingActiveStaysWithoutTarget(t *testing.T) {
	for name, target := range map[string]map[string]any{
		"model-spent": withFable(usageOf(10, 10), 100),
		"over-a-bar":  withFable(usageOf(97, 10), 20),
		"week-spent":  withFable(usageOf(10, 100), 20),
	} {
		t.Run(name, func(t *testing.T) {
			clk := newClk()
			f := twoAccounts(clk, dictEntry(usageOf(42, 20)), dictEntry(target))
			rec := &recorder{}
			e := build(t, f, fableSettings(), rec, clk, false)
			if got := e.Tick(); got != NoAction {
				t.Fatalf("outcome = %v, want NoAction (kinds=%v)", got, rec.kinds())
			}
			if got := reasonOf(t, rec.last("no-switch")); got != "model-missing-no-target" {
				t.Fatalf("reason %s", got)
			}
			if deref(f.current) != "1" {
				t.Fatalf("moved to %q", deref(f.current))
			}
		})
	}
}

// Without a counted model, or when no account reports it, nothing changes.
func TestModelMissingNeedsAReportedCountedModel(t *testing.T) {
	for name, tc := range map[string]struct {
		s      settings.AutoSwitchSettings
		target map[string]any
	}{
		"not-counted":    {settings.Default(), withFable(usageOf(10, 10), 20)},
		"nobody-reports": {fableSettings(), usageOf(5, 5)},
	} {
		t.Run(name, func(t *testing.T) {
			clk := newClk()
			f := twoAccounts(clk, dictEntry(usageOf(42, 20)), dictEntry(tc.target))
			rec := &recorder{}
			e := build(t, f, tc.s, rec, clk, false)
			if got := e.Tick(); got != NoAction {
				t.Fatalf("outcome = %v, want NoAction (kinds=%v)", got, rec.kinds())
			}
			if got := reasonOf(t, rec.last("no-switch")); got != "below-threshold" {
				t.Fatalf("reason %s", got)
			}
		})
	}
}

func TestModelMissingRespectsCooldown(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(usageOf(42, 20)), dictEntry(withFable(usageOf(10, 10), 20)))
	rec := &recorder{}
	e := build(t, f, fableSettings(), rec, clk, false)
	if _, err := e.mutateState(func(st map[string]any) { st["lastSwitchAt"] = e.nowSeconds() }); err != nil {
		t.Fatal(err)
	}
	if got := e.Tick(); got != NoAction {
		t.Fatalf("outcome = %v, want NoAction (kinds=%v)", got, rec.kinds())
	}
	if got := reasonOf(t, rec.last("no-switch")); got != "cooldown" {
		t.Fatalf("reason %s", got)
	}
}

// An active account that reports the model is judged on it as before.
func TestModelReportedActiveIsUnchanged(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(withFable(usageOf(42, 20), 30)), dictEntry(withFable(usageOf(10, 10), 20)))
	rec := &recorder{}
	e := build(t, f, fableSettings(), rec, clk, false)
	if got := e.Tick(); got != NoAction {
		t.Fatalf("outcome = %v, want NoAction (kinds=%v)", got, rec.kinds())
	}
	if got := reasonOf(t, rec.last("no-switch")); got != "below-threshold" {
		t.Fatalf("reason %s", got)
	}
}
