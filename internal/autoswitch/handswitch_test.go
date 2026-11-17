package autoswitch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/settings"
)

// handSwitched records a switch by hand onto slot in e's state file, the way
// every hand switch does it (DESIGN A94).
func handSwitched(t *testing.T, e *Engine, slot string) {
	t.Helper()
	if err := HandSwitch(e.statePath, e.clk, func(err error) { t.Fatal(err) }, func() (string, error) { return slot, nil }); err != nil {
		t.Fatal(err)
	}
}

// A switch by hand holds every trigger for the cooldown and says so: the
// 2026-11-16 16:37:17 dashboard switch to account 4 was undone three seconds
// later by engine:at-limit. After the cooldown the engine decides as always,
// and its own switch stamps no hand switch.
func TestHandSwitchHoldsEveryTrigger(t *testing.T) {
	for trigger, active := range map[string]map[string]any{
		"at-limit":  usageOf(0, 100),
		"proactive": usageOf(0, 98),
	} {
		t.Run(trigger, func(t *testing.T) {
			clk := newClk()
			f := twoAccounts(clk, dictEntry(active), dictEntry(usageOf(0, 10)))
			rec := &recorder{}
			e := build(t, f, settings.Default(), rec, clk, false)
			handSwitched(t, e, "1")
			clk.Advance(10 * time.Second)
			if got := e.Tick(); got != NoAction {
				t.Fatalf("outcome = %v, want NoAction (held by the hand switch)", got)
			}
			ns := rec.last("no-switch").(NoSwitchEvent)
			if ns.Reason != "cooldown" || ns.Detail != "switched by hand to account 1; held for another 290s" {
				t.Fatalf("no-switch = %+v", ns)
			}
			clk.Advance(time.Duration(settings.Default().CooldownSeconds) * time.Second)
			if got := e.Tick(); got != Switched {
				t.Fatalf("after the cooldown: outcome = %v, want Switched", got)
			}
			if sw := rec.last("switch").(SwitchEvent); sw.Trigger != trigger {
				t.Fatalf("trigger = %q", sw.Trigger)
			}
			state := e.readState()
			if _, marked := state["lastSwitchBy"]; marked || state["lastSwitchTo"] != "2" {
				t.Fatalf("the engine's own switch left %v", state)
			}
		})
	}
}

// A failover and a login that cannot serve a counted model wait for a hand
// switch too: whatever the trigger.
func TestHandSwitchHoldsFailover(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, nilEntry(), dictEntry(usageOf(0, 10)))
	rec := &recorder{}
	s := settings.Default()
	s.UnhealthyTicks = 1
	e := build(t, f, s, rec, clk, false)
	handSwitched(t, e, "1")
	if got := e.Tick(); got != NoAction || reasonOf(t, rec.last("no-switch")) != "cooldown" {
		t.Fatalf("outcome %v, last no-switch %+v", got, rec.last("no-switch"))
	}
}

// The engine's own recent switch still lets an at-limit tick through: only
// a hand switch holds every trigger.
func TestEngineSwitchStillLetsAtLimitThrough(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(usageOf(0, 100)), dictEntry(usageOf(0, 10)))
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false)
	handSwitched(t, e, "1")
	if _, err := e.mutateState(func(st map[string]any) { stampSwitch(st, "1", e.nowSeconds(), false) }); err != nil {
		t.Fatal(err)
	}
	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, want Switched (at-limit escapes the engine's cooldown)", got)
	}
}

// A hand switch that made nothing active stamps nothing; one that failed
// returns its error and stamps nothing.
func TestHandSwitchWithoutASwitchStampsNothing(t *testing.T) {
	clk := newClk()
	e := build(t, newFake(), settings.Default(), &recorder{}, clk, false)
	if err := HandSwitch(e.statePath, clk, nil, func() (string, error) { return "", nil }); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("slot 2 has no stored login")
	if err := HandSwitch(e.statePath, clk, nil, func() (string, error) { return "2", failed }); !errors.Is(err, failed) {
		t.Fatalf("err = %v", err)
	}
	if state := e.readState(); len(state) != 0 {
		t.Fatalf("state = %v, want none", state)
	}
	if !strings.HasSuffix(e.lockPath, ".autoswitch_state.lock") {
		t.Fatalf("lock = %q", e.lockPath)
	}
}
