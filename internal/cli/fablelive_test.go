// Tests that the Fable tick checks the Fable store's live login itself and
// leaves a dead one (DESIGN A102).
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
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
	login := fableLoginOf("2", sw.Clk.Now().Add(-time.Hour).UnixMilli())
	f, _ := sw.Store.ForFable()
	if err := f.WriteAccountCredentials("2", fableEmail("2"), login); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"), login)
	return sw, asked
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
