package autoswitch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

func TestStartTargets(t *testing.T) {
	set := settings.Default()
	entries := map[string]usage.UsageEntry{
		"1": dictEntry(usageOf(90, 30)), "2": nilEntry(),
		"3": dictEntry(usageOf(20, 30)), "4": dictEntry(usageOf(20, 30)),
		"5": dictEntry(usageOf(100, 0)), "6": dictEntry(usageOf(5, 10)),
	}
	got := StartTargets([]string{"1", "2", "3", "4", "5", "6"}, entries, set, nil)
	if want := []string{"6", "3", "4", "1", "2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	entries["6"] = dictEntry(map[string]any{"seven_day": win(10, ""), "scoped": []any{scopedWin("Opus", 20, "")}})
	if got := StartTargets([]string{"1", "2", "6"}, entries, set, []string{"all"}); !reflect.DeepEqual(got, []string{"6"}) {
		t.Fatalf("model coverage = %v", got)
	}
	if got := StartTargets([]string{"1", "6"}, entries, set, []string{"missing"}); len(got) != 0 {
		t.Fatalf("invented model coverage: %v", got)
	}
}

type startTestSwitcher struct {
	*fakeSwitcher
	guard sync.Mutex
	moves int
}

func (f *startTestSwitcher) CurrentAccountNumber() *string {
	f.guard.Lock()
	defer f.guard.Unlock()
	return strp(deref(f.current))
}

func (f *startTestSwitcher) SwitchTo(num string, jsonOut bool) (map[string]any, error) {
	f.guard.Lock()
	defer f.guard.Unlock()
	if deref(f.current) == num {
		return map[string]any{"switched": false}, nil
	}
	result, err := f.fakeSwitcher.SwitchTo(num, jsonOut)
	if err == nil {
		f.moves++
	}
	return result, err
}

func startTestSetup(t *testing.T) (*startTestSwitcher, *clock.Fake, string, func() (StartSnapshot, error)) {
	t.Helper()
	clk := newClk()
	f := &startTestSwitcher{fakeSwitcher: twoAccounts(clk, dictEntry(usageOf(50, 50)), dictEntry(usageOf(10, 10)))}
	path := filepath.Join(t.TempDir(), StateFilename)
	snapshot := func() (StartSnapshot, error) {
		current := deref(f.CurrentAccountNumber())
		set := settings.Default()
		set.Enabled = true
		return StartSnapshot{Current: current, CurrentMarked: current == "2", Marked: []string{"2"},
			Candidates: []string{"2"}, Entries: f.entries, Settings: set}, nil
	}
	return f, clk, path, snapshot
}

func TestStartSwitchAndNoop(t *testing.T) {
	f, clk, path, snapshot := startTestSetup(t)
	if err := os.WriteFile(path, []byte(`{"active":{"number":"1"},"quarantine":{"9":{"reason":"held"}},"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := fakeOAuth(func(string) oauth.RefreshOutcome {
		t.Fatal("unexpected network refresh")
		return oauth.RefreshOutcome{}
	})
	for i := 0; i < 2; i++ {
		out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path), WithOAuthClient(client))
		if i == 0 && (!out.Switched || out.To != "2") || i == 1 && (out.Switched || out.Detail != "") {
			t.Fatalf("attempt %d: %+v", i, out)
		}
	}
	state := readStateFile(path)
	if f.moves != 1 || state["lastSwitchTo"] != "2" || state["lastSwitchAt"] != clock.Seconds(clk) || state["active"] != nil || state["extra"] != true || !quarantinedSet(state)["9"] {
		t.Fatalf("moves=%d state=%v", f.moves, state)
	}
}

func TestStartConcurrent(t *testing.T) {
	f, clk, path, snapshot := startTestSetup(t)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path))
			if out.Reason != "" && out.Reason != "already-start-login" {
				t.Errorf("concurrent start: %+v", out)
			}
		})
	}
	workers.Wait()
	if f.moves != 1 {
		t.Fatalf("moves=%d, want one", f.moves)
	}
}

func TestStartConcurrentWithEngineSwitch(t *testing.T) {
	f, clk, path, snapshot := startTestSetup(t)
	set := settings.Default()
	set.Enabled = true
	engine := NewEngine(f, set, func(Event) {}, false, WithClock(clk), WithStatePath(path))
	var workers sync.WaitGroup
	workers.Go(func() {
		StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path))
	})
	workers.Go(func() {
		if _, err := engine.perform("2", f.emails["2"], "proactive", "7d"); err != nil {
			t.Error(err)
		}
	})
	workers.Wait()
	if f.moves != 1 || readStateFile(path)["lastSwitchTo"] != "2" {
		t.Fatalf("moves=%d state=%v", f.moves, readStateFile(path))
	}
}

func TestStartRechecksOffAndExplicitException(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "off-before-switch", true: "explicit-with-off"}[explicit], func(t *testing.T) {
			f, clk, path, original := startTestSetup(t)
			reads := 0
			snapshot := func() (StartSnapshot, error) {
				reads++
				snap, _ := original()
				snap.Settings.Enabled = reads == 1 && !explicit
				return snap, nil
			}
			account := ""
			if explicit {
				account = "2"
			}
			out := StartSelection(f, account, snapshot, WithClock(clk), WithStatePath(path))
			if out.Switched != explicit {
				t.Fatalf("outcome=%+v", out)
			}
			if !explicit {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("off start stamped state: %v", err)
				}
			}
		})
	}
}

func TestStartRetriesPreparationAndQuarantines(t *testing.T) {
	for _, failure := range []string{oauth.ErrInvalidGrant, oauth.ErrTransient, "identity-conflict", "ownership"} {
		t.Run(failure, func(t *testing.T) {
			f, clk, path, original := startTestSetup(t)
			f.emails["3"], f.creds["3"] = "third@example.com", farFutureCreds(clk, "third")
			f.creds["2"] = nearExpiryCreds(clk, "second")
			f.entries["3"] = dictEntry(usageOf(20, 20))
			if failure == "ownership" {
				f.liveSessions["2"] = []int{123}
			}
			f.identities["2"] = map[string]string{"uuid": "expected"}
			snapshot := func() (StartSnapshot, error) {
				snap, _ := original()
				snap.Marked, snap.Candidates = []string{"2", "3"}, []string{"2", "3"}
				return snap, nil
			}
			client := fakeOAuth(func(string) oauth.RefreshOutcome {
				if failure == "identity-conflict" {
					return oauth.RefreshOutcome{Credentials: farFutureCreds(clk, "new"), TokenAccount: &oauth.Identity{UUID: "different"}}
				}
				return oauth.RefreshOutcome{Error: failure}
			})
			out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path), WithOAuthClient(client))
			if !out.Switched || out.To != "3" || f.moves != 1 {
				t.Fatalf("outcome=%+v moves=%d", out, f.moves)
			}
			quarantined := quarantinedSet(readStateFile(path))["2"]
			if quarantined != (failure == oauth.ErrInvalidGrant || failure == "identity-conflict") {
				t.Fatalf("quarantine=%v", quarantined)
			}
		})
	}
}

func TestStartStampFailureReportsCompletedSwitch(t *testing.T) {
	f, clk, path, snapshot := startTestSetup(t)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path))
	if !out.Switched || out.Reason != "stamp-failed" || !strings.Contains(out.Detail, "Switched") || f.moves != 1 {
		t.Fatalf("outcome=%+v moves=%d", out, f.moves)
	}
}

func TestStartFailuresAndSilentSkips(t *testing.T) {
	for _, reason := range []string{"no-start-login", "already-start-login", "auto-off", "unreadable", "switch-failed", "none-usable"} {
		t.Run(reason, func(t *testing.T) {
			f, clk, path, original := startTestSetup(t)
			if reason == "switch-failed" {
				f.switchTo = func(*fakeSwitcher, string) (map[string]any, error) { return nil, errors.New("store lock unavailable") }
			}
			snapshot := func() (StartSnapshot, error) {
				snap, _ := original()
				switch reason {
				case "no-start-login":
					snap.Marked = nil
				case "already-start-login":
					snap.CurrentMarked = true
				case "auto-off":
					snap.Settings.Enabled = false
				case "unreadable":
					return snap, errors.New("settings.json needs repair")
				case "none-usable":
					snap.Candidates = nil
				}
				return snap, nil
			}
			out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path))
			if out.Switched || f.moves != 0 {
				t.Fatalf("outcome=%+v", out)
			}
			if reason == "no-start-login" || reason == "already-start-login" || reason == "auto-off" {
				if out.Detail != "" {
					t.Fatalf("skip printed %q", out.Detail)
				}
				if _, err := os.Stat(filepath.Dir(path) + "/.autoswitch_state.lock"); !os.IsNotExist(err) {
					t.Fatalf("skip acquired lock: %v", err)
				}
			} else if out.Detail == "" {
				t.Fatal("failure was silent")
			}
		})
	}
}

func TestStartCooldownAndEngineCoordination(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(map[bool]string{false: "proactive-held", true: "limit-bypasses"}[exhausted], func(t *testing.T) {
			f, clk, path, snapshot := startTestSetup(t)
			if out := StartSelection(f, "", snapshot, WithClock(clk), WithStatePath(path)); !out.Switched {
				t.Fatal(out)
			}
			pct := 90.0
			if exhausted {
				pct = 100
			}
			f.entries["2"] = dictEntry(usageOf(pct, pct))
			f.entries["3"] = dictEntry(usageOf(5, 5))
			f.emails["3"], f.creds["3"] = "third@example.com", farFutureCreds(clk, "third")
			f.switchable = []string{"1", "2", "3"}
			set := settings.Default()
			set.Enabled = true
			rec := &recorder{}
			engine := NewEngine(f, set, rec.on, false, WithClock(clk), WithStatePath(path))
			engine.Tick()
			want := 1
			if exhausted {
				want = 2
			}
			if f.moves != want {
				t.Fatalf("moves=%d want=%d events=%v", f.moves, want, rec.events)
			}
		})
	}
}
