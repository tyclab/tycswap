package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/testutil"
)

// Claude Code keeps running at "Login successful. Press Enter to continue…";
// the account is stored and the command returns without that Enter (A82).
func TestAddLoginStoresBeforeClaudeCodeEnds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the login is ended with SIGTERM")
	}
	f := newLoginFixture(t)
	testutil.Setenv(t, "FAKE_CLAUDE_MODE", "wait")
	testutil.Setenv(t, "FAKE_CLAUDE_PLAN", "Ultra")
	start := time.Now()
	code, out, errb := f.run(t, "add", "--login", "--switch")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("add waited %s for the login process to end", took)
	}
	if !strings.Contains(out, "Added Account 2: b@example.com [personal] (from login)") || strings.Contains(out, "Press Enter") {
		t.Errorf("output = %q", out)
	}
	seq := f.sequence(t)
	if seq.Accounts["2"]["email"] != "b@example.com" || seq.Accounts["2"]["plan"] != "Ultra" {
		t.Errorf("slot 2 = %v", seq.Accounts["2"])
	}
	if seq.Active == nil || *seq.Active != 2 {
		t.Errorf("activeAccountNumber = %v, want 2", seq.Active)
	}
	assertNoScratch(t)
}

// The plan Claude Code records for the login is kept and shown (A79).
func TestAddLoginRecordsThePlanAndListShowsIt(t *testing.T) {
	f := newLoginFixture(t)
	testutil.Setenv(t, "FAKE_CLAUDE_PLAN", "Ultra")
	testutil.Setenv(t, "FAKE_CLAUDE_ORGNAME", "b@example.com's Organization")
	if code, _, errb := f.run(t, "add", "--login"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	code, out, errb := runHuman(t, "list")
	if code != 0 {
		t.Fatalf("list: exit %d, stderr %q", code, errb)
	}
	if !strings.Contains(out, "b@example.com [personal · Ultra]") {
		t.Errorf("list output = %q", out)
	}
	code, out, errb = runCLI(t, []string{"list", "--json"}, false, false)
	if code != 0 {
		t.Fatalf("list --json: exit %d, stderr %q", code, errb)
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil || len(payload.Accounts) != 2 {
		t.Fatalf("list --json: %v %q", err, out)
	}
	if plan, _ := payload.Accounts[1]["plan"].(string); plan != "Ultra" {
		t.Errorf("row 2 plan = %v", payload.Accounts[1]["plan"])
	}
	if _, present := payload.Accounts[0]["plan"]; present {
		t.Errorf("row 1 carries a plan it never reported: %v", payload.Accounts[0]["plan"])
	}
}

func TestAddLoginRenewsAccountWithAnIdleSessionClaim(t *testing.T) {
	f := newLoginFixture(t)
	if code, _, errb := f.run(t, "add", "--login"); code != 0 {
		t.Fatalf("initial login: exit %d, stderr %q", code, errb)
	}
	root := paths.GetBackupRoot()
	dir := sessprofile.SessionDirFor(root, "2", "b@example.com")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	account := groups.Account{Number: "2", Email: "b@example.com", UUID: "uuid-b@example.com"}
	for path, value := range map[string]any{
		filepath.Join(dir, ".claude.json"):      map[string]any{"oauthAccount": map[string]any{"emailAddress": account.Email, "organizationUuid": ""}},
		filepath.Join(dir, ".credentials.json"): map[string]any{"claudeAiOauth": map[string]any{"accessToken": "old-access", "refreshToken": "old-refresh"}},
	} {
		if err := groups.WriteJSON(path, value); err != nil {
			t.Fatal(err)
		}
	}
	registry := groups.Registry{Version: 1, Claims: map[string]groups.Owner{"2": {Scope: "legacy:2", ProfileDir: dir, Account: account}}}
	if err := registry.Save(root); err != nil {
		t.Fatal(err)
	}
	testutil.Setenv(t, "FAKE_CLAUDE_TOKEN", "renewed-access")
	testutil.Setenv(t, "FAKE_CLAUDE_REFRESH", "renewed-refresh")
	if code, out, errb := f.run(t, "add", "--login", "--switch"); code != 0 {
		t.Fatalf("renewal: exit %d, stdout %q, stderr %q", code, out, errb)
	}
	seq := f.sequence(t)
	if len(seq.Accounts) != 2 || seq.Active == nil || *seq.Active != 2 {
		t.Fatal("renewal did not activate the original slot")
	}
	if !strings.Contains(f.liveCreds(t), "renewed-access") {
		t.Fatal("renewed credential was not made live")
	}
	assertNoScratch(t)
}
