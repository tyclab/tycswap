// helpers_test.go — a fake ~/.codex tree and JWT/auth.json builders. Ports
// claude-swap PR #252 tests/conftest_codex.py.
//
// No test may touch the developer's real ~/.codex or backup root: isolate
// points HOME (and USERPROFILE), XDG_DATA_HOME and CODEX_HOME at t.TempDir.

package authfile

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.dpemmons.com/dpemmons/cswap/internal/testutil"
)

const (
	testAccount = "2f4dac8f-f15f-4c58-a567-e96985d51cfd"
	testUser    = "user-K6XCCWw4gcRpfaGR6VQKAFgA"
)

// isolate redirects every root this package resolves into a fresh temp home
// and returns it. CODEX_HOME is unset so Home() falls back to <home>/.codex.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testutil.Setenv(t, "HOME", home)
	testutil.Setenv(t, "USERPROFILE", home)
	testutil.Setenv(t, "XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	testutil.Unsetenv(t, "CODEX_HOME")
	return home
}

// codexHome builds an isolated, empty ~/.codex and returns it.
func codexHome(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(isolate(t), ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeLiveAuth writes payload as the fake codex home's auth.json.
func writeLiveAuth(t *testing.T, dir string, payload map[string]any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// makeJWT builds an unsigned JWT carrying claims. The signature is a fixed
// placeholder: cswap never verifies these tokens, it only reads the claims.
func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	seg := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return seg(map[string]any{"alg": "none", "typ": "JWT"}) + "." + seg(claims) + ".sig"
}

// authOpts are make_auth_json's keyword arguments; zero values take its defaults.
type authOpts struct {
	Email         string
	Plan          string
	Exp           int64
	Organizations []any
}

// makeAuthJSON builds an auth.json payload in the real shape codex writes.
func makeAuthJSON(t *testing.T, o authOpts) map[string]any {
	t.Helper()
	if o.Email == "" {
		o.Email = "a@example.com"
	}
	if o.Plan == "" {
		o.Plan = "pro"
	}
	if o.Exp == 0 {
		o.Exp = time.Now().Unix() + 3600
	}
	auth := map[string]any{
		"chatgpt_account_id": testAccount,
		"chatgpt_user_id":    testUser,
		"chatgpt_plan_type":  o.Plan,
	}
	if o.Organizations != nil {
		auth["organizations"] = o.Organizations
	}
	token := makeJWT(t, map[string]any{"exp": o.Exp, "email": o.Email, AuthClaim: auth})
	return map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      token,
			"access_token":  token,
			"refresh_token": "rt-a",
			"account_id":    testAccount,
		},
		"last_refresh": "2026-08-16T00:00:00Z",
	}
}

// stripAccountClaim removes chatgpt_account_id and tokens.account_id,
// reproducing a phone-login auth file that only the organization fallback can
// identify. The JWT is rewritten so the removal is visible to the parser.
func stripAccountClaim(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	tokens := payload["tokens"].(map[string]any)
	claims := DecodeJWTClaims(tokens["id_token"])
	delete(claims[AuthClaim].(map[string]any), "chatgpt_account_id")
	token := makeJWT(t, claims)
	tokens["id_token"] = token
	tokens["access_token"] = token
	tokens["account_id"] = nil
	return payload
}
