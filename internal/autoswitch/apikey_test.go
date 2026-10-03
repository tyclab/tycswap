package autoswitch

import (
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

// apiKeyFixture: active subscription account "1", subscription account "2",
// API-key account "9". The fake lists "9" as switchable on purpose: the
// engine must keep it out on its own, not only because store.RotationEligible
// already does (DESIGN A33).
func apiKeyFixture(activePct, otherPct float64) *fakeSwitcher {
	f := newFake()
	f.current = strp("1")
	f.switchable = []string{"1", "2", "9"}
	f.emails = map[string]string{"1": "a@x", "2": "b@x", "9": "key@x"}
	f.kinds = map[string]string{"9": "api_key"}
	f.entries = map[string]usage.UsageEntry{
		"1": dictEntry(usageOf(activePct, 0)),
		"2": dictEntry(usageOf(otherPct, 0)),
	}
	return f
}

// TestAPIKeyIsNeverATarget: an API-key account is never a target, not even
// when every subscription account is at its limit and the engine has nowhere
// else to go. Moving onto one changes how Claude Code authenticates, which a
// running session does not pick up, so the engine reports the wall instead.
func TestAPIKeyIsNeverATarget(t *testing.T) {
	clk := newClk()
	f := apiKeyFixture(100, 100)
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, true)

	if got := e.Tick(); got == Switched {
		t.Fatalf("the engine must not switch onto an API-key account: %v", rec.last("switch"))
	}
	if rec.count("all-exhausted") != 1 {
		t.Errorf("expected all-exhausted, kinds=%v", rec.kinds())
	}
}

// TestAPIKeyNeverTakenProactively: the same on a proactive tick, where the
// active account still has room.
func TestAPIKeyNeverTakenProactively(t *testing.T) {
	clk := newClk()
	f := apiKeyFixture(95, 96)
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, true)

	if got := e.Tick(); got == Switched {
		t.Fatalf("a proactive tick must not move onto an API key: %v", rec.last("switch"))
	}
}

// TestEngineLeavesAnActiveAPIKeyAlone: once a person has put Claude Code on an
// API-key account by hand, the engine leaves it: switching back is the same
// auth-mode change in reverse, and every running session would keep the key.
func TestEngineLeavesAnActiveAPIKeyAlone(t *testing.T) {
	clk := newClk()
	f := apiKeyFixture(10, 10) // both subscription accounts have room
	f.current = strp("9")      // ...but the user chose the API key
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, true)

	if got := e.Tick(); got != NoAction {
		t.Fatalf("outcome = %v, want NoAction (kinds=%v)", got, rec.kinds())
	}
	if deref(f.current) != "9" {
		t.Errorf("active = %q, want the API-key account untouched", deref(f.current))
	}
	if r := reasonOf(t, rec.last("no-switch")); r != "active-api-key" {
		t.Errorf("no-switch reason = %q, want active-api-key", r)
	}
}
