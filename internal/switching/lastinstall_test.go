// Default switches while ~/.claude.json names the Fable store's account, as
// every Fable session writes it, and the default login is another account's,
// rotated by Claude Code and not yet attributed (DESIGN A100's last install).
package switching

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
)

// fableSessionConfig: the Fable store is on Account-2 and a Fable session
// wrote Account-2 into ~/.claude.json. Before, the default store was switched
// to 3 and back to 1 when installed is true. The default login is Account-1's,
// rotated since, and the profile endpoint does not answer.
func fableSessionConfig(t *testing.T, fableLogins, installed bool) (*store.Store, string) {
	t.Helper()
	s, f := fableSwitchStore(t)
	if _, err := SwitchFable(f, "2", FableSwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	if !fableLogins {
		for _, dir := range []string{paths.FableCredentialsDir(s.BackupDir()), paths.FableScopeDir(s.BackupDir())} {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
	}
	if installed {
		for _, id := range []string{"3", "1"} {
			if _, err := SwitchTo(s, id, true, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity { return nil }}
	rotated := planCreds("at-d1-rotated", "rt-d1-rotated", "max")
	seedLive(t, s, fableEmails["2"], "org-2", rotated)
	return s, rotated
}

func TestDefaultSwitchWithAFableSessionsConfigBacksUpTheLastInstall(t *testing.T) {
	s, rotated := fableSessionConfig(t, true, true)
	twoBefore, _ := s.ReadAccountCredentials("2", fableEmails["2"])
	oneBefore, _ := s.ReadAccountCredentials("1", fableEmails["1"])
	res, err := SwitchTo(s, "5", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if two, _ := s.ReadAccountCredentials("2", fableEmails["2"]); two != twoBefore {
		t.Errorf("Account-2's default login was replaced by %s", two)
	}
	if one, _ := s.ReadAccountCredentials("1", fableEmails["1"]); one != rotated {
		t.Errorf("Account-1's default login = %s, want the rotated one", one)
	}
	if prev, _ := s.Creds.ReadPrev("1", fableEmails["1"]); prev != oneBefore {
		t.Errorf("Account-1's .prev = %s, want %s", prev, oneBefore)
	}
	rows, _ := s.Creds.ListUnclaimed()
	if len(rows) != 1 {
		t.Fatalf("unclaimed %v, want one copy", rows)
	}
	for _, row := range rows {
		if row["reason"] != store.SourceLastInstall || row["slot"] != "1" {
			t.Errorf("unclaimed row %v", row)
		}
	}
	if n, _ := refOf(t, res.(map[string]any)["from"]); n != 1 {
		t.Errorf("from %v, want Account-1", res.(map[string]any)["from"])
	}
}

// No default switch since the Fable logins came, so no chain record names
// one: the roster's active account is still the last install, and the login
// is backed up into it.
func TestDefaultSwitchWithAFableSessionsConfigAndNoChainRecordBacksUpTheActiveAccount(t *testing.T) {
	s, rotated := fableSessionConfig(t, true, false)
	if _, err := os.Stat(filepath.Join(s.BackupDir(), "live_lineage.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the default chain record exists: %v", err)
	}
	twoBefore, _ := s.ReadAccountCredentials("2", fableEmails["2"])
	if _, err := SwitchTo(s, "5", true, false); err != nil {
		t.Fatal(err)
	}
	if two, _ := s.ReadAccountCredentials("2", fableEmails["2"]); two != twoBefore {
		t.Errorf("Account-2's default login was replaced by %s", two)
	}
	if one, _ := s.ReadAccountCredentials("1", fableEmails["1"]); one != rotated {
		t.Errorf("Account-1's default login = %s, want the rotated one", one)
	}
	if rows, _ := s.Creds.ListUnclaimed(); len(rows) != 1 {
		t.Errorf("unclaimed %v, want one copy", rows)
	}
}

// The roster names no active account: no owner. The login is stashed and the
// switch writes it into no slot.
func TestDefaultSwitchWithAFableSessionsConfigAndNoActiveAccountStashesTheLogin(t *testing.T) {
	s, _ := fableSessionConfig(t, true, false)
	data := mustSeq(t, s)
	data.ActiveAccountNumber = nil
	writeSeq(t, s, data)
	twoBefore, _ := s.ReadAccountCredentials("2", fableEmails["2"])
	oneBefore, _ := s.ReadAccountCredentials("1", fableEmails["1"])
	res, err := SwitchTo(s, "5", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if two, _ := s.ReadAccountCredentials("2", fableEmails["2"]); two != twoBefore {
		t.Errorf("Account-2's default login was replaced by %s", two)
	}
	if one, _ := s.ReadAccountCredentials("1", fableEmails["1"]); one != oneBefore {
		t.Errorf("Account-1's default login = %s, want it unchanged", one)
	}
	if rows, _ := s.Creds.ListUnclaimed(); len(rows) != 1 {
		t.Fatalf("unclaimed %v, want the rotated login", rows)
	}
	if live := readActiveCreds(t, s); !oauth.SameAccountBlock(live, planCreds("at-d5", "rt-d5", "max")) {
		t.Errorf("live = %s, want Account-5's", live)
	}
	if from := res.(map[string]any)["from"]; from != nil && from.(map[string]any) != nil {
		t.Errorf("from %v, want none", from)
	}
}

// The same scenario on a box without a Fable login gives v0.15.1's result:
// the slot the config names takes the login (the pre-fix backup), nothing is
// stashed and no chain record is written.
func TestDefaultSwitchWithoutFableLoginsBacksUpIntoTheConfigsSlot(t *testing.T) {
	s, rotated := fableSessionConfig(t, false, true)
	res, err := SwitchTo(s, "5", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if two, _ := s.ReadAccountCredentials("2", fableEmails["2"]); two != rotated {
		t.Errorf("Account-2's default login = %s, want the live login (v0.15.1)", two)
	}
	if rows, _ := s.Creds.ListUnclaimed(); len(rows) != 0 {
		t.Errorf("unclaimed %v, want none", rows)
	}
	if _, err := os.Stat(filepath.Join(s.BackupDir(), "live_lineage.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a switch without Fable logins wrote the chain record: %v", err)
	}
	if n, _ := refOf(t, res.(map[string]any)["from"]); n != 2 {
		t.Errorf("from %v, want Account-2", res.(map[string]any)["from"])
	}
}
