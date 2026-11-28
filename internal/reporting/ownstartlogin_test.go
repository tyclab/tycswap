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

const noOwnStartLoginWarning = "No start login has a Mythos login of its own, so tycswap start switches or holds the default login or the Fable store for every session on it. Sign one in with tycswap add --login --lineage mythos --slot N."

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
// Mythos mark and none has a ready Mythos login; else nowhere.
func TestStatusAndListWarnWithoutAnOwnStartLogin(t *testing.T) {
	for name, tc := range map[string]struct{ marked, mythos, enabled, own, warns bool }{
		"marked, no own login": {true, true, true, false, runtime.GOOS == "linux"},
		"an own login":         {true, true, true, true, false},
		"no marks":             {false, true, true, false, false},
		"selection off":        {true, true, false, false, false},
		"never a Mythos login": {true, false, true, false, false},
	} {
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
				if got := strings.Count(text, noOwnStartLoginWarning); got != map[bool]int{true: 1}[tc.warns] {
					t.Errorf("%s shows the warning %d times:\n%s", surface, got, text)
				}
			}
			for surface, payload := range map[string]any{"status --json": statusJSON, "list --json": listJSON} {
				got, present := payload.(map[string]any)["ownStartLoginWarning"]
				if present != tc.warns || present && got != noOwnStartLoginWarning {
					t.Errorf("%s ownStartLoginWarning = %v (%v)", surface, got, present)
				}
			}
		})
	}
}
