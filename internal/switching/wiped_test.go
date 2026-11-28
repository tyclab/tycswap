// A live login whose tokens Claude Code wiped never replaces a slot's stored
// login (DESIGN A130).
package switching

import (
	"strings"
	"testing"
)

const wipedLogin = `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":4102444800000,"scopes":["user:inference"]}}`

func TestSwitchOffAWipedLoginKeepsTheStoredLogin(t *testing.T) {
	s, _ := rotatedSeat(t, wipedLogin, "6")
	out, err := SwitchTo(s, "5", true, false)
	if err != nil {
		t.Fatalf("SwitchTo(5): %v", err)
	}
	m := asMap(t, out)
	if m["switched"] != true || refField(t, m, "from") != 6 || refField(t, m, "to") != 5 {
		t.Fatalf("got %v, want a switch from 6 to 5", m)
	}
	if got, _ := s.ReadAccountCredentials("6", lineageEmail); got != lineageBackup6 {
		t.Errorf("Account-6's stored login changed to %s", got)
	}
	warnings, _ := m["warnings"].([]string)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Account-6's stored login was kept") {
		t.Errorf("warnings = %q, want one naming Account-6's kept login", warnings)
	}
	if log := logText(t, s); strings.Contains(log, "pre-fix backup") {
		t.Errorf("the wiped login was backed up:\n%s", log)
	}
}

// A wiped login attributed by the last install is neither stashed nor backed up.
func TestSwitchOffAWipedLastInstallStashesNothing(t *testing.T) {
	s, _ := fableSessionConfig(t, true, true)
	seedLive(t, s, fableEmails["2"], "org-2", wipedLogin)
	oneBefore, _ := s.ReadAccountCredentials("1", fableEmails["1"])
	if _, err := SwitchTo(s, "5", true, false); err != nil {
		t.Fatal(err)
	}
	if one, _ := s.ReadAccountCredentials("1", fableEmails["1"]); one != oneBefore {
		t.Errorf("Account-1's stored login changed to %s", one)
	}
	if rows, _ := s.Creds.ListUnclaimed(); len(rows) != 0 {
		t.Errorf("unclaimed %v, want none", rows)
	}
	if log := logText(t, s); strings.Contains(log, "as its last install") {
		t.Errorf("the wiped login was taken for the last install:\n%s", log)
	}
}

// A wiped Fable store login the last install attributes leaves that
// account's Fable login as stored, unstashed, and says so.
func TestSwitchFableOffAWipedLoginKeepsTheFableLogin(t *testing.T) {
	s, f := fableSwitchStore(t)
	if _, err := SwitchFable(f, "1", FableSwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	setFableLive(t, s, wipedLogin)
	if _, err := SwitchFable(f, "2", FableSwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.ReadAccountCredentials("1", fableEmails["1"]); got != planCreds("at-f1", "rt-f1", "max") {
		t.Errorf("Account-1's Fable login = %s, want it unchanged", got)
	}
	if rows, _ := f.Creds.ListUnclaimed(); len(rows) != 0 {
		t.Errorf("unclaimed %v, want none", rows)
	}
	if log := logText(t, s); !strings.Contains(log, "account 1's Fable login was kept") {
		t.Errorf("no warning about the kept Fable login:\n%s", log)
	}
}

// A wiped login in a store no account is named for is not stashed either, as
// the default switch stashes none (A130).
func TestSwitchFableOffAWipedUnknownLoginStashesNothing(t *testing.T) {
	s, f := fableSwitchStore(t)
	if _, err := SwitchFable(f, "1", FableSwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := EmptyFable(f); err != nil {
		t.Fatal(err)
	}
	setFableLive(t, s, wipedLogin)
	if _, err := SwitchFable(f, "2", FableSwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := f.Creds.ListUnclaimed(); len(rows) != 0 {
		t.Errorf("unclaimed %v, want none", rows)
	}
	if log := logText(t, s); !strings.Contains(log, "The Fable store's login carries no tokens") {
		t.Errorf("no warning about the wiped login:\n%s", log)
	}
}
