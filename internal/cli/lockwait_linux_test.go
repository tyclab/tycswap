// Tests that a session placement decided inside the move guard asks for no
// lock the guard holds and decides on complete inputs, that the empty
// credential the nested read gave changed no decision, and that the store
// lock still covers the reads inside the guard (DESIGN A118).
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store/ownertest"
	"github.com/tyclab/tycswap/internal/testutil"
)

// requireOwnedAccounts fails unless a former session profile owns account 2
// and the Opus group account 5, as the ownership fixture builds them.
func requireOwnedAccounts(t *testing.T, sw *core.Switcher) {
	t.Helper()
	for num, scope := range map[string]string{"2": "legacy:2", "5": "opus"} {
		if owner, err := sw.CredentialOwner(num); err != nil || owner.Scope != scope {
			t.Fatalf("fixture: Account-%s is owned by %q (%v), want %q", num, owner.Scope, err, scope)
		}
	}
}

// startView is what a Fable start snapshot says: the inputs start's decision
// takes, its answer, and each account's usage entry as its sentinel and
// whether it has a reading to decide on.
type startView struct {
	Marked, Candidates                      []string
	Current                                 string
	CurrentDisabled, CurrentMarked, Enabled bool
	Model                                   string
	ToDefault                               bool
	Err                                     string
	Entries                                 map[string]string
}

func viewStart(snap autoswitch.StartSnapshot, toDefault bool, err error) startView {
	v := startView{Marked: snap.Marked, Candidates: snap.Candidates, Current: snap.Current, CurrentDisabled: snap.CurrentDisabled,
		CurrentMarked: snap.CurrentMarked, Enabled: snap.Settings.Enabled, ToDefault: toDefault, Entries: map[string]string{}}
	if snap.Settings.Model != nil {
		v.Model = *snap.Settings.Model
	}
	if err != nil {
		v.Err = err.Error()
	}
	for num, e := range snap.Entries {
		v.Entries[num] = fmt.Sprintf("%s/%t", e.Sentinel, e.DecisionValue() != nil)
	}
	return v
}

// outsideStart is the Fable start snapshot read outside any guard.
func outsideStart(sw *core.Switcher) startView {
	return viewStart(fableStartsOnDefault(sw, ""))
}

// watchFableStarts collects, until the test ends, every Fable start snapshot
// fableStartsOnDefault answers on and, for each, whether another holder of
// the store lock had to wait at the moment it was read.
func watchFableStarts(t *testing.T, sw *core.Switcher) (views *[]startView, lockBusy *[]bool) {
	t.Helper()
	views, lockBusy = &[]startView{}, &[]bool{}
	prevView, prevRead := observeFableStartsOnDefault, observeFableStartSnapshot
	observeFableStartsOnDefault = func(snap autoswitch.StartSnapshot, toDefault bool, err error) {
		*views = append(*views, viewStart(snap, toDefault, err))
	}
	observeFableStartSnapshot = func() { *lockBusy = append(*lockBusy, storeLockBusy(sw.LockFile)) }
	t.Cleanup(func() { observeFableStartsOnDefault, observeFableStartSnapshot = prevView, prevRead })
	return views, lockBusy
}

// storeLockBusy reports whether another goroutine, as another process would,
// fails to take the lock file at path.
func storeLockBusy(path string) bool {
	busy := make(chan bool)
	go func() {
		l := filelock.New(path, 50*time.Millisecond)
		ok, err := l.Acquire(0)
		if ok {
			l.Release()
		}
		busy <- !ok && err == nil
	}()
	return <-busy
}

func inspectLink(t *testing.T, sw *core.Switcher, id string) reporting.SessionLink {
	t.Helper()
	rec, err := reporting.InspectSessionLink(sw.BackupDir(), id, sw.Clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// Each of the five entries that decide a session's move inside the move guard
// (DESIGN A109), on the ownership fixture with accounts a former session
// profile and the Opus group own, moves a session to the Fable store without
// asking for a lock the guard holds. The Fable start snapshot it decides on
// is the one read outside the guard, it is read while the store lock is held,
// and the move goes where the decision outside the guard sends it.
func TestFableMovesDecideWithoutANestedLock(t *testing.T) {
	for _, entry := range []struct {
		name     string
		model    string // the linked session's
		explicit bool
		// allocated allocates the Fable store's account to Opus: the move asks.
		allocated bool
		run       func(t *testing.T, sw *core.Switcher, s linked, model string)
	}{
		{name: "the tick", model: "claude-fable-5-2", run: func(t *testing.T, sw *core.Switcher, _ linked, _ string) {
			placeFableSessions(sw)
		}},
		{name: "the status line", model: "claude-fable-5-2", run: func(t *testing.T, sw *core.Switcher, s linked, model string) {
			if _, err := evaluateSessionPlacement(sw, s.id, s.link, model, "", true, "", "status line"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "tycswap use fable", model: "claude-opus-5-5", explicit: true, run: func(t *testing.T, sw *core.Switcher, s linked, _ string) {
			if code, _, errb := runCLI(t, []string{"use", "fable", "--session", s.id}, false, false); code != 0 {
				t.Fatalf("use fable = %d: %s", code, errb)
			}
		}},
		{name: "the dashboard's move route", model: "claude-opus-5-5", explicit: true, run: func(t *testing.T, sw *core.Switcher, s linked, _ string) {
			if err := (&dashboard{sw: sw}).UseSession(s.id, "fable", "dashboard"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "the allocation answer", model: "claude-fable-5-2", allocated: true, run: func(t *testing.T, sw *core.Switcher, s linked, model string) {
			q := pendingQuestion(t, sw, s, model)
			if err := answerSessionQuestion(sw, q, true, nil); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			sw, _ := costFixture(t, 0)
			requireOwnedAccounts(t, sw)
			if entry.allocated {
				setRecord(t, sw, "6", "models", []any{"opus"})
			}
			s := linkSession(t, sw, costSessionID(0), entry.model, sw.DefaultProfileDir(), "")
			rec := inspectLink(t, sw, s.id)
			model := "fable"
			if entry.explicit {
				model = explicitSessionModel("fable", rec.Family)
			}
			want, _, err := sessionDestination(placementFor(sw.Store, false), &rec, "fable", model, entry.explicit)
			if err != nil || want.dir != paths.FableStoreDir(sw.BackupDir()) || (want.allocation != "") != entry.allocated {
				t.Fatalf("fixture: outside the guard the session goes to %+v (%v)", want, err)
			}
			outside := outsideStart(sw)

			views, busy := watchFableStarts(t, sw)
			nested := testutil.NestedLocks(t)
			entry.run(t, sw, s, entry.model)
			if got := nested(); len(got) != 0 {
				t.Errorf("the decision asked %d times for a lock its goroutine holds; the first:\n%s", len(got), got[0])
			}
			if len(*views) == 0 {
				t.Fatal("no Fable start snapshot was read")
			}
			for i, got := range *views {
				if !reflect.DeepEqual(got, outside) {
					t.Errorf("inside the guard the Fable start snapshot reads\n%+v\noutside\n%+v", got, outside)
				}
				if !(*busy)[i] {
					t.Error("the store lock was free while the decision read the Fable start snapshot")
				}
			}
			requireLinkTarget(t, s.link, want.dir)
		})
	}
}

// pendingQuestion is the allocation question the status line puts for s.
func pendingQuestion(t *testing.T, sw *core.Switcher, s linked, model string) sessionQuestion {
	t.Helper()
	result, err := evaluateSessionPlacement(sw, s.id, s.link, model, "", false, "", "status line")
	if err != nil || result.question == nil {
		t.Fatalf("fixture: the status line put no allocation question: %+v %v", result, err)
	}
	requireLinkTarget(t, s.link, sw.DefaultProfileDir())
	return *result.question
}

// decisions is every answer a session placement takes from what does not
// depend on the session (the Fable start snapshot, the Fable room rule, the
// Mythos move placement) and each destination's for each session.
type decisions struct {
	Start      startView
	Room       string
	Mythos     string
	Placements map[string]string
}

func decide(sw *core.Switcher, sessions map[string]reporting.SessionLink) decisions {
	d := decisions{Start: outsideStart(sw), Placements: map[string]string{}}
	toDefault, notice, hinted := fableSessionOnDefault(sw.Store)
	d.Room = fmt.Sprintf("%v %q %v", toDefault, notice, hinted)
	target, err := mythosMovePlacement(placementFor(sw.Store, false).view)
	d.Mythos = fmt.Sprintf("%+v %v", target, err)
	for name, rec := range sessions {
		auto := reporting.SessionLoginForFamily(rec.Family)
		d.Placements[name+" automatic"] = destination(sw, rec, auto, rec.Family, false)
		for _, dest := range []string{"mythos", "fable", "default"} {
			d.Placements[name+" use "+dest] = destination(sw, rec, dest, explicitSessionModel(dest, rec.Family), true)
		}
	}
	return d
}

func destination(sw *core.Switcher, rec reporting.SessionLink, dest, model string, explicit bool) string {
	target, identity, err := sessionDestination(placementFor(sw.Store, false), &rec, dest, model, explicit)
	return fmt.Sprintf("%+v %s %v", target, identity, err)
}

// expireCredentials gives the credential at path an expiry in the past.
func expireCredentials(t *testing.T, path string) {
	t.Helper()
	raw := readOrAbsent(t, path)
	expired := strings.Replace(raw, `"expiresAt":4102444800000`, `"expiresAt":1000`, 1)
	if expired == raw {
		t.Fatalf("fixture: no far expiry in %s", path)
	}
	writeFile(t, path, expired)
}

// What the nested read's empty credential changed (DESIGN A118): before the
// fix every read of an owned account's credential inside the move guard
// waited for the lock the guard held and came back empty. Inside the guard,
// with that read failing as it failed then, every placement answer equals
// the one read outside it, in states that vary each input the credential
// feeds and each the decisions read beside it. The snapshot's usage entries
// of the owned accounts do differ (no credentials); no decision reads them.
func TestTheEmptyOwnedCredentialChangedNoDecision(t *testing.T) {
	owned := func(f *ownertest.Fixture) []string {
		return []string{filepath.Join(f.Legacy["2"], ".credentials.json"), filepath.Join(f.Legacy["3"], ".credentials.json"), filepath.Join(f.Group, ".credentials.json")}
	}
	for _, state := range []struct {
		name    string
		prepare func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture)
	}{
		{name: "as built"},
		{name: "owned logins expired", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			for _, path := range owned(f) {
				expireCredentials(t, path)
			}
		}},
		{name: "owned logins gone", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			for _, path := range owned(f) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "owned accounts at their limits", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			for _, num := range []string{"2", "3", "5"} {
				setUsage(t, sw, num, 100)
			}
		}},
		{name: "owned account with a Fable login", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			giveFableLogin(t, sw, f, "2")
		}},
		{name: "owned account's Fable login to sign in again", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			giveFableLogin(t, sw, f, "2")
			if err := autoswitch.Quarantine(fableStatePath(sw), sw.Clk, "2", f.Emails["2"], "invalid_grant", ownertest.Creds("fable-2"), func(autoswitch.Event) {}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "owned accounts on a plan without Fable", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			for _, num := range []string{"2", "3", "5"} {
				setRecord(t, sw, num, "plan", "Pro", "fableContinue", false)
			}
		}},
		{name: "no start login in the Fable pool", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			setRecord(t, sw, "6", "startLogin", false)
			setRecord(t, sw, "7", "startLogin", false)
		}},
		{name: "Mythos logins spent", prepare: spendMythos},
	} {
		t.Run(state.name, func(t *testing.T) {
			sw, f := costFixture(t, 0)
			requireOwnedAccounts(t, sw)
			// Before the fix a nested request waits out the store lock's
			// timeout; this one gives up after a tenth of a second.
			sw.Lock = filelock.New(sw.LockFile, 100*time.Millisecond)
			root, def, fable := sw.BackupDir(), sw.DefaultProfileDir(), paths.FableStoreDir(sw.BackupDir())
			sessions := map[string]reporting.SessionLink{}
			var anchor linked
			for i, s := range []struct{ name, model, target string }{
				{"fable on the default login", "claude-fable-5-2", def},
				{"fable on the Fable store", "claude-fable-5-2", fable},
				{"opus on the default login", "claude-opus-5-5", def},
				{"mythos on account 7's login", "claude-mythos-5-2", f.Mythos["7"]},
			} {
				l := linkSession(t, sw, costSessionID(200+i), s.model, s.target, "")
				sessions[s.name], anchor = inspectLink(t, sw, l.id), l
			}
			if state.prepare != nil {
				state.prepare(t, sw, f)
			}
			outside := decide(sw, sessions)
			nested := testutil.NestedLocks(t)
			var inside decisions
			if err := reporting.UpdateSessionLink(root, anchor.id, anchor.link, sw.Clk.Now(), func(*reporting.SessionLink) error {
				inside = decide(sw, sessions)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(nested()) == 0 {
				t.Fatal("inside the guard no read asked for the lock the guard holds: nothing was compared")
			}
			for num, got := range inside.Start.Entries {
				if want := outside.Start.Entries[num]; got != want {
					t.Logf("Account-%s's usage entry in the Fable start snapshot: %s inside the guard, %s outside", num, got, want)
				}
			}
			inside.Start.Entries, outside.Start.Entries = nil, nil
			if !reflect.DeepEqual(inside, outside) {
				t.Errorf("inside the guard\n%+v\noutside\n%+v", inside, outside)
			}
		})
	}
}

// giveFableLogin gives account num a Fable login.
func giveFableLogin(t *testing.T, sw *core.Switcher, f *ownertest.Fixture, num string) {
	t.Helper()
	fable, err := sw.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	if err := fable.Creds.WriteBackup(num, f.Emails[num], ownertest.Creds("fable-"+num)); err != nil {
		t.Fatal(err)
	}
	if !sw.HasFableLogin(num) {
		t.Fatalf("fixture: Account-%s has no Fable login", num)
	}
}

// Outside the move guard the Fable start snapshot reads an owned account's
// credential under the store lock as before: while another holder has the
// lock the read gives up, and the snapshot shows the account's usage entry
// without its credential, as the nested read left it.
func TestTheFableStartSnapshotOutsideTheGuardTakesTheStoreLock(t *testing.T) {
	sw, _ := costFixture(t, 0)
	requireOwnedAccounts(t, sw)
	sw.Lock = filelock.New(sw.LockFile, 100*time.Millisecond)
	free := outsideStart(sw)
	release := testutil.HoldLock(t, sw.LockFile)
	held := outsideStart(sw)
	release()
	for _, num := range []string{"2", "3", "5"} {
		if !strings.HasPrefix(free.Entries[num], "/") || !strings.HasPrefix(held.Entries[num], jsonout.UsageNoCredentials+"/") {
			t.Errorf("Account-%s's usage entry: %s with the store lock free, %s while another holder has it", num, free.Entries[num], held.Entries[num])
		}
	}
	if again := outsideStart(sw); !reflect.DeepEqual(again, free) {
		t.Errorf("after the holder released the lock the snapshot reads\n%+v\nwant\n%+v", again, free)
	}
}
