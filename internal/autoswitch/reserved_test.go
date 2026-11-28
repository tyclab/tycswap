package autoswitch

import (
	"testing"
	"time"

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

// lastOption names the reserved account the engine takes last: the Fable
// store's live account beside a usable Fable store (DESIGN A102).
func lastOption(f *fakeSwitcher, num string) Option {
	return WithReservedRank(func(n string) int {
		switch {
		case n == num:
			return RankFableStore
		case f.reserved[n]:
			return RankReserved
		}
		return RankFree
	})
}

// Among reserved targets the one reserved last ranks after the others, under
// both strategies; an unreserved one still comes first.
func TestReservedLastRanksAfterTheOtherReserved(t *testing.T) {
	for _, strategy := range []string{"soonest-reset", "best"} {
		t.Run(strategy, func(t *testing.T) {
			clk := newClk()
			// Active at its 7d limit; #2 (the Fable store's) has more room than #3.
			f := threeAccounts(clk, dictEntry(usageOf(10, 100)), dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 20)))
			f.reserved["2"], f.reserved["3"] = true, true
			s := settings.Default()
			s.Strategy = strategy
			rec := &recorder{}
			e := build(t, f, s, rec, clk, false, reservedOption(f), lastOption(f, "2"))
			if got := e.Tick(); got != Switched || deref(f.current) != "3" {
				t.Fatalf("outcome = %v, current = %q, want a switch to 3 (kinds=%v)", got, deref(f.current), rec.kinds())
			}
		})
	}
}

// An active account reserved last, under every bar, hands over to another
// account below every bar, reserved or not; from a reserved account that is
// not last the hand-over still takes an unreserved one only, so it does not
// come back (DESIGN A102).
func TestReservedLastActiveHandsOverToAReservedAccount(t *testing.T) {
	clk := newClk()
	f := threeAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 30)), dictEntry(usageOf(90, 10)))
	f.reserved["1"], f.reserved["2"] = true, true
	f.creds["1"] = farFutureCreds(clk, "r1")
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, reservedOption(f), lastOption(f, "1"))
	if got := e.Tick(); got != Switched || deref(f.current) != "2" {
		t.Fatalf("outcome = %v, current = %q, want a switch to 2 (kinds=%v)", got, deref(f.current), rec.kinds())
	}
	if sw, ok := rec.last("switch").(SwitchEvent); !ok || sw.Trigger != "reserved" || sw.Axis != "" {
		t.Fatalf("switch event = %#v, want trigger reserved without an axis", rec.last("switch"))
	}
	if got := e.Tick(); got != NoAction || reasonOf(t, rec.last("no-switch")) != "cooldown" {
		t.Fatalf("within the cooldown: outcome = %v, reason = %q", got, reasonOf(t, rec.last("no-switch")))
	}
	clk.Advance(time.Hour)
	if got := e.Tick(); got != NoAction || reasonOf(t, rec.last("no-switch")) != "reserved-no-target" || deref(f.current) != "2" {
		t.Fatalf("after the cooldown: outcome = %v, reason = %q, current %q", got, reasonOf(t, rec.last("no-switch")), deref(f.current))
	}
}

// With no other account below every bar the account reserved last stays and
// says why.
func TestReservedLastActiveStaysWithoutATarget(t *testing.T) {
	clk := newClk()
	f := threeAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(90, 10)), dictEntry(usageOf(10, 98)))
	f.reserved["1"], f.reserved["2"] = true, true
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, reservedOption(f), lastOption(f, "1"))
	if got := e.Tick(); got != NoAction || deref(f.current) != "1" {
		t.Fatalf("outcome = %v, current %q", got, deref(f.current))
	}
	ns, _ := rec.last("no-switch").(NoSwitchEvent)
	if ns.Reason != "reserved-no-target" || ns.Detail != "the Fable store runs on the active account; no other account is below every bar unless its Mythos login is in use" {
		t.Fatalf("no-switch = %+v", ns)
	}
}

// rankOptions ranks the fake's accounts in the default rotation (DESIGN
// A131) and reserves every account of rank 1 or more.
func rankOptions(f *fakeSwitcher, ranks map[string]int) []Option {
	for num, rank := range ranks {
		f.reserved[num] = rank > 0
	}
	return []Option{reservedOption(f), WithReservedRank(func(n string) int { return ranks[n] })}
}

// Two accounts below every bar, the Fable store's (rank 2) and one whose
// Mythos login is in use (rank 3), every other at a limit: the default login
// rests on the Fable store's account and does not cycle (DESIGN A131).
func TestReservedHandOverGoesToALowerRankOnly(t *testing.T) {
	clk := newClk()
	f := threeAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 30)), dictEntry(usageOf(10, 100)))
	f.creds["1"] = farFutureCreds(clk, "r1")
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, rankOptions(f, map[string]int{"1": 2, "2": 3})...)
	if got := e.Tick(); got != NoAction || deref(f.current) != "1" {
		t.Fatalf("on the Fable store's account: outcome = %v, current %q, want it kept (kinds=%v)", got, deref(f.current), rec.kinds())
	}
	ns, _ := rec.last("no-switch").(NoSwitchEvent)
	if ns.Reason != "reserved-no-target" || ns.Detail != "the Fable store runs on the active account; no other account is below every bar unless its Mythos login is in use" {
		t.Fatalf("no-switch = %+v", ns)
	}
	f.current = strp("2")
	if got := e.Tick(); got != Switched || deref(f.current) != "1" {
		t.Fatalf("on the busy Mythos login's account: outcome = %v, current %q, want a switch to 1 (kinds=%v)", got, deref(f.current), rec.kinds())
	}
	if sw, ok := rec.last("switch").(SwitchEvent); !ok || sw.Trigger != "reserved" {
		t.Fatalf("switch event = %#v, want trigger reserved", rec.last("switch"))
	}
	clk.Advance(time.Hour)
	if got := e.Tick(); got != NoAction || reasonOf(t, rec.last("no-switch")) != "reserved-no-target" || deref(f.current) != "1" {
		t.Fatalf("after the cooldown: outcome = %v, reason = %q, current %q", got, reasonOf(t, rec.last("no-switch")), deref(f.current))
	}
}

// Two accounts whose Mythos logins are in use hand the default login to
// neither: the same rank is no better place (DESIGN A131).
func TestReservedHandOverNeverBetweenTheSameRank(t *testing.T) {
	clk := newClk()
	f := threeAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 30)), dictEntry(usageOf(10, 100)))
	f.creds["1"] = farFutureCreds(clk, "r1")
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, rankOptions(f, map[string]int{"1": 3, "2": 3})...)
	for _, from := range []string{"1", "2"} {
		f.current = strp(from)
		if got := e.Tick(); got != NoAction || deref(f.current) != from {
			t.Fatalf("on %s: outcome = %v, current %q, want it kept (kinds=%v)", from, got, deref(f.current), rec.kinds())
		}
		ns, _ := rec.last("no-switch").(NoSwitchEvent)
		if ns.Reason != "reserved-no-target" || ns.Detail != "the active account's Mythos login is in use; no other account is below every bar unless its Mythos login is in use too" {
			t.Fatalf("on %s: no-switch = %+v", from, ns)
		}
	}
}

// As a target the account whose Mythos login is in use ranks after the Fable
// store's, under both strategies, whatever its room (DESIGN A131).
func TestReservedMythosRanksAfterTheFableStore(t *testing.T) {
	for _, strategy := range []string{"soonest-reset", "best"} {
		t.Run(strategy, func(t *testing.T) {
			clk := newClk()
			f := threeAccounts(clk, dictEntry(usageOf(10, 100)), dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 20)))
			s := settings.Default()
			s.Strategy = strategy
			rec := &recorder{}
			e := build(t, f, s, rec, clk, false, rankOptions(f, map[string]int{"2": 3, "3": 2})...)
			if got := e.Tick(); got != Switched || deref(f.current) != "3" {
				t.Fatalf("outcome = %v, current = %q, want a switch to 3 (kinds=%v)", got, deref(f.current), rec.kinds())
			}
		})
	}
}
