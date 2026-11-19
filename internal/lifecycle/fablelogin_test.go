// Tests for AddFableLogin (DESIGN A101): a scratch sign-in stored as an
// account's Fable login, its refusals, and the renewal of the live one.
package lifecycle

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
)

func planCreds(access, refresh, plan string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","expiresAt":4102444800000,"subscriptionType":"` + plan + `"}}`
}

func fableAcct(num, email, plan string) acct {
	return acct{num: num, email: email,
		creds:  planCreds("at-d"+num, "rt-d"+num, plan),
		config: `{"oauthAccount":{"emailAddress":"` + email + `"}}`}
}

// fableLifecycleStore: accounts 1 and 2 on max plans, 3 on pro and 4 an API
// key; slot 1's default login is live. No Fable login yet.
func fableLifecycleStore(t *testing.T) *store.Store {
	t.Helper()
	s := newStore(t)
	s.Platform = platform.Linux
	seed(t, s, ip(1),
		fableAcct("1", "a@example.com", "max"),
		fableAcct("2", "b@example.com", "max"),
		fableAcct("3", "c@example.com", "pro"),
		acct{num: "4", email: "d@example.com", kind: "api_key", creds: "sk-ant-api03-key-4", config: `{}`})
	seedLiveLogin(t, s, "a@example.com", "", "", "uuid-a", planCreds("at-d1", "rt-d1", "max"))
	return s
}

// signIn is a scratch profile `claude auth login` left behind.
func signIn(t *testing.T, email, creds string) AddSource {
	t.Helper()
	dir := t.TempDir()
	cfg := `{"oauthAccount":{"emailAddress":"` + email + `","organizationUuid":""}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if creds != "" {
		if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return LoginDir(dir, nil)
}

// everyFile is every file under the store root and the home's Claude Code
// files, path → bytes, the log left out (a refusal may log).
func everyFile(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range []string{s.BackupDir(), filepath.Join(s.Home, ".claude"), filepath.Join(s.Home, ".claude.json")} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(p, ".log") || strings.HasSuffix(p, ".lock") {
				return nil
			}
			raw, _ := os.ReadFile(p)
			out[p] = string(raw)
			return nil
		})
	}
	return out
}

func sameFiles(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for p, want := range before {
		if got, ok := after[p]; !ok {
			t.Errorf("%s deleted %s", what, p)
		} else if got != want {
			t.Errorf("%s rewrote %s", what, p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("%s created %s", what, p)
		}
	}
}

func fableLoginOf(t *testing.T, s *store.Store, num, email string) string {
	t.Helper()
	f, err := s.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	creds, _ := f.ReadAccountCredentials(num, email)
	return creds
}

func TestAddFableLoginRefusalsStoreNothing(t *testing.T) {
	for _, tc := range []struct {
		name, email, creds string
		slot               *int
		want               string
	}{
		{"not in the roster", "z@example.com", planCreds("at-z", "rt-z", "max"), nil,
			"Account z@example.com is not stored yet: add it first with tycswap add --login."},
		{"an API-key account's email", "d@example.com", planCreds("at-x", "rt-x", "max"), nil,
			"Account d@example.com is not stored yet: add it first with tycswap add --login."},
		{"another slot", "a@example.com", planCreds("at-f1", "rt-f1", "max"), ip(2),
			"This sign-in is a@example.com, not Account-2 (b@example.com); nothing stored."},
		{"an empty slot", "a@example.com", planCreds("at-f1", "rt-f1", "max"), ip(9),
			"This sign-in is a@example.com, not Account-9; nothing stored."},
		{"no Fable on the plan", "c@example.com", planCreds("at-f3", "rt-f3", "pro"), nil,
			"Account-3's plan does not offer Fable."},
		{"the slot's default login", "b@example.com", planCreds("at-other", "rt-d2", "max"), nil,
			"This is Account-2's default login, not a second sign-in; sign in again in the browser."},
		{"the live default credential", "a@example.com", planCreds("at-d1", "rt-d1", "max"), nil,
			"This is Account-1's default login, not a second sign-in; sign in again in the browser."},
		{"an API key", "a@example.com", "sk-ant-api03-test-key-1", nil,
			"claude's login made an API key, which is a different auth axis: use tycswap --add-token"},
		{"an incomplete login", "a@example.com", "", nil,
			"claude's login did not complete; nothing stored, the live login untouched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fableLifecycleStore(t)
			out := captureOut(t)
			before := everyFile(t, s)
			num, err := AddFableLogin(s, signIn(t, tc.email, tc.creds), tc.slot)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("AddFableLogin = %q, %v; want %q", num, err, tc.want)
			}
			sameFiles(t, "the refusal", before, everyFile(t, s))
			for _, dir := range []string{paths.FableCredentialsDir(s.BackupDir()), paths.FableScopeDir(s.BackupDir())} {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Errorf("the refusal created %s", dir)
				}
			}
			if out.Len() != 0 {
				t.Errorf("the refusal printed %q", out.String())
			}
		})
	}
}

func TestAddFableLoginStoresOnlyTheFableLogin(t *testing.T) {
	s := fableLifecycleStore(t)
	out := captureOut(t)
	before := everyFile(t, s)
	login := planCreds("at-f2", "rt-f2", "max")
	num, err := AddFableLogin(s, signIn(t, "b@example.com", login), ip(2))
	if err != nil || num != "2" {
		t.Fatalf("AddFableLogin = %q, %v", num, err)
	}
	if got := fableLoginOf(t, s, "2", "b@example.com"); got != login {
		t.Fatalf("Fable login = %q, want %q", got, login)
	}
	after := everyFile(t, s)
	for p := range after {
		if strings.HasPrefix(p, paths.FableCredentialsDir(s.BackupDir())) || strings.HasPrefix(p, paths.FableScopeDir(s.BackupDir())) {
			delete(after, p)
		}
	}
	sameFiles(t, "storing a Fable login", before, after)
	if got := out.String(); got != "Stored the Fable login for Account-2 (b@example.com).\n" {
		t.Errorf("output = %q", got)
	}
	if st := s.FableStore(); st.On {
		t.Errorf("FableStore = %+v; storing alone must not install the login", st)
	}
	for _, dir := range []string{paths.FableCredentialsDir(s.BackupDir()), paths.FableScopeDir(s.BackupDir()), paths.FableStoreDir(s.BackupDir())} {
		if info, err := os.Stat(dir); err != nil || (!platform.IsWindows() && info.Mode().Perm() != 0o700) {
			t.Errorf("%s: %v %v, want a 0700 directory", dir, info, err)
		}
	}
}

// The plan is the account's, as the roster and the default login know it: a
// Fable login whose credential names no subscriptionType (null when Claude
// Code's profile fetch at login fails) is stored, so the switch takes it too.
func TestAStoredFableLoginWithoutAPlanCanBeSwitchedTo(t *testing.T) {
	s := fableLifecycleStore(t)
	captureOut(t)
	noPlan := `{"claudeAiOauth":{"accessToken":"at-f1","refreshToken":"rt-f1","expiresAt":4102444800000,"subscriptionType":null}}`
	num, err := AddFableLogin(s, signIn(t, "a@example.com", noPlan), ip(1))
	if err != nil {
		t.Fatalf("AddFableLogin = %v", err)
	}
	f, _ := s.ForFable()
	if s.FableCapable(num) != f.FableCapable(num) {
		t.Errorf("FableCapable(%s): default scope %v, Fable scope %v", num, s.FableCapable(num), f.FableCapable(num))
	}
	if _, err := switching.SwitchFable(f, num, switching.FableSwitchOptions{}); err != nil {
		t.Fatalf("SwitchFable refused the Fable login AddFableLogin stored: %v", err)
	}
	if st := s.FableStore(); !st.On || st.Live.Slot != num {
		t.Errorf("FableStore = %+v, want on Account-%s", st, num)
	}
}

func TestAddFableLoginAgainReplacesTheFileAndKeepsPrev(t *testing.T) {
	s := fableLifecycleStore(t)
	captureOut(t)
	first, second := planCreds("at-f2", "rt-f2", "max"), planCreds("at-f2b", "rt-f2b", "max")
	for _, login := range []string{first, second} {
		if _, err := AddFableLogin(s, signIn(t, "b@example.com", login), nil); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := s.ForFable()
	if got := fableLoginOf(t, s, "2", "b@example.com"); got != second {
		t.Errorf("Fable login = %q, want the second", got)
	}
	if prev, _ := f.Creds.ReadPrev("2", "b@example.com"); prev != first {
		t.Errorf(".prev = %q, want the first", prev)
	}
}

// The live Fable login's grant is dead (an invalid_grant quarantine on its
// fingerprint): signing the account in again installs the new login in the
// store, keeping the store's MCP logins, so the store is usable again.
func TestAddFableLoginRenewsADeadLiveLogin(t *testing.T) {
	s := fableLifecycleStore(t)
	out := captureOut(t)
	dead := planCreds("at-f1", "rt-f1", "max")
	if _, err := AddFableLogin(s, signIn(t, "a@example.com", dead), nil); err != nil {
		t.Fatal(err)
	}
	f, _ := s.ForFable()
	if _, err := switching.SwitchFable(f, "1", switching.FableSwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	storeFile := filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json")
	raw, _ := os.ReadFile(storeFile)
	withMCP := strings.TrimSuffix(string(raw), "}") + `,"mcpOAuth":{"srv":{"accessToken":"m"}}}`
	if err := os.WriteFile(storeFile, []byte(withMCP), 0o600); err != nil {
		t.Fatal(err)
	}
	fp := *oauth.CredentialFingerprint(dead)
	state := `{"quarantine":{"1":{"email":"a@example.com","reason":"invalid_grant","at":"t","refreshTokenFingerprint":"` + fp + `"}}}`
	if err := os.WriteFile(f.StatePath(), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := s.FableStore(); !st.On || st.Usable {
		t.Fatalf("precondition: FableStore = %+v, want on and unusable", st)
	}
	out.Reset()

	fresh := planCreds("at-f1b", "rt-f1b", "max")
	if _, err := AddFableLogin(s, signIn(t, "a@example.com", fresh), ip(1)); err != nil {
		t.Fatal(err)
	}
	if st := s.FableStore(); !st.On || !st.Usable || st.Live.Slot != "1" {
		t.Fatalf("FableStore = %+v, want on, usable, Account-1", st)
	}
	live, _, _ := f.Creds.ReadActive()
	if !oauth.SameAccountBlock(live, fresh) || !strings.Contains(live, `"mcpOAuth"`) {
		t.Errorf("the store holds %s, want the new login with its MCP logins", live)
	}
	if got := fableLoginOf(t, s, "1", "a@example.com"); got != fresh {
		t.Errorf("Fable login = %q, want the new one", got)
	}
	var chain struct{ Fingerprint, Slot string }
	if raw, err := os.ReadFile(filepath.Join(paths.FableScopeDir(s.BackupDir()), "live_lineage.json")); err != nil || json.Unmarshal(raw, &chain) != nil ||
		chain.Slot != "1" || chain.Fingerprint != *oauth.CredentialFingerprint(fresh) {
		t.Errorf("Fable chain record %+v (%v), want the new login on Account-1", chain, err)
	}
	if got := out.String(); got != "Stored the Fable login for Account-1 (a@example.com).\nThe Fable store now uses Account-1.\n" {
		t.Errorf("output = %q", got)
	}
}

// A Fable login of an account the store does not hold leaves the store alone.
func TestAddFableLoginOfAnotherAccountLeavesTheStore(t *testing.T) {
	s := fableLifecycleStore(t)
	captureOut(t)
	if _, err := AddFableLogin(s, signIn(t, "a@example.com", planCreds("at-f1", "rt-f1", "max")), nil); err != nil {
		t.Fatal(err)
	}
	f, _ := s.ForFable()
	if _, err := switching.SwitchFable(f, "1", switching.FableSwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	storeFile := filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json")
	before, _ := os.ReadFile(storeFile)
	if _, err := AddFableLogin(s, signIn(t, "b@example.com", planCreds("at-f2", "rt-f2", "max")), nil); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(storeFile); string(after) != string(before) {
		t.Errorf("the store changed: %s", after)
	}
}

// A Fable session wrote Account-2 into ~/.claude.json while the default
// credential is Account-1's: a plain add of the live login refuses rather
// than store Account-1's grant as Account-2's (DESIGN A101).
func TestAddOfTheLiveLoginRefusesAFableSessionsConfig(t *testing.T) {
	s := fableLifecycleStore(t)
	captureOut(t)
	if _, err := AddFableLogin(s, signIn(t, "b@example.com", planCreds("at-f2", "rt-f2", "max")), nil); err != nil {
		t.Fatal(err)
	}
	f, _ := s.ForFable()
	if _, err := switching.SwitchFable(f, "2", switching.FableSwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	seedLiveLogin(t, s, "b@example.com", "", "", "uuid-b", planCreds("at-d1", "rt-d1", "max"))
	before := everyFile(t, s)
	err := AddAccount(s, nil, true, nil)
	want := "~/.claude.json names Account-2, the Fable store's account, but the default login is Account-1's; nothing stored."
	if err == nil || err.Error() != want {
		t.Fatalf("AddAccount = %v, want %q", err, want)
	}
	sameFiles(t, "the refusal", before, everyFile(t, s))
}
