// The dashboard's side of an API-key account with a base URL (DESIGN A46):
// the add-token route's baseUrl, the account row's baseUrl, and the auth
// overrides notice, which does not count tycswap's own endpoint profile.
package web

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/usage"
)

func TestAddToken_BaseURL(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "email": "gw@example.com", "baseUrl": "  https://gw.example.com/anthropic "})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	want := []string{"AddAccountFromTokenWithBaseURL(<token>,https://gw.example.com/anthropic,gw@example.com,<nil>,true)"}
	if got := h.fa.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}

	// Without a base URL the frozen method is used, as before.
	h2 := newHarness(t)
	if resp := h2.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "baseUrl": "  "}); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := h2.fa.Calls(); !reflect.DeepEqual(got, []string{"AddAccountFromToken(<token>,<nil>,<nil>,true)"}) {
		t.Fatalf("calls %v", got)
	}

	// A facade that cannot store one says so instead of dropping the URL.
	h3 := newHarness(t, withoutBaseURLAdder())
	resp = h3.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "baseUrl": "https://gw.example.com"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	if body := string(readBody(t, resp)); strings.Contains(body, secretSetupToken) {
		t.Errorf("the token was echoed: %s", body)
	}
	if got := h3.fa.Calls(); len(got) != 0 {
		t.Fatalf("facade reached: %v", got)
	}
}

func TestStateRowCarriesTheBaseURL(t *testing.T) {
	h := newHarness(t)
	snap := sampleSnapshot()
	snap.Accounts[1].BaseURL = "https://gw.example.com/anthropic"
	snap.Accounts[1].Usage = usage.UsageEntry{Sentinel: "api key"}
	h.fa.mu.Lock()
	h.fa.snap = snap
	h.fa.mu.Unlock()
	st := decodeJSON(t, h.get("/api/state"))
	rows, _ := st["accounts"].([]any)
	if len(rows) < 2 {
		t.Fatalf("rows = %v", st["accounts"])
	}
	if got := rows[1].(map[string]any)["baseUrl"]; got != "https://gw.example.com/anthropic" {
		t.Errorf("row 2 baseUrl = %v", got)
	}
	if _, present := rows[0].(map[string]any)["baseUrl"]; present {
		t.Errorf("a subscription row carries a baseUrl: %v", rows[0])
	}
}

// TestAuthOverridesLeaveTheEndpointProfileOut: while tycswap's record says
// its endpoint profile is in settings.json, the two keys it wrote are the
// active account's login and are not listed; anything else still is, and
// without the record (or for another settings file) they are listed too.
func TestAuthOverridesLeaveTheEndpointProfileOut(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.json")
	sidecar := filepath.Join(dir, "store", ccsettings.SidecarName)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"apiKeyHelper": "/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ccsettings.Apply(settings, sidecar, ccsettings.Profile{BaseURL: "https://gw.example.com", Token: "k"}); err != nil {
		t.Fatal(err)
	}
	if v := detectAuthOverrides(nil, settings, sidecar); !reflect.DeepEqual(v.Settings, []string{"apiKeyHelper"}) {
		t.Errorf("with the record: %v, want only the user's apiKeyHelper", v.Settings)
	}
	want := []string{"apiKeyHelper", "env.ANTHROPIC_AUTH_TOKEN", "env.ANTHROPIC_BASE_URL"}
	if v := detectAuthOverrides(nil, settings, ""); !reflect.DeepEqual(v.Settings, want) {
		t.Errorf("without a record: %v, want %v", v.Settings, want)
	}
	other := filepath.Join(dir, "other", "settings.json")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(settings)
	if err := os.WriteFile(other, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if v := detectAuthOverrides(nil, other, sidecar); !reflect.DeepEqual(v.Settings, want) {
		t.Errorf("another settings file: %v, want %v", v.Settings, want)
	}
}
