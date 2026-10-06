// Tests for an OAuth credential holding nothing but seat-wide keys (DESIGN
// A29): Claude Code writes such a file, or Keychain item, when an MCP server
// is signed in to or added with a client secret while an API key is active. It
// is no OAuth login, so ReadActive reads the managed key behind it.
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

// seatKey is shaped like a managed key so the store routes it to Claude Code's
// managed-key path, and is nothing more.
const seatKey = "sk-ant-api03-seat-fixture-0123456789"

const (
	mcpOnly        = `{"mcpOAuth":{"srv|1111":{"accessToken":"mcp-live"}}}`
	clientOnly     = `{"mcpOAuthClientConfig":{"srv|1111":{"clientSecret":"cs-fixture"}}}`
	seatWideBoth   = `{"mcpOAuth":{"srv|1111":{"accessToken":"mcp-live"}},"mcpOAuthClientConfig":{"srv|1111":{"clientSecret":"cs-fixture"}}}`
	loginBesideMCP = `{"claudeAiOauth":{"accessToken":"live-access","refreshToken":"live-refresh"},"mcpOAuth":{"srv|1111":{"accessToken":"mcp-live"}}}`
)

func TestReadActive_SeatWideOnlyFileIsNoLogin(t *testing.T) {
	for _, tc := range []struct {
		name, file, key, want string
	}{
		{"MCP server logins, key behind", mcpOnly, seatKey, seatKey},
		{"MCP client secrets, key behind", clientOnly, seatKey, seatKey},
		{"both, key behind", seatWideBoth, seatKey, seatKey},
		{"the empty object, key behind", `{}`, seatKey, seatKey},
		{"MCP server logins, no key", mcpOnly, "", ""},
		{"MCP client secrets, no key", clientOnly, "", ""},
		{"the empty object, no key", `{}`, "", ""},
		{"a login beside the MCP logins is the login", loginBesideMCP, seatKey, loginBesideMCP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fh := testutil.BuildFixtureHome(t)
			writeFile(t, fh.CredentialsFile, tc.file)
			if err := ccfile.UpdateGlobalConfig(func(c map[string]any) {
				delete(c, "primaryApiKey")
				if tc.key != "" {
					c["primaryApiKey"] = tc.key
				}
			}); err != nil {
				t.Fatal(err)
			}
			s := newStore(t, platform.Linux, t.TempDir(), newFakeKC(), nil)

			got, kcUnavailable, err := s.ReadActive()
			if err != nil || kcUnavailable {
				t.Fatalf("ReadActive: err=%v keychainUnavailable=%v", err, kcUnavailable)
			}
			if got != tc.want {
				t.Fatalf("ReadActive = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReadActive_SeatWideOnlyKeychainItem_macOS: the same on the Keychain
// seam, with and without a shadow file holding the same bytes.
func TestReadActive_SeatWideOnlyKeychainItem_macOS(t *testing.T) {
	for _, tc := range []struct {
		name, item string
		shadow     bool
	}{
		{"MCP server logins", mcpOnly, false},
		{"MCP client secrets", clientOnly, false},
		{"MCP server logins with the shadow file", mcpOnly, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fh := testutil.BuildFixtureHome(t)
			if tc.shadow {
				writeFile(t, fh.CredentialsFile, tc.item)
			} else if err := os.Remove(fh.CredentialsFile); err != nil {
				t.Fatal(err)
			}
			kc := newFakeKC()
			kc.put(claudeCodeKeychainService, keychain.AccountName(), tc.item)
			kc.put(managedKeychainService, keychain.AccountName(), seatKey)
			s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)

			got, kcUnavailable, err := s.ReadActive()
			if err != nil || kcUnavailable {
				t.Fatalf("ReadActive: err=%v keychainUnavailable=%v", err, kcUnavailable)
			}
			if got != seatKey {
				t.Fatalf("ReadActive = %q, want the managed Keychain key behind the item", got)
			}
		})
	}
}

// TestReadActive_LoginKeychainItemBesideMCP_macOS: a Keychain item holding a
// login is the login, whatever seat-wide keys sit beside it.
func TestReadActive_LoginKeychainItemBesideMCP_macOS(t *testing.T) {
	testutil.BuildFixtureHome(t)
	kc := newFakeKC()
	kc.put(claudeCodeKeychainService, keychain.AccountName(), loginBesideMCP)
	kc.put(managedKeychainService, keychain.AccountName(), seatKey)
	s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)

	if got, _, _ := s.ReadActive(); got != loginBesideMCP {
		t.Fatalf("ReadActive = %q, want the Keychain login verbatim", got)
	}
}

// -- a switch onto an API key keeps the seat-wide part -----------------------

func TestWriteActive_APIKeyKeepsTheSeatWidePart_FileMode(t *testing.T) {
	for _, tc := range []struct{ name, live, want string }{
		{"a login beside MCP server logins", loginBesideMCP, mcpOnly},
		{"a login beside MCP client secrets", `{"claudeAiOauth":{"accessToken":"live-access"},"mcpOAuthClientConfig":{"srv|1111":{"clientSecret":"cs-fixture"}},"trustedDeviceToken":"device-live"}`, clientOnly},
		{"seat-wide keys only", seatWideBoth, seatWideBoth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fh := testutil.BuildFixtureHome(t)
			writeFile(t, fh.CredentialsFile, tc.live)
			s := newStore(t, platform.Linux, t.TempDir(), newFakeKC(), nil)

			if err := s.WriteActive(seatKey); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(fh.CredentialsFile)
			if err != nil {
				t.Fatalf("the seat-wide keys went with the OAuth login: %v", err)
			}
			if string(raw) != tc.want {
				t.Fatalf("credentials file = %s, want the seat-wide part alone %s", raw, tc.want)
			}
			if got, _, _ := s.ReadActive(); got != seatKey {
				t.Fatalf("ReadActive = %q, want the managed key", got)
			}
		})
	}
}

func TestWriteActive_APIKeyWithoutASeatWidePartClearsTheFile(t *testing.T) {
	for _, live := range []string{
		storedAcct,     // a login and nothing seat-wide
		`{"mcpOAuth":`, // malformed: nothing can be kept
	} {
		fh := testutil.BuildFixtureHome(t)
		writeFile(t, fh.CredentialsFile, live)
		s := newStore(t, platform.Linux, t.TempDir(), newFakeKC(), nil)

		if err := s.WriteActive(seatKey); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(fh.CredentialsFile); err == nil {
			t.Fatalf("live %q: a credential with no seat-wide part must be cleared whole", live)
		}
	}
}

func TestWriteActive_APIKeyKeepsTheSeatWidePart_Keychain_macOS(t *testing.T) {
	fh := testutil.BuildFixtureHome(t) // the shadow .credentials.json is present
	kc := newFakeKC()
	kc.put(claudeCodeKeychainService, keychain.AccountName(), loginBesideMCP)
	s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)

	if err := s.WriteActive(seatKey); err != nil {
		t.Fatal(err)
	}
	if v, _ := kc.peek(managedKeychainService, keychain.AccountName()); v != seatKey {
		t.Fatalf("managed Keychain item = %q, want the key", v)
	}
	item, ok := kc.peek(claudeCodeKeychainService, keychain.AccountName())
	if !ok || item != mcpOnly {
		t.Fatalf("OAuth Keychain item = %q (present %v), want the MCP server logins alone", item, ok)
	}
	// The shadow file carries the same bytes, as after every Keychain write.
	if raw, _ := os.ReadFile(fh.CredentialsFile); string(raw) != mcpOnly {
		t.Errorf("shadow .credentials.json = %s, want the Keychain item's bytes", raw)
	}
	if got, _, _ := s.ReadActive(); got != seatKey {
		t.Fatalf("ReadActive = %q, want the managed key", got)
	}
}

// TestWriteActive_APIKeyKeychainWriteFailsKeepsTheSeatWidePartInTheFile_macOS:
// when the Keychain cannot be written, the key falls back to primaryApiKey and
// the seat-wide part to the plaintext file (the shadow of the Keychain item),
// as any OAuth write does, and the stale Keychain login goes.
func TestWriteActive_APIKeyKeychainWriteFailsKeepsTheSeatWidePartInTheFile_macOS(t *testing.T) {
	fh := testutil.BuildFixtureHome(t)
	writeFile(t, fh.CredentialsFile, loginBesideMCP)
	kc := newFakeKC()
	kc.put(claudeCodeKeychainService, keychain.AccountName(), loginBesideMCP)
	kc.failSet = true
	s := newStore(t, platform.MacOS, t.TempDir(), kc, testutil.FixedClock(t, "2026-07-17T00:00:00Z"))

	if err := s.WriteActive(seatKey); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(fh.CredentialsFile); string(raw) != mcpOnly {
		t.Fatalf("credentials file = %s, want the MCP server logins alone", raw)
	}
	if _, ok := kc.peek(claudeCodeKeychainService, keychain.AccountName()); ok {
		t.Fatal("the Keychain login survived beside the API key")
	}
	if got, _, _ := s.ReadActive(); got != seatKey {
		t.Fatalf("ReadActive = %q, want the managed key", got)
	}
}

// -- a slot backup holds the account part only (DESIGN A59) -------------------

func assertAccountOnly(t *testing.T, what, text string) {
	t.Helper()
	if strings.Contains(text, "mcpOAuth") || !strings.Contains(text, `"refreshToken":"live-refresh"`) {
		t.Fatalf("%s = %s, want the account part without seat-wide keys", what, text)
	}
}

func TestWriteBackup_StoresTheAccountPartOnly(t *testing.T) {
	s := newStore(t, platform.Linux, t.TempDir(), newFakeKC(), nil)
	if err := s.WriteBackup("2", "bob@e.com", loginBesideMCP); err != nil {
		t.Fatal(err)
	}
	raw, ok := s.decodeEnc(s.backupEncPath("2", "bob@e.com"), "read")
	if !ok {
		t.Fatal("no .enc written")
	}
	assertAccountOnly(t, "stored .enc", raw)
}

// TestReadBackup_SetsALegacySlotsSeatWideKeysAside: a slot stored before the
// split reads as its account part from the .enc and the macOS Keychain alike,
// and its stored bytes stay until the slot's next write.
func TestReadBackup_SetsALegacySlotsSeatWideKeysAside(t *testing.T) {
	for _, tc := range []struct {
		name string
		plat platform.Platform
	}{{".enc", platform.Linux}, {"Keychain", platform.MacOS}} {
		t.Run(tc.name, func(t *testing.T) {
			kc := newFakeKC()
			s := newStore(t, tc.plat, t.TempDir(), kc, nil)
			user, path := s.backupUsername("2", "bob@e.com"), s.backupEncPath("2", "bob@e.com")
			if tc.plat == platform.MacOS {
				kc.put(securityService, user, loginBesideMCP)
			} else {
				if err := os.MkdirAll(s.credentialsDir, 0o700); err != nil {
					t.Fatal(err)
				}
				writeFile(t, path, b64(loginBesideMCP))
			}
			got, err := s.ReadBackup("2", "bob@e.com")
			if err != nil {
				t.Fatal(err)
			}
			assertAccountOnly(t, "ReadBackup", got)
			item, _ := kc.peek(securityService, user)
			onDisk, _ := os.ReadFile(path)
			if item != loginBesideMCP && string(onDisk) != b64(loginBesideMCP) {
				t.Fatal("ReadBackup rewrote the slot; it must leave the bytes until the next write")
			}
		})
	}
}
