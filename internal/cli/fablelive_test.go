// Tests that the Fable tick checks the Fable store's live login itself and
// leaves a dead one (DESIGN A102).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
)

// deadLiveFixture: the default login on 1, the Fable store on 2 with a live
// Fable login that expired an hour ago, Fable logins of 2 and others, every
// account with room. Once *dead is set, a refresh of 2's Fable login answers
// invalid_grant; every other refresh fails transiently.
func deadLiveFixture(t *testing.T, others []string, dead *bool) (*core.Switcher, *refreshes) {
	t.Helper()
	fx := fableTickFixture{def: "1", live: "2", logins: append([]string{"2"}, others...),
		usage: map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 10}, "3": {10, 20, 10}, "4": {10, 20, 50}},
		refresh: func(creds string) oauth.RefreshOutcome {
			if *dead && strings.Contains(creds, `"rt-f2"`) {
				return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
			}
			return oauth.RefreshOutcome{Error: oauth.ErrTransient}
		}}
	sw, asked := fx.build(t)
	putLiveFableLogin(t, sw, fableLoginOf("2", sw.Clk.Now().Add(-time.Hour).UnixMilli()))
	return sw, asked
}

// putLiveFableLogin makes login Account-2's Fable login and the Fable store's.
func putLiveFableLogin(t *testing.T, sw *core.Switcher, login string) {
	t.Helper()
	f, _ := sw.Store.ForFable()
	if err := f.WriteAccountCredentials("2", fableEmail("2"), login); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"), login)
}

// defaultStoreFiles is the default login's files and the default store's state.
func defaultStoreFiles(t *testing.T, sw *core.Switcher) map[string]string {
	t.Helper()
	files := defaultFiles(t, sw.Home)
	files["state"] = readOrAbsent(t, autoswitch.StatePath(sw.BackupDir()))
	return files
}

// runDeadLive runs one tick with the live Fable login alive, then one with it
// dead, and checks the second left the default store byte for byte as it was.
func runDeadLive(t *testing.T, sw *core.Switcher, dead *bool) (int, []map[string]any) {
	t.Helper()
	runCodex(t, "", "auto", "--once", "--json")
	before := defaultStoreFiles(t, sw)
	*dead = true
	code, out, _ := runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ := splitByStore(t, out)
	if after := defaultStoreFiles(t, sw); !sameStrings(before, after) {
		t.Errorf("the default store changed:\nbefore %v\nafter  %v", before, after)
	}
	if q := eventOf(fable, "account-quarantined"); q == nil || q["number"] != "2" || q["reason"] != "invalid_grant" {
		t.Errorf("Fable events %v, want Account-2 quarantined", fable)
	}
	entries := autoswitch.ReadQuarantineEntries(fableStatePath(sw))
	if entries["2"].Reason != "invalid_grant" {
		t.Errorf("Fable quarantine %v", entries)
	}
	return code, fable
}

// A live Fable login whose refresh answers invalid_grant is quarantined, and
// the Fable store moves to the other account with a usable Fable login.
func TestDeadLiveFableLoginMovesTheFableStore(t *testing.T) {
	dead := false
	sw, asked := deadLiveFixture(t, []string{"3"}, &dead)
	code, fable := runDeadLive(t, sw, &dead)
	if got := refreshedSlots(asked); !strings.Contains(got, "2") {
		t.Errorf("refreshed %q; the expired live login was not checked", got)
	}
	ev := eventOf(fable, "switch")
	from, _ := ev["from"].(map[string]any)
	to, _ := ev["to"].(map[string]any)
	if code != 0 || ev == nil || from["number"] != float64(2) || to["number"] != float64(3) {
		t.Fatalf("exit %d, Fable events %v", code, fable)
	}
	if got := storeLive(t, sw); !strings.Contains(got, "at-f3") {
		t.Fatalf("Fable store holds %s, want Account-3's Fable login", got)
	}
	if st := sw.Store.FableStore(); !st.Usable || st.Live.Slot != "3" {
		t.Fatalf("FableStore = %+v", st)
	}
	if log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")); !strings.Contains(log, "Switched the Fable store to account 3 by engine:failover") {
		t.Fatalf("log:\n%s", log)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(readOrAbsent(t, fableStatePath(sw))), &state); err != nil || state["lastSwitchTo"] != "3" || state["lastSwitchBy"] != nil {
		t.Fatalf("Fable state %v (%v), want the move stamped as the engine's own", state, err)
	}
}

// Auto-switch turned off while the tick refreshes the expired live Fable
// login: the dead login is quarantined, as the engine quarantines a dead
// target, but the store is neither moved nor emptied, as the engine's own
// switch re-reads the key under its lock (DESIGN A83, A102).
func TestDeadLiveFableLoginStaysOnceAutoSwitchIsTurnedOff(t *testing.T) {
	for name, others := range map[string][]string{"move": {"3"}, "empty": nil} {
		t.Run(name, func(t *testing.T) {
			var root string
			dead := false
			fx := fableTickFixture{def: "1", live: "2", logins: append([]string{"2"}, others...),
				usage: map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 10}, "3": {10, 20, 10}, "4": {10, 20, 50}},
				refresh: func(creds string) oauth.RefreshOutcome {
					if !dead || !strings.Contains(creds, `"rt-f2"`) {
						return oauth.RefreshOutcome{Error: oauth.ErrTransient}
					}
					if _, err := settings.SetSetting(root, "autoswitch.enabled", "false"); err != nil {
						t.Error(err)
					}
					return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
				}}
			sw, _ := fx.build(t)
			root = sw.BackupDir()
			putLiveFableLogin(t, sw, fableLoginOf("2", sw.Clk.Now().Add(-time.Hour).UnixMilli()))
			runCodex(t, "", "auto", "--once", "--json")
			f, _ := sw.Store.ForFable()
			files := func() map[string]string {
				m := defaultStoreFiles(t, sw)
				m["live"], m["chain"] = storeLive(t, sw), readOrAbsent(t, filepath.Join(f.ScopeRoot(), "live_lineage.json"))
				for _, num := range []string{"2", "3"} {
					m["fable"+num], _ = f.ReadAccountCredentials(num, fableEmail(num))
				}
				return m
			}
			before := files()
			dead = true
			_, out, _ := runCodex(t, "", "auto", "--once", "--json")
			_, fable, _ := splitByStore(t, out)
			if q, ns := eventOf(fable, "account-quarantined"), eventOf(fable, "no-switch"); q == nil || ns == nil || ns["reason"] != "disabled" || eventOf(fable, "switch") != nil {
				t.Fatalf("Fable events %v", fable)
			}
			if after := files(); !sameStrings(before, after) {
				t.Fatalf("written while off:\nbefore %v\nafter  %v", before, after)
			}
			var state map[string]any
			if err := json.Unmarshal([]byte(readOrAbsent(t, fableStatePath(sw))), &state); err != nil || state["lastSwitchTo"] != nil {
				t.Fatalf("Fable state %v (%v), want no switch stamped", state, err)
			}
			if st := sw.Store.FableStore(); !st.On || st.Live.Slot != "2" {
				t.Fatalf("FableStore = %+v, want still on Account-2", st)
			}
		})
	}
}

// The move off a dead live login goes to the account the cached usage ranks
// first, not to the pool's first: 3 has no Fable room left, 4 has.
func TestDeadLiveFableLoginMovesToTheBestRankedAccount(t *testing.T) {
	fx := fableTickFixture{def: "1", live: "2", logins: []string{"2", "3", "4"},
		usage: map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 10}, "3": {10, 20, 100}, "4": {10, 20, 10}},
		refresh: func(creds string) oauth.RefreshOutcome {
			if strings.Contains(creds, `"rt-f2"`) {
				return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
			}
			return oauth.RefreshOutcome{Error: oauth.ErrTransient}
		}}
	sw, _ := fx.build(t)
	putLiveFableLogin(t, sw, fableLoginOf("2", sw.Clk.Now().Add(-time.Hour).UnixMilli()))
	_, out, _ := runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ := splitByStore(t, out)
	ev := eventOf(fable, "switch")
	to, _ := ev["to"].(map[string]any)
	if ev == nil || ev["trigger"] != "failover" || to["number"] != float64(4) {
		t.Fatalf("Fable events %v, want the failover to Account-4", fable)
	}
}

// With every account of the pool at its limit the move off a dead live login
// still takes the pool's first: start's ranking of logins at a limit (DESIGN
// A105) does not reach it.
func TestDeadLiveFableLoginWithEveryAccountAtItsLimit(t *testing.T) {
	fx := fableTickFixture{def: "1", live: "2", logins: []string{"2", "3", "4"},
		usage: map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 10}, "3": {10, 20, 100}, "4": {10, 100, 100}},
		refresh: func(creds string) oauth.RefreshOutcome {
			if strings.Contains(creds, `"rt-f2"`) {
				return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
			}
			return oauth.RefreshOutcome{Error: oauth.ErrTransient}
		}}
	sw, _ := fx.build(t)
	putLiveFableLogin(t, sw, fableLoginOf("2", sw.Clk.Now().Add(-time.Hour).UnixMilli()))
	_, out, _ := runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ := splitByStore(t, out)
	ev := eventOf(fable, "switch")
	to, _ := ev["to"].(map[string]any)
	if ev == nil || ev["trigger"] != "failover" || to["number"] != float64(3) {
		t.Fatalf("Fable events %v, want the failover to Account-3", fable)
	}
}

// A live Fable login with no refresh token cannot be refreshed: it is
// quarantined and left as an invalid_grant one is (DESIGN A102).
func TestLiveFableLoginWithoutARefreshTokenIsLeft(t *testing.T) {
	fx := fableTickFixture{def: "1", live: "2", logins: []string{"2", "3"},
		usage: map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 10}, "3": {10, 20, 10}, "4": {10, 20, 50}},
		refresh: func(creds string) oauth.RefreshOutcome {
			if !strings.Contains(creds, `"refreshToken"`) {
				return oauth.RefreshOutcome{Error: oauth.ErrNoRefreshToken}
			}
			return oauth.RefreshOutcome{Error: oauth.ErrTransient}
		}}
	sw, asked := fx.build(t)
	expired := strconv.FormatInt(sw.Clk.Now().Add(-time.Hour).UnixMilli(), 10)
	putLiveFableLogin(t, sw, `{"claudeAiOauth":{"accessToken":"at-f2","expiresAt":`+expired+`}}`)
	code, out, _ := runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ := splitByStore(t, out)
	if len(asked.all()) == 0 {
		t.Fatal("the expired live login was not checked")
	}
	if q := eventOf(fable, "account-quarantined"); q == nil || q["number"] != "2" {
		t.Fatalf("Fable events %v, want Account-2 quarantined", fable)
	}
	if ev := eventOf(fable, "switch"); code != 0 || ev == nil || ev["trigger"] != "failover" || !strings.Contains(storeLive(t, sw), "at-f3") {
		t.Fatalf("exit %d, Fable events %v, store %s", code, fable, storeLive(t, sw))
	}
}

// With no other usable Fable login the store is emptied: start sends Fable
// sessions to the default login and says why.
func TestDeadLiveFableLoginWithNoOtherEmptiesTheFableStore(t *testing.T) {
	dead := false
	sw, _ := deadLiveFixture(t, nil, &dead)
	code, fable := runDeadLive(t, sw, &dead)
	if ns := eventOf(fable, "no-switch"); code != 3 || ns == nil || ns["reason"] != "no-viable-target" || eventOf(fable, "switch") != nil {
		t.Fatalf("exit %d, Fable events %v", code, fable)
	}
	st := sw.Store.FableStore()
	if st.On || st.Usable {
		t.Fatalf("FableStore = %+v, want off", st)
	}
	f, _ := sw.Store.ForFable()
	if got, _ := f.ReadAccountCredentials("2", fableEmail("2")); !strings.Contains(got, "at-f2") {
		t.Fatalf("Account-2's Fable login %q, want it kept", got)
	}
	_, onFable, notice := startEnvironment(os.Environ(), []string{"claude", "--model", "fable"}, st, true, fableStoreDir())
	if onFable || !strings.Contains(notice, "the Fable store has no usable login (the store is empty); starting on the default login.") {
		t.Fatalf("start: on the Fable store %v, notice %q", onFable, notice)
	}
}

// A live Fable login already quarantined as dead (a keep-alive or an earlier
// tick recorded it) is left at once, with no refresh, even before it expires.
func TestQuarantinedLiveFableLoginIsLeftWithoutARefresh(t *testing.T) {
	fx := atFableLimit
	fx.usage = map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 10}, "3": {10, 20, 10}, "4": {10, 20, 50}}
	sw, asked := fx.build(t)
	if err := autoswitch.Quarantine(fableStatePath(sw), sw.Clk, "2", fableEmail("2"), oauth.ErrInvalidGrant, fableLoginOf("2", farExpiry), func(autoswitch.Event) {}); err != nil {
		t.Fatal(err)
	}
	if st := sw.Store.FableStore(); st.Usable {
		t.Fatalf("precondition: FableStore = %+v", st)
	}
	code, out, _ := runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ := splitByStore(t, out)
	if ev := eventOf(fable, "switch"); code != 0 || ev == nil || ev["trigger"] != "failover" {
		t.Fatalf("exit %d, Fable events %v", code, fable)
	}
	if got := refreshedSlots(asked); got != "" {
		t.Fatalf("refreshed %q", got)
	}
	if st := sw.Store.FableStore(); !st.Usable || st.Live.Slot != "3" {
		t.Fatalf("FableStore = %+v", st)
	}
}

// A switch of the Fable store by hand holds the move off its dead login for
// the cooldown, as it holds the tick (A94); the login is quarantined at once,
// so start already sends Fable sessions to the default login.
func TestDeadLiveFableLoginMoveWaitsOutAHandSwitch(t *testing.T) {
	dead := false
	sw, _ := deadLiveFixture(t, []string{"3"}, &dead)
	writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"), fableLoginOf("3", farExpiry))
	if r, err := sw.SwitchFable("2", true); err != nil || r["switched"] != true {
		t.Fatalf("hand switch: %v, %v", r, err)
	}
	dead = true
	_, out, _ := runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ := splitByStore(t, out)
	held := eventOf(fable, "no-switch")
	if eventOf(fable, "account-quarantined") == nil || eventOf(fable, "switch") != nil || held == nil ||
		held["reason"] != "cooldown" || held["detail"] != "switched by hand to account 2; held for another 300s" {
		t.Fatalf("Fable events %v", fable)
	}
	if st := sw.Store.FableStore(); !st.On || st.Usable || st.Live.Slot != "2" {
		t.Fatalf("FableStore = %+v, want on Account-2 and unusable", st)
	}
}

// Two ticks at once (the timer and a status line) both find the live Fable
// login dead. The second one's move, computed before the first moved the
// store, finds under the state lock that the store has left the dead login:
// it does nothing and says nothing, whatever the ranking now says (F2-6).
func TestASecondMoveOffADeadFableLoginFindsItLeft(t *testing.T) {
	for name, others := range map[string][]string{"move": {"3", "4"}, "empty": nil} {
		t.Run(name, func(t *testing.T) {
			fx := fableTickFixture{def: "4", live: "2", logins: append([]string{"2"}, others...),
				usage:   map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 10}, "3": {10, 20, 10}, "4": {10, 50, 50}},
				refresh: func(string) oauth.RefreshOutcome { return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant} }}
			sw, _ := fx.build(t)
			putLiveFableLogin(t, sw, fableLoginOf("2", sw.Clk.Now().Add(-time.Hour).UnixMilli()))
			a, err := newFableAdapter(sw, sw.Log)
			if err != nil {
				t.Fatal(err)
			}
			s := settings.Load(sw.BackupDir())
			names := reporting.FableWindowNames(sw.Store, sw.Clk.Now())
			model := strings.Join(names, ",")
			s.Model = &model
			var events []map[string]any
			emit := func(ev autoswitch.Event) { events = append(events, ev.JSON()) }
			checkFableLive(a, sw, s, names, emit)
			first, logPath := storeLive(t, sw), filepath.Join(sw.BackupDir(), "tycswap.log")
			log, state := readOrAbsent(t, logPath), readOrAbsent(t, fableStatePath(sw))
			// The second process read the same dead login; then a usage fetch
			// ranks 4 first.
			cache := filepath.Join(sw.BackupDir(), "cache", "usage.json")
			for _, ranking := range []string{"same", "changed"} {
				if ranking == "changed" {
					writeFile(t, cache, strings.ReplaceAll(readOrAbsent(t, cache), `"pct":50`, `"pct":1`))
				}
				events = nil
				if moved, on := leaveDeadFableLogin(a, sw, "2", fableEmail("2"), s, names, emit); moved || !on || len(events) != 0 {
					t.Fatalf("second move, %s ranking: moved=%v on=%v events %v", ranking, moved, on, events)
				}
				if storeLive(t, sw) != first || readOrAbsent(t, logPath) != log || readOrAbsent(t, fableStatePath(sw)) != state {
					t.Fatalf("second move, %s ranking, changed the store, the log or the state:\n%s", ranking, strings.TrimPrefix(readOrAbsent(t, logPath), log))
				}
			}
		})
	}
}
