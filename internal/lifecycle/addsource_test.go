package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
)

func TestCheckLoginDir(t *testing.T) {
	const (
		oauthCfg   = `{"oauthAccount":{"emailAddress":"b@example.com","organizationUuid":""}}`
		oauthCreds = `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-test-token-1","refreshToken":"refresh-token-1"}}`
		incomplete = "did not complete"
		apiKey     = "made an API key"
	)
	for _, tc := range []struct {
		name, cfg, creds, want string
	}{
		{"subscription login", oauthCfg, oauthCreds, ""},
		{"nothing written", "", "", incomplete},
		{"config only", oauthCfg, "", incomplete},
		{"credentials only", "", oauthCreds, incomplete},
		{"no email", `{"oauthAccount":{}}`, oauthCreds, incomplete},
		{"bare api key", oauthCfg, "sk-ant-api03-test-key-1", apiKey},
		{"credentials without claudeAiOauth", oauthCfg, `{"apiKey":"sk-ant-api03-test-key-1"}`, apiKey},
		{"console key in config", `{"primaryApiKey":"sk-ant-api03-test-key-1"}`, "", apiKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.cfg != "" {
				if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(tc.cfg), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.creds != "" {
				if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(tc.creds), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := CheckLogin(LoginDir(dir, nil))
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestCheckLoginKeychainFallback: with no .credentials.json, a login whose
// credential Claude Code put in the Keychain (macOS) is read from the item
// keyed by the scratch dir; no item is an incomplete login; the file, when
// present, wins.
func TestCheckLoginKeychainFallback(t *testing.T) {
	const (
		cfg   = `{"oauthAccount":{"emailAddress":"b@example.com","organizationUuid":""}}`
		creds = `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-test-token-1","refreshToken":"refresh-token-1"}}`
	)
	newDir := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("item present", func(t *testing.T) {
		dir := newDir(t)
		kc := keychain.NewFake()
		_ = kc.Set(LoginKeychainService(dir), keychain.AccountName(), creds)
		src := LoginDir(dir, kc)
		if err := CheckLogin(src); err != nil {
			t.Fatalf("CheckLogin = %v, want nil", err)
		}
		got, found, err := src.loginCredentials()
		if err != nil || !found || got != creds {
			t.Errorf("loginCredentials = %q %v %v", got, found, err)
		}
	})
	t.Run("item for another dir", func(t *testing.T) {
		dir := newDir(t)
		kc := keychain.NewFake()
		_ = kc.Set(LoginKeychainService(dir+"-other"), keychain.AccountName(), creds)
		if err := CheckLogin(LoginDir(dir, kc)); err == nil || !strings.Contains(err.Error(), "did not complete") {
			t.Errorf("CheckLogin = %v, want did not complete", err)
		}
	})
	t.Run("no item", func(t *testing.T) {
		if err := CheckLogin(LoginDir(newDir(t), keychain.NewFake())); err == nil || !strings.Contains(err.Error(), "did not complete") {
			t.Errorf("CheckLogin = %v, want did not complete", err)
		}
	})
	t.Run("api key item", func(t *testing.T) {
		dir := newDir(t)
		kc := keychain.NewFake()
		_ = kc.Set(LoginKeychainService(dir), keychain.AccountName(), "sk-ant-api03-test-key-1")
		if err := CheckLogin(LoginDir(dir, kc)); err == nil || !strings.Contains(err.Error(), "made an API key") {
			t.Errorf("CheckLogin = %v, want the API-key refusal", err)
		}
	})
}

// TestLoginDirMaterialStoresTheAccountOnly: a login directory's credential is
// stored without any mcpOAuth it might carry, like the live login's.
func TestLoginDirMaterialStoresTheAccountOnly(t *testing.T) {
	const (
		cfg   = `{"oauthAccount":{"emailAddress":"b@example.com","organizationUuid":""}}`
		creds = `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-test-token-1","refreshToken":"refresh-token-1"},"mcpOAuth":{"srv|1111":{"accessToken":"mcp-login"}}}`
	)
	dir := t.TempDir()
	for name, body := range map[string]string{".claude.json": cfg, ".credentials.json": creds} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, gotCfg, err := LoginDir(dir, nil).material(nil)
	if err != nil {
		t.Fatalf("material: %v", err)
	}
	if strings.Contains(got, "mcpOAuth") {
		t.Errorf("login credential stored with mcpOAuth: %s", got)
	}
	if !strings.Contains(got, "sk-ant-oat01-test-token-1") || !strings.Contains(got, "refresh-token-1") {
		t.Errorf("login credential lost its account part: %s", got)
	}
	if gotCfg != cfg {
		t.Errorf("config = %q, want verbatim", gotCfg)
	}
}
