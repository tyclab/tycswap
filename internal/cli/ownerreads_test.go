package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/store/ownertest"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

// ownerSwitcher is the ownership fixture with auto-switch on and no OAuth
// client, so nothing is fetched.
func ownerSwitcher(t *testing.T, opts ownertest.Options) (*core.Switcher, *ownertest.Fixture) {
	t.Helper()
	testutil.IsolateHome(t)
	testutil.Setenv(t, "NO_COLOR", "1")
	sw, err := newSwitcher(store.Options{Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	f := ownertest.Build(t, sw.Store, opts)
	if err := os.WriteFile(settings.SettingsPath(sw.BackupDir()), []byte(`{"schemaVersion":1,"autoswitch":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return sw, f
}

// onceEngine is the default engine as `auto --once` builds it, recording its
// events; observe, when set, sees each event first.
func onceEngine(sw *core.Switcher, events *[]autoswitch.Event, observe func(autoswitch.Event)) *autoswitch.Engine {
	merged := settings.Load(sw.BackupDir())
	policy := defaultSessionPolicy(sw.Store, merged, sw.Clk.Now())
	return defaultEngine(sw, policy, func(ev autoswitch.Event) {
		if observe != nil {
			observe(ev)
		}
		*events = append(*events, ev)
	}, false)
}

// atLimit puts account 1, the live one, at its 7-day limit in the usage cache.
func atLimit(t *testing.T, sw *core.Switcher) {
	t.Helper()
	path := filepath.Join(sw.BackupDir(), "cache", "usage.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	row := doc["accounts"].(map[string]any)["1"].(map[string]any)
	row["lastGood"].(map[string]any)["seven_day"].(map[string]any)["pct"] = 100.0
	raw, _ = json.Marshal(doc)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func liveAccount(t *testing.T, sw *core.Switcher) string {
	t.Helper()
	email, _, _ := ccfile.ReadOAuthIdentityFrom(sw.DefaultConfigPath())
	return email
}

// The dashboard's Auto view and one scheduled tick each read every identity
// file and scan every profile's live sessions at most once (DESIGN A116).
func TestAutoViewReadsOwnershipInputsOnce(t *testing.T) {
	sw, _ := ownerSwitcher(t, ownertest.Options{Accounts: 6, Fable: true})
	a := newAutoFacade(sw, nil)
	a.managedBy = testOwner
	counts := ownertest.CountReads(t)
	a.View()
	counts.AtMostOnce(t, "Auto view")
}

func TestOnceTickReadsOwnershipInputsOnce(t *testing.T) {
	sw, _ := ownerSwitcher(t, ownertest.Options{Accounts: 6, Fable: true})
	var events []autoswitch.Event
	engine := onceEngine(sw, &events, nil)
	counts := ownertest.CountReads(t)
	if got := engine.Tick(); got != autoswitch.NoAction {
		t.Fatalf("tick = %v, want no action; events %+v", got, events)
	}
	counts.AtMostOnce(t, "tick")
	if last, ok := events[len(events)-1].(autoswitch.NoSwitchEvent); !ok || last.Reason != "below-threshold" {
		t.Fatalf("the tick did not run to its decision: %+v", events)
	}
}

// Between choosing a target and switching to it, a busy former profile signs
// in as the target: the switch sees it and does not happen.
func TestTickSeesAnIdentityChangedBeforeItSwitches(t *testing.T) {
	sw, f := ownerSwitcher(t, ownertest.Options{Accounts: 6, Journal: true})
	atLimit(t, sw)
	var events []autoswitch.Event
	changed := false
	engine := onceEngine(sw, &events, func(ev autoswitch.Event) {
		if _, poll := ev.(autoswitch.PollEvent); poll && !changed {
			changed = true
			if err := os.WriteFile(filepath.Join(f.Legacy["2"], ".claude.json"), []byte(ownertest.Identity(f.Emails["4"], "", 0)), 0o600); err != nil {
				t.Error(err)
			}
		}
	})
	engine.Tick()
	if !changed {
		t.Fatal("the tick never polled")
	}
	for _, ev := range events {
		if _, switched := ev.(autoswitch.SwitchEvent); switched {
			t.Fatalf("switched to an account a running profile signed in as: %+v", events)
		}
	}
	if got := liveAccount(t, sw); got != f.Emails["1"] {
		t.Fatalf("live login = %s, want account 1's", got)
	}
}

// One engine, two ticks: the target's former profile runs a session in the
// first and none in the second, which switches to it.
func TestTickSeesASessionEndedSinceTheLastTick(t *testing.T) {
	sw, f := ownerSwitcher(t, ownertest.Options{Accounts: 6, Journal: true})
	atLimit(t, sw)
	ownertest.LiveSession(t, f.Legacy["4"])
	var events []autoswitch.Event
	engine := onceEngine(sw, &events, nil)
	if got := engine.Tick(); got != autoswitch.Blocked {
		t.Fatalf("first tick = %v, want blocked; events %+v", got, events)
	}
	ownertest.EndSessions(t, f.Legacy["4"])
	events = nil
	if got := engine.Tick(); got != autoswitch.Switched {
		t.Fatalf("second tick = %v, want a switch; events %+v", got, events)
	}
	if got := liveAccount(t, sw); got != f.Emails["4"] {
		t.Fatalf("live login = %s, want account 4's", got)
	}
}
