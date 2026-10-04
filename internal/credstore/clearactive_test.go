// Tests for ClearActive (DESIGN A46): the credential write of a switch onto
// an API-key account with a base URL, whose key goes into Claude Code's
// settings.json instead. Every login leaves the store, the seat-wide part
// stays, and nothing is stored in their place.
package credstore

import (
	"errors"
	"os"
	"testing"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestClearActive_File(t *testing.T) {
	for _, tc := range []struct {
		name, file, key string
		// wantFile is the credentials file afterwards, "" for none.
		wantFile string
	}{
		{"a login beside MCP logins", loginBesideMCP, "", mcpOnly},
		{"a bare login", `{"claudeAiOauth":{"accessToken":"a","refreshToken":"r"}}`, "", ""},
		{"a managed key beside MCP logins", mcpOnly, seatKey, mcpOnly},
		{"a managed key, no file", "", seatKey, ""},
		{"nothing", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fh := testutil.BuildFixtureHome(t)
			if tc.file != "" {
				writeFile(t, fh.CredentialsFile, tc.file)
			} else {
				_ = os.Remove(fh.CredentialsFile)
			}
			if err := ccfile.UpdateGlobalConfig(func(c map[string]any) {
				delete(c, "primaryApiKey")
				if tc.key != "" {
					c["primaryApiKey"] = tc.key
				}
			}); err != nil {
				t.Fatal(err)
			}
			s := newStore(t, platform.Linux, t.TempDir(), newFakeKC(), nil)

			if err := s.ClearActive(); err != nil {
				t.Fatalf("ClearActive: %v", err)
			}
			if got, _, err := s.ReadActive(); err != nil || got != "" {
				t.Errorf("ReadActive = %q, %v; want no credential", got, err)
			}
			raw, err := os.ReadFile(fh.CredentialsFile)
			switch {
			case tc.wantFile == "" && !errors.Is(err, os.ErrNotExist):
				t.Errorf("credentials file = %q (%v), want none", raw, err)
			case tc.wantFile != "" && string(raw) != tc.wantFile:
				t.Errorf("credentials file = %q, want %q", raw, tc.wantFile)
			}
			cfg, err := ccfile.ReadGlobalConfig()
			if err != nil {
				t.Fatal(err)
			}
			if k, present := cfg["primaryApiKey"]; present {
				t.Errorf("primaryApiKey = %v survived", k)
			}
		})
	}
}

func TestClearActive_Keychain_macOS(t *testing.T) {
	testutil.BuildFixtureHome(t)
	kc := newFakeKC()
	kc.m[kckey(claudeCodeKeychainService, keychain.AccountName())] = loginBesideMCP
	kc.m[kckey(managedKeychainService, keychain.AccountName())] = seatKey
	s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)

	if err := s.ClearActive(); err != nil {
		t.Fatalf("ClearActive: %v", err)
	}
	if got, _, err := s.ReadActive(); err != nil || got != "" {
		t.Errorf("ReadActive = %q, %v; want no credential", got, err)
	}
	if v, ok, _ := kc.Get(managedKeychainService, keychain.AccountName()); ok {
		t.Errorf("managed item = %q, want none", v)
	}
	if v, _, _ := kc.Get(claudeCodeKeychainService, keychain.AccountName()); v != mcpOnly {
		t.Errorf("OAuth item = %q, want the MCP logins alone", v)
	}
}
