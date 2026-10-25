// Tests for ResolveLiveSlot: the live slot follows the credential when the
// config names another managed OAuth slot (spec 2026-10-25 Part 3).
package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/storenames"
)

const lineageEmail = "obi@example.com"

const lineageWarning = "live config names slot 6 (obi@example.com, org-six) but the live credential is slot 5's; using 5. A running Claude Code rewrote ~/.claude.json"

const threeSlotRoster = `{
  "activeAccountNumber": 6,
  "lastUpdated": "2026-07-17T09:00:00Z",
  "sequence": [5, 6, 7],
  "accounts": {
    "5": {"email": "obi@example.com", "uuid": "", "organizationUuid": "org-five", "organizationName": "Five", "added": "t"},
    "6": {"email": "obi@example.com", "uuid": "", "organizationUuid": "org-six", "organizationName": "Six", "added": "t"},
    "7": {"email": "obi@example.com", "uuid": "", "organizationUuid": "org-seven", "organizationName": "Seven", "added": "t"}
  }
}`

func lineageCreds(access, refresh string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","expiresAt":4102444800000}}`
}

// lineageFixture: slots 5 and 6 are one login in two organisations with
// distinct refresh tokens; the credential file holds slot 5's token while
// ~/.claude.json names slot 6. kind5 sets slot 5's record kind ("" = oauth).
func lineageFixture(t *testing.T, kind5 string) *Store {
	t.Helper()
	s := freshStore(t)
	kind := ""
	if kind5 != "" {
		kind = `, "kind": "` + kind5 + `"`
	}
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 6,
  "lastUpdated": "2026-07-17T09:00:00Z",
  "sequence": [5, 6],
  "accounts": {
    "5": {"email": "obi@example.com", "uuid": "", "organizationUuid": "org-five", "organizationName": "Five", "added": "t"`+kind+`},
    "6": {"email": "obi@example.com", "uuid": "", "organizationUuid": "org-six", "organizationName": "Six", "added": "t"}
  }
}`)
	for _, num := range []string{"5", "6"} {
		if err := s.Creds.WriteBackup(num, lineageEmail, lineageCreds("at-"+num, "rt-"+num)); err != nil {
			t.Fatal(err)
		}
		writeBackupConfig(t, s, num, lineageEmail, `{"oauthAccount":{}}`)
	}
	writeLiveCreds(t, s, lineageCreds("at-5", "rt-5"))
	writeLiveIdentity(t, s, lineageEmail, "org-six")
	return s
}

func writeLiveCreds(t *testing.T, s *Store, creds string) {
	t.Helper()
	dir := filepath.Join(s.Home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeLiveIdentity(t *testing.T, s *Store, email, org string) {
	t.Helper()
	writeGlobalConfig(t, s, `{"oauthAccount":{"emailAddress":"`+email+`","organizationUuid":"`+org+`"}}`)
}

func resolveLive(t *testing.T, s *Store) LiveSlot {
	t.Helper()
	data, err := s.ReadSequence()
	if err != nil {
		t.Fatal(err)
	}
	return s.ResolveLiveSlot(data)
}

func lineageWarnings(t *testing.T, s *Store) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.BackupDir(), "tycswap.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "but the live credential is slot")
}

func TestResolveLiveSlotPrefersCredentialLineage(t *testing.T) {
	for _, tc := range []struct{ name, live string }{
		{"same bytes", lineageCreds("at-5", "rt-5")},
		{"access token rotated", lineageCreds("at-5-rotated", "rt-5")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := lineageFixture(t, "")
			writeLiveCreds(t, s, tc.live)
			got := resolveLive(t, s)
			if want := (LiveSlot{Slot: "5", ConfigSlot: "6", ByLineage: true}); got != want {
				t.Fatalf("ResolveLiveSlot = %+v, want %+v", got, want)
			}
			if n := s.CurrentAccountNumber(); n == nil || *n != "5" {
				t.Fatalf("CurrentAccountNumber = %v, want 5", n)
			}
			raw, _ := os.ReadFile(filepath.Join(s.BackupDir(), "tycswap.log"))
			if !strings.Contains(string(raw), "WARNING") || !strings.Contains(string(raw), lineageWarning) {
				t.Fatalf("log lacks the lineage WARNING; got: %s", raw)
			}
			if n := lineageWarnings(t, s); n != 1 {
				t.Fatalf("lineage warnings = %d, want 1", n)
			}
		})
	}
}

func TestResolveLiveSlotAgreesWithoutWarning(t *testing.T) {
	s := lineageFixture(t, "")
	writeLiveIdentity(t, s, lineageEmail, "org-five")
	if got, want := resolveLive(t, s), (LiveSlot{Slot: "5", ConfigSlot: "5"}); got != want {
		t.Fatalf("ResolveLiveSlot = %+v, want %+v", got, want)
	}
	if n := s.CurrentAccountNumber(); n == nil || *n != "5" {
		t.Fatalf("CurrentAccountNumber = %v, want 5", n)
	}
	if n := lineageWarnings(t, s); n != 0 {
		t.Fatalf("lineage warnings = %d, want 0", n)
	}
}

func TestResolveLiveSlotUnmanagedConfigStaysNil(t *testing.T) {
	s := lineageFixture(t, "")
	writeLiveIdentity(t, s, "stranger@example.com", "")
	if got := resolveLive(t, s); got != (LiveSlot{}) {
		t.Fatalf("ResolveLiveSlot = %+v, want zero", got)
	}
	if n := s.CurrentAccountNumber(); n != nil {
		t.Fatalf("CurrentAccountNumber = %q, want nil", *n)
	}
	if n := lineageWarnings(t, s); n != 0 {
		t.Fatalf("lineage warnings = %d, want 0", n)
	}
}

func TestResolveLiveSlotSkipsAPIKeySlots(t *testing.T) {
	t.Run("matching slot is an API key", func(t *testing.T) {
		s := lineageFixture(t, "api_key")
		if got, want := resolveLive(t, s), (LiveSlot{Slot: "6", ConfigSlot: "6"}); got != want {
			t.Fatalf("ResolveLiveSlot = %+v, want %+v", got, want)
		}
	})
	t.Run("config slot is an API key", func(t *testing.T) {
		s := lineageFixture(t, "api_key")
		writeLiveIdentity(t, s, lineageEmail, "org-five")
		writeLiveCreds(t, s, lineageCreds("at-6", "rt-6"))
		if got, want := resolveLive(t, s), (LiveSlot{Slot: "5", ConfigSlot: "5"}); got != want {
			t.Fatalf("ResolveLiveSlot = %+v, want %+v", got, want)
		}
		if n := lineageWarnings(t, s); n != 0 {
			t.Fatalf("lineage warnings = %d, want 0", n)
		}
	})
}

func TestResolveLiveSlotUnreadableBackupFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, s *Store)
	}{
		{"corrupt backup", func(t *testing.T, s *Store) {
			path := filepath.Join(s.CredentialsDir, storenames.CredsFile("5", lineageEmail))
			if err := os.WriteFile(path, []byte("%%% not base64 %%%"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing backup", func(t *testing.T, s *Store) {
			if err := s.Creds.DeleteBackup("5", lineageEmail); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty live credential", func(t *testing.T, s *Store) {
			writeLiveCreds(t, s, "")
		}},
		{"copied into the config slot", func(t *testing.T, s *Store) {
			if err := s.Creds.WriteBackup("6", lineageEmail, lineageCreds("at-5", "rt-5")); err != nil {
				t.Fatal(err)
			}
		}},
		{"copied into another slot", func(t *testing.T, s *Store) {
			writeSequenceRaw(t, s, threeSlotRoster)
			if err := s.Creds.WriteBackup("7", lineageEmail, lineageCreds("at-5", "rt-5")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := lineageFixture(t, "")
			tc.mutate(t, s)
			if got, want := resolveLive(t, s), (LiveSlot{Slot: "6", ConfigSlot: "6"}); got != want {
				t.Fatalf("ResolveLiveSlot = %+v, want %+v", got, want)
			}
			if n := lineageWarnings(t, s); n != 0 {
				t.Fatalf("lineage warnings = %d, want 0", n)
			}
		})
	}
}

func TestResolveLiveSlotWarnsOncePerPair(t *testing.T) {
	s := lineageFixture(t, "")
	for i := 0; i < 3; i++ {
		resolveLive(t, s)
	}
	if n := lineageWarnings(t, s); n != 1 {
		t.Fatalf("after three resolves: lineage warnings = %d, want 1", n)
	}

	// A third org slot named by the config is a new (ConfigSlot, Slot) pair.
	writeSequenceRaw(t, s, threeSlotRoster)
	if err := s.Creds.WriteBackup("7", lineageEmail, lineageCreds("at-7", "rt-7")); err != nil {
		t.Fatal(err)
	}
	writeLiveIdentity(t, s, lineageEmail, "org-seven")
	for i := 0; i < 2; i++ {
		if got, want := resolveLive(t, s), (LiveSlot{Slot: "5", ConfigSlot: "7", ByLineage: true}); got != want {
			t.Fatalf("ResolveLiveSlot = %+v, want %+v", got, want)
		}
	}
	if n := lineageWarnings(t, s); n != 2 {
		t.Fatalf("after a new pair: lineage warnings = %d, want 2", n)
	}
}

func TestResolveLiveSlotWarnsOnceForAlternatingPairs(t *testing.T) {
	s := lineageFixture(t, "")
	writeSequenceRaw(t, s, threeSlotRoster)
	if err := s.Creds.WriteBackup("7", lineageEmail, lineageCreds("at-7", "rt-7")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		for _, org := range []string{"org-six", "org-seven"} {
			writeLiveIdentity(t, s, lineageEmail, org)
			if got := resolveLive(t, s); !got.ByLineage || got.Slot != "5" {
				t.Fatalf("ResolveLiveSlot(%s) = %+v, want slot 5 by lineage", org, got)
			}
		}
	}
	if n := lineageWarnings(t, s); n != 2 {
		t.Fatalf("after alternating pairs: lineage warnings = %d, want 2", n)
	}
}

// Without a refresh token the fingerprint is the whole blob's hash, so a live
// copy carrying the seat's MCP logins matches slot 5 by its account block only.
func TestResolveLiveSlotMatchesByAccountBlockAlone(t *testing.T) {
	s := lineageFixture(t, "")
	backup := `{"claudeAiOauth":{"accessToken":"at-5","expiresAt":4102444800000}}`
	if err := s.Creds.WriteBackup("5", lineageEmail, backup); err != nil {
		t.Fatal(err)
	}
	writeLiveCreds(t, s, `{"claudeAiOauth":{"accessToken":"at-5","expiresAt":4102444800000},"mcpOAuth":{"srv":{"accessToken":"m"}}}`)
	if got, want := resolveLive(t, s), (LiveSlot{Slot: "5", ConfigSlot: "6", ByLineage: true}); got != want {
		t.Fatalf("ResolveLiveSlot = %+v, want %+v", got, want)
	}
}

// Ownership is one more consumer of the credential rule: the default login
// owns the slot whose credential it holds, not the one its config names.
func TestCredentialOwnerFollowsTheCredential(t *testing.T) {
	s := lineageFixture(t, "")
	opus, err := s.ForGroup(groups.Opus)
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string]*Store{"default": s, "group-scoped": opus} {
		if owner, err := st.CredentialOwner("5"); err != nil || owner.Scope != "default" || owner.Uncertain {
			t.Errorf("%s: CredentialOwner(5) = %+v, %v; want the default login, certain", name, owner, err)
		}
		if owner, err := st.CredentialOwner("6"); err != nil || owner.Scope != "" {
			t.Errorf("%s: CredentialOwner(6) = %+v, %v; want no owner", name, owner, err)
		}
	}
	if err := s.EnsureAccountAvailable("5"); err != nil {
		t.Errorf("EnsureAccountAvailable(5) = %v, want nil", err)
	}
	if got, err := s.ReadOwnedCredentials("5", lineageEmail); err != nil || got != lineageCreds("at-5", "rt-5") {
		t.Errorf("ReadOwnedCredentials(5) = %q, %v; want the live credential", got, err)
	}
}

// lockingKeychain answers the default login's item once, then fails as a
// Keychain locked between two reads would.
type lockingKeychain struct {
	keychain.KeychainClient
	service string
	reads   int
}

func (k *lockingKeychain) Get(service, account string) (string, bool, error) {
	if service == k.service {
		if k.reads++; k.reads > 1 {
			return "", false, &keychain.KeychainError{Msg: "keychain locked"}
		}
	}
	return k.KeychainClient.Get(service, account)
}

// A failed read of the default credential surfaces as itself, not as a
// changed identity, even while the config names another slot.
func TestReadOwnedCredentialsReportsTheReadError(t *testing.T) {
	s := lineageFixture(t, "")
	s.Platform = platform.MacOS
	fake := keychain.NewFake()
	fake.Seed(s.DefaultKeychainService(), keychain.AccountName(), lineageCreds("at-5", "rt-5"))
	s.kc = &lockingKeychain{KeychainClient: fake, service: s.DefaultKeychainService()}
	if _, err := s.ReadOwnedCredentials("5", lineageEmail); err == nil || err.Error() != "keychain locked" {
		t.Fatalf("ReadOwnedCredentials(5) error = %v, want the Keychain's", err)
	}
}
