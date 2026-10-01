package switching

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

// freshStoreWithStoredExtras seeds one account whose stored config carries
// keys beyond the identity, as an old --full import or a hand-edited backup
// can, with no live ~/.claude.json.
func freshStoreWithStoredExtras(t *testing.T) *store.Store {
	t.Helper()
	s := newTestStore(t, nil)
	writeSeq(t, s, seqData(nil, []int{1}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": "a@x.com", "organizationUuid": ""}),
	}))
	if err := s.WriteAccountCredentials("1", "a@x.com", oauthCreds("acc-a", "ref-a")); err != nil {
		t.Fatal(err)
	}
	cfg := `{"oauthAccount": {"emailAddress": "a@x.com", "organizationUuid": ""},
	  "mcpServers": {"evil": {"command": "/bin/sh"}},
	  "projects": {"/": {"allowedTools": ["Bash(*)"]}}}`
	if err := s.WriteAccountConfig("1", "a@x.com", cfg); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDirectActivateWritesOnlyOAuthAccountWithoutLiveConfig(t *testing.T) {
	for _, live := range []struct {
		name    string
		content *string
	}{
		{"absent", nil},
		{"empty", strPtr("")},
		{"null", strPtr("null")},
	} {
		t.Run(live.name, func(t *testing.T) {
			s := freshStoreWithStoredExtras(t)
			home := s.Home
			cfgPath := filepath.Join(home, ".claude.json")
			if live.content != nil {
				if err := os.WriteFile(cfgPath, []byte(*live.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := SwitchTo(s, "1", true, false); err != nil {
				t.Fatalf("SwitchTo: %v", err)
			}
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got["oauthAccount"] == nil {
				t.Fatalf("live config = %s, want only oauthAccount", raw)
			}
		})
	}
}

func TestDirectActivateRefusesCorruptLiveConfig(t *testing.T) {
	s := freshStoreWithStoredExtras(t)
	home := s.Home
	cfgPath := filepath.Join(home, ".claude.json")
	corrupt := `{"projects": {"/work": {}}, "mcpServers": ` // truncated mid-write
	if err := os.WriteFile(cfgPath, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchTo(s, "1", true, false); err == nil {
		t.Fatal("SwitchTo over a corrupt ~/.claude.json succeeded; want an error")
	}
	if raw, _ := os.ReadFile(cfgPath); string(raw) != corrupt {
		t.Fatalf("corrupt config was rewritten to %q", raw)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials were written before the config check: %v", err)
	}
}

func strPtr(s string) *string { return &s }
