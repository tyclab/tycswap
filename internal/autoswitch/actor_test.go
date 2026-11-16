package autoswitch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/settings"
)

// rosterFake is the fake switcher with the roster's record of the last
// tycswap switch: the slot it made active and when.
type rosterFake struct {
	*fakeSwitcher
	recorded   string
	recordedAt float64
}

func (r *rosterFake) RecordedSwitch() (string, float64) { return r.recorded, r.recordedAt }

func logMessages(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "tycswap.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if rec, ok := logging.ParseLine(line); ok {
			out = append(out, rec.Message)
		}
	}
	return out
}

// A live login that changed without a tycswap switch is reported once: by one
// event and one log line, across ticks and engine instances; a tycswap
// switch, the engine's own and a dry run report nothing.
func TestExternalLoginChangeLoggedOnce(t *testing.T) {
	clk := newClk()
	dir := t.TempDir()
	statePath := filepath.Join(dir, StateFilename)
	f := &rosterFake{fakeSwitcher: twoAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 10))), recorded: "1", recordedAt: float64(clk.Now().Unix()) - 3600}
	f.current = strp("1")
	rec := &recorder{}
	engine := func(dryRun bool) *Engine {
		return NewEngine(f, settings.Default(), rec.on, dryRun, WithClock(clk), WithStatePath(statePath), WithLogger(logging.NewWithClock(dir, false, clk)))
	}
	engine(false).Tick()
	if rec.count("live-login-changed") != 0 {
		t.Fatal("the first tick reported a change it never saw")
	}
	f.current = strp("2") // a /login in some session; the roster still names 1
	engine(true).Tick()
	if rec.count("live-login-changed") != 0 {
		t.Fatal("a dry run reported the change")
	}
	e := engine(false)
	e.Tick()
	e.Tick()
	engine(false).Tick()
	if n := rec.count("live-login-changed"); n != 1 {
		t.Fatalf("live-login-changed reported %d times, want once (kinds %v)", n, rec.kinds())
	}
	ev := rec.last("live-login-changed")
	if got := ev.JSON(); got["from"] != "1" || got["to"] != "2" {
		t.Fatalf("event = %v, want from 1 to 2", got)
	}
	var logged []string
	for _, m := range logMessages(t, dir) {
		if strings.Contains(m, "live login changed") || strings.Contains(m, "Live login changed") {
			logged = append(logged, m)
		}
	}
	if len(logged) != 1 || logged[0] != "Live login changed from account 1 to 2 without a tycswap switch" {
		t.Fatalf("log lines = %q", logged)
	}

	clk.Advance(60e9)
	f.current, f.recorded, f.recordedAt = strp("1"), "1", float64(clk.Now().Unix())
	engine(false).Tick()
	if n := rec.count("live-login-changed"); n != 1 {
		t.Fatalf("a tycswap switch was reported as a change outside it (%d events)", n)
	}

	// The engine's own switch: the next tick knows where it went.
	f.entries["1"] = dictEntry(usageOf(10, 99))
	f.creds["2"] = farFutureCreds(clk, "r2")
	if got := engine(false).Tick(); got != Switched || deref(f.current) != "2" {
		t.Fatalf("outcome = %v, current %s (kinds %v)", got, deref(f.current), rec.kinds())
	}
	engine(false).Tick()
	if n := rec.count("live-login-changed"); n != 1 {
		t.Fatalf("the engine's own switch was reported as a change outside tycswap (%d events)", n)
	}
}

// An engine switch names its trigger on its own line, after the switch
// layer's; a dry run names nothing.
func TestEngineSwitchNamesItsTrigger(t *testing.T) {
	clk := newClk()
	dir := t.TempDir()
	f := twoAccounts(clk, dictEntry(usageOf(10, 99)), dictEntry(usageOf(10, 10)))
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, true, WithLogger(logging.NewWithClock(dir, false, clk)))
	e.Tick()
	if msgs := logMessages(t, dir); len(msgs) != 0 {
		t.Fatalf("a dry run logged %q", msgs)
	}
	e = build(t, f, settings.Default(), rec, clk, false, WithLogger(logging.NewWithClock(dir, false, clk)))
	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v (kinds %v)", got, rec.kinds())
	}
	var actors []string
	for _, m := range logMessages(t, dir) {
		if strings.Contains(m, " by ") {
			actors = append(actors, m)
		}
	}
	if len(actors) != 1 || actors[0] != "Switched to account 2 by engine:proactive" {
		t.Fatalf("actor lines = %q, want one naming engine:proactive", actors)
	}
}

// Every real tick records when it saw the live login, the last tick the
// dashboard shows under a scheduler; a dry run records nothing.
func TestRealTicksRecordTheLastTick(t *testing.T) {
	clk := newClk()
	statePath := filepath.Join(t.TempDir(), StateFilename)
	f := &rosterFake{fakeSwitcher: twoAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(10, 10))), recorded: "1"}
	engine := func(dryRun bool) *Engine {
		return NewEngine(f, settings.Default(), (&recorder{}).on, dryRun, WithClock(clk), WithStatePath(statePath))
	}
	if ReadLastTick(statePath) != nil {
		t.Fatal("a last tick before any tick")
	}
	engine(false).Tick()
	first := float64(clk.Now().UnixNano()) / 1e9
	clk.Advance(2 * time.Minute)
	engine(false).Tick()
	second := float64(clk.Now().UnixNano()) / 1e9
	if got := ReadLastTick(statePath); got == nil || *got != second || *got == first {
		t.Fatalf("last tick = %v, want the second tick's %v", got, second)
	}
	clk.Advance(2 * time.Minute)
	engine(true).Tick()
	if got := ReadLastTick(statePath); got == nil || *got != second {
		t.Fatalf("a dry run moved the last tick to %v", got)
	}
}
