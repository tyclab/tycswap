// export and import never carry a Fable login (DESIGN A101).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/transfer"
)

func TestExportAndImportLeaveTheFableLoginsOut(t *testing.T) {
	f := newFableLoginFixture(t)
	if code, _, errb := f.run(t, "add", "--login", "--lineage", "fable"); code != 0 {
		t.Fatalf("Fable login of Account-1: exit %d, stderr %q", code, errb)
	}
	signInAs(t, "b@example.com", "fable-2")
	if code, _, errb := f.run(t, "add", "--login", "--lineage", "fable"); code != 0 {
		t.Fatalf("Fable login of Account-2: exit %d, stderr %q", code, errb)
	}
	fableDir := paths.FableCredentialsDir(paths.GetBackupRoot())
	before := snapshotDir(t, fableDir)
	if len(before) != 2 {
		t.Fatalf("precondition: credentials-fable/ holds %v", before)
	}

	prevErr := transfer.Stderr
	transfer.Stderr = &strings.Builder{}
	t.Cleanup(func() { transfer.Stderr = prevErr })
	envelope := filepath.Join(t.TempDir(), "accounts.tycswap")
	if code, _, errb := f.run(t, "export", envelope); code != 0 {
		t.Fatalf("export: exit %d, stderr %q", code, errb)
	}
	raw, err := os.ReadFile(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range []string{"fable-1", "fable-2", "credentials-fable"} {
		if strings.Contains(string(raw), grant) {
			t.Errorf("the export carries %q:\n%s", grant, raw)
		}
	}
	if !strings.Contains(string(raw), "refresh-token-1") {
		t.Errorf("the export lacks Account-1's default login:\n%s", raw)
	}

	if code, _, errb := f.run(t, "import", envelope, "--force"); code != 0 {
		t.Fatalf("import: exit %d, stderr %q", code, errb)
	}
	if after := snapshotDir(t, fableDir); !sameStrings(before, after) {
		t.Errorf("import changed credentials-fable/:\nbefore %v\nafter  %v", before, after)
	}
}

func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out[e.Name()] = string(raw)
	}
	return out
}

// A Fable session writes its own account, bob, into ~/.claude.json while the
// default login is alice's. Export takes the live login from the resolved
// live account, not from the config: each account's entry carries its own
// login and config, never the other's (DESIGN A101).
func TestExportTakesTheLiveLoginFromTheCredential(t *testing.T) {
	for _, tc := range []struct {
		name string
		live func(t *testing.T, sw *core.Switcher) string
	}{
		{"attributed", func(t *testing.T, sw *core.Switcher) string {
			creds, err := sw.ReadAccountCredentials("1", "alice@example.com")
			if err != nil || creds == "" {
				t.Fatalf("alice's backup: %q, %v", creds, err)
			}
			return creds
		}},
		{"unattributed", func(*testing.T, *core.Switcher) string {
			return `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-LIVE-UNPROVEN","refreshToken":"rt-live-unproven","expiresAt":4102444800000}}`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw := fableCLISwitcher(t)
			home, _ := os.UserHomeDir()
			writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"), fableLogin("f2"))
			writeFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"emailAddress":"bob@example.com","organizationUuid":null}}`)
			live := tc.live(t, sw)
			writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), live)
			prevErr := transfer.Stderr
			transfer.Stderr = &strings.Builder{}
			t.Cleanup(func() { transfer.Stderr = prevErr })

			entry := func(account string) (creds, email string) {
				t.Helper()
				path := filepath.Join(t.TempDir(), account+".tycswap")
				if code, _, errb := runCLI(t, []string{"export", path, "--account", account}, false, false); code != 0 {
					t.Fatalf("export of %s: exit %d, stderr %q", account, code, errb)
				}
				var env struct {
					Accounts []struct {
						Credentials json.RawMessage `json:"credentials"`
						Config      struct {
							OAuthAccount struct {
								Email string `json:"emailAddress"`
							} `json:"oauthAccount"`
						} `json:"config"`
					} `json:"accounts"`
				}
				raw, _ := os.ReadFile(path)
				if err := json.Unmarshal(raw, &env); err != nil || len(env.Accounts) != 1 {
					t.Fatalf("export of %s: %v\n%s", account, err, raw)
				}
				return string(env.Accounts[0].Credentials), env.Accounts[0].Config.OAuthAccount.Email
			}
			bobCreds, bobEmail := entry("2")
			if !strings.Contains(bobCreds, "BBBBfixture") || bobEmail != "bob@example.com" {
				t.Errorf("bob's entry: credentials %s, config names %q; want bob's own login", bobCreds, bobEmail)
			}
			aliceCreds, aliceEmail := entry("1")
			if strings.Contains(aliceCreds, "BBBBfixture") || aliceEmail != "alice@example.com" {
				t.Errorf("alice's entry: credentials %s, config names %q; want alice's own login", aliceCreds, aliceEmail)
			}
			if strings.Contains(bobCreds, "AAAAfixture") || strings.Contains(bobCreds, "LIVE-UNPROVEN") {
				t.Errorf("bob's entry carries the default login, which is alice's: %s", bobCreds)
			}
		})
	}
}

// Import never replaces a slot's identity: --force overwrites the slot of the
// same identity, whose Fable login stays its own, and a new account lands in a
// slot the roster does not hold. A Fable login left under that slot and email
// (by an older tycswap's remove or move) is not the new account's: it goes
// (DESIGN A101).
func TestImportOfANewAccountDropsAFableLoginLeftInItsSlot(t *testing.T) {
	sw := fableCLISwitcher(t)
	f, err := sw.Store.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WriteAccountCredentials("7", "dave@example.com", fableLogin("f7-left")); err != nil {
		t.Fatal(err)
	}
	bobBefore, _ := f.ReadAccountCredentials("2", "bob@example.com")
	envelope := filepath.Join(t.TempDir(), "in.tycswap")
	writeFile(t, envelope, `{"version":1,"exportedAt":"2026-11-18T00:00:00Z","exportedFrom":"linux","swapVersion":"t","encrypted":false,"activeAccountNumber":null,"accounts":[`+
		`{"number":7,"email":"dave@example.com","uuid":"","organizationUuid":"","organizationName":"","added":"t","credentials":{"claudeAiOauth":{"accessToken":"at-dave","refreshToken":"rt-dave"}},"config":{"oauthAccount":{"emailAddress":"dave@example.com"}}},`+
		`{"number":2,"email":"bob@example.com","uuid":"","organizationUuid":"","organizationName":"","added":"t","credentials":{"claudeAiOauth":{"accessToken":"at-bob-new","refreshToken":"rt-bob-new"}},"config":{"oauthAccount":{"emailAddress":"bob@example.com"}}}]}`)
	prevErr := transfer.Stderr
	transfer.Stderr = &strings.Builder{}
	t.Cleanup(func() { transfer.Stderr = prevErr })
	if code, _, errb := runCLI(t, []string{"import", envelope, "--force"}, false, false); code != 0 {
		t.Fatalf("import: exit %d, stderr %q", code, errb)
	}
	if got, _ := f.ReadAccountCredentials("7", "dave@example.com"); got != "" {
		t.Errorf("the imported Account-7 holds the Fable login left in its slot: %s", got)
	}
	if got, _ := f.ReadAccountCredentials("2", "bob@example.com"); got != bobBefore {
		t.Errorf("--force over bob's own slot changed his Fable login: %s", got)
	}
}
