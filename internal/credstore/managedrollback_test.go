// A switch's write of a managed key (WriteActiveAccount) that fails after the
// key reached the macOS Keychain must leave the live credential as it found
// it. A switch rolls the credential back only after a write that succeeded,
// so a key a failed write leaves in the Keychain is one nothing removes: it
// outlives the failed switch beside the OAuth login, or replaces the key the
// switcher still records as live. The file backend stores the key and its
// approval in one ~/.claude.json write, so there a failed write leaves nothing
// behind. WriteActive, which a rollback restores with, keeps a key it stored
// (TestWriteActive_RemainderFallbackConfigFailureIsReported_macOS).
package credstore

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/testutil"
)

// prevKey is the managed key live before the write, shaped like seatKey.
const prevKey = "sk-ant-api03-previous-fixture-0123456789"

// readFileOrNone returns path's bytes, "" when it does not exist.
func readFileOrNone(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// assertKeychainItem checks a Keychain item against want, "" for no item.
func assertKeychainItem(t *testing.T, kc *fakeKC, service, want string) {
	t.Helper()
	item, present := kc.peek(service, keychain.AccountName())
	switch {
	case want == "" && present:
		t.Errorf("Keychain item %q = %q after the failed write, want no item", service, item)
	case want != "" && item != want:
		t.Errorf("Keychain item %q = %q (present %v) after the failed write, want %q", service, item, present, want)
	}
}

// TestWriteActiveAccount_ManagedKeyConfigFailureRestoresTheKeychain_macOS:
// the key is stored in the Keychain, then the ~/.claude.json update recording
// its approval fails. The error leaves the managed Keychain item as it was (no
// item, or the previous key), and the OAuth credential, its shadow file and
// the config untouched.
func TestWriteActiveAccount_ManagedKeyConfigFailureRestoresTheKeychain_macOS(t *testing.T) {
	for _, tc := range []struct {
		name string
		// oauth is the OAuth Keychain item and prev the managed Keychain
		// item, "" for none; file is the credentials file.
		oauth, file, prev string
	}{
		{"from an OAuth login", loginBesideMCP, loginBesideMCP, ""},
		{"from another key", "", mcpOnly, prevKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fh := testutil.BuildFixtureHome(t)
			writeFile(t, fh.CredentialsFile, tc.file)
			// The config no longer parses, so its update after the Keychain
			// write fails, as a truncated or unwritable file makes it fail.
			writeFile(t, fh.GlobalConfig, "{")
			kc := newFakeKC()
			if tc.oauth != "" {
				kc.put(claudeCodeKeychainService, keychain.AccountName(), tc.oauth)
			}
			if tc.prev != "" {
				kc.put(managedKeychainService, keychain.AccountName(), tc.prev)
			}
			s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)
			live, _, err := s.ReadActive()
			if err != nil || live == "" {
				t.Fatalf("precondition: ReadActive = %q, %v", live, err)
			}

			err = s.WriteActiveAccount(seatKey)
			if err == nil || !strings.Contains(err.Error(), "Failed to write managed API key") {
				t.Fatalf("WriteActiveAccount = %v, want the config failure reported", err)
			}
			assertKeychainItem(t, kc, managedKeychainService, tc.prev)
			assertKeychainItem(t, kc, claudeCodeKeychainService, tc.oauth)
			if got := readFileOrNone(t, fh.CredentialsFile); got != tc.file {
				t.Errorf("credentials file = %q, want it untouched %q", got, tc.file)
			}
			if got := readFileOrNone(t, fh.GlobalConfig); got != "{" {
				t.Errorf("config = %q, want it untouched", got)
			}
			if got, _, _ := s.ReadActive(); got != live {
				t.Errorf("ReadActive = %q after the failed write, want the credential live before it %q", got, live)
			}
		})
	}
}

// TestWriteActiveAccount_ManagedKeyFallbackConfigFailureRestoresTheKeychain_macOS:
// the seat's MCP data is too large for the Keychain. The new key lands in the
// Keychain and its approval in the config, then the config write that must
// precede the file fallback fails. The managed item is as it was, the OAuth
// credential is untouched, and the live credential is the one live before.
func TestWriteActiveAccount_ManagedKeyFallbackConfigFailureRestoresTheKeychain_macOS(t *testing.T) {
	login := `{"claudeAiOauth":{"accessToken":"original-oauth-fixture"},` + largeSeatRemainder()[1:]
	for _, tc := range []struct {
		name string
		// oauth is the OAuth Keychain item and prev the managed Keychain
		// item, "" for none; file is the credentials file, "" to keep the
		// fixture's shadow file.
		oauth, file, prev string
	}{
		{"from an OAuth login", login, "", ""},
		{"from another key", "", largeSeatRemainder(), prevKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fh := testutil.BuildFixtureHome(t)
			if tc.file != "" {
				writeFile(t, fh.CredentialsFile, tc.file)
			}
			file := readFileOrNone(t, fh.CredentialsFile)
			kc := limitedSeatKC{fakeKC: newFakeKC(), onTooLarge: func() {
				writeFile(t, fh.GlobalConfig, "{")
			}}
			if tc.oauth != "" {
				kc.put(claudeCodeKeychainService, keychain.AccountName(), tc.oauth)
			}
			if tc.prev != "" {
				kc.put(managedKeychainService, keychain.AccountName(), tc.prev)
			}
			s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)
			live, _, err := s.ReadActive()
			if err != nil || live == "" {
				t.Fatalf("precondition: ReadActive = %q, %v", live, err)
			}

			err = s.WriteActiveAccount(seatKey)
			if err == nil || !strings.Contains(err.Error(), "managed API key for OAuth file fallback") {
				t.Fatalf("WriteActiveAccount = %v, want the fallback config failure reported", err)
			}
			assertKeychainItem(t, kc.fakeKC, managedKeychainService, tc.prev)
			assertKeychainItem(t, kc.fakeKC, claudeCodeKeychainService, tc.oauth)
			if got := readFileOrNone(t, fh.CredentialsFile); got != file {
				t.Error("the failed write replaced the credentials file")
			}
			if got, _, _ := s.ReadActive(); got != live {
				t.Errorf("ReadActive = %q after the failed write, want the credential live before it %q", got, live)
			}
		})
	}
}

// TestWriteActiveAccount_ManagedKeyFallbackConfigFailureKeepsAConfigKeyLive_macOS:
// the previous key lives in primaryApiKey alone, with no managed Keychain
// item. The first config update drops it, then the file fallback's config
// write fails. The previous key goes into the Keychain item, which is read
// before primaryApiKey, so the seat keeps it rather than having no credential.
func TestWriteActiveAccount_ManagedKeyFallbackConfigFailureKeepsAConfigKeyLive_macOS(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	fh := testutil.BuildFixtureHome(t)
	rest := largeSeatRemainder()
	writeFile(t, fh.CredentialsFile, rest)
	writeFile(t, fh.GlobalConfig, `{"primaryApiKey":"`+prevKey+`"}`)
	t.Cleanup(func() { _ = os.Chmod(fh.Home, 0o700) })
	kc := limitedSeatKC{fakeKC: newFakeKC(), onTooLarge: func() {
		// The first config update has landed; the fallback's write cannot,
		// and the config stays readable.
		if err := os.Chmod(fh.Home, 0o500); err != nil {
			t.Fatal(err)
		}
	}}
	s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)
	if live, _, err := s.ReadActive(); live != prevKey || err != nil {
		t.Fatalf("precondition: ReadActive = %q, %v; want the key in primaryApiKey", live, err)
	}

	err := s.WriteActiveAccount(seatKey)
	if err == nil || !strings.Contains(err.Error(), "managed API key for OAuth file fallback") {
		t.Fatalf("WriteActiveAccount = %v, want the fallback config failure reported", err)
	}
	assertKeychainItem(t, kc.fakeKC, managedKeychainService, prevKey)
	if got := readFileOrNone(t, fh.CredentialsFile); got != rest {
		t.Error("the failed write replaced the seat's MCP data in the credentials file")
	}
	for _, reader := range []*FileKeychainStore{s, newStore(t, platform.MacOS, t.TempDir(), kc, nil)} {
		if got, _, _ := reader.ReadActive(); got != prevKey {
			t.Errorf("ReadActive = %q after the failed write, want the previous key", got)
		}
	}
}

// TestWriteActiveAccount_ManagedKeyRestoreFailureIsReported_macOS: when the
// Keychain item cannot be put back either, the error says the new key is
// still there, and carries the Keychain failure beside the config one.
func TestWriteActiveAccount_ManagedKeyRestoreFailureIsReported_macOS(t *testing.T) {
	fh := testutil.BuildFixtureHome(t)
	writeFile(t, fh.GlobalConfig, "{")
	kc := newFakeKC()
	kc.failDelete = true
	s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)

	err := s.WriteActiveAccount(seatKey)
	if err == nil || !strings.Contains(err.Error(), "Failed to write managed API key") ||
		!strings.Contains(err.Error(), "the new API key is still in the Keychain") {
		t.Fatalf("WriteActiveAccount = %v, want the config failure and the failed restore reported", err)
	}
	var ce *cerr.Error
	if !errors.As(err, &ce) || ce.Kind != cerr.KindCredentialWrite {
		t.Errorf("error %v is not a CredentialWriteError", err)
	}
	if !keychain.IsUnusable(err) {
		t.Errorf("error %v does not carry the Keychain failure", err)
	}
	if item, _ := kc.peek(managedKeychainService, keychain.AccountName()); item != seatKey {
		t.Errorf("managed Keychain item = %q, want the key the error reports", item)
	}
}

// TestWriteActiveAccount_ManagedKeyConfigFailureWritesNothing_FileMode: the
// file backend's key is the config write itself, so its failure leaves the
// config, the credentials file and the live credential as they were.
func TestWriteActiveAccount_ManagedKeyConfigFailureWritesNothing_FileMode(t *testing.T) {
	fh := testutil.BuildFixtureHome(t)
	writeFile(t, fh.GlobalConfig, "{")
	file := readFileOrNone(t, fh.CredentialsFile)
	s := newStore(t, platform.Linux, t.TempDir(), newFakeKC(), nil)
	live, _, _ := s.ReadActive()

	err := s.WriteActiveAccount(seatKey)
	if err == nil || !strings.Contains(err.Error(), "Failed to write managed API key") {
		t.Fatalf("WriteActiveAccount = %v, want the config failure reported", err)
	}
	if got := readFileOrNone(t, fh.GlobalConfig); got != "{" {
		t.Errorf("config = %q, want it untouched", got)
	}
	if got := readFileOrNone(t, fh.CredentialsFile); got != file {
		t.Errorf("credentials file = %q, want it untouched", got)
	}
	if got, _, _ := s.ReadActive(); got != live {
		t.Errorf("ReadActive = %q after the failed write, want %q", got, live)
	}
}

// managedReadFailsKC fails the first n reads of the managed-key item.
type managedReadFailsKC struct {
	*fakeKC
	n *int
}

func (k managedReadFailsKC) Get(service, account string) (string, bool, error) {
	if service == managedKeychainService && *k.n > 0 {
		*k.n--
		return "", false, &keychain.KeychainError{Msg: "managed get boom"}
	}
	return k.fakeKC.Get(service, account)
}

// TestWriteActiveAccount_ManagedKeyItemRead_macOS: the key goes into the
// Keychain only once what it replaces has been read, with the active read's
// bounded retry, from a login whose MCP server logins are in the OAuth
// Keychain item with no shadow file. A read that fails once is retried and
// the write runs as with no failure: the key is in the Keychain alone and the
// MCP server logins stay in the OAuth item. A read that keeps failing takes
// the file fallback a failed Keychain write takes, and the key is live from
// primaryApiKey.
func TestWriteActiveAccount_ManagedKeyItemRead_macOS(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures int
		inItem   bool
	}{
		{"fails once", 1, true},
		{"keeps failing", activeReadAttempts, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fh := testutil.BuildFixtureHome(t)
			if err := os.Remove(fh.CredentialsFile); err != nil {
				t.Fatal(err)
			}
			n := tc.failures
			kc := managedReadFailsKC{fakeKC: newFakeKC(), n: &n}
			kc.put(claudeCodeKeychainService, keychain.AccountName(), loginBesideMCP)
			s := newStore(t, platform.MacOS, t.TempDir(), kc, testutil.FixedClock(t, "2026-10-03T12:00:00Z"))

			if err := s.WriteActiveAccount(seatKey); err != nil {
				t.Fatal(err)
			}
			item, present := kc.peek(managedKeychainService, keychain.AccountName())
			if tc.inItem && item != seatKey {
				t.Errorf("managed Keychain item = %q (present %v), want the key after the retried read", item, present)
			}
			if !tc.inItem && present {
				t.Errorf("managed Keychain item = %q, want the key kept out of a Keychain it could not read", item)
			}
			cfg, err := ccfile.ReadGlobalConfig()
			if err != nil {
				t.Fatal(err)
			}
			key, inConfig := cfg["primaryApiKey"]
			if tc.inItem && inConfig {
				t.Errorf("primaryApiKey = %v after the retried read, want the key in the Keychain alone", key)
			}
			if !tc.inItem && key != seatKey {
				t.Errorf("primaryApiKey = %v, want the key", key)
			}
			if tc.inItem {
				if got := s.LastActiveBackend(); got != "keychain" {
					t.Errorf("reported backend %q after the retried read, want keychain", got)
				}
				if got, _ := kc.peek(claudeCodeKeychainService, keychain.AccountName()); got != mcpOnly {
					t.Errorf("OAuth Keychain item = %q after the retried read, want the MCP server logins kept alone", got)
				}
			}
			if got, _, _ := s.ReadActive(); got != seatKey {
				t.Errorf("ReadActive = %q, want the managed key", got)
			}
		})
	}
}
