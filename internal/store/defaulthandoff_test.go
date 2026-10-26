// Pins defaultMovedFrom and the two checks that refuse a default login that
// moved during a default-to-group handoff (DESIGN A68).
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
)

const (
	handoffEmail    = "obi@example.com"
	handoffStranger = "stranger@example.com"
)

func handoffCreds(num string) string {
	return `{"claudeAiOauth":{"accessToken":"at-` + num + `","refreshToken":"rt-` + num + `","expiresAt":4102444800000}}`
}

func handoffConfig(email, org string) string {
	return `{"theme":"dark","oauthAccount":{"emailAddress":"` + email + `","organizationUuid":"` + org + `"}}`
}

// handoffSeat: slots 5 and 6 are one login in two organisations, slot 7 is
// another login. The default config names configOrg's slot of email and the
// default credential is live ("" = none). The journal is a handoff of slot 5
// whose snapshot holds slot 5's credential while the config named slot 6.
func handoffSeat(t *testing.T, email, configOrg, live string) (*Store, *groups.Journal) {
	t.Helper()
	s := freshStore(t)
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 6,
  "lastUpdated": "2026-07-17T09:00:00Z",
  "sequence": [5, 6, 7],
  "accounts": {
    "5": {"email": "obi@example.com", "uuid": "", "organizationUuid": "org-five", "organizationName": "Five", "added": "t"},
    "6": {"email": "obi@example.com", "uuid": "", "organizationUuid": "org-six", "organizationName": "Six", "added": "t"},
    "7": {"email": "seven@example.com", "uuid": "", "organizationUuid": "org-seven", "organizationName": "Seven", "added": "t"}
  }
}`)
	for num, slotEmail := range map[string]string{"5": handoffEmail, "6": handoffEmail, "7": "seven@example.com"} {
		if err := s.Creds.WriteBackup(num, slotEmail, handoffCreds(num)); err != nil {
			t.Fatal(err)
		}
		writeBackupConfig(t, s, num, slotEmail, `{"oauthAccount":{}}`)
	}
	if err := os.MkdirAll(s.DefaultProfileDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.writeDefaultCredentials(live); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.DefaultConfigPath(), []byte(handoffConfig(email, configOrg)), 0o600); err != nil {
		t.Fatal(err)
	}
	return s, &groups.Journal{
		Version:           1,
		Group:             groups.Opus,
		Target:            groups.Account{Number: "5", Email: handoffEmail, OrgUUID: "org-five"},
		DefaultHandoff:    true,
		DefaultCredential: groups.Snapshot{Exists: true, Text: handoffCreds("5")},
		DefaultConfig:     groups.Snapshot{Exists: true, Text: handoffConfig(handoffEmail, "org-six")},
	}
}

func defaultState(t *testing.T, s *Store) (string, string) {
	t.Helper()
	creds, err := s.ReadDefaultCredentials()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(s.DefaultConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	return creds, string(cfg)
}

// The handoff's own credential decides where the config's login sits, not the
// one live now: with the config naming slot 6, the journal's slot 5 lineage
// keeps the target, whatever the default profile holds.
func TestDefaultMovedFromReadsTheJournalCredential(t *testing.T) {
	cases := map[string]struct {
		journal, live string
		want          bool
	}{
		"journal holds the target, live holds slot 6": {handoffCreds("5"), handoffCreds("6"), false},
		"journal holds slot 6, live holds the target": {handoffCreds("6"), handoffCreds("5"), true},
		"journal holds the target, nothing live":      {handoffCreds("5"), "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s, journal := handoffSeat(t, handoffEmail, "org-six", tc.live)
			journal.DefaultCredential.Text = tc.journal
			moved, err := s.defaultMovedFrom(journal)
			if err != nil || moved != tc.want {
				t.Fatalf("defaultMovedFrom = %v, %v; want %v", moved, err, tc.want)
			}
		})
	}
}

// A clear interrupted after removing the default credential leaves the config
// naming slot 6 and nothing live; read with the journal's credential the login
// has not moved, so both a retried clear and a rollback complete.
func TestHalfClearedDefaultHandoffCompletesWithTheJournalCredential(t *testing.T) {
	t.Run("clear", func(t *testing.T) {
		s, journal := handoffSeat(t, handoffEmail, "org-six", "")
		if err := s.ClearDefaultForGroup(journal); err != nil {
			t.Fatalf("ClearDefaultForGroup: %v", err)
		}
		creds, cfg := defaultState(t, s)
		var m map[string]any
		if err := json.Unmarshal([]byte(cfg), &m); err != nil {
			t.Fatal(err)
		}
		if _, has := m["oauthAccount"]; creds != "" || has || m["theme"] != "dark" {
			t.Fatalf("default after the clear = %q / %s, want no credential and no oauthAccount", creds, cfg)
		}
	})
	t.Run("restore", func(t *testing.T) {
		s, journal := handoffSeat(t, handoffEmail, "org-six", "")
		if err := s.restoreDefaultHandoff(journal); err != nil {
			t.Fatalf("restoreDefaultHandoff: %v", err)
		}
		if creds, cfg := defaultState(t, s); creds != journal.DefaultCredential.Text || cfg != journal.DefaultConfig.Text {
			t.Fatalf("default after the rollback = %q / %s, want the journal's snapshot", creds, cfg)
		}
	})
}

// A default login that really moved is refused by both paths and nothing is
// written: an unmanaged identity fails the identity check; a login moved to
// another managed slot resolves to the target by the journal's lineage and is
// caught by the fingerprint check after it.
func TestMovedDefaultLoginRefusesTheHandoff(t *testing.T) {
	cases := []struct {
		name, email, org, live string
		moved                  bool
		clearErr, restoreErr   string
	}{
		{"unmanaged identity", handoffStranger, "org-x", handoffCreds("x"), true,
			"the default login changed during group handoff", "default identity changed"},
		{"another managed login", "seven@example.com", "org-seven", handoffCreds("7"), false,
			"the default credential changed during group handoff", "default credential changed; account claims remain held"},
	}
	for _, tc := range cases {
		for _, path := range []string{"clear", "restore"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				s, journal := handoffSeat(t, tc.email, tc.org, tc.live)
				if moved, err := s.defaultMovedFrom(journal); err != nil || moved != tc.moved {
					t.Fatalf("defaultMovedFrom = %v, %v; want %v", moved, err, tc.moved)
				}
				credsBefore, cfgBefore := defaultState(t, s)
				run, want := s.ClearDefaultForGroup, tc.clearErr
				if path == "restore" {
					run, want = s.restoreDefaultHandoff, tc.restoreErr
				}
				if err := run(journal); err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("%s = %v, want %q", path, err, want)
				}
				if creds, cfg := defaultState(t, s); creds != credsBefore || cfg != cfgBefore {
					t.Fatalf("a refused %s wrote the default login: %q / %s", path, creds, cfg)
				}
				if _, err := os.Stat(filepath.Join(s.DefaultProfileDir(), ".credentials.json")); err != nil {
					t.Fatalf("default credential file: %v", err)
				}
			})
		}
	}
}
