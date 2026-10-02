// Tests for an OAuth credential holding nothing but seat-wide keys (DESIGN
// A29): Claude Code writes such a file, or Keychain item, when an MCP server
// is signed in to or added with a client secret while an API key is active. It
// is no OAuth login, so ReadActive reads the managed key behind it.
package credstore

import (
	"os"
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
