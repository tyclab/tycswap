// Tests for what remove, move, swap and purge do with Fable logins (DESIGN
// A101).
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/storenames"
	"github.com/tyclab/tycswap/internal/switching"
)

// withFableLogins: accounts 1 and 2 hold Fable logins (each signed in twice,
// so each has a .prev); the Fable store is on Account-live, or empty for "".
func withFableLogins(t *testing.T, live string) *store.Store {
	t.Helper()
	s := fableLifecycleStore(t)
	captureOut(t)
	for _, n := range []string{"1", "2"} {
		email := map[string]string{"1": "a@example.com", "2": "b@example.com"}[n]
		for _, g := range []string{"", "b"} {
			if _, err := AddFableLogin(s, signIn(t, email, planCreds("at-f"+n+g, "rt-f"+n+g, "max")), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if live != "" {
		f, _ := s.ForFable()
		if _, err := switching.SwitchFable(f, live, switching.FableSwitchOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func fableFile(s *store.Store, num, email string) string {
	return filepath.Join(paths.FableCredentialsDir(s.BackupDir()), storenames.CredsFile(num, email))
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func readFile(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRemoveDeletesTheFableLogin(t *testing.T) {
	s := withFableLogins(t, "1")
	storeFile := filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json")
	keep := readFile(t, fableFile(s, "1", "a@example.com"))
	liveBefore := readFile(t, storeFile)
	if err := RemoveAccount(s, "2", true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fableFile(s, "2", "b@example.com"), fableFile(s, "2", "b@example.com") + ".prev"} {
		if exists(p) {
			t.Errorf("%s survived the remove", p)
		}
	}
	if readFile(t, fableFile(s, "1", "a@example.com")) != keep || readFile(t, storeFile) != liveBefore {
		t.Error("the remove touched Account-1's Fable login or the store")
	}
}

func TestRemoveOfTheFableStoresAccountEmptiesTheStore(t *testing.T) {
	s := withFableLogins(t, "2")
	others := map[string]string{}
	for _, p := range []string{fableFile(s, "1", "a@example.com"), fableFile(s, "1", "a@example.com") + ".prev"} {
		others[p] = readFile(t, p)
	}
	// Account-1's default login through the store: a file on Linux, the
	// Keychain on macOS.
	defaultBefore, err := s.ReadAccountCredentials("1", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveAccount(s, "2", true); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadAccountCredentials("1", "a@example.com"); err != nil || got != defaultBefore {
		t.Errorf("the remove changed Account-1's default login (%v)", err)
	}
	if st := s.FableStore(); st.On {
		t.Errorf("FableStore = %+v, want off", st)
	}
	for p, want := range others {
		if readFile(t, p) != want {
			t.Errorf("the remove changed %s", p)
		}
	}
	for _, p := range []string{fableFile(s, "2", "b@example.com"), fableFile(s, "2", "b@example.com") + ".prev"} {
		if exists(p) {
			t.Errorf("%s survived the remove", p)
		}
	}
	if _, ok := recordAt(readSeq(t, s), "2"); ok {
		t.Error("Account-2 is still in the roster")
	}
}

// The Fable store is on Account-2 and Claude Code rotated its login since the
// last harvest; the profile endpoint does not answer. Removing Account-2, or
// putting another account in slot 2, empties the store first: the login is the
// last install's, so it goes into Account-2's Fable login before that goes.
func TestRemovingTheFableStoresAccountEmptiesAnUnattributedLogin(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(s *store.Store) error
	}{
		{"remove", func(s *store.Store) error { return RemoveAccount(s, "2", true) }},
		{"add --slot", func(s *store.Store) error {
			_, err := AddAccountFrom(s, loginAs(t, "z@example.com", "", planCreds("at-z", "rt-z", "max")), ip(2), true, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := withFableLogins(t, "2")
			s.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity { return nil }}
			putFableStore(t, s, planCreds("at-f2-rotated", "rt-f2-rotated", "max"))
			if err := tc.act(s); err != nil {
				t.Fatal(err)
			}
			if st := s.FableStore(); st.On {
				t.Errorf("FableStore = %+v: Fable sessions keep starting on the removed account's login", st)
			}
			assertNoFableLogin(t, s, "2", "b@example.com")
			f, _ := s.ForFable()
			if rows, _ := f.Creds.ListUnclaimed(); len(rows) != 1 {
				t.Errorf("unclaimed %v, want the store's login kept", rows)
			}
		})
	}
}

func TestRemoveStopsWhenTheFableStoreCannotBeEmptied(t *testing.T) {
	s := withFableLogins(t, "2")
	prev := EmptyFableStoreWithin
	EmptyFableStoreWithin = func(s *store.Store, fn func(empty func() error) error) error {
		return prev(s, func(func() error) error {
			return fn(func() error { return errors.New("the store could not be cleared") })
		})
	}
	t.Cleanup(func() { EmptyFableStoreWithin = prev })
	before := everyFile(t, s)
	err := RemoveAccount(s, "2", true)
	if err == nil || !strings.Contains(err.Error(), "the store could not be cleared") || !strings.Contains(err.Error(), "nothing was removed") {
		t.Fatalf("RemoveAccount = %v", err)
	}
	sameFiles(t, "the failed remove", before, everyFile(t, s))
}

// The Fable store is on Account-2 by its last install only: Claude Code
// rotated the login and the profile endpoint does not answer. move, swap and
// an add --slot that moves Account-2 take the chain record along, so the
// store's login is still the moved account's last install.
func TestRenumberingFollowsTheChainRecordOfAnUnattributedLogin(t *testing.T) {
	for _, tc := range []struct {
		name, to string
		act      func(s *store.Store) error
	}{
		{"move", "5", func(s *store.Store) error { _, _, _, err := MoveAccount(s, "2", "5"); return err }},
		{"swap", "1", func(s *store.Store) error { _, _, err := SwapAccounts(s, "1", "2"); return err }},
		{"add --slot", "5", func(s *store.Store) error {
			_, err := AddAccountFrom(s, signIn(t, "b@example.com", planCreds("at-n2", "rt-n2", "max")), ip(5), true, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := withFableLogins(t, "2")
			s.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity { return nil }}
			putFableStore(t, s, planCreds("at-f2-rotated", "rt-f2-rotated", "max"))
			if err := tc.act(s); err != nil {
				t.Fatal(err)
			}
			f, _ := s.ForFable()
			if got := f.ResolveOwnerSlot(readSeq(t, s)); got.Slot != tc.to || got.Source != store.SourceLastInstall {
				t.Errorf("the store's owner = %+v, want Account-%s by last install", got, tc.to)
			}
		})
	}
}

// move, swap, add --slot and add-token --slot carry the renumbering into the
// Fable engine state: lastSeenLive and lastSwitchTo follow their account and
// every other field stays, so the next Fable tick reports no login made
// elsewhere (DESIGN A102). A state file that does not exist is not created.
func TestRenumberingCarriesTheFableEngineState(t *testing.T) {
	email, five := "b@example.com", "5"
	for _, tc := range []struct {
		name, seen, switchTo string
		act                  func(t *testing.T, s *store.Store) error
	}{
		{"move", "5", "1", func(t *testing.T, s *store.Store) error { _, _, _, err := MoveAccount(s, "2", "5"); return err }},
		{"swap", "1", "2", func(t *testing.T, s *store.Store) error { _, _, err := SwapAccounts(s, "1", "2"); return err }},
		{"add --slot", "5", "1", func(t *testing.T, s *store.Store) error {
			_, err := AddAccountFrom(s, signIn(t, email, planCreds("at-n2", "rt-n2", "max")), ip(5), true, nil)
			return err
		}},
		{"add-token --slot", "5", "1", func(t *testing.T, s *store.Store) error {
			return AddAccountFromToken(s, "sk-ant-oat01-N2", &email, &five, true)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := withFableLogins(t, "2")
			f, _ := s.ForFable()
			before := map[string]any{"schemaVersion": 1.0, "lastSeenLive": "2", "lastSeenAt": 100.0, "lastSwitchTo": "1", "lastSwitchAt": 50.0,
				"lastSwitchBy": "hand", "active": map[string]any{"number": "2", "unhealthyTicks": 1.0, "idleHoldSince": nil},
				"quarantine": map[string]any{"3": map[string]any{"email": "c@example.com", "reason": "invalid_grant", "at": "t", "refreshTokenFingerprint": nil}}}
			raw, _ := json.Marshal(before)
			if err := os.WriteFile(f.StatePath(), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := tc.act(t, s); err != nil {
				t.Fatal(err)
			}
			var after map[string]any
			if err := json.Unmarshal([]byte(readFile(t, f.StatePath())), &after); err != nil {
				t.Fatal(err)
			}
			before["lastSeenLive"], before["lastSwitchTo"] = tc.seen, tc.switchTo
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("state %v, want %v", after, before)
			}
		})
	}
	t.Run("no state file", func(t *testing.T) {
		s := withFableLogins(t, "2")
		f, _ := s.ForFable()
		if _, _, _, err := MoveAccount(s, "2", "5"); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{f.StatePath(), filepath.Join(filepath.Dir(f.StatePath()), ".autoswitch_state.lock")} {
			if exists(p) {
				t.Errorf("%s created", p)
			}
		}
	})
}

// relocate and swap carry each Fable login with its account; the chain
// record follows the Fable store's account.
func TestMoveCarriesTheFableLoginAndTheChainRecord(t *testing.T) {
	s := withFableLogins(t, "2")
	login := readFile(t, fableFile(s, "2", "b@example.com"))
	if _, _, swapped, err := MoveAccount(s, "2", "5"); err != nil || swapped {
		t.Fatalf("MoveAccount = %v, %v", swapped, err)
	}
	if exists(fableFile(s, "2", "b@example.com")) || exists(fableFile(s, "2", "b@example.com")+".prev") {
		t.Error("the Fable login stayed under the old number")
	}
	if got := readFile(t, fableFile(s, "5", "b@example.com")); got != login {
		t.Errorf("Account-5's Fable login = %q, want the moved one", got)
	}
	if exists(fableFile(s, "5", "b@example.com") + ".prev") {
		t.Error("the moved login has a .prev under the new number")
	}
	assertChain(t, s, "5")
}

func TestSwapCarriesBothFableLoginsAndTheChainRecord(t *testing.T) {
	s := withFableLogins(t, "1")
	a, b := readFile(t, fableFile(s, "1", "a@example.com")), readFile(t, fableFile(s, "2", "b@example.com"))
	if _, _, err := SwapAccounts(s, "1", "2"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, fableFile(s, "2", "a@example.com")); got != a {
		t.Errorf("a@example.com's Fable login = %q, want it under 2", got)
	}
	if got := readFile(t, fableFile(s, "1", "b@example.com")); got != b {
		t.Errorf("b@example.com's Fable login = %q, want it under 1", got)
	}
	for _, p := range []string{fableFile(s, "1", "a@example.com"), fableFile(s, "2", "b@example.com")} {
		if exists(p) {
			t.Errorf("%s survived the swap", p)
		}
	}
	assertChain(t, s, "2")
}

// assertChain checks the Fable store resolves its live login to slot by the
// chain record alone, once slot's Fable login no longer matches it.
func assertChain(t *testing.T, s *store.Store, slot string) {
	t.Helper()
	f, _ := s.ForFable()
	r, _ := recordAt(readSeq(t, s), slot)
	if err := f.Creds.DeleteBackup(slot, r.str("email")); err != nil {
		t.Fatal(err)
	}
	if st := s.FableStore(); st.Live.Slot != slot || st.Live.Source != store.SourceChain {
		t.Errorf("FableStore live = %+v, want %s by chain", st.Live, slot)
	}
}

func TestSwapFailureRollsBackBothLineages(t *testing.T) {
	s := withFableLogins(t, "1")
	before := everyFile(t, s)
	s.Creds = &fakeCreds{Store: s.Creds, failWrite: func(num, _, creds string) bool { return num == "1" && strings.Contains(creds, "rt-d2") }}
	if _, _, err := SwapAccounts(s, "1", "2"); err == nil {
		t.Fatal("the swap did not fail")
	}
	sameFiles(t, "the failed swap", before, everyFile(t, s))
}

func TestSameEmailSwapStagesAndRestoresTheFableLogins(t *testing.T) {
	s := fableLifecycleStore(t)
	captureOut(t)
	seed(t, s, ip(1),
		acct{num: "1", email: "a@example.com", org: "org-x", creds: planCreds("at-d1", "rt-d1", "max"), config: `{"oauthAccount":{}}`},
		acct{num: "2", email: "a@example.com", org: "org-y", creds: planCreds("at-d2", "rt-d2", "max"), config: `{"oauthAccount":{}}`})
	f, _ := s.ForFable()
	if err := f.SetupFableDirectories(); err != nil {
		t.Fatal(err)
	}
	for n, c := range map[string]string{"1": planCreds("at-f1", "rt-f1", "max"), "2": planCreds("at-f2", "rt-f2", "max")} {
		if err := f.WriteAccountCredentials(n, "a@example.com", c); err != nil {
			t.Fatal(err)
		}
	}
	before := everyFile(t, s)
	s.Creds = &fakeCreds{Store: s.Creds, failWrite: func(num, _, creds string) bool { return num == "1" && strings.Contains(creds, "rt-d2") }}
	if _, _, err := SwapAccounts(s, "1", "2"); err == nil {
		t.Fatal("the swap did not fail")
	}
	sameFiles(t, "the failed same-email swap", before, everyFile(t, s))

	s.Creds = s.Creds.(*fakeCreds).Store
	if _, _, err := SwapAccounts(s, "1", "2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.ReadAccountCredentials("1", "a@example.com"); !strings.Contains(got, "rt-f2") {
		t.Errorf("slot 1's Fable login = %q, want org-y's", got)
	}
	if got, _ := f.ReadAccountCredentials("2", "a@example.com"); !strings.Contains(got, "rt-f1") {
		t.Errorf("slot 2's Fable login = %q, want org-x's", got)
	}
	for _, n := range []string{"1", "2"} {
		if exists(stagingPath(f, "creds", n)) || exists(fableFile(s, n, "a@example.com")+".prev") {
			t.Errorf("slot %s kept a staging copy or a .prev", n)
		}
	}
}

// purge removes the Fable logins and the Fable store, and refuses while a
// Claude session runs and the store holds a login: a session's record does
// not name its store.
func TestPurgeRemovesTheFableStoreAndRefusesWhileASessionRuns(t *testing.T) {
	s := withFableLogins(t, "1")
	writePurgeRegistry(t, filepath.Join(s.Home, ".claude"), "sessions", "self.json", `{"pid":`+strconv.Itoa(os.Getpid())+`}`)
	before := everyFile(t, s)
	answerYes(t)
	if err := Purge(s); errKind(err) != "SessionError" || !strings.Contains(err.Error(), "Fable store") {
		t.Fatalf("purge with a live session = %v", err)
	}
	sameFiles(t, "the refused purge", before, everyFile(t, s))

	if err := os.RemoveAll(filepath.Join(s.Home, ".claude", "sessions")); err != nil {
		t.Fatal(err)
	}
	answerYes(t)
	if err := Purge(s); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{paths.FableCredentialsDir(s.BackupDir()), paths.FableScopeDir(s.BackupDir())} {
		if exists(dir) {
			t.Errorf("%s survived the purge", dir)
		}
	}
}

// Without the Fable store holding a login a live session does not stop purge,
// as before.
func TestPurgeWithAnEmptyFableStoreIgnoresSessions(t *testing.T) {
	s := withFableLogins(t, "")
	writePurgeRegistry(t, filepath.Join(s.Home, ".claude"), "sessions", "self.json", `{"pid":`+strconv.Itoa(os.Getpid())+`}`)
	answerYes(t)
	if err := Purge(s); err != nil {
		t.Fatal(err)
	}
}
