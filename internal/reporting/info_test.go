package reporting

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

// lineageFixture: slots 5 and 6 are one login in two organisations; the
// credential file holds slot 5's token while ~/.claude.json names slot 6.
func lineageFixture(t *testing.T) *store.Store {
	t.Helper()
	s := newStore(t, nil, nil)
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 6,
  "lastUpdated": "2026-07-17T09:00:00Z",
  "sequence": [5, 6],
  "accounts": {
    "5": {"email": "obi@example.com", "uuid": "", "organizationUuid": "org-five", "organizationName": "Five", "added": "t"},
    "6": {"email": "obi@example.com", "uuid": "", "organizationUuid": "org-six", "organizationName": "Six", "added": "t"}
  }
}`)
	for _, num := range []string{"5", "6"} {
		writeBackup(t, s, num, "obi@example.com", oauthCreds("at-"+num, "rt-"+num, 4102444800000), `{"oauthAccount":{}}`)
	}
	writeActiveCreds(t, s, oauthCreds("at-5", "rt-5", 4102444800000))
	writeLiveConfig(t, s, "obi@example.com", "org-six")
	return s
}

func TestBuildAccountsInfoActiveFollowsLineage(t *testing.T) {
	s := lineageFixture(t)
	active := map[int]bool{}
	for _, info := range BuildAccountsInfo(s) {
		active[info.Number] = info.IsActive
	}
	if !active[5] || active[6] {
		t.Fatalf("IsActive by slot = %v, want slot 5 active and slot 6 not", active)
	}
}

func TestStatusActiveFollowsLineage(t *testing.T) {
	s := lineageFixture(t)
	active, _ := buildStatusPayload(s)["active"].(map[string]any)
	if active["number"] != 5 || active["organizationUuid"] != "org-five" || active["email"] != "obi@example.com" {
		t.Fatalf("status active = %v, want slot 5 (org-five)", active)
	}
	var out bytes.Buffer
	renderStatus(&out, s)
	if !strings.Contains(out.String(), "Account-5") || !strings.Contains(out.String(), "[Five]") {
		t.Fatalf("human status does not name slot 5: %q", out.String())
	}
}
