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
	"time"

	"github.com/tyclab/tycswap/internal/clock"
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

// Only the profile attributes the rotated credential to slot 5 and nothing
// asks it under the lock, so --force onto 5 resolves first: a config repair
// that keeps the live token, not an activation of 5's older backup over it.
func TestForceSwitchToAProfileAttributedSlotRepairsTheConfig(t *testing.T) {
	live := oauthCreds("at-5-new", "rt-5-rotated")
	s, calls := rotatedSeat(t, live, "5")
	before := credentialsHash(t, s)
	out, err := SwitchTo(s, "5", true, true)
	if err != nil {
		t.Fatalf("SwitchTo(5, --force): %v", err)
	}
	m := asMap(t, out)
	if m["switched"] != true || m["reason"] != "config-repaired" || refField(t, m, "from") != 5 || refField(t, m, "to") != 5 {
		t.Fatalf("got switched=%v reason=%v from=%d to=%d, want the config repaired for 5", m["switched"], m["reason"], refField(t, m, "from"), refField(t, m, "to"))
	}
	if credentialsHash(t, s) != before {
		t.Fatalf("live credential = %s, want the rotated token kept", readActiveCreds(t, s))
	}
	if got := orgOf(liveConfig(t, s)); got != "org-five" {
		t.Errorf("live config names %v, want org-five", got)
	}
	assertOwnRotatedBackup(t, s, "5", live)
	if got, _ := s.ReadAccountCredentials("6", lineageEmail); got != lineageBackup6 {
		t.Errorf("Account-6 backup = %s, want it untouched", got)
	}
	if *calls != 1 {
		t.Errorf("profile calls = %d, want 1", *calls)
	}
}

// The miss memo gates the resolver only: a switch asks the profile again
// before the lock, so one transient miss cannot disable the foreign-credential
// guard and back slot 5's credential into the config's slot 6.
func TestSwitchReasksTheProfileAfterAMiss(t *testing.T) {
	live := oauthCreds("at-5-new", "rt-5-rotated")
	s, _ := rotatedSeat(t, live, "5")
	calls, answer := 0, false
	s.OAuth = &oauth.FakeClient{ProfileFn: func(_ context.Context, token string) *oauth.Identity {
		calls++
		if answer && token == "at-5-new" {
			return &oauth.Identity{UUID: "acct-obi", Email: lineageEmail, OrgUUID: "org-five"}
		}
		return nil
	}}
	data, _ := s.ReadSequence()
	if got := s.ResolveLiveSlot(data); got.Slot != "6" || calls != 1 {
		t.Fatalf("first resolve = %+v after %d calls, want the config slot after a miss", got, calls)
	}
	s.Clk.(*clock.Fake).Advance(2 * time.Minute)
	answer = true
	out, err := SwitchTo(s, "5", true, false)
	if err != nil {
		t.Fatalf("SwitchTo(5): %v", err)
	}
	if m := asMap(t, out); m["reason"] != "config-repaired" || refField(t, m, "from") != 5 || refField(t, m, "to") != 5 {
		t.Fatalf("got reason=%v from=%d to=%d, want the credential classified as 5's", m["reason"], refField(t, m, "from"), refField(t, m, "to"))
	}
	if calls != 2 {
		t.Errorf("profile calls = %d, want 2: one more after the miss", calls)
	}
	if got, _ := s.ReadAccountCredentials("6", lineageEmail); got != lineageBackup6 {
		t.Errorf("Account-6 backup = %s, want it untouched", got)
	}
	assertOwnRotatedBackup(t, s, "5", live)
}

// A miss is reused for a minute, so one switch asks an unreachable endpoint
// once, --force included.
func TestSwitchAsksAFreshMissOnce(t *testing.T) {
	for _, force := range []bool{false, true} {
		live := oauthCreds("at-5-new", "rt-5-rotated")
		s, _ := rotatedSeat(t, live, "5")
		calls := 0
		s.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity {
			calls++
			return nil
		}}
		if _, err := SwitchTo(s, "5", true, force); err != nil {
			t.Fatalf("SwitchTo(5, force=%v): %v", force, err)
		}
		if calls != 1 {
			t.Errorf("force=%v: profile calls = %d, want 1", force, calls)
		}
	}
}
