// Switches while ~/.claude.json names slot 6 but the credential file holds
// slot 5's lineage (DESIGN A68): the outgoing slot is the credential's, and a
// switch to it repairs the config without touching the credential.
package switching

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

const lineageEmail = "obi@example.com"

var (
	lineageBackup5 = oauthCreds("at-5", "rt-5")
	lineageBackup6 = oauthCreds("at-6", "rt-6")
)

var lineageLives = []struct{ name, live string }{
	{"same bytes", oauthCreds("at-5", "rt-5")},
	{"access token rotated", oauthCreds("at-5-rotated", "rt-5")},
}

// lineageSeat: slots 5 and 6 are one login in two organisations; no OAuth
// client, so the provenance of a diverging live credential cannot be resolved.
func lineageSeat(t *testing.T, active int, live string) *store.Store {
	t.Helper()
	s := newTestStore(t, nil)
	writeSeq(t, s, seqData(ptrInt(active), []int{5, 6}, map[string]json.RawMessage{
		"5": record(map[string]any{"email": lineageEmail, "organizationUuid": "org-five", "organizationName": "Five"}),
		"6": record(map[string]any{"email": lineageEmail, "organizationUuid": "org-six", "organizationName": "Six"}),
	}))
	seedBackup(t, s, "5", lineageEmail, lineageBackup5, "org-five")
	seedBackup(t, s, "6", lineageEmail, lineageBackup6, "org-six")
	seedLive(t, s, lineageEmail, "org-six", live)
	cfg := `{"theme":"dark","oauthAccount":{"emailAddress":"` + lineageEmail + `","organizationUuid":"org-six"}}`
	if err := os.WriteFile(filepath.Join(s.Home, ".claude.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

func orgOf(m map[string]any) any {
	oa, _ := m["oauthAccount"].(map[string]any)
	return oa["organizationUuid"]
}

func storedConfigOrg(t *testing.T, s *store.Store, num string) any {
	t.Helper()
	text, err := s.ReadAccountConfig(num, lineageEmail)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("Account-%s config: %v\n%s", num, err, text)
	}
	return orgOf(m)
}

func credentialsHash(t *testing.T, s *store.Store) [32]byte {
	t.Helper()
	raw, err := os.ReadFile(liveCredentialsPath(s))
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

func logText(t *testing.T, s *store.Store) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.BackupDir(), "tycswap.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

// assertSwitchedOffTheCredentialSlot: slot 6 keeps its own backup, slot 5's
// backup holds the live credential it gave up, and slot 6 is live.
func assertSwitchedOffTheCredentialSlot(t *testing.T, s *store.Store, m map[string]any, live string) {
	t.Helper()
	if m["switched"] != true || m["reason"] != "switched" || refField(t, m, "from") != 5 || refField(t, m, "to") != 6 {
		t.Errorf("got switched=%v reason=%v from=%d to=%d, want a switch from 5 to 6",
			m["switched"], m["reason"], refField(t, m, "from"), refField(t, m, "to"))
	}
	if got, _ := s.ReadAccountCredentials("6", lineageEmail); got != lineageBackup6 {
		t.Errorf("Account-6 backup = %s, want its own credential untouched", got)
	}
	if prev, _ := s.Creds.ReadPrev("6", lineageEmail); prev != "" {
		t.Errorf("Account-6 backup was displaced into .prev: %s", prev)
	}
	if got, _ := s.ReadAccountCredentials("5", lineageEmail); got != oauth.AccountOnly(live) {
		t.Errorf("Account-5 backup = %s, want the live credential %s", got, live)
	}
	if got := readActiveCreds(t, s); got != lineageBackup6 {
		t.Errorf("live credential = %s, want Account-6's", got)
	}
	if got := storedConfigOrg(t, s, "5"); got != "org-five" {
		t.Errorf("Account-5 stored config names %v, want org-five", got)
	}
	if got := orgOf(liveConfig(t, s)); got != "org-six" {
		t.Errorf("live config names %v, want org-six", got)
	}
}

func TestSwitchToUnderLineageMismatchSwitchesOffTheCredentialSlot(t *testing.T) {
	for _, tc := range lineageLives {
		t.Run(tc.name, func(t *testing.T) {
			s := lineageSeat(t, 5, tc.live)
			out, err := SwitchTo(s, "6", true, false)
			if err != nil {
				t.Fatalf("SwitchTo(6): %v", err)
			}
			assertSwitchedOffTheCredentialSlot(t, s, asMap(t, out), tc.live)
		})
	}
}

func TestRotationUnderLineageMismatchSwitchesOffTheCredentialSlot(t *testing.T) {
	for _, tc := range lineageLives {
		t.Run(tc.name, func(t *testing.T) {
			s := lineageSeat(t, 5, tc.live)
			out, err := Switch(s, nil, true, nil, nil)
			if err != nil {
				t.Fatalf("Switch: %v", err)
			}
			assertSwitchedOffTheCredentialSlot(t, s, asMap(t, out), tc.live)
		})
	}
}

func TestSwitchRepairsConfigUnderLineageMismatch(t *testing.T) {
	const message = "Repaired the live config for Account-5 (obi@example.com)"
	paths := []struct {
		name, strategy string
		run            func(s *store.Store, jsonOut bool) (any, error)
	}{
		{"switch-to", "direct", func(s *store.Store, jsonOut bool) (any, error) { return SwitchTo(s, "5", jsonOut, false) }},
		{"switch-to --force", "direct", func(s *store.Store, jsonOut bool) (any, error) { return SwitchTo(s, "5", jsonOut, true) }},
		// Recorded active 6: the rotation lands on 5, the credential's slot.
		{"rotation", "rotation", func(s *store.Store, jsonOut bool) (any, error) { return Switch(s, nil, jsonOut, nil, nil) }},
	}
	for _, p := range paths {
		for _, tc := range lineageLives {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				s := lineageSeat(t, 6, tc.live)
				before := credentialsHash(t, s)
				out, err := p.run(s, true)
				if err != nil {
					t.Fatalf("switch: %v", err)
				}
				m := asMap(t, out)
				if m["switched"] != true || m["reason"] != "config-repaired" || m["strategy"] != p.strategy || m["message"] != message {
					t.Fatalf("got switched=%v reason=%v strategy=%v message=%q", m["switched"], m["reason"], m["strategy"], m["message"])
				}
				want := map[string]any{"number": 5, "email": lineageEmail}
				for _, key := range []string{"from", "to"} {
					ref, _ := m[key].(map[string]any)
					if refField(t, m, key) != 5 || ref["email"] != want["email"] {
						t.Errorf("%s = %v, want %v", key, ref, want)
					}
				}
				if credentialsHash(t, s) != before {
					t.Errorf("the credential file changed: %s", readActiveCreds(t, s))
				}
				cfg := liveConfig(t, s)
				if got := orgOf(cfg); got != "org-five" {
					t.Errorf("live config names %v, want org-five", got)
				}
				if cfg["theme"] != "dark" {
					t.Errorf("live config lost its other keys: %v", cfg)
				}
				if got, _ := s.ReadAccountCredentials("6", lineageEmail); got != lineageBackup6 {
					t.Errorf("Account-6 backup = %s, want it untouched", got)
				}
				if got := storedConfigOrg(t, s, "6"); got != "org-six" {
					t.Errorf("Account-6 stored config names %v, want org-six", got)
				}
				if got := storedConfigOrg(t, s, "5"); got != "org-five" {
					t.Errorf("Account-5 stored config names %v, want org-five", got)
				}
				data, _ := s.ReadSequence()
				if data.ActiveAccountNumber == nil || *data.ActiveAccountNumber != 5 {
					t.Errorf("activeAccountNumber = %v, want 5", data.ActiveAccountNumber)
				}
				if got := s.ResolveLiveSlot(data); got != (store.LiveSlot{Slot: "5", ConfigSlot: "5"}) {
					t.Errorf("after the repair ResolveLiveSlot = %+v, want config and credential agreeing on 5", got)
				}
				log := logText(t, s)
				if !strings.Contains(log, "Repaired the live config for account 5") || strings.Contains(log, "Switched from account") {
					t.Errorf("log lacks the repair line or records a switch:\n%s", log)
				}
			})
		}
	}
	for _, p := range paths {
		t.Run(p.name+"/human", func(t *testing.T) {
			s := lineageSeat(t, 6, lineageBackup5)
			var err error
			out := captureStdout(t, func() { _, err = p.run(s, false) })
			if err != nil {
				t.Fatalf("switch: %v", err)
			}
			if !strings.Contains(out, message+"\n") || strings.Contains(out, "Already on") || strings.Contains(out, "Switched to") {
				t.Fatalf("stdout = %q, want the repair line only", out)
			}
		})
	}
}

// The default login's claim on the credential's slot is certain, so the plain
// ownership gate lets the repair through; a group claiming the slot as well
// still refuses it, and nothing is written.
func TestConfigRepairRefusesAnotherOwner(t *testing.T) {
	s := lineageSeat(t, 6, lineageBackup5)
	reg := &groups.Registry{Version: 1, Claims: map[string]groups.Owner{"5": {
		Scope:      string(groups.Fable),
		ProfileDir: groups.ProfileDir(s.BackupDir(), groups.Fable),
		Account:    groups.Account{Number: "5", Email: lineageEmail, OrgUUID: "org-five"},
	}}}
	if err := reg.Save(s.BackupDir()); err != nil {
		t.Fatal(err)
	}
	before := credentialsHash(t, s)
	if _, err := SwitchTo(s, "5", true, false); err == nil || !strings.Contains(err.Error(), "conflicting credential owners") {
		t.Fatalf("SwitchTo(5) = %v, want the conflicting-owners refusal", err)
	}
	if credentialsHash(t, s) != before || orgOf(liveConfig(t, s)) != "org-six" {
		t.Fatalf("a refused repair wrote the credential or the config")
	}
}

// --force onto the config's slot is unchanged: a direct activation from its
// stored backup that backs nothing into the credential's slot.
func TestForceSwitchToTheConfigSlotUnderLineageMismatchActivates(t *testing.T) {
	for _, tc := range lineageLives {
		t.Run(tc.name, func(t *testing.T) {
			s := lineageSeat(t, 6, tc.live)
			out, err := SwitchTo(s, "6", true, true)
			if err != nil {
				t.Fatalf("SwitchTo(6, --force): %v", err)
			}
			m := asMap(t, out)
			if m["switched"] != false || m["reason"] != "activated" || refField(t, m, "from") != 6 || refField(t, m, "to") != 6 {
				t.Fatalf("got switched=%v reason=%v from=%d to=%d, want activated 6", m["switched"], m["reason"], refField(t, m, "from"), refField(t, m, "to"))
			}
			if got := readActiveCreds(t, s); got != lineageBackup6 {
				t.Errorf("live credential = %s, want Account-6's", got)
			}
			for num, want := range map[string]string{"5": lineageBackup5, "6": lineageBackup6} {
				if got, _ := s.ReadAccountCredentials(num, lineageEmail); got != want {
					t.Errorf("Account-%s backup = %s, want it untouched", num, got)
				}
			}
		})
	}
}
