// Tests the standing warning while no start login has a Mythos login of its
// own, so a linked start switches or holds a shared login (DESIGN A126).
package reporting

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

const noOwnStartLoginWarning = "No start login has a Mythos login of its own, so tycswap start switches or holds the default login or the Fable store for every session on it. Give Account-2 one with "

// ownStartLoginStore: Account-1 live; Account-2 with the start mark when
// marked, the Mythos mark when mythos, and a signed-in Mythos login of its own
// when own; auto-switch on when enabled.
func ownStartLoginStore(t *testing.T, marked, mythos, enabled, own bool) *store.Store {
	t.Helper()
	s := newStore(t, nil, nil)
	writeLiveConfig(t, s, "alice@example.com", "")
	start := ""
	if marked {
		start = `, "startLogin": true`
	}
	if mythos {
		start += `, "mythos": true`
	}
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 1, "lastUpdated": "x", "sequence": [1, 2],
  "accounts": {
    "1": {"email": "alice@example.com", "organizationUuid": "", "organizationName": ""},
    "2": {"email": "bob@example.com", "organizationUuid": "", "organizationName": ""`+start+`}
  }
}`)
	writeActiveCreds(t, s, oauthCreds("at1", "rt1", 0))
	writeBackup(t, s, "2", "bob@example.com", oauthCreds("at2", "rt2", 0), `{"oauthAccount":{"emailAddress":"bob@example.com"}}`)
	if enabled {
		if _, err := settings.SetSetting(s.BackupDir(), "autoswitch.enabled", "true"); err != nil {
			t.Fatal(err)
		}
	}
	if own {
		dir := store.MythosLoginDir(s.BackupDir(), "bob@example.com", "")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(oauthCreds("mythos-2", "refresh-2", 0)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// The warning shows in status and list, text and JSON, while auto-switch is
// on, starts get links of their own (Linux and WSL), a start login carries the
// Mythos mark or login and none has a ready Mythos login; else nowhere. The
// warning names what gives Account-2 one back.
func TestStatusAndListWarnWithoutAnOwnStartLogin(t *testing.T) {
	for name, tc := range map[string]struct {
		marked, mythos, enabled, own bool
		hint                         string
	}{
		"marked, no own login": {true, true, true, false, "tycswap add --login --lineage mythos --slot 2."},
		"Mythos mark removed":  {true, false, true, true, "tycswap mythos --allow 2."},
		"an own login":         {true, true, true, true, ""},
		"no marks":             {false, true, true, false, ""},
		"selection off":        {true, true, false, false, ""},
		"never a Mythos login": {true, false, true, false, ""},
	} {
		warns := tc.hint != "" && runtime.GOOS == "linux"
		want := noOwnStartLoginWarning + tc.hint
		t.Run(name, func(t *testing.T) {
			s := ownStartLoginStore(t, tc.marked, tc.mythos, tc.enabled, tc.own)
			var status, list bytes.Buffer
			renderStatus(&status, s)
			infos := BuildAccountsInfo(s)
			renderAccounts(&list, s, infos, CollectUsageEntries(s, infos, nil), false)
			statusJSON, err := Status(s, true)
			if err != nil {
				t.Fatal(err)
			}
			listJSON, err := ListAccounts(s, false, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			if ready := !strings.Contains(list.String(), "sign in again"); runtime.GOOS == "linux" && tc.mythos && ready != tc.own {
				t.Fatalf("Account-2's Mythos login ready %v, want %v:\n%s", ready, tc.own, list.String())
			}
			for surface, text := range map[string]string{"status": status.String(), "list": list.String()} {
				if got := strings.Count(text, noOwnStartLoginWarning); got != map[bool]int{true: 1}[warns] || warns && !strings.Contains(text, want) {
					t.Errorf("%s shows the warning %d times:\n%s", surface, got, text)
				}
			}
			for surface, payload := range map[string]any{"status --json": statusJSON, "list --json": listJSON} {
				got, present := payload.(map[string]any)["ownStartLoginWarning"]
				if present != warns || present && got != want {
					t.Errorf("%s ownStartLoginWarning = %v (%v)", surface, got, present)
				}
			}
		})
	}
}

// An account that is no OAuth login can never have a Mythos login: with the
// start and Mythos marks it is not enrolled, and nothing is said (A126).
func TestOwnStartLoginWarningSkipsAnAccountThatIsNoOAuthLogin(t *testing.T) {
	s := newStore(t, nil, nil)
	writeLiveConfig(t, s, "alice@example.com", "")
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 1, "lastUpdated": "x", "sequence": [1, 2, 3],
  "accounts": {
    "1": {"email": "alice@example.com", "organizationUuid": "", "organizationName": ""},
    "2": {"email": "bob@example.com", "organizationUuid": "", "organizationName": "", "kind": "api_key", "startLogin": true, "mythos": true},
    "3": {"email": "carol@example.com", "organizationUuid": "", "organizationName": "", "startLogin": true}
  }
}`)
	writeActiveCreds(t, s, oauthCreds("at1", "rt1", 0))
	writeBackup(t, s, "3", "carol@example.com", oauthCreds("at3", "rt3", 0), `{"oauthAccount":{"emailAddress":"carol@example.com"}}`)
	if _, err := settings.SetSetting(s.BackupDir(), "autoswitch.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	data, err := s.ReadSequence()
	if err != nil {
		t.Fatal(err)
	}
	if ready, enrolled := s.OwnStartLogins(data); len(ready) != 0 || len(enrolled) != 0 {
		t.Errorf("OwnStartLogins = %v, %v; want none", ready, enrolled)
	}
	if warning := OwnStartLoginWarning(s, data); warning != "" {
		t.Errorf("warning = %q, want none", warning)
	}
}
