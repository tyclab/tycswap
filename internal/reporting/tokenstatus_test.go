package reporting

import (
	"testing"

	"github.com/tyclab/tycswap/internal/testutil"
)

// With showTokenStatus the JSON rows carry a non-empty tokenStatus built from
// oauth.BuildTokenStatus (the dashboard's Token column reads it); without it
// the key is absent, so the plain `list --json` contract is unchanged.
func TestListPayload_TokenStatusOptIn(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	s := seedAtLimitStore(t, clk)

	got, err := ListAccounts(s, true, true, map[string]bool{})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	for _, r := range got.(map[string]any)["accounts"].([]any) {
		row := r.(map[string]any)
		ts, ok := row["tokenStatus"].(string)
		if !ok || ts == "" {
			t.Errorf("row %v: tokenStatus = %v, want a non-empty string", row["number"], row["tokenStatus"])
		}
	}

	plain, err := ListAccounts(s, false, true, map[string]bool{})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	for _, r := range plain.(map[string]any)["accounts"].([]any) {
		if _, ok := r.(map[string]any)["tokenStatus"]; ok {
			t.Errorf("tokenStatus present without showTokenStatus: %v", r)
		}
	}
}
