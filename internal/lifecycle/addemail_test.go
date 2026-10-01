package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/storenames"
)

// TestAddRefusesUnsafeEmail: the email in a .claude.json names the backup
// files, so add refuses anything a file name cannot carry, from the live
// login and from a scratch login directory, before any write.
func TestAddRefusesUnsafeEmail(t *testing.T) {
	for _, email := range []string{"a/../../../x", "a/../../../x@example.com", "a@example.com\n", `a\..\x@example.com`, "a:b@example.com", "a@b@example.com"} {
		t.Run("live "+email, func(t *testing.T) {
			s := newStore(t)
			seedLiveLogin(t, s, email, "", "", "uuid", oauthBlob)
			err := AddAccount(s, nil, true, nil)
			if err == nil || !strings.Contains(err.Error(), "cannot name a store file") {
				t.Fatalf("AddAccount = %v, want the email refusal", err)
			}
			assertNoBackups(t, s.ConfigsDir, s.CredentialsDir)
			if _, err := os.Stat(filepath.Join(filepath.Dir(s.BackupDir()), "x.json")); err == nil {
				t.Error("a file was written outside the store")
			}
		})
		t.Run("login dir "+email, func(t *testing.T) {
			s := newStore(t)
			dir := t.TempDir()
			cfg, _ := json.Marshal(map[string]any{"oauthAccount": map[string]any{"emailAddress": email, "organizationUuid": ""}})
			if err := os.WriteFile(filepath.Join(dir, ".claude.json"), cfg, 0o600); err != nil {
				t.Fatal(err)
			}
			kc := keychain.NewFake()
			_ = kc.Set(LoginKeychainService(dir), keychain.AccountName(), oauthBlob)
			if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(oauthBlob), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := AddAccountFrom(s, LoginDir(dir, kc), nil, true, nil)
			if err == nil || !strings.Contains(err.Error(), "cannot name a store file") {
				t.Fatalf("AddAccountFrom = %v, want the email refusal", err)
			}
			assertNoBackups(t, s.ConfigsDir, s.CredentialsDir)
		})
	}
}

// TestAddAcceptsEveryAddressAFileNameCanCarry: an apostrophe or a non-ASCII
// letter is a real address; add stores it under its raw name.
func TestAddAcceptsEveryAddressAFileNameCanCarry(t *testing.T) {
	for _, email := range []string{"a'b@example.com", "jörg@example.com", "a@xn--bcher-kva.example"} {
		t.Run(email, func(t *testing.T) {
			s := newStore(t)
			seedLiveLogin(t, s, email, "", "", "uuid", oauthBlob)
			if err := AddAccount(s, nil, true, nil); err != nil {
				t.Fatalf("AddAccount(%q) = %v", email, err)
			}
			if _, err := os.Stat(filepath.Join(s.ConfigsDir, storenames.ConfigFile("1", email))); err != nil {
				t.Errorf("config backup not written under the raw name: %v", err)
			}
			if got, _ := s.ReadAccountCredentials("1", email); got != oauthBlob {
				t.Errorf("credential backup = %q, want the login's", got)
			}
		})
	}
}

func assertNoBackups(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		entries, _ := os.ReadDir(d)
		if len(entries) != 0 {
			t.Errorf("%s holds %d entries after a refused add", d, len(entries))
		}
	}
}
