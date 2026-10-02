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

// limitedSeatKC reproduces security -i's real payload limit: the managed API
// key fits, but a credential containing a large MCP token does not.
type limitedSeatKC struct {
	*fakeKC
	onTooLarge func()
}

func (f limitedSeatKC) Set(service, account, password string) error {
	if !keychain.FitsStdin(service, account, password) {
		if f.onTooLarge != nil {
			f.onTooLarge()
		}
		return &keychain.KeychainError{Msg: "secret too large for Keychain stdin", TooLarge: true}
	}
	return f.fakeKC.Set(service, account, password)
}

func largeSeatRemainder() string {
	return `{"mcpOAuth":{"server":{"accessToken":"` + strings.Repeat("x", 3000) + `"}},"mcpOAuthClientConfig":{"server":{"clientSecret":"client-secret-fixture"}}}`
}

func TestWriteActive_ManagedKeySurvivesRemainderFileFallback_macOS(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		name := "no shadow file"
		if shadow {
			name = "existing shadow file"
		}
		t.Run(name, func(t *testing.T) {
			fh := testutil.BuildFixtureHome(t)
			rest := largeSeatRemainder()
			live := `{"claudeAiOauth":{"accessToken":"oauth-fixture"},` + rest[1:]
			if shadow {
				writeFile(t, fh.CredentialsFile, live)
			} else if err := os.Remove(fh.CredentialsFile); err != nil {
				t.Fatal(err)
			}
			kc := limitedSeatKC{fakeKC: newFakeKC()}
			kc.put(claudeCodeKeychainService, keychain.AccountName(), live)
			s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)
			if err := s.WriteActive(seatKey); err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(fh.CredentialsFile); err != nil || string(raw) != rest {
				t.Fatalf("seat-wide remainder was not preserved: err=%v", err)
			}
			if _, found := kc.peek(claudeCodeKeychainService, keychain.AccountName()); found {
				t.Fatal("old OAuth Keychain login survived the file fallback")
			}
			if stored, _ := kc.peek(managedKeychainService, keychain.AccountName()); stored != seatKey {
				t.Fatal("managed Keychain copy was lost")
			}
			cfg, err := ccfile.ReadGlobalConfig()
			if err != nil || cfg["primaryApiKey"] != seatKey {
				t.Fatalf("managed key missing from fallback config: %v", err)
			}
			if s.LastActiveBackend() != "file" {
				t.Fatalf("reported backend %q, want file", s.LastActiveBackend())
			}
			for _, reader := range []*FileKeychainStore{s, newStore(t, platform.MacOS, t.TempDir(), kc, nil)} {
				got, unavailable, err := reader.ReadActive()
				if got != seatKey || unavailable || err != nil {
					t.Fatalf("API key unreadable after successful write: unavailable=%v err=%v", unavailable, err)
				}
			}
		})
	}
}

func TestWriteActive_RemainderFallbackConfigFailureIsReported_macOS(t *testing.T) {
	fh := testutil.BuildFixtureHome(t)
	originalShadow, err := os.ReadFile(fh.CredentialsFile)
	if err != nil {
		t.Fatal(err)
	}
	kc := limitedSeatKC{fakeKC: newFakeKC(), onTooLarge: func() {
		// Simulate a config becoming invalid after the first successful write,
		// before the remainder forces a second write for the managed key.
		writeFile(t, fh.GlobalConfig, "{")
	}}
	live := `{"claudeAiOauth":{"accessToken":"original-oauth-fixture"},` + largeSeatRemainder()[1:]
	kc.put(claudeCodeKeychainService, keychain.AccountName(), live)
	s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)
	err = s.WriteActive(seatKey)
	if err == nil || !strings.Contains(err.Error(), "managed API key for OAuth file fallback") {
		t.Fatalf("fallback config write failure was not reported: %v", err)
	}
	if stored, _ := kc.peek(managedKeychainService, keychain.AccountName()); stored != seatKey {
		t.Fatal("the failure lost the successful managed Keychain copy")
	}
	if original, _ := kc.peek(claudeCodeKeychainService, keychain.AccountName()); original != live {
		t.Fatal("the failed write replaced the original OAuth login or MCP credentials")
	}
	if raw, _ := os.ReadFile(fh.CredentialsFile); string(raw) != string(originalShadow) {
		t.Fatal("the failed write replaced the original shadow credential")
	}
	if got, _, err := s.ReadActive(); got != live || err != nil {
		t.Fatalf("the original OAuth login is no longer active: %v", err)
	}
}

// A Keychain read can fail after the managed-key write succeeded. Without a
// remainder in the file, clearing the old login still needs a readable key.
type unreadableOAuthKC struct{ *fakeKC }

func (f unreadableOAuthKC) Get(service, account string) (string, bool, error) {
	if service == claudeCodeKeychainService {
		return "", false, &keychain.KeychainError{Msg: "OAuth item unavailable"}
	}
	return f.fakeKC.Get(service, account)
}

func TestWriteActive_ManagedKeySurvivesOAuthReadFailure_macOS(t *testing.T) {
	testutil.BuildFixtureHome(t) // shadow has OAuth only, no seat-wide keys
	kc := unreadableOAuthKC{newFakeKC()}
	s := newStore(t, platform.MacOS, t.TempDir(), kc, testutil.FixedClock(t, "2026-10-02T12:00:00Z"))
	if err := s.WriteActive(seatKey); err != nil {
		t.Fatal(err)
	}
	if got, unavailable, err := s.ReadActive(); got != seatKey || unavailable || err != nil {
		t.Fatalf("managed key became unreadable after the OAuth read failed: unavailable=%v err=%v", unavailable, err)
	}
	if s.LastActiveBackend() != "file" {
		t.Fatalf("reported backend %q, want file", s.LastActiveBackend())
	}
}
