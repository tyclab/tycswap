// The live-slot resolver's identity is the switch's provenance (DESIGN A68):
// a slot the profile or the chain record attributed backs up as own-rotated,
// and the profile endpoint is asked once per credential.
package switching

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

// rotatedSeat: slots 5 and 6 are one login (one uuid) in two organisations,
// ~/.claude.json names 6 and the credential file holds live, which matches no
// backup; the profile endpoint names slot owner for live's access token.
func rotatedSeat(t *testing.T, live, owner string) (*store.Store, *int) {
	t.Helper()
	calls := new(int)
	org := map[string]string{"5": "org-five", "6": "org-six"}
	access := oauth.ExtractAccessToken(live)
	s := newTestStore(t, &oauth.FakeClient{ProfileFn: func(_ context.Context, token string) *oauth.Identity {
		*calls++
		if token == access {
			return &oauth.Identity{UUID: "acct-obi", Email: lineageEmail, OrgUUID: org[owner]}
		}
		return nil
	}})
	writeSeq(t, s, seqData(ptrInt(6), []int{5, 6}, map[string]json.RawMessage{
		"5": record(map[string]any{"email": lineageEmail, "uuid": "acct-obi", "organizationUuid": "org-five", "organizationName": "Five"}),
		"6": record(map[string]any{"email": lineageEmail, "uuid": "acct-obi", "organizationUuid": "org-six", "organizationName": "Six"}),
	}))
	seedBackup(t, s, "5", lineageEmail, lineageBackup5, "org-five")
	seedBackup(t, s, "6", lineageEmail, lineageBackup6, "org-six")
	seedLive(t, s, lineageEmail, "org-six", live)
	return s, calls
}

func assertOwnRotatedBackup(t *testing.T, s *store.Store, slot, live string) {
	t.Helper()
	if got, _ := s.ReadAccountCredentials(slot, lineageEmail); got != oauth.AccountOnly(live) {
		t.Errorf("Account-%s backup = %s, want the live credential", slot, got)
	}
	log := logText(t, s)
	if !regexp.MustCompile(`(?m)Backed up account `+slot+`$`).MatchString(log) || strings.Contains(log, "pre-fix backup") {
		t.Errorf("log lacks the own-rotated backup line or logs the unresolved one:\n%s", log)
	}
}

func TestSwitchOffAProfileAttributedSlotBacksUpOwnRotated(t *testing.T) {
	live := oauthCreds("at-5-new", "rt-5-rotated")
	s, calls := rotatedSeat(t, live, "5")
	out, err := SwitchTo(s, "6", true, false)
	if err != nil {
		t.Fatalf("SwitchTo(6): %v", err)
	}
	if m := asMap(t, out); m["switched"] != true || refField(t, m, "from") != 5 || refField(t, m, "to") != 6 {
		t.Fatalf("got %v, want a switch from 5 to 6", m)
	}
	assertOwnRotatedBackup(t, s, "5", live)
	if *calls != 1 {
		t.Errorf("profile calls = %d, want 1", *calls)
	}
}

func TestSwitchAsksTheProfileOnceForTheConfigSlotsOwnRotation(t *testing.T) {
	live := oauthCreds("at-6-new", "rt-6-rotated")
	s, calls := rotatedSeat(t, live, "6")
	if _, err := SwitchTo(s, "5", true, false); err != nil {
		t.Fatalf("SwitchTo(5): %v", err)
	}
	if *calls != 1 {
		t.Errorf("profile calls = %d, want 1", *calls)
	}
	assertOwnRotatedBackup(t, s, "6", live)
	if _, err := os.Stat(filepath.Join(s.BackupDir(), "live_lineage.json")); err != nil {
		t.Errorf("live_lineage.json: %v, want the profile's answer recorded", err)
	}
}
