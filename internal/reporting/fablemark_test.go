// The account snapshot carries what decides whether an account runs Fable,
// for the menu's Allow / Disallow Fable (DESIGN A107).
package reporting

import (
	"testing"

	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestSnapshotCarriesWhatDecidesFable(t *testing.T) {
	s := newStore(t, testutil.FixedClock(t, fixedNow), nil)
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": null,
  "lastUpdated": "2026-07-17T08:00:00Z",
  "sequence": [1, 2, 3, 4],
  "accounts": {
    "1": {"email": "max@example.com", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "x"},
    "2": {"email": "org@example.com", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "x"},
    "3": {"email": "marked@example.com", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "x", "fableContinue": true},
    "4": {"email": "key@example.com", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "x", "kind": "api_key"}
  }
}`)
	writeBackup(t, s, "1", "max@example.com", `{"claudeAiOauth":{"accessToken":"at1","refreshToken":"rt1","subscriptionType":"max"}}`, `{"oauthAccount":{}}`)
	writeBackup(t, s, "2", "org@example.com", oauthCreds("at2", "rt2", 0), `{"oauthAccount":{}}`)
	writeBackup(t, s, "3", "marked@example.com", oauthCreds("at3", "rt3", 0), `{"oauthAccount":{}}`)
	want := map[string]string{"1": store.FableByPlan, "2": store.FableUnknown, "3": store.FableByMark, "4": store.FableAPIKey}
	for _, a := range Snapshot(s, nil).Accounts {
		if a.FableBy != want[a.Number] {
			t.Errorf("slot %s FableBy = %q, want %q", a.Number, a.FableBy, want[a.Number])
		}
	}
}
