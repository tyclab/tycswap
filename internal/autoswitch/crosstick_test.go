package autoswitch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/logging"
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

// Past IdleHoldMaxS the hold stamp stays but no longer holds: candidates are
// polled again, so the fall-through failover has a target to compare.
func TestExpiredIdleHoldPollsCandidates(t *testing.T) {
	clk := newClk()
	now := clock.Seconds(clk)
	age := usage.StaleOKS + 60
	fetchedAt := now - age
	stale := usage.UsageEntry{LastGood: usageOf(10, 0), AgeS: &age, FetchedAt: &fetchedAt}
	f := twoAccounts(clk, sentinelEntry(jsonout.UsageTokenExpired), stale)
	f.liveSessions["1"] = []int{1}
	f.fetched = map[string]usage.UsageEntry{"2": dictEntry(usageOf(10, 0))}
	statePath := sharedStatePath(t)
	raw := fmt.Sprintf(`{"schemaVersion": 1, "active": {"number": "1", "unhealthyTicks": 0, "idleHoldSince": %v}}`, now-IdleHoldMaxS-60)
	if err := os.WriteFile(statePath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	out, rec := tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-usage-unknown", "1/3 before failover")
	polled := false
	for _, call := range f.fetchCalls {
		polled = polled || slices.Contains(call, "2")
	}
	if !polled {
		t.Fatalf("candidate 2 not requested after the hold expired: calls=%v", f.fetchCalls)
	}
	out, rec = tickOnce(t, f, clk, statePath, false)
	wantNoSwitch(t, out, rec, "active-usage-unknown", "2/3 before failover")
	out, rec = tickOnce(t, f, clk, statePath, false)
	if out != Switched {
		t.Fatalf("third tick outcome = %v, want Switched (kinds=%v)", out, rec.kinds())
	}
	if sw := rec.last("switch").(SwitchEvent); sw.Trigger != "failover" || deref(f.current) != "2" {
		t.Errorf("trigger = %q, current = %q; want a failover to 2", sw.Trigger, deref(f.current))
	}
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

func nextNoSwitch(t *testing.T, ch <-chan Event) NoSwitchEvent {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if ns, ok := ev.(NoSwitchEvent); ok {
				return ns
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for no-switch")
		}
	}
}

func TestLoopEngineRestartContinuesCount(t *testing.T) {
	clk := newClk()
	f := unknownActive(clk)
	statePath := sharedStatePath(t)
	ch := make(chan Event, 100)
	e := NewEngine(f, settings.Default(), func(ev Event) { ch <- ev }, false,
		WithClock(clk), WithRNG(func() float64 { return 0.5 }),
		WithStatePath(statePath), WithSleeper(&blockingSleeper{}))
	done := make(chan int, 1)
	go func() { done <- e.RunLoop() }()
	for i, want := range []string{"1/3 before failover", "2/3 before failover"} {
		if i > 0 {
			e.Wake()
		}
		if ns := nextNoSwitch(t, ch); ns.Reason != "active-usage-unknown" || ns.Detail != want {
			t.Fatalf("loop tick %d no-switch = %q %q, want %q", i+1, ns.Reason, ns.Detail, want)
		}
	}
	e.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunLoop did not return after Stop")
	}

	out, rec := tickOnce(t, f, clk, statePath, false)
	if out != Switched {
		t.Fatalf("restarted engine outcome = %v, want Switched (kinds=%v)", out, rec.kinds())
	}
	if sw := rec.last("switch").(SwitchEvent); sw.Trigger != "failover" {
		t.Errorf("trigger = %q, want failover", sw.Trigger)
	}
}

func TestDryRunReportsWhatARealTickWouldDo(t *testing.T) {
	clk := newClk()
	f := unknownActive(clk)
	statePath := sharedStatePath(t)
	raw := []byte(`{"schemaVersion": 1, "active": {"number": "1", "unhealthyTicks": 2, "idleHoldSince": null}}`)
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	e := onceEngine(t, f, rec, clk, statePath, true)
	for i := 0; i < 2; i++ {
		rec.reset()
		wantDryFailover(t, e.Tick(), rec)
		after, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, after) {
			t.Fatalf("dry tick %d rewrote the state file:\nbefore %s\nafter  %s", i+1, raw, after)
		}
		clk.Advance(2 * time.Minute)
	}
	if deref(f.current) != "1" {
		t.Errorf("dry run switched to %q", deref(f.current))
	}
}

func TestDryRunOverUnchangedFileRepeatsCount(t *testing.T) {
	clk := newClk()
	f := unknownActive(clk)
	statePath := sharedStatePath(t)
	rec := &recorder{}
	e := onceEngine(t, f, rec, clk, statePath, true)
	for i := 0; i < 2; i++ {
		rec.reset()
		wantNoSwitch(t, e.Tick(), rec, "active-usage-unknown", "1/3 before failover")
		clk.Advance(2 * time.Minute)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("dry run created the state file (stat err %v)", err)
	}
}

func TestModelNameWarningOncePerStore(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(usageOf(10, 0)), dictEntry(usageOf(10, 0)))
	statePath := sharedStatePath(t)
	s := settings.Default()
	s.Model = strp("Fable")
	engine := func(rec *recorder, dryRun bool) *Engine {
		return build(t, f, s, rec, clk, dryRun, WithStatePath(statePath))
	}
	wantWarnings := func(rec *recorder, want int, checked any) {
		t.Helper()
		if n := rec.count("config-warning"); n != want {
			t.Fatalf("config warnings = %d, want %d (kinds=%v)", n, want, rec.kinds())
		}
		if got := readStateFile(statePath)["modelNamesChecked"]; got != checked {
			t.Fatalf("modelNamesChecked = %v, want %v", got, checked)
		}
	}

	dry := &recorder{}
	engine(dry, true).Tick()
	wantWarnings(dry, 1, nil)
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("dry run created the state file (stat err %v)", err)
	}

	a := &recorder{}
	engine(a, false).Tick()
	wantWarnings(a, 1, "fable")

	b := &recorder{}
	eb := engine(b, false)
	eb.Tick()
	wantWarnings(b, 0, "fable")
	eb.ApplyModels("Opus")
	eb.Tick()
	wantWarnings(b, 1, "opus")

	c := &recorder{}
	s.Model = strp("Opus")
	engine(c, false).Tick()
	wantWarnings(c, 0, "opus")
}

func TestTickResultDrivesNextDelay(t *testing.T) {
	clk := newClk()
	exhausted := map[string]any{"five_hour": win(100, clk.Now().Add(30*time.Minute).Format(time.RFC3339)), "seven_day": win(10, "")}
	e := build(t, twoAccounts(clk, dictEntry(exhausted), dictEntry(exhausted)), settings.Default(), &recorder{}, clk, false)
	r := e.tick()
	if r.Outcome != Blocked || r.SleepUntil == nil || !r.WaitLong {
		t.Fatalf("result = %+v, want Blocked with a reset to sleep until", r)
	}
	if want := clock.Seconds(clk) + 1800 + usage.ResetSlackS; *r.SleepUntil != want {
		t.Errorf("SleepUntil = %v, want %v", *r.SleepUntil, want)
	}
	if d := e.nextDelay(r); d != 1800+usage.ResetSlackS {
		t.Errorf("nextDelay = %v, want %v", d, 1800+usage.ResetSlackS)
	}

	f := twoAccounts(clk, sentinelEntry(jsonout.UsageTokenExpired), dictEntry(usageOf(10, 0)))
	f.liveSessions["1"] = []int{1}
	e = build(t, f, settings.Default(), &recorder{}, clk, false)
	if r = e.tick(); r.Outcome != NoAction || !r.IdleSlow || r.SleepUntil != nil || r.WaitLong {
		t.Fatalf("idle result = %+v, want NoAction with the idle crawl only", r)
	}
	if d := e.nextDelay(r); d != NoResetFallbackS {
		t.Errorf("idle nextDelay = %v, want %v", d, NoResetFallbackS)
	}
}

// warningsUntilNoSwitch counts config warnings up to the end of the next tick.
func warningsUntilNoSwitch(t *testing.T, ch <-chan Event) int {
	t.Helper()
	n := 0
	for {
		select {
		case ev := <-ch:
			switch ev.(type) {
			case ConfigWarningEvent:
				n++
			case NoSwitchEvent:
				return n
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for no-switch")
		}
	}
}

func TestDryLoopModelWarningOncePerList(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(usageOf(10, 0)), dictEntry(usageOf(10, 0)))
	statePath := sharedStatePath(t)
	s := settings.Default()
	s.Model = strp("Fable")
	ch := make(chan Event, 100)
	e := NewEngine(f, s, func(ev Event) { ch <- ev }, true,
		WithClock(clk), WithRNG(func() float64 { return 0.5 }),
		WithStatePath(statePath), WithSleeper(&blockingSleeper{}))
	done := make(chan int, 1)
	go func() { done <- e.RunLoop() }()
	warnings := 0
	for i := 0; i < 3; i++ {
		if i > 0 {
			e.Wake()
		}
		warnings += warningsUntilNoSwitch(t, ch)
	}
	if warnings != 1 {
		t.Fatalf("warnings over three dry ticks = %d, want 1", warnings)
	}
	e.ApplyModels("Opus")
	warnings += warningsUntilNoSwitch(t, ch)
	e.Wake()
	warnings += warningsUntilNoSwitch(t, ch)
	e.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunLoop did not return after Stop")
	}
	if warnings != 2 {
		t.Errorf("warnings after the list changed = %d, want 2", warnings)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("dry run created the state file (stat err %v)", err)
	}
}

// A dry engine remembers the last list it checked, clean or not, so a list
// warns again after another list was checked in between.
func TestDryModelCheckRemembersTheLastCheckedList(t *testing.T) {
	clk := newClk()
	opus := map[string]any{"five_hour": win(10, ""), "seven_day": win(0, ""), "scoped": []any{scopedWin("Opus", 10, "")}}
	f := twoAccounts(clk, dictEntry(opus), dictEntry(opus))
	statePath := sharedStatePath(t)
	s := settings.Default()
	s.Model = strp("Fable")
	rec := &recorder{}
	e := build(t, f, s, rec, clk, true, WithStatePath(statePath))
	for i, step := range []struct {
		model string
		want  int
	}{{"", 1}, {"Opus", 0}, {"Fable", 1}} {
		if step.model != "" {
			e.ApplyModels(step.model)
		}
		rec.reset()
		e.Tick()
		if n := rec.count("config-warning"); n != step.want {
			t.Fatalf("dry tick %d (%q): config warnings = %d, want %d (kinds=%v)", i+1, step.model, n, step.want, rec.kinds())
		}
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("dry run created the state file (stat err %v)", err)
	}
}

// The model-name key is advisory: a state file that cannot be written logs a
// warning and the tick still reaches its decision.
func TestModelNameCheckWriteFailureKeepsTheDecision(t *testing.T) {
	clk := newClk()
	statePath := sharedStatePath(t)
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	s := settings.Default()
	s.Model = strp("Fable")
	rec := &recorder{}
	f := twoAccounts(clk, dictEntry(usageOf(10, 0)), dictEntry(usageOf(10, 0)))
	e := build(t, f, s, rec, clk, false, WithStatePath(statePath), WithLogger(logging.NewWithClock(logDir, false, clk)))
	wantNoSwitch(t, e.Tick(), rec, "below-threshold", "")
	if n := rec.count("error"); n != 0 {
		t.Errorf("error events = %d, want 0 (kinds=%v)", n, rec.kinds())
	}
	if n := rec.count("config-warning"); n != 1 {
		t.Errorf("config warnings = %d, want 1 (kinds=%v)", n, rec.kinds())
	}
	raw, err := os.ReadFile(filepath.Join(logDir, "tycswap.log"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "WARNING - Model-name check not recorded"); n != 1 {
		t.Errorf("log has %d not-recorded warnings, want 1:\n%s", n, raw)
	}
}

func TestModelNameCheckRecordsOnlyACompletedCheck(t *testing.T) {
	fable := map[string]any{"five_hour": win(10, ""), "seven_day": win(0, ""), "scoped": []any{scopedWin("Fable", 10, "")}}
	cases := map[string]struct {
		active, cand usage.UsageEntry
		checked      any
	}{
		"clean check":     {dictEntry(fable), dictEntry(fable), "fable"},
		"unreadable slot": {dictEntry(usageOf(10, 0)), nilEntry(), nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clk := newClk()
			statePath := sharedStatePath(t)
			s := settings.Default()
			s.Model = strp("Fable")
			rec := &recorder{}
			build(t, twoAccounts(clk, tc.active, tc.cand), s, rec, clk, false, WithStatePath(statePath)).Tick()
			if n := rec.count("config-warning"); n != 0 {
				t.Errorf("config warnings = %d, want 0 (kinds=%v)", n, rec.kinds())
			}
			if got := readStateFile(statePath)["modelNamesChecked"]; got != tc.checked {
				t.Errorf("modelNamesChecked = %v, want %v", got, tc.checked)
			}
		})
	}
}
