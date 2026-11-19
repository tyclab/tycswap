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

// switch 2, the account ~/.claude.json names because a Fable session wrote
// it, while the default login is Account-1's (its last install): Account-2
// is not live, so the switch backs Account-1's login up and installs 2's.
// switch 1 repairs the config, saving the rotated login first.
func TestSwitchToTheFableSessionsConfigAccountSwitches(t *testing.T) {
	t.Run("the config's account", func(t *testing.T) {
		s, rotated := fableSessionConfig(t, true, true)
		res, err := SwitchTo(s, "2", true, false)
		if err != nil {
			t.Fatal(err)
		}
		m := res.(map[string]any)
		if m["switched"] != true {
			t.Fatalf("result %v, want a switch to Account-2", m)
		}
		if one, _ := s.ReadAccountCredentials("1", fableEmails["1"]); one != rotated {
			t.Errorf("Account-1's default login = %s, want the rotated one", one)
		}
		if live := readActiveCreds(t, s); !oauth.SameAccountBlock(live, planCreds("at-d2", "rt-d2", "max")) {
			t.Errorf("live = %s, want Account-2's", live)
		}
	})
	t.Run("the last install", func(t *testing.T) {
		s, rotated := fableSessionConfig(t, true, true)
		res, err := SwitchTo(s, "1", true, false)
		if err != nil {
			t.Fatal(err)
		}
		if m := res.(map[string]any); m["reason"] != "config-repaired" {
			t.Fatalf("result %v, want the config repaired", m)
		}
		if one, _ := s.ReadAccountCredentials("1", fableEmails["1"]); one != rotated {
			t.Errorf("Account-1's default login = %s, want the rotated one", one)
		}
		if live := readActiveCreds(t, s); live != rotated {
			t.Errorf("live = %s, want the rotated login left in place", live)
		}
	})
}

// Without a Fable login, switch 2 in the same state answers as v0.15.1:
// already on Account-2, nothing written.
func TestSwitchToTheConfigsAccountWithoutFableLoginsIsAlreadyActive(t *testing.T) {
	s, rotated := fableSessionConfig(t, false, true)
	before := snapshotTree(t, s.BackupDir())
	res, err := SwitchTo(s, "2", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if m := res.(map[string]any); m["switched"] != false || m["reason"] != "already-active" {
		t.Fatalf("result %v, want already-active", m)
	}
	if live := readActiveCreds(t, s); live != rotated {
		t.Errorf("live = %s, want it untouched", live)
	}
	after := snapshotTree(t, s.BackupDir())
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed", k)
		}
	}
	if len(after) != len(before) {
		t.Errorf("files %d, want %d", len(after), len(before))
	}
}

// switch --strategy next-available and the plain rotation, while a Fable
// session's Account-2 is in ~/.claude.json and the default login is
// Account-1's last install: the current account is the owner switch N takes,
// so the next account is 2 and Account-1's rotated login is backed up.
func TestSwitchByStrategyTakesTheOwnerAsCurrent(t *testing.T) {
	for _, strategy := range []string{"next-available", "rotation"} {
		t.Run(strategy, func(t *testing.T) {
			s, rotated := fableSessionConfig(t, true, true)
			res, err := Switch(s, &strategy, true)
			if err != nil {
				t.Fatal(err)
			}
			m := res.(map[string]any)
			from, _ := refOf(t, m["from"])
			to, _ := refOf(t, m["to"])
			if m["switched"] != true || from != 1 || to != 2 {
				t.Fatalf("result %v, want a switch from Account-1 to Account-2", m)
			}
			if one, _ := s.ReadAccountCredentials("1", fableEmails["1"]); one != rotated {
				t.Errorf("Account-1's default login = %s, want the rotated one", one)
			}
		})
	}
}

// Without a Fable login the same state gives v0.16.0's result: the config's
// Account-2 is current, so next-available takes 3 and the rotation answers
// already-active.
func TestSwitchByStrategyWithoutFableLoginsTakesTheConfigsAccount(t *testing.T) {
	for _, tc := range []struct {
		strategy, reason string
		to               int
	}{{"next-available", "", 3}, {"rotation", "already-active", 2}} {
		t.Run(tc.strategy, func(t *testing.T) {
			s, _ := fableSessionConfig(t, false, true)
			res, err := Switch(s, &tc.strategy, true)
			if err != nil {
				t.Fatal(err)
			}
			m := res.(map[string]any)
			if to, _ := refOf(t, m["to"]); to != tc.to || m["switched"] != (tc.reason == "") || tc.reason != "" && m["reason"] != tc.reason {
				t.Fatalf("result %v, want to Account-%d, reason %q", m, tc.to, tc.reason)
			}
		})
	}
}
