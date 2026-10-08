// Package reporting is the switcher's read surface: list/status renderers and --json payloads, the usage collector, warnings, AccountsSnapshot.
package reporting

import (
	"encoding/json"

	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

func init() {
	// The JSON usage projection recomputes each window's countdown/clock at
	// serialization time via oauth.fresh_reset_strings (spec 02§10.1). jsonout
	// was kept a leaf with no oauth import (WP0 note); reporting installs the
	// oauth-backed implementation into its ResetStrings seam. The human path in
	// render.go calls oauth.FreshResetStrings directly.
	jsonout.ResetStrings = oauth.FreshResetStrings
}

// KeychainUnavailable is set only for the active slot, telling USAGE_KEYCHAIN_UNAVAILABLE from USAGE_NO_CREDENTIALS.
type AccountInfo struct {
	Number              int
	Email               string
	OrgName             string
	OrgUUID             string
	IsActive            bool
	Creds               string
	Alias               string
	KeychainUnavailable bool
	// BaseURL is the endpoint an API-key account carries (DESIGN A46), ""
	// for none. Such an account keeps no credential in Claude Code's store,
	// so the active one reads as an API key from this, not from Creds.
	BaseURL string
}

// A nil-safe seam so reporting need not import lifecycle; core wires it.
var FirstRunSetup func(s *store.Store) error

// Consulted on macOS only; Security{} equals the store's production client, which free functions cannot reach.
var reportKC keychain.KeychainClient = keychain.Security{}

func decodeRecord(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}

func recordFor(data *store.SequenceData, num string) (map[string]any, bool) {
	if data == nil {
		return nil, false
	}
	raw, ok := data.Accounts[num]
	if !ok {
		return nil, false
	}
	return decodeRecord(raw), true
}

func recStr(rec map[string]any, key string) string {
	if v, ok := rec[key].(string); ok {
		return v
	}
	return ""
}

func recordDisabled(data *store.SequenceData, num string) bool {
	rec, ok := recordFor(data, num)
	if !ok {
		return false
	}
	d, _ := rec["disabled"].(bool)
	return d
}
