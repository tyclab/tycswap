package autoswitch

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

// onceEngine is one `auto --once` process: a fresh engine over a shared state file.
func onceEngine(t *testing.T, f *fakeSwitcher, rec *recorder, clk *clock.Fake, statePath string, dryRun bool) *Engine {
	t.Helper()
	return build(t, f, settings.Default(), rec, clk, dryRun, WithStatePath(statePath))
}

func sharedStatePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), StateFilename)
}

func unknownActive(clk *clock.Fake) *fakeSwitcher {
	return twoAccounts(clk, nilEntry(), dictEntry(usageOf(10, 0)))
}

func tickOnce(t *testing.T, f *fakeSwitcher, clk *clock.Fake, statePath string, dryRun bool) (TickOutcome, *recorder) {
	t.Helper()
	rec := &recorder{}
	out := onceEngine(t, f, rec, clk, statePath, dryRun).Tick()
	clk.Advance(2 * time.Minute)
	return out, rec
}

func wantNoSwitch(t *testing.T, out TickOutcome, rec *recorder, reason, detail string) {
	t.Helper()
	if out != NoAction {
		t.Fatalf("outcome = %v, want NoAction (kinds=%v)", out, rec.kinds())
	}
	ns, ok := rec.last("no-switch").(NoSwitchEvent)
	if !ok {
		t.Fatalf("no no-switch event; kinds=%v", rec.kinds())
	}
	if ns.Reason != reason || (detail != "" && ns.Detail != detail) {
		t.Fatalf("no-switch = %q %q, want %q %q", ns.Reason, ns.Detail, reason, detail)
	}
}

func TestOnceTicksAccumulateFailoverAcrossProcesses(t *testing.T) {
	clk := newClk()
	f := unknownActive(clk)
	statePath := sharedStatePath(t)
	for _, want := range []string{"1/3 before failover", "2/3 before failover"} {
		out, rec := tickOnce(t, f, clk, statePath, false)
		wantNoSwitch(t, out, rec, "active-usage-unknown", want)
	}
	out, rec := tickOnce(t, f, clk, statePath, false)
	if out != Switched {
		t.Fatalf("third process outcome = %v, want Switched (kinds=%v)", out, rec.kinds())
	}
	if sw := rec.last("switch").(SwitchEvent); sw.Trigger != "failover" {
		t.Errorf("trigger = %q, want failover", sw.Trigger)
	}
	if _, ok := readStateFile(statePath)["active"]; ok {
		t.Errorf("active memory survived the switch: %v", readStateFile(statePath)["active"])
	}
}

func TestKnownHeadroomClearsPersistedCount(t *testing.T) {
	clk := newClk()
	f := unknownActive(clk)
	statePath := sharedStatePath(t)
	tickOnce(t, f, clk, statePath, false)
	tickOnce(t, f, clk, statePath, false)
	if got := activeMemoryOf(readStateFile(statePath), "1").UnhealthyTicks; got != 2 {
		t.Fatalf("persisted count = %d, want 2", got)
	}

	f.entries["1"] = dictEntry(usageOf(10, 10))
	out, rec := tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "below-threshold", "")
	if _, ok := readStateFile(statePath)["active"]; ok {
		t.Errorf("known headroom left active memory: %v", readStateFile(statePath)["active"])
	}

	f.entries["1"] = nilEntry()
	out, rec = tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-usage-unknown", "1/3 before failover")
}

func TestActiveChangeResetsPersistedCount(t *testing.T) {
	clk := newClk()
	f := unknownActive(clk)
	statePath := sharedStatePath(t)
	tickOnce(t, f, clk, statePath, false)
	tickOnce(t, f, clk, statePath, false)

	f.current = strp("2")
	f.entries["2"] = nilEntry()
	out, rec := tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-usage-unknown", "1/3 before failover")
	if got := activeMemoryOf(readStateFile(statePath), "2").UnhealthyTicks; got != 1 {
		t.Errorf("persisted count for slot 2 = %d, want 1", got)
	}
}

func TestIdleHoldSpansProcesses(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, sentinelEntry(jsonout.UsageTokenExpired), dictEntry(usageOf(10, 0)))
	f.liveSessions["1"] = []int{1}
	statePath := sharedStatePath(t)
	t0 := clock.Seconds(clk)

	out, rec := tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-idle", "")
	m := activeMemoryOf(readStateFile(statePath), "1")
	if m.IdleHoldSince == nil || *m.IdleHoldSince != t0 {
		t.Fatalf("persisted idle hold = %v, want %v", m.IdleHoldSince, t0)
	}

	clk.Set(baseTime.Add(time.Duration(IdleHoldMaxS+1) * time.Second))
	out, rec = tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-usage-unknown", "1/3 before failover")
}

func TestMalformedActiveMemoryReadsAsAbsent(t *testing.T) {
	cases := map[string]string{
		"not an object":         `"junk"`,
		"an array":              `[1, 2]`,
		"numeric number":        `{"number": 1, "unhealthyTicks": 2}`,
		"string count":          `{"number": "1", "unhealthyTicks": "2"}`,
		"negative count":        `{"number": "1", "unhealthyTicks": -4}`,
		"oversized count":       `{"number": "1", "unhealthyTicks": 1e300}`,
		"missing number":        `{"unhealthyTicks": 2}`,
		"another slot's memory": `{"number": "2", "unhealthyTicks": 2}`,
	}
	for name, active := range cases {
		t.Run(name, func(t *testing.T) {
			clk := newClk()
			f := unknownActive(clk)
			statePath := sharedStatePath(t)
			raw := `{"schemaVersion": 1, "active": ` + active + `}`
			if err := os.WriteFile(statePath, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			out, rec := tickOnce(t, f, clk, statePath, false)
			wantNoSwitch(t, out, rec, "active-usage-unknown", "1/3 before failover")
			if got := activeMemoryOf(readStateFile(statePath), "1").UnhealthyTicks; got != 1 {
				t.Errorf("persisted count = %d, want 1", got)
			}
		})
	}
}

func TestDryRunReadsButNeverWritesActiveMemory(t *testing.T) {
	clk := newClk()
	f := unknownActive(clk)
	statePath := sharedStatePath(t)
	tickOnce(t, f, clk, statePath, false)
	tickOnce(t, f, clk, statePath, false)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	out, rec := tickOnce(t, f, clk, statePath, true)
	if out != Switched {
		t.Fatalf("dry-run outcome = %v, want Switched (kinds=%v)", out, rec.kinds())
	}
	sw, ok := rec.last("switch").(SwitchEvent)
	if !ok || !sw.DryRun || sw.Trigger != "failover" {
		t.Fatalf("switch event = %+v, want a dry-run failover", rec.last("switch"))
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("dry run rewrote the state file:\nbefore %s\nafter  %s", before, after)
	}
	if got := activeMemoryOf(readStateFile(statePath), "1").UnhealthyTicks; got != 2 {
		t.Errorf("persisted count = %d, want 2", got)
	}
	if deref(f.current) != "1" {
		t.Errorf("dry run switched to %q", deref(f.current))
	}
}

func TestConcurrentEnginesShareOneCount(t *testing.T) {
	clk := newClk()
	f := unknownActive(clk)
	statePath := sharedStatePath(t)
	rec := &recorder{}
	engines := []*Engine{
		onceEngine(t, f, rec, clk, statePath, false),
		onceEngine(t, f, rec, clk, statePath, false),
	}
	var wg sync.WaitGroup
	for _, e := range engines {
		wg.Add(1)
		go func(e *Engine) {
			defer wg.Done()
			e.Tick()
		}(e)
	}
	wg.Wait()
	if got := activeMemoryOf(readStateFile(statePath), "1").UnhealthyTicks; got != 2 {
		t.Errorf("persisted count = %d, want 2 (kinds=%v)", got, rec.kinds())
	}
}

func TestFutureIdleHoldRestartsNow(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, sentinelEntry(jsonout.UsageTokenExpired), dictEntry(usageOf(10, 0)))
	statePath := sharedStatePath(t)
	raw := `{"schemaVersion": 1, "active": {"number": "1", "unhealthyTicks": 0, "idleHoldSince": 1e300}}`
	if err := os.WriteFile(statePath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t0 := clock.Seconds(clk)
	out, rec := tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-idle", "")
	if m := activeMemoryOf(readStateFile(statePath), "1"); m.IdleHoldSince == nil || *m.IdleHoldSince != t0 {
		t.Fatalf("persisted idle hold = %v, want %v", m.IdleHoldSince, t0)
	}
	clk.Set(baseTime.Add(time.Duration(IdleHoldMaxS+1) * time.Second))
	out, rec = tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-usage-unknown", "1/3 before failover")
}

func wantDryFailover(t *testing.T, out TickOutcome, rec *recorder) {
	t.Helper()
	if out != Switched {
		t.Fatalf("outcome = %v, want Switched (kinds=%v)", out, rec.kinds())
	}
	sw, ok := rec.last("switch").(SwitchEvent)
	if !ok || !sw.DryRun || sw.Trigger != "failover" {
		t.Fatalf("switch event = %+v, want a dry-run failover", rec.last("switch"))
	}
}

func TestLoopDryRunCountsInMemory(t *testing.T) {
	clk := newClk()
	f := unknownActive(clk)
	statePath := sharedStatePath(t)
	rec := &recorder{}
	e := onceEngine(t, f, rec, clk, statePath, true)
	for _, want := range []string{"1/3 before failover", "2/3 before failover"} {
		rec.reset()
		wantNoSwitch(t, e.Tick(), rec, "active-usage-unknown", want)
		clk.Advance(2 * time.Minute)
	}
	for i := 3; i <= 4; i++ {
		rec.reset()
		wantDryFailover(t, e.Tick(), rec)
		clk.Advance(2 * time.Minute)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("dry run created the state file (stat err %v)", err)
	}
	if deref(f.current) != "1" {
		t.Errorf("dry run switched to %q", deref(f.current))
	}
}

func TestLoopDryRunIdleHoldExpires(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, sentinelEntry(jsonout.UsageTokenExpired), dictEntry(usageOf(10, 0)))
	f.liveSessions["1"] = []int{1}
	statePath := sharedStatePath(t)
	rec := &recorder{}
	e := onceEngine(t, f, rec, clk, statePath, true)
	wantNoSwitch(t, e.Tick(), rec, "active-idle", "")

	clk.Set(baseTime.Add(time.Duration(IdleHoldMaxS+1) * time.Second))
	rec.reset()
	wantNoSwitch(t, e.Tick(), rec, "active-usage-unknown", "1/3 before failover")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("dry run created the state file (stat err %v)", err)
	}
}

func TestLoopDryRunFollowsHigherPersistedCount(t *testing.T) {
	clk := newClk()
	f := unknownActive(clk)
	statePath := sharedStatePath(t)
	rec := &recorder{}
	dry := onceEngine(t, f, rec, clk, statePath, true)
	wantNoSwitch(t, dry.Tick(), rec, "active-usage-unknown", "1/3 before failover")
	tickOnce(t, f, clk, statePath, false)
	tickOnce(t, f, clk, statePath, false)

	rec.reset()
	wantDryFailover(t, dry.Tick(), rec)
	if got := activeMemoryOf(readStateFile(statePath), "1").UnhealthyTicks; got != 2 {
		t.Errorf("persisted count = %d, want 2", got)
	}
}

func TestUnknownDetailCarriesSkipReason(t *testing.T) {
	const mismatch = "live credential does not match slot 1's backup; refresh skipped"
	const owned = "owner holds an expired token; Claude Code refreshes it on next use"
	clk := newClk()
	f := twoAccounts(clk, usage.UsageEntry{SkipReason: mismatch}, dictEntry(usageOf(10, 0)))
	statePath := sharedStatePath(t)
	out, rec := tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-usage-unknown", "1/3 before failover · "+mismatch)

	f = twoAccounts(clk, usage.UsageEntry{Sentinel: jsonout.UsageTokenExpired, SkipReason: owned}, dictEntry(usageOf(10, 0)))
	f.liveSessions["1"] = []int{1}
	statePath = sharedStatePath(t)
	idleStart := clk.Now()
	out, rec = tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-idle", "token expired while Claude Code is idle; resumes on next use · "+owned)

	clk.Set(idleStart.Add(time.Duration(IdleHoldMaxS+1) * time.Second))
	out, rec = tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-usage-unknown", "1/3 before failover · "+owned)
}

func TestUnknownDetailIgnoresOtherAccountsSkipReason(t *testing.T) {
	cand := dictEntry(usageOf(10, 0))
	cand.SkipReason = "live credential does not match slot 2's backup; refresh skipped"
	clk := newClk()
	out, rec := tickOnce(t, twoAccounts(clk, nilEntry(), cand), clk, sharedStatePath(t), false)
	wantNoSwitch(t, out, rec, "active-usage-unknown", "1/3 before failover")
}
