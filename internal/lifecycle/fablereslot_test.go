// Tests that a Fable login stays with the identity it was signed in as when
// add moves an account to another slot or puts another identity in a slot
// (DESIGN A101).
package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

// loginAs is a scratch profile signed in as (email, org).
func loginAs(t *testing.T, email, org, creds string) AddSource {
	t.Helper()
	dir := t.TempDir()
	cfg := `{"oauthAccount":{"emailAddress":"` + email + `","organizationUuid":"` + org + `"}}`
	for name, text := range map[string]string{".claude.json": cfg, ".credentials.json": creds} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return LoginDir(dir, nil)
}

func assertNoFableLogin(t *testing.T, s *store.Store, num, email string) {
	t.Helper()
	for _, p := range []string{fableFile(s, num, email), fableFile(s, num, email) + ".prev"} {
		if exists(p) {
			t.Errorf("%s survived", p)
		}
	}
}

// add --slot 2 puts another identity in slot 2: Account-2's Fable login goes,
// and the Fable store is emptied first when it is on Account-2.
func TestAddSlotDisplacementRemovesTheDisplacedFableLogin(t *testing.T) {
	for _, tc := range []struct{ name, live, email, org string }{
		{"same email, other organisation", "", "b@example.com", "org-new"},
		{"other email", "", "z@example.com", ""},
		{"the Fable store's account", "2", "b@example.com", "org-new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := withFableLogins(t, tc.live)
			keep := readFile(t, fableFile(s, "1", "a@example.com"))
			if _, err := AddAccountFrom(s, loginAs(t, tc.email, tc.org, planCreds("at-n", "rt-n", "max")), ip(2), true, nil); err != nil {
				t.Fatal(err)
			}
			if r := rec(t, readSeq(t, s), "2"); r.str("email") != tc.email || r.str("organizationUuid") != tc.org {
				t.Fatalf("slot 2 = %v", r.vals)
			}
			assertNoFableLogin(t, s, "2", "b@example.com")
			if s.HasFableLogin("2") {
				t.Error("the new Account-2 reads the displaced account's Fable login as its own")
			}
			if st := s.FableStore(); tc.live != "" && st.On {
				t.Errorf("FableStore = %+v, want off", st)
			}
			if readFile(t, fableFile(s, "1", "a@example.com")) != keep {
				t.Error("Account-1's Fable login changed")
			}
		})
	}
}

// add --slot 2 (and add --login --slot 2) of the identity already in slot 2
// renews its default login, which tycswap's own hints recommend: its Fable
// login and .prev stay byte for byte, wherever the Fable store is.
func TestAddSlotOfTheIdentityInTheSlotKeepsItsFableLogin(t *testing.T) {
	for _, live := range []string{"", "1", "2"} {
		t.Run("fable store on "+live, func(t *testing.T) {
			s := withFableLogins(t, live)
			file := fableFile(s, "2", "b@example.com")
			before := map[string]string{file: readFile(t, file), file + ".prev": readFile(t, file+".prev")}
			num, err := AddAccountFrom(s, signIn(t, "b@example.com", planCreds("at-renew", "rt-renew", "max")), ip(2), true, nil)
			if err != nil || num != "2" {
				t.Fatalf("AddAccountFrom = %q, %v", num, err)
			}
			for p, want := range before {
				if !exists(p) || readFile(t, p) != want {
					t.Errorf("%s changed or went by re-adding Account-2 into its own slot", p)
				}
			}
			if !s.HasFableLogin("2") {
				t.Error("HasFableLogin(2) = false")
			}
		})
	}
}

// add --slot 5 of Account-2's own identity moves Account-2 to 5: its Fable
// login moves with it and the chain record follows the Fable store.
func TestAddSlotMigrationCarriesTheFableLogin(t *testing.T) {
	s := withFableLogins(t, "2")
	login := readFile(t, fableFile(s, "2", "b@example.com"))
	if _, err := AddAccountFrom(s, signIn(t, "b@example.com", planCreds("at-n2", "rt-n2", "max")), ip(5), true, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := recordAt(readSeq(t, s), "2"); ok {
		t.Fatal("Account-2 is still in the roster")
	}
	assertNoFableLogin(t, s, "2", "b@example.com")
	if got := readFile(t, fableFile(s, "5", "b@example.com")); got != login {
		t.Errorf("Account-5's Fable login = %q, want the moved one", got)
	}
	if exists(fableFile(s, "5", "b@example.com") + ".prev") {
		t.Error("the moved login has a .prev under the new number")
	}
	assertChain(t, s, "5")
}

// An add --slot that fails after its Fable step leaves the Fable logins in
// step with the roster: Account-2's live session stops the migration at the
// default side's delete, the roster keeps Account-2 in slot 2 and so does its
// Fable login, and the retry once the session is gone carries it to 5.
func TestAFailedAddSlotMigrationLeavesTheFableLoginWithTheRoster(t *testing.T) {
	s := withFableLogins(t, "")
	login := readFile(t, fableFile(s, "2", "b@example.com"))
	startLiveSession(t, s, "2", "b@example.com")
	add := func() error {
		_, err := AddAccountFrom(s, signIn(t, "b@example.com", planCreds("at-n2", "rt-n2", "max")), ip(5), true, nil)
		return err
	}
	if err := add(); err == nil || !strings.Contains(err.Error(), "live session-mode Claude instance") {
		t.Fatalf("AddAccountFrom = %v, want the live session's refusal", err)
	}
	if _, ok := recordAt(readSeq(t, s), "2"); !ok {
		t.Fatal("Account-2 left slot 2")
	}
	if !s.HasFableLogin("2") || exists(fableFile(s, "5", "b@example.com")) {
		t.Errorf("the failed add moved Account-2's Fable login to 5 while the roster keeps it in 2")
	}
	if err := os.RemoveAll(s.SessionDir("2", "b@example.com")); err != nil {
		t.Fatal(err)
	}
	if err := add(); err != nil {
		t.Fatal(err)
	}
	if !exists(fableFile(s, "5", "b@example.com")) || readFile(t, fableFile(s, "5", "b@example.com")) != login {
		t.Error("Account-2's Fable login is lost after the retry")
	}
	assertNoFableLogin(t, s, "2", "b@example.com")
}

// The same for a displacement: Account-2's live session stops the add before
// its default files go, and its Fable login stays with it.
func TestAFailedAddSlotDisplacementKeepsTheDisplacedFableLogin(t *testing.T) {
	s := withFableLogins(t, "")
	file := fableFile(s, "2", "b@example.com")
	before := map[string]string{file: readFile(t, file), file + ".prev": readFile(t, file+".prev")}
	startLiveSession(t, s, "2", "b@example.com")
	_, err := AddAccountFrom(s, loginAs(t, "b@example.com", "org-new", planCreds("at-n", "rt-n", "max")), ip(2), true, nil)
	if err == nil || !strings.Contains(err.Error(), "live session-mode Claude instance") {
		t.Fatalf("AddAccountFrom = %v, want the live session's refusal", err)
	}
	if r := rec(t, readSeq(t, s), "2"); r.str("organizationUuid") == "org-new" {
		t.Fatal("the displacing account took slot 2")
	}
	for p, want := range before {
		if !exists(p) || readFile(t, p) != want {
			t.Errorf("the failed add deleted or changed %s", p)
		}
	}
}

// add-token --slot 2 puts an API key in slot 2: Account-2's Fable login goes.
func TestAddTokenSlotDisplacementRemovesTheFableLogin(t *testing.T) {
	s := withFableLogins(t, "2")
	email, slot := "k@example.com", "2"
	if err := AddAccountFromToken(s, "sk-ant-api03-"+strings.Repeat("k", 40), &email, &slot, true); err != nil {
		t.Fatal(err)
	}
	assertNoFableLogin(t, s, "2", "b@example.com")
	if st := s.FableStore(); st.On {
		t.Errorf("FableStore = %+v, want off", st)
	}
}
