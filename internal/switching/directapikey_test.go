// End-to-end direct activations onto and away from an API key, on the file
// backend with a real temp store. A direct activation (`--force`, a fresh
// machine, a live login no slot holds) reads ~/.claude.json before it writes
// the credential, to refuse a corrupt one. The credential write changes that
// file itself: a managed key is stored there (primaryApiKey and its approval)
// and an OAuth write drops it. The oauthAccount must therefore go into the file
// as it is after that write, or the key is lost, or a replaced key stays.
package switching

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

// liveConfig returns the decoded live ~/.claude.json.
func liveConfig(t *testing.T, s *store.Store) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Home, ".claude.json"))
	if err != nil {
		t.Fatalf("live config: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("live config is not JSON: %v\n%s", err, raw)
	}
	return m
}

// approvedKey reports whether cfg approves key the way Claude Code records it.
func approvedKey(cfg map[string]any, key string) bool {
	responses, _ := cfg["customApiKeyResponses"].(map[string]any)
	approved, _ := responses["approved"].([]any)
	for _, v := range approved {
		if v == credstore.ApprovedForm(key) {
			return true
		}
	}
	return false
}

// oauthEmail returns the live config's oauthAccount email.
func oauthEmail(cfg map[string]any) string {
	acct, _ := cfg["oauthAccount"].(map[string]any)
	email, _ := acct["emailAddress"].(string)
	return email
}

// TestDirectActivationOntoAnAPIKeyLeavesTheKeyLive: every way into the direct
// activation ends with the target's key live, approved, and the target's
// oauthAccount in the config. The seat's MCP server logins stay in the
// credentials file beside the key.
func TestDirectActivationOntoAnAPIKeyLeavesTheKeyLive(t *testing.T) {
	cases := []struct {
		name  string
		force bool
		seed  func(t *testing.T, s *store.Store)
		// wantFile is the live credentials file afterwards, "" for none.
		wantFile string
	}{
		{"--force over a managed login", true, func(t *testing.T, s *store.Store) {
			seedLive(t, s, oauthSeatEmail, "", withMCPOAuth(t, oauthCreds("acc-a", "ref-a"), "srv|1111", "mcp-live"))
		}, `{"mcpOAuth":{"srv|1111":{"accessToken":"mcp-live","refreshToken":"mcp-live-refresh"}}}`},
		{"a live login no slot holds", false, func(t *testing.T, s *store.Store) {
			seedLive(t, s, "stranger@x.com", "", oauthCreds("acc-x", "ref-x"))
		}, ""},
		{"a fresh machine", false, func(t *testing.T, s *store.Store) {}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t, nil)
			writeSeq(t, s, seqData(nil, []int{1, 2}, map[string]json.RawMessage{
				"1": record(map[string]any{"email": oauthSeatEmail, "organizationUuid": ""}),
				"2": record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key"}),
			}))
			seedBackup(t, s, "1", oauthSeatEmail, oauthCreds("acc-a", "ref-a"), "")
			seedBackup(t, s, "2", apiKeySeatEmail, apiKeySeatKey, "")
			tc.seed(t, s)

			ApproveAPIKeySwitch("2") // the user confirmed the auth-mode change (DESIGN A33)
			if _, err := SwitchTo(s, "2", true, tc.force); err != nil {
				t.Fatalf("SwitchTo(2): %v", err)
			}
			if got := readActiveCreds(t, s); got != apiKeySeatKey {
				t.Errorf("live credential = %q, want the account's key", got)
			}
			cfg := liveConfig(t, s)
			if cfg["primaryApiKey"] != apiKeySeatKey {
				t.Errorf("primaryApiKey = %v, want the account's key", cfg["primaryApiKey"])
			}
			if !approvedKey(cfg, apiKeySeatKey) {
				t.Errorf("the key is not approved: customApiKeyResponses = %v", cfg["customApiKeyResponses"])
			}
			if got := oauthEmail(cfg); got != apiKeySeatEmail {
				t.Errorf("oauthAccount email = %q, want %q", got, apiKeySeatEmail)
			}
			raw, err := os.ReadFile(liveCredentialsPath(s))
			switch {
			case tc.wantFile == "" && !os.IsNotExist(err):
				t.Errorf("live credentials file = %q (%v), want none", raw, err)
			case tc.wantFile != "" && string(raw) != tc.wantFile:
				t.Errorf("live credentials file = %q, want the seat's MCP logins alone %q", raw, tc.wantFile)
			}
		})
	}
}

// TestForcedActivationFromAnAPIKeyClearsTheKey: a --force from a live API key
// onto a subscription account leaves no key in ~/.claude.json, so nothing but
// the OAuth login is live, and the seat's MCP server logins ride over it.
func TestForcedActivationFromAnAPIKeyClearsTheKey(t *testing.T) {
	s := newTestStore(t, nil)
	apiKeySeat(t, s, seatWideFiles[0].file)
	if got := liveConfig(t, s)["primaryApiKey"]; got != apiKeySeatKey {
		t.Fatalf("precondition: primaryApiKey = %v, want the key live", got)
	}

	if _, err := SwitchTo(s, "1", true, true); err != nil {
		t.Fatalf("SwitchTo(1) --force: %v", err)
	}
	cfg := liveConfig(t, s)
	if key, present := cfg["primaryApiKey"]; present {
		t.Errorf("primaryApiKey = %v is still in ~/.claude.json after the activation onto a subscription account", key)
	}
	if got := oauthEmail(cfg); got != oauthSeatEmail {
		t.Errorf("oauthAccount email = %q, want %q", got, oauthSeatEmail)
	}
	live := readActiveCreds(t, s)
	if got := oauth.ExtractAccessToken(live); got != "acc-a" {
		t.Errorf("live access token = %q, want the subscription login's", got)
	}
	if !hasMCPOAuth(t, live) {
		t.Errorf("live credential %s lost the seat's MCP server logins", live)
	}
}
