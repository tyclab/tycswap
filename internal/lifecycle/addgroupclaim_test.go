package lifecycle

import (
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/store"
)

func claimForGroup(t *testing.T, s *store.Store, id groups.ID, num, email, org string) {
	t.Helper()
	registry := &groups.Registry{Version: 1, Claims: map[string]groups.Owner{num: {
		Scope:      string(id),
		ProfileDir: groups.ProfileDir(s.BackupDir(), id),
		Account:    groups.Account{Number: num, Email: email, OrgUUID: org},
	}}}
	if err := registry.Save(s.BackupDir()); err != nil {
		t.Fatal(err)
	}
}

// 2026-11-04: re-adding the live login overwrote the backup of an account the
// Opus group still held, which left its claim unverifiable (DESIGN A74).
func TestAddRefusesAnAccountAGroupHolds(t *testing.T) {
	for name, slot := range map[string]*int{"refresh-in-place": nil, "slotted": ip(2)} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			old := `{"claudeAiOauth": {"accessToken": "tok-group", "scopes": ["user:inference"]}}`
			seed(t, s, ip(2), acct{num: "2", email: "b@example.com", org: "org-b", orgName: "B", uuid: "u-b",
				creds: old, config: `{"oauthAccount": {"emailAddress": "b@example.com", "organizationUuid": "org-b"}}`})
			claimForGroup(t, s, groups.Opus, "2", "b@example.com", "org-b")
			seedLiveLogin(t, s, "b@example.com", "org-b", "B", "u-b", `{"claudeAiOauth": {"accessToken": "tok-new", "scopes": ["user:inference"]}}`)
			err := AddAccount(s, slot, true, nil)
			if err == nil || !strings.Contains(err.Error(), "held by the retired Opus / other group") || !strings.Contains(err.Error(), "next auto-switch tick hands the account back") {
				t.Fatalf("add into a group-held slot: %v", err)
			}
			if got, _ := s.ReadAccountCredentials("2", "b@example.com"); got != old {
				t.Fatalf("refused add overwrote the backup: %s", got)
			}
		})
	}
}
