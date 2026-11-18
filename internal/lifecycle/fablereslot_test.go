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
