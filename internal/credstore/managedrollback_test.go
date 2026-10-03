// A write of a managed key that fails after the key reached the macOS Keychain
// must leave the Keychain as it found it. A switch rolls the credential back
// only after a write that succeeded, so a key a failed write leaves in the
// Keychain is one nothing removes: it outlives the failed switch beside the
// OAuth login, or replaces the key the switcher still records as live. The file
// backend stores the key and its approval in one ~/.claude.json write, so there
// a failed write leaves nothing behind.
package credstore

import (
	"os"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/ccfile"
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

// TestWriteActive_ManagedKeyConfigFailureRestoresTheKeychain_macOS: the key is
// stored in the Keychain, then the ~/.claude.json update recording its
// approval fails. The error leaves the managed Keychain item as it was (no
// item, or the previous key), and the OAuth credential, its shadow file and
// the config untouched.
func TestWriteActive_ManagedKeyConfigFailureRestoresTheKeychain_macOS(t *testing.T) {
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

			err = s.WriteActive(seatKey)
			if err == nil || !strings.Contains(err.Error(), "Failed to write managed API key") {
				t.Fatalf("WriteActive = %v, want the config failure reported", err)
			}
			item, present := kc.peek(managedKeychainService, keychain.AccountName())
			switch {
			case tc.prev == "" && present:
				t.Errorf("the failed write left the key in the managed Keychain item (%q)", item)
			case tc.prev != "" && item != tc.prev:
				t.Errorf("managed Keychain item = %q, want the previous key back", item)
			}
			if item, present := kc.peek(claudeCodeKeychainService, keychain.AccountName()); item != tc.oauth || present != (tc.oauth != "") {
				t.Errorf("OAuth Keychain item = %q (present %v), want it untouched", item, present)
			}
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

// TestWriteActive_ManagedKeyFallbackConfigFailureRestoresTheKeychain_macOS:
// an API-key seat whose MCP data is too large for the Keychain. The new key
// lands in the Keychain and its approval in the config, then the config write
// that must precede the file fallback fails. The previous key is the
// Keychain's again, so it is still the live credential.
func TestWriteActive_ManagedKeyFallbackConfigFailureRestoresTheKeychain_macOS(t *testing.T) {
	fh := testutil.BuildFixtureHome(t)
	rest := largeSeatRemainder()
	writeFile(t, fh.CredentialsFile, rest)
	kc := limitedSeatKC{fakeKC: newFakeKC(), onTooLarge: func() {
		writeFile(t, fh.GlobalConfig, "{")
	}}
	kc.put(managedKeychainService, keychain.AccountName(), prevKey)
	s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)
	if live, _, err := s.ReadActive(); live != prevKey || err != nil {
		t.Fatalf("precondition: ReadActive = %q, %v; want the previous key", live, err)
	}

	err := s.WriteActive(seatKey)
	if err == nil || !strings.Contains(err.Error(), "managed API key for OAuth file fallback") {
		t.Fatalf("WriteActive = %v, want the fallback config failure reported", err)
	}
	if item, _ := kc.peek(managedKeychainService, keychain.AccountName()); item != prevKey {
		t.Errorf("managed Keychain item = %q, want the previous key back", item)
	}
	if _, present := kc.peek(claudeCodeKeychainService, keychain.AccountName()); present {
		t.Error("the failed write created an OAuth Keychain item")
	}
	if got := readFileOrNone(t, fh.CredentialsFile); got != rest {
		t.Error("the failed write replaced the seat's MCP data in the credentials file")
	}
	if got, _, _ := s.ReadActive(); got != prevKey {
		t.Errorf("ReadActive = %q after the failed write, want the previous key", got)
	}
}

// TestWriteActive_ManagedKeyConfigFailureWritesNothing_FileMode: the file
// backend's key is the config write itself, so its failure leaves the config,
// the credentials file and the live credential as they were.
func TestWriteActive_ManagedKeyConfigFailureWritesNothing_FileMode(t *testing.T) {
	fh := testutil.BuildFixtureHome(t)
	writeFile(t, fh.GlobalConfig, "{")
	file := readFileOrNone(t, fh.CredentialsFile)
	s := newStore(t, platform.Linux, t.TempDir(), newFakeKC(), nil)
	live, _, _ := s.ReadActive()

	err := s.WriteActive(seatKey)
	if err == nil || !strings.Contains(err.Error(), "Failed to write managed API key") {
		t.Fatalf("WriteActive = %v, want the config failure reported", err)
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

// TestWriteActive_ManagedKeyUnreadableKeychainItemFallsBackToConfig_macOS:
// the key goes into the Keychain only once what it replaces has been read, so
// a failure can put that back. A Keychain that cannot read the item takes the
// file fallback a failed Keychain write takes.
func TestWriteActive_ManagedKeyUnreadableKeychainItemFallsBackToConfig_macOS(t *testing.T) {
	testutil.BuildFixtureHome(t)
	kc := newFakeKC()
	kc.failGet = true
	s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)

	if err := s.WriteActive(seatKey); err != nil {
		t.Fatal(err)
	}
	if item, present := kc.peek(managedKeychainService, keychain.AccountName()); present {
		t.Errorf("managed Keychain item = %q, want the key kept out of a Keychain it could not read", item)
	}
	if got := readManagedKeyFromConfig(t); got != seatKey {
		t.Errorf("primaryApiKey = %q, want the key", got)
	}
	if got, _, _ := s.ReadActive(); got != seatKey {
		t.Errorf("ReadActive = %q, want the managed key", got)
	}
	if s.LastActiveBackend() != "file" {
		t.Errorf("reported backend %q, want file", s.LastActiveBackend())
	}
}

// readManagedKeyFromConfig returns ~/.claude.json's primaryApiKey.
func readManagedKeyFromConfig(t *testing.T) string {
	t.Helper()
	cfg, err := ccfile.ReadGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := cfg["primaryApiKey"].(string)
	return key
}
