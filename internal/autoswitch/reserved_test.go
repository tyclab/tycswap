package autoswitch

import (
	"testing"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

// reservedOption wires the fake's reserved set into the engine (DESIGN A71).
func reservedOption(f *fakeSwitcher) Option {
	return WithReserved(func(num string) bool { return f.reserved[num] })
}

// threeAccounts wires current="1" plus candidates "2" and "3", all oauth with
// fresh credentials, carrying the given decision entries.
func threeAccounts(clk *clock.Fake, active, two, three usage.UsageEntry) *fakeSwitcher {
	f := twoAccounts(clk, active, two)
	f.switchable = []string{"1", "2", "3"}
	f.emails["3"] = "c@x.com"
	f.entries["3"] = three
	f.creds["3"] = farFutureCreds(clk, "r3")
	return f
}

func TestReservedCandidateRanksAfterUnreserved(t *testing.T) {
	for _, strategy := range []string{"soonest-reset", "best"} {
		t.Run(strategy, func(t *testing.T) {
			clk := newClk()
			// Active over the 7d bar; #2 has more room than #3 but can run
			// Fable, so #3 takes the work.
			f := threeAccounts(clk, dictEntry(usageOf(10, 98)), dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 20)))
			f.reserved["2"] = true
			s := settings.Default()
			s.Strategy = strategy
			rec := &recorder{}
			e := build(t, f, s, rec, clk, false, reservedOption(f))
			if got := e.Tick(); got != Switched {
				t.Fatalf("outcome = %v, want Switched (kinds=%v)", got, rec.kinds())
			}
			if deref(f.current) != "3" {
				t.Fatalf("current = %q, want 3 (the account without Fable)", deref(f.current))
			}
		})
	}
}

func TestReservedCandidateIsTheLastResort(t *testing.T) {
	clk := newClk()
	// Only the Fable-capable account is below every bar: it still serves.
	f := threeAccounts(clk, dictEntry(usageOf(10, 98)), dictEntry(usageOf(10, 10)), dictEntry(usageOf(5, 100)))
	f.reserved["2"] = true
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, reservedOption(f))
	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, want Switched (kinds=%v)", got, rec.kinds())
	}
	if deref(f.current) != "2" {
		t.Fatalf("current = %q, want 2", deref(f.current))
	}
}

func TestReservedActiveHandsOverBelowEveryBar(t *testing.T) {
	clk := newClk()
	// The active account is under every bar, but it can run Fable and #2
	// cannot: the group hands over, naming no window.
	f := twoAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 10)))
	f.reserved["1"] = true
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, reservedOption(f))
	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, want Switched (kinds=%v)", got, rec.kinds())
	}
	sw, ok := rec.last("switch").(SwitchEvent)
	if !ok || sw.Trigger != "reserved" || sw.Axis != "" {
		t.Fatalf("switch event = %#v, want trigger reserved without an axis", rec.last("switch"))
	}
	if deref(f.current) != "2" {
		t.Fatalf("current = %q, want 2", deref(f.current))
	}
	// A second tick within the cooldown holds, as a proactive move would.
	f.current = strp("1")
	if got := e.Tick(); got != NoAction || reasonOf(t, rec.last("no-switch")) != "cooldown" {
		t.Fatalf("outcome = %v, reason = %q, want NoAction/cooldown", got, reasonOf(t, rec.last("no-switch")))
	}
}

func TestReservedActiveStaysWithoutUnreservedTarget(t *testing.T) {
	clk := newClk()
	// #2 can run Fable too and #3 is over its 5h bar: nothing to hand over to.
	f := threeAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 10)), dictEntry(usageOf(90, 10)))
	f.reserved["1"], f.reserved["2"] = true, true
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, reservedOption(f))
	if got := e.Tick(); got != NoAction {
		t.Fatalf("outcome = %v, want NoAction (kinds=%v)", got, rec.kinds())
	}
	if reason := reasonOf(t, rec.last("no-switch")); reason != "reserved-no-target" {
		t.Fatalf("reason = %q, want reserved-no-target", reason)
	}
	if deref(f.current) != "1" {
		t.Fatalf("current = %q, want 1", deref(f.current))
	}
}

func TestNothingReservedKeepsBelowThreshold(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 10)))
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false)
	if got := e.Tick(); got != NoAction || reasonOf(t, rec.last("no-switch")) != "below-threshold" {
		t.Fatalf("outcome = %v, reason = %q, want NoAction/below-threshold", got, reasonOf(t, rec.last("no-switch")))
	}
}
