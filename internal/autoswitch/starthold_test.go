// Tests for a start login at its limit and the start hold (DESIGN A105).
package autoswitch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

const atLimitNotice = "Switched to start login Account-2, which is at its limit; auto-switch moves on once the session is up."

var holdToken = regexp.MustCompile(`^[0-9a-f]{32}$`)

func holdsOf(path string) map[string]any {
	holds, _ := readStateFile(path)["startHolds"].(map[string]any)
	return holds
}

// writeHolds puts holds (token -> expiry) into the state file at path beside
// its other fields.
func writeHolds(t *testing.T, path string, holds map[string]any) {
	t.Helper()
	state := readStateFile(path)
	state["startHolds"] = holds
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Below every bar, then room (unknown, or over a bar and below 100 %), then
// at a limit in sequence order; a dead token stays out. StartTargets, which
// the move off a dead Fable login ranks with, still leaves the limits out.
func TestStartRanksALoginAtItsLimitLast(t *testing.T) {
	entries := map[string]usage.UsageEntry{
		"1": dictEntry(usageOf(100, 0)), "2": nilEntry(), "3": dictEntry(usageOf(90, 30)),
		"4": dictEntry(usageOf(5, 10)), "5": dictEntry(usageOf(0, 100)),
		"6": {LastGood: usageOf(1, 1), AuthDeadStrikes: usage.AuthDeadStrikes},
	}
	candidates := []string{"1", "2", "3", "4", "5", "6"}
	var nums []string
	var tiers []int
	for _, row := range rankStartTargets(candidates, entries, settings.Default(), nil, baseTime) {
		nums, tiers = append(nums, row.num), append(tiers, row.tier)
	}
	if want := []string{"4", "3", "2", "1", "5"}; !reflect.DeepEqual(nums, want) {
		t.Fatalf("ranking = %v, want %v", nums, want)
	}
	if want := []int{startBelowBars, startRoom, startRoom, startAtLimit, startAtLimit}; !reflect.DeepEqual(tiers, want) {
		t.Fatalf("tiers = %v, want %v", tiers, want)
	}
	if got := StartTargets(candidates, entries, settings.Default(), nil, baseTime); !reflect.DeepEqual(got, []string{"4", "3", "2"}) {
		t.Fatalf("StartTargets = %v", got)
	}
}

// The logins at a limit keep sequence order whatever their room: one read
// over 100 % keeps its place.
func TestStartRanksLoginsAtALimitInSequenceOrder(t *testing.T) {
	entries := map[string]usage.UsageEntry{"1": dictEntry(usageOf(0, 101)), "2": dictEntry(usageOf(100, 0))}
	var nums []string
	for _, row := range rankStartTargets([]string{"1", "2"}, entries, settings.Default(), nil, baseTime) {
		nums = append(nums, row.num)
	}
	if !reflect.DeepEqual(nums, []string{"1", "2"}) {
		t.Fatalf("ranking = %v, want sequence order", nums)
	}
}

func TestStartTakesAMarkedLoginAtItsLimit(t *testing.T) {
	f, clk, path, snapshot := startTestSetup(t)
	f.entries["2"] = dictEntry(usageOf(10, 100))
	out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path))
	if !out.Switched || out.To != "2" || out.Reason != "" || out.Detail != atLimitNotice || !holdToken.MatchString(out.Hold) || f.moves != 1 {
		t.Fatalf("outcome=%+v moves=%d", out, f.moves)
	}
	state := readStateFile(path)
	now := clock.Seconds(clk)
	if state["lastSwitchTo"] != "2" || state["lastSwitchAt"] != now {
		t.Fatalf("state = %v", state)
	}
	if holds := holdsOf(path); !reflect.DeepEqual(holds, map[string]any{out.Hold: now + 30}) {
		t.Fatalf("holds = %v, want %s until %v", holds, out.Hold, now+30)
	}
}

// A marked login with room wins over one at its limit; the one at its limit
// is still taken when the one with room cannot be prepared.
func TestStartPrefersAMarkedLoginWithRoom(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "room-wins", true: "room-unusable"}[live], func(t *testing.T) {
			f, clk, path, original := startTestSetup(t)
			f.emails["3"], f.creds["3"] = "third@example.com", farFutureCreds(clk, "third")
			f.entries["2"], f.entries["3"] = dictEntry(usageOf(10, 100)), dictEntry(usageOf(90, 30))
			if live {
				f.liveSessions["3"] = []int{123}
			}
			snapshot := func() (StartSnapshot, error) {
				snap, _ := original()
				snap.Marked, snap.Candidates = []string{"2", "3"}, []string{"2", "3"}
				return snap, nil
			}
			out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path))
			if live {
				if out.To != "2" || out.Hold == "" || out.Detail != atLimitNotice {
					t.Fatalf("outcome=%+v", out)
				}
				return
			}
			if out.To != "3" || out.Hold != "" || out.Detail != "Switched to start login Account-3." {
				t.Fatalf("outcome=%+v", out)
			}
			if _, has := readStateFile(path)["startHolds"]; has {
				t.Fatalf("a start with room recorded a hold: %v", readStateFile(path))
			}
		})
	}
}

// An explicit --account at its limit is used; with auto-switch off nothing
// would act on a hold, so none is recorded and the notice is the plain one.
func TestStartTakesAnExplicitAccountAtItsLimit(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(map[bool]string{true: "auto-on", false: "auto-off"}[on], func(t *testing.T) {
			f, clk, path, original := startTestSetup(t)
			f.entries["2"] = dictEntry(usageOf(100, 10))
			snapshot := func() (StartSnapshot, error) {
				snap, _ := original()
				snap.Settings.Enabled = on
				return snap, nil
			}
			out := StartSelection(f, "2", snapshot, WithClock(clk), WithStatePath(path))
			if !out.Switched || out.To != "2" || f.moves != 1 {
				t.Fatalf("outcome=%+v", out)
			}
			if on && (out.Hold == "" || out.Detail != atLimitNotice || len(holdsOf(path)) != 1) {
				t.Fatalf("outcome=%+v holds=%v", out, holdsOf(path))
			}
			if _, has := readStateFile(path)["startHolds"]; !on && (has || out.Hold != "" || out.Detail != "Switched to start login Account-2.") {
				t.Fatalf("auto off: outcome=%+v state=%v", out, readStateFile(path))
			}
		})
	}
}

// A start login already current at its limit gets a hold of its own and is
// not switched; one with room stays silent and takes no lock.
func TestStartHoldsALoginAlreadyCurrentAtItsLimit(t *testing.T) {
	for _, tc := range []struct {
		name, account, reason, detail string
		atLimit                       bool
	}{
		{name: "marked", reason: "already-start-login", atLimit: true},
		{name: "explicit", account: "2", reason: "already-active", detail: "Requested start login is already active.", atLimit: true},
		{name: "marked-with-room", reason: "already-start-login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, clk, path, snapshot := startTestSetup(t)
			f.current = strp("2")
			if tc.atLimit {
				f.entries["2"] = dictEntry(usageOf(10, 100))
			}
			out := StartSelection(f, tc.account, snapshot, WithClock(clk), WithStatePath(path))
			if out.Switched || out.Reason != tc.reason || out.Detail != tc.detail || f.moves != 0 {
				t.Fatalf("outcome=%+v moves=%d", out, f.moves)
			}
			if !tc.atLimit {
				if _, err := os.Stat(filepath.Dir(path) + "/.autoswitch_state.lock"); out.Hold != "" || !os.IsNotExist(err) {
					t.Fatalf("hold %q, lock %v", out.Hold, err)
				}
				return
			}
			state := readStateFile(path)
			if !holdToken.MatchString(out.Hold) || !reflect.DeepEqual(holdsOf(path), map[string]any{out.Hold: clock.Seconds(clk) + 30}) || state["lastSwitchAt"] != nil {
				t.Fatalf("hold %q, state %v", out.Hold, state)
			}
		})
	}
}

// A target already current when start switches under the lock gets the
// hold too.
func TestStartHoldsATargetFoundCurrentUnderTheLock(t *testing.T) {
	f, clk, path, snapshot := startTestSetup(t)
	f.entries["2"] = dictEntry(usageOf(10, 100))
	f.switchTo = func(*fakeSwitcher, string) (map[string]any, error) { return map[string]any{"switched": false}, nil }
	out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path))
	if out.Switched || out.Reason != "already-active" || !holdToken.MatchString(out.Hold) || len(holdsOf(path)) != 1 {
		t.Fatalf("outcome=%+v holds=%v", out, holdsOf(path))
	}
}

// The start login current and at its limit when start looks, but moved by
// the time start holds the lock: the change is said and nothing is held.
func TestStartHoldsNothingOnceTheCurrentLoginMoved(t *testing.T) {
	f, clk, path, original := startTestSetup(t)
	f.current = strp("2")
	f.entries["2"] = dictEntry(usageOf(10, 100))
	reads := 0
	snapshot := func() (StartSnapshot, error) {
		if reads++; reads == 2 {
			f.current = strp("1")
		}
		return original()
	}
	out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path))
	if out.Reason != "selection-changed" || out.Hold != "" || holdsOf(path) != nil || f.moves != 0 {
		t.Fatalf("outcome=%+v holds=%v moves=%d", out, holdsOf(path), f.moves)
	}
}

// A hold that cannot be written is said; the session gets no token.
func TestStartHoldThatCannotBeWritten(t *testing.T) {
	f, clk, path, snapshot := startTestSetup(t)
	f.current = strp("2")
	f.entries["2"] = dictEntry(usageOf(10, 100))
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path))
	if out.Reason != "already-start-login" || out.Hold != "" || !strings.HasPrefix(out.Detail, "Could not hold auto-switch for the starting session: ") {
		t.Fatalf("outcome=%+v", out)
	}
}

// Every start that writes the state drops the expired holds and keeps the
// others; a state without holds left loses the key.
func TestStartDropsExpiredHolds(t *testing.T) {
	for _, atLimit := range []bool{false, true} {
		t.Run(map[bool]string{false: "with-room", true: "at-limit"}[atLimit], func(t *testing.T) {
			f, clk, path, snapshot := startTestSetup(t)
			now := clock.Seconds(clk)
			writeHolds(t, path, map[string]any{"old": now - 1, "running": now + 10})
			if atLimit {
				f.entries["2"] = dictEntry(usageOf(10, 100))
			}
			out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path))
			want := map[string]any{"running": now + 10}
			if atLimit {
				want[out.Hold] = now + 30
			}
			if holds := holdsOf(path); !out.Switched || !reflect.DeepEqual(holds, want) {
				t.Fatalf("outcome=%+v holds=%v, want %v", out, holds, want)
			}
		})
	}
	f, clk, path, snapshot := startTestSetup(t)
	writeHolds(t, path, map[string]any{"old": clock.Seconds(clk) - 1})
	if out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path)); !out.Switched {
		t.Fatal(out)
	}
	if _, has := readStateFile(path)["startHolds"]; has {
		t.Fatalf("state = %v", readStateFile(path))
	}
}

// While a hold runs the engine makes no switch, whatever the trigger, and
// says why; once it has expired the same tick switches.
func TestStartHoldHoldsEveryTrigger(t *testing.T) {
	for trigger, active := range map[string]usage.UsageEntry{
		"at-limit":  dictEntry(usageOf(0, 100)),
		"proactive": dictEntry(usageOf(0, 98)),
		"failover":  nilEntry(),
	} {
		t.Run(trigger, func(t *testing.T) {
			clk := newClk()
			f := twoAccounts(clk, active, dictEntry(usageOf(0, 10)))
			rec := &recorder{}
			s := settings.Default()
			s.UnhealthyTicks = 1
			e := build(t, f, s, rec, clk, false)
			writeHolds(t, e.statePath, map[string]any{"session": e.nowSeconds() + 30})
			clk.Advance(2 * time.Second)
			if got := e.Tick(); got != NoAction || deref(f.current) != "1" {
				t.Fatalf("outcome = %v, current %s", got, deref(f.current))
			}
			ns := rec.last("no-switch").(NoSwitchEvent)
			if ns.Reason != "cooldown" || ns.Detail != "a session is starting; held for another 28s" {
				t.Fatalf("no-switch = %+v", ns)
			}
			clk.Advance(29 * time.Second)
			if got := e.Tick(); got != Switched || rec.last("switch").(SwitchEvent).Trigger != trigger {
				t.Fatalf("after the hold: outcome = %v, events %v", got, rec.kinds())
			}
		})
	}
}

// holdingSwitcher records a hold while the tick prepares its target: after
// the tick's own check, before its switch.
type holdingSwitcher struct {
	*fakeSwitcher
	hold func()
}

func (h *holdingSwitcher) LiveSessionPidsFor(num, email string) []int {
	if h.hold != nil {
		h.hold()
		h.hold = nil
	}
	return h.fakeSwitcher.LiveSessionPidsFor(num, email)
}

// A tick during a hold stops before it prepares a target.
func TestStartHoldPreparesNoTarget(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(usageOf(0, 100)), dictEntry(usageOf(0, 10)))
	path := filepath.Join(t.TempDir(), StateFilename)
	writeHolds(t, path, map[string]any{"session": clock.Seconds(clk) + 30})
	prepared := false
	h := &holdingSwitcher{fakeSwitcher: f, hold: func() { prepared = true }}
	e := NewEngine(h, settings.Default(), (&recorder{}).on, false, WithClock(clk), WithStatePath(path))
	if got := e.Tick(); got != NoAction || prepared {
		t.Fatalf("outcome = %v, target prepared %v", got, prepared)
	}
}

func TestStartHoldHoldsASwitchUnderWay(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(usageOf(0, 100)), dictEntry(usageOf(0, 10)))
	rec := &recorder{}
	path := filepath.Join(t.TempDir(), StateFilename)
	h := &holdingSwitcher{fakeSwitcher: f}
	h.hold = func() { writeHolds(t, path, map[string]any{"session": clock.Seconds(clk) + 30}) }
	e := NewEngine(h, settings.Default(), rec.on, false, WithClock(clk), WithStatePath(path))
	if got := e.Tick(); got != NoAction || deref(f.current) != "1" {
		t.Fatalf("outcome = %v, current %s", got, deref(f.current))
	}
	if ns := rec.last("no-switch").(NoSwitchEvent); ns.Reason != "cooldown" || ns.Detail != "a session is starting; held for another 30s" {
		t.Fatalf("no-switch = %+v", ns)
	}
}

// The engine's switch outside a tick waits for a hold; a switch by hand does not.
func TestStartHoldHoldsTheEnginesOwnSwitchOnly(t *testing.T) {
	clk := newClk()
	path := filepath.Join(t.TempDir(), StateFilename)
	writeHolds(t, path, map[string]any{"session": clock.Seconds(clk) + 30})
	ran := 0
	switchFn := func() (string, error) { ran++; return "2", nil }
	held, err := EngineSwitch(path, clk, settings.Default(), nil, switchFn)
	if err != nil || ran != 0 || held != "a session is starting; held for another 30s" {
		t.Fatalf("held %q, err %v, ran %d", held, err, ran)
	}
	if err := HandSwitch(path, clk, nil, switchFn); err != nil || ran != 1 {
		t.Fatalf("hand switch: err %v, ran %d", err, ran)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeHolds(t, path, map[string]any{"session": clock.Seconds(clk) + 30})
	clk.Advance(31 * time.Second)
	if held, err := EngineSwitch(path, clk, settings.Default(), nil, switchFn); err != nil || held != "" || ran != 2 {
		t.Fatalf("after the hold: held %q, err %v, ran %d", held, err, ran)
	}
}

// A hold that ends more than the bound from now was not written by this
// clock; it holds nothing (DESIGN A105).
func TestStartHoldBeyondTheBoundHoldsNothing(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(usageOf(0, 100)), dictEntry(usageOf(0, 10)))
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false)
	writeHolds(t, e.statePath, map[string]any{"elsewhere": e.nowSeconds() + 3600})
	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, events %v", got, rec.kinds())
	}
}

func TestReleaseStartHold(t *testing.T) {
	clk := newClk()
	path := filepath.Join(t.TempDir(), StateFilename)
	now := clock.Seconds(clk)
	if err := os.WriteFile(path, []byte(`{"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeHolds(t, path, map[string]any{"mine": now + 30, "other": now + 20, "old": now - 1})
	if found, err := ReleaseStartHold(path, clk, "mine"); !found || err != nil {
		t.Fatalf("found %v, err %v", found, err)
	}
	state := readStateFile(path)
	if !reflect.DeepEqual(holdsOf(path), map[string]any{"other": now + 20}) || state["extra"] != true {
		t.Fatalf("state = %v", state)
	}
	before, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	clk.Advance(time.Second)
	for _, token := range []string{"mine", "unknown", ""} {
		if found, err := ReleaseStartHold(path, clk, token); found || err != nil {
			t.Fatalf("%q: found %v, err %v", token, found, err)
		}
	}
	after, _ := os.ReadFile(path)
	if later, _ := os.Stat(path); string(after) != string(before) || !later.ModTime().Equal(info.ModTime()) {
		t.Fatal("a release that found nothing wrote the state")
	}
	clk.Advance(20 * time.Second)
	if found, err := ReleaseStartHold(path, clk, "other"); found || err != nil {
		t.Fatalf("expired: found %v, err %v", found, err)
	}
	if _, has := readStateFile(path)["startHolds"]; has || readStateFile(path)["extra"] != true {
		t.Fatalf("the expired hold was kept: %v", readStateFile(path))
	}
}

// A missing or corrupt state file holds nothing; nothing is created or written.
func TestReleaseStartHoldWithoutAState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StateFilename)
	if found, err := ReleaseStartHold(path, newClk(), "mine"); found || err != nil {
		t.Fatalf("missing: found %v, err %v", found, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("created %v", entries)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := ReleaseStartHold(path, newClk(), "mine"); found || err != nil {
		t.Fatalf("corrupt: found %v, err %v", found, err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "{" {
		t.Fatalf("corrupt state rewritten: %q", raw)
	}
}

// sleepHook is a fake clock that runs hook on each sleep.
type sleepHook struct {
	*clock.Fake
	hook func()
}

func (c *sleepHook) Sleep(d time.Duration) {
	c.Fake.Sleep(d)
	if c.hook != nil {
		c.hook()
	}
}

func TestWaitStartHolds(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFilename)
	clk := &sleepHook{Fake: newClk()}
	WaitStartHolds(path, clk)
	if len(clk.Slept) != 0 {
		t.Fatalf("slept %v without a hold", clk.Slept)
	}
	writeHolds(t, path, map[string]any{"other": clock.Seconds(clk) + 0.35})
	WaitStartHolds(path, clk)
	if want := []time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond}; !reflect.DeepEqual(clk.Slept, want) {
		t.Fatalf("slept %v, want %v", clk.Slept, want)
	}
	clk.Slept = nil
	writeHolds(t, path, map[string]any{"other": clock.Seconds(clk) + 30})
	clk.hook = func() {
		if _, err := ReleaseStartHold(path, clk, "other"); err != nil {
			t.Error(err)
		}
	}
	WaitStartHolds(path, clk)
	if len(clk.Slept) != 1 {
		t.Fatalf("slept %v, want once: the other session reported", clk.Slept)
	}
	clk.Slept = nil
	start := clk.Now()
	clk.hook = func() {
		if clk.Now().Sub(start) < 40*time.Second {
			writeHolds(t, path, map[string]any{"next": clock.Seconds(clk) + 30})
		}
	}
	writeHolds(t, path, map[string]any{"first": clock.Seconds(clk) + 30})
	WaitStartHolds(path, clk)
	if waited := clk.Now().Sub(start); waited < 30*time.Second || waited > 31*time.Second {
		t.Fatalf("waited %v while sessions kept starting, want the 30 s bound", waited)
	}
}
