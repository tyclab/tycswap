package autoswitch

import (
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

// scheduled is an active entry whose poll plan is not yet due: a cached 42%,
// as at 10:18:29Z on 2026-11-04 while the account was really near its limit.
func scheduled(pct float64, now float64) usage.UsageEntry {
	e := dictEntry(usageOf(pct, 20))
	next := now + 120
	age := 30.0
	e.NextPollAt, e.AgeS = &next, &age
	return e
}

func TestFreshActiveFetchesBeforeItsPlan(t *testing.T) {
	for name, fresh := range map[string]bool{"scheduled": false, "fresh": true} {
		t.Run(name, func(t *testing.T) {
			clk := newClk()
			now := float64(clk.Now().Unix())
			f := twoAccounts(clk, scheduled(42, now), dictEntry(usageOf(10, 10)))
			f.fetched = map[string]usage.UsageEntry{"1": dictEntry(usageOf(99, 20))}
			rec := &recorder{}
			opts := []Option{}
			if fresh {
				opts = append(opts, WithFreshActive())
			}
			e := build(t, f, settings.Default(), rec, clk, false, opts...)
			got := e.Tick()
			if fresh && (got != Switched || deref(f.current) != "2") {
				t.Fatalf("fresh tick = %v on %q (kinds=%v)", got, deref(f.current), rec.kinds())
			}
			if !fresh && got != NoAction {
				t.Fatalf("scheduled tick = %v (kinds=%v)", got, rec.kinds())
			}
		})
	}
}

// A fresh request never overrides a 429 backoff.
func TestFreshActiveRespectsA429(t *testing.T) {
	clk := newClk()
	now := float64(clk.Now().Unix())
	active := scheduled(42, now)
	last := now - 60
	active.Last429At = &last
	f := twoAccounts(clk, active, dictEntry(usageOf(10, 10)))
	f.fetched = map[string]usage.UsageEntry{"1": dictEntry(usageOf(99, 20))}
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, WithFreshActive())
	if got := e.Tick(); got != NoAction {
		t.Fatalf("tick = %v (kinds=%v)", got, rec.kinds())
	}
}
