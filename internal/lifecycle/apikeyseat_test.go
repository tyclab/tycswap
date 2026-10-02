// add on a seat whose credentials file holds nothing but seat-wide keys
// (DESIGN A29): that file is no OAuth login, so add reads the managed key
// behind it and refuses it as it refuses any live API key, or finds no
// credential at all; it never stores the seat's MCP data as an account.
package lifecycle

import (
	"os"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/ccfile"
)

const seatWideOnlyFile = `{"mcpOAuth":{"srv|1111":{"accessToken":"mcp-live"}},"mcpOAuthClientConfig":{"srv|1111":{"clientSecret":"cs-fixture"}}}`

func TestAddOnASeatWideOnlyFile(t *testing.T) {
	for _, tc := range []struct {
		name, key, wantKind, wantMsg string
	}{
		{"an API key behind it is refused as a live API key", "sk-ant-api03-seat-fixture-0123456789", "ValidationError", "API-key account"},
		{"no key behind it is no credential", "", "CredentialReadError", "No credentials found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			seedLiveLogin(t, s, "new@x.com", "", "", "", seatWideOnlyFile)
			if tc.key != "" {
				if err := ccfile.UpdateGlobalConfig(func(c map[string]any) { c["primaryApiKey"] = tc.key }); err != nil {
					t.Fatal(err)
				}
			}

			err := AddAccount(s, nil, true, nil)
			if err == nil {
				t.Fatal("add stored the seat-wide keys as an account")
			}
			if errKind(err) != tc.wantKind || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("add: %s %v, want %s naming %q", errKind(err), err, tc.wantKind, tc.wantMsg)
			}
			if _, statErr := os.Stat(s.SequenceFile); statErr == nil {
				if data, _ := s.ReadSequence(); data != nil && len(data.Accounts) != 0 {
					t.Fatalf("add recorded %d account(s)", len(data.Accounts))
				}
			}
		})
	}
}
