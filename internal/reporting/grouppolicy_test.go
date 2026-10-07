package reporting

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestManagedHookReadsCapturedDefaultCredential(t *testing.T) {
	s := seedAtLimitStore(t, testutil.FixedClock(t, fixedNow))
	profile := groups.ProfileDir(s.BackupDir(), groups.Fable)
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, ".credentials.json"), []byte(oauthCreds("group-token", "group-refresh", 0)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", profile)
	infos := BuildAccountsInfo(s)
	if len(infos) != 2 || !infos[0].IsActive || oauth.ExtractAccessToken(infos[0].Creds) != "at1" {
		t.Fatal("group hook attributed its credential to the captured default account")
	}
}

func TestGroupModelPolicyFollowsLiveFamiliesAndSurvivesCollectorRestart(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	s := seedAtLimitStore(t, clk)
	now := clock.Seconds(clk)
	writeUsageRows(t, s, map[string]any{"1": map[string]any{
		"email": "alice@example.com", "organizationUuid": "", "fetchedAt": now - 600,
		"nextPollAt": now + 2800, "pollIntervalS": 3000,
		"lastGood": map[string]any{"five_hour": map[string]any{"pct": 10.0}, "seven_day": map[string]any{"pct": 20.0},
			"scoped": []any{map[string]any{"name": "Fable", "pct": 100.0}, map[string]any{"name": "Opus", "pct": 80.0}, map[string]any{"name": "Sonnet", "pct": 25.0}}},
	}})
	for _, id := range groups.All() {
		if err := groups.WriteJSON(groups.ActivePath(s.BackupDir(), id), groups.Account{Number: "2", Email: "bob@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	opus, err := s.ForGroup(groups.Opus)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(opus.ProfileDir(), "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := groups.WriteJSON(filepath.Join(opus.ProfileDir(), "sessions", strconv.Itoa(os.Getpid())+".json"), map[string]any{"pid": os.Getpid(), "sessionId": "live-sonnet"}); err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.NewStore(s.BackupDir()).Record(recovery.Event{SessionID: "live-sonnet", Provider: "claude", Group: "opus", Model: "sonnet", Kind: "SessionStart", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got := GroupSettings(opus, settings.Default())
	if got.Model == nil || *got.Model != "Sonnet" {
		t.Fatalf("live Sonnet counted wrong quotas: %+v", got.Model)
	}
	policies := resolvePollPolicies(s)
	if len(policies) != 3 {
		t.Fatalf("new collector lost saved group policies: %+v", policies)
	}
	ReplanCachedUsage(s)
	entry := UsageEntriesByAccount(s, map[string]bool{})["1"]
	if entry.NextPollAt == nil || *entry.NextPollAt >= now+1200 {
		t.Fatalf("usable Sonnet stayed parked behind Fable: %+v", entry)
	}
}
