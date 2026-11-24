// Tests for statusline.json's logins (DESIGN A113): each login a session link
// can point at, by its resolved directory, names the account whose credential
// it holds now.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/statusline"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

var loginEmails = map[string][2]string{"1": {"a@example.com", "org-a"}, "2": {"b@example.com", "org-b"}, "3": {"c@example.com", ""}, "4": {"d@example.com", "org-d"}}

func loginCreds(access, refresh string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","expiresAt":4102444800000}}`
}

func writeLoginFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// loginsStore is a store whose ~/.claude is a symlink, with accounts 1 to 4,
// each with its default login backed up: the default login live on 1, the
// Fable store on 2's Fable login, an independent Mythos login for 3, and
// ~/.claude.json naming 4. Nothing has been published since.
func loginsStore(t *testing.T) *Store {
	t.Helper()
	home := testutil.IsolateHome(t)
	dotfiles := filepath.Join(home, "dotfiles", "claude")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfiles, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Clock: testutil.FixedClock(t, "2026-07-17T09:00:00Z"), Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	data := &SequenceData{LastUpdated: "x", Sequence: []int{1, 2, 3, 4}, Accounts: map[string]json.RawMessage{}}
	for num, id := range loginEmails {
		data.Accounts[num], _ = json.Marshal(map[string]any{"email": id[0], "uuid": "u-" + num, "organizationUuid": id[1], "organizationName": ""})
		if err := s.WriteAccountCredentials(num, id[0], loginCreds("at-d"+num, "rt-d"+num)); err != nil {
			t.Fatal(err)
		}
	}
	writeLoginFile(t, filepath.Join(s.DefaultProfileDir(), ".credentials.json"), loginCreds("at-d1", "rt-d1"))
	writeLoginFile(t, filepath.Join(s.Home, ".claude.json"), `{"oauthAccount":{"emailAddress":"d@example.com","organizationUuid":"org-d"}}`)
	f, err := s.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetupFableDirectories(); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteAccountCredentials("2", "b@example.com", loginCreds("at-f2", "rt-f2")); err != nil {
		t.Fatal(err)
	}
	writeLoginFile(t, filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json"), loginCreds("at-f2", "rt-f2"))
	writeLoginFile(t, filepath.Join(MythosLoginDir(s.BackupDir(), "c@example.com", ""), ".credentials.json"), loginCreds("at-m3", "rt-m3"))
	if err := s.writeSequenceFile(data); err != nil {
		t.Fatal(err)
	}
	return s
}

// publishedLogins publishes and returns the file's logins, nil without the
// key, after checking that every value is a key of accounts.
func publishedLogins(t *testing.T, s *Store) map[string]any {
	t.Helper()
	s.PublishStatusline()
	raw, err := os.ReadFile(statusline.Path(s.BackupDir()))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	logins, _ := doc["logins"].(map[string]any)
	accounts, _ := doc["accounts"].(map[string]any)
	for dir, key := range logins {
		if k, _ := key.(string); accounts[k] == nil {
			t.Errorf("logins[%s] = %v, not a key of accounts", dir, key)
		}
	}
	return logins
}

// resolved is dir with its symlinks resolved.
func resolved(t *testing.T, dir string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// loginDirs are the three logins' resolved directories; the default login's
// differs from ~/.claude, a symlink.
func loginDirs(t *testing.T, s *Store) (def, fable, mythos string) {
	t.Helper()
	if def = resolved(t, s.DefaultProfileDir()); def == s.DefaultProfileDir() {
		t.Fatalf("%s resolves to itself", def)
	}
	return def, resolved(t, paths.FableStoreDir(s.BackupDir())), resolved(t, MythosLoginDir(s.BackupDir(), "c@example.com", ""))
}

// The default login, the Fable store and a Mythos login held by three
// accounts publish three entries under their resolved directories; the
// default login's follows its credential, not ~/.claude.json.
func TestStatuslineNamesEachLoginsAccount(t *testing.T) {
	s := loginsStore(t)
	def, fable, mythos := loginDirs(t, s)
	want := map[string]any{def: "a@example.com|org-a", fable: "b@example.com|org-b", mythos: "c@example.com|"}
	if got := publishedLogins(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("logins = %v, want %v", got, want)
	}
}

// A login whose owner cannot be verified is left out; the others stay.
func TestStatuslineLeavesOutAnUnverifiedLogin(t *testing.T) {
	t.Run("unknown fingerprint", func(t *testing.T) {
		s := loginsStore(t)
		writeLoginFile(t, filepath.Join(s.DefaultProfileDir(), ".credentials.json"), loginCreds("at-x", "rt-x"))
		_, fable, mythos := loginDirs(t, s)
		if got := publishedLogins(t, s); !reflect.DeepEqual(got, map[string]any{fable: "b@example.com|org-b", mythos: "c@example.com|"}) {
			t.Fatalf("logins = %v", got)
		}
	})
	t.Run("no credential", func(t *testing.T) {
		s := loginsStore(t)
		if err := os.Remove(filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json")); err != nil {
			t.Fatal(err)
		}
		def, _, mythos := loginDirs(t, s)
		if got := publishedLogins(t, s); !reflect.DeepEqual(got, map[string]any{def: "a@example.com|org-a", mythos: "c@example.com|"}) {
			t.Fatalf("logins = %v", got)
		}
	})
}

// Without a Fable or Mythos login only the default login is named; without
// managed accounts the file has no logins key and only its five keys.
func TestStatuslineLoginsOnlyWhereThereAreLogins(t *testing.T) {
	t.Run("default login only", func(t *testing.T) {
		s := loginsStore(t)
		def, _, _ := loginDirs(t, s)
		for _, dir := range []string{paths.FableScopeDir(s.BackupDir()), paths.FableCredentialsDir(s.BackupDir()), filepath.Join(s.BackupDir(), "mythos")} {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
		if got := publishedLogins(t, s); !reflect.DeepEqual(got, map[string]any{def: "a@example.com|org-a"}) {
			t.Fatalf("logins = %v", got)
		}
	})
	t.Run("no managed accounts", func(t *testing.T) {
		s := freshStore(t)
		writeLoginFile(t, filepath.Join(s.DefaultProfileDir(), ".credentials.json"), loginCreds("at-d1", "rt-d1"))
		s.PublishStatusline()
		raw, err := os.ReadFile(statusline.Path(s.BackupDir()))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil || len(doc) != 5 || doc["logins"] != nil {
			t.Fatalf("statusline.json = %s (%v)", raw, err)
		}
	})
}

// Two records with one identity: the login's value is the key accounts
// publishes, under the lower slot, whichever slot holds the credential.
func TestStatuslineLoginValueIsAnAccountsKey(t *testing.T) {
	s := loginsStore(t)
	data, err := s.ReadSequence()
	if err != nil {
		t.Fatal(err)
	}
	data.Accounts["5"], _ = json.Marshal(map[string]any{"email": "a@example.com", "uuid": "u-5", "organizationUuid": "org-a"})
	data.Sequence = append(data.Sequence, 5)
	if err := s.WriteAccountCredentials("5", "a@example.com", loginCreds("at-d5", "rt-d5")); err != nil {
		t.Fatal(err)
	}
	if err := s.writeSequenceFile(data); err != nil {
		t.Fatal(err)
	}
	writeLoginFile(t, filepath.Join(s.DefaultProfileDir(), ".credentials.json"), loginCreds("at-d5", "rt-d5"))
	def, _, _ := loginDirs(t, s)
	if got := publishedLogins(t, s)[def]; got != "a@example.com|org-a" {
		t.Fatalf("logins[default] = %v", got)
	}
	if acct := readPublished(t, s).Accounts["a@example.com|org-a"]; acct.Slot != 1 {
		t.Fatalf("accounts[a@example.com|org-a] = %+v, want slot 1", acct)
	}
}

// A login the profile endpoint re-attributes (the chain record written for a
// rotated credential) is named at once, without a usage change.
func TestStatuslineFollowsALoginItsProfileReattributes(t *testing.T) {
	s := loginsStore(t)
	rotated := loginCreds("at-d1-new", "rt-d1-new")
	writeLoginFile(t, filepath.Join(s.DefaultProfileDir(), ".credentials.json"), rotated)
	writeLoginFile(t, filepath.Join(s.Home, ".claude.json"), `{"oauthAccount":{"emailAddress":"a@example.com","organizationUuid":"org-a"}}`)
	def, _, _ := loginDirs(t, s)
	if got := publishedLogins(t, s)[def]; got != nil {
		t.Fatalf("a rotated login nothing names: logins[default] = %v", got)
	}
	s.OAuth = &oauth.FakeClient{ProfileFn: func(_ context.Context, token string) *oauth.Identity {
		if token != "at-d1-new" {
			return nil
		}
		return &oauth.Identity{UUID: "u-1", Email: "a@example.com", OrgUUID: "org-a"}
	}}
	data, err := s.ReadSequence()
	if err != nil {
		t.Fatal(err)
	}
	if ls := s.ResolveLiveSlot(data); ls.Slot != "1" || ls.Source != SourceProfile {
		t.Fatalf("live slot %+v", ls)
	}
	raw, err := os.ReadFile(statusline.Path(s.BackupDir()))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Logins map[string]string `json:"logins"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Logins[def] != "a@example.com|org-a" {
		t.Fatalf("after the profile named the login: %s (%v)", raw, err)
	}
}
