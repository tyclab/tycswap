package switching

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

func TestGroupReleaseReturnsNewestCredentialToPool(t *testing.T) {
	s := groupFixture(t)
	opus := scope(t, s, groups.Opus)
	if _, err := SwitchTo(opus, "1", true, false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.SwitchableAccountNumbers(), ","); got != "2,3,4" {
		t.Fatalf("claimed account offered to the default login: %s", got)
	}
	rotated := withMCPOAuth(t, groupCreds("max", "a1-new", "r1"), "srv", "mcp")
	if err := opus.Creds.WriteActive(rotated); err != nil {
		t.Fatal(err)
	}
	result, err := ReleaseGroup(opus, false)
	if err != nil {
		t.Fatal(err)
	}
	if ref, _ := result["released"].(map[string]any); ref == nil || ref["email"] != "one@x.com" {
		t.Fatalf("result: %v", result)
	}
	backup, _ := s.ReadAccountCredentials("1", "one@x.com")
	if oauth.ExtractAccessToken(backup) != "a1-new" || mcpTokenOf(t, backup, "srv") != "" {
		t.Fatalf("backup did not take the group's account-only credential: %s", backup)
	}
	live, _, _ := opus.Creds.ReadActive()
	if oauth.ExtractAccessToken(live) != "" || mcpTokenOf(t, live, "srv") != "mcp" {
		t.Fatalf("profile kept its login or lost its MCP sign-in: %s", live)
	}
	if _, _, ok := sessprofile.ReadSessionIdentity(opus.ProfileDir()); ok {
		t.Fatal("profile still names the released account")
	}
	if active, _ := groups.LoadActive(s.SharedRoot(), groups.Opus); active != nil {
		t.Fatal("active record survived the release")
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != "" {
		t.Fatalf("released account still owned: %+v, %v", owner, err)
	}
	if got := strings.Join(s.SwitchableAccountNumbers(), ","); got != "1,2,3,4" {
		t.Fatalf("released account not offered to the default login: %s", got)
	}
	if err := s.EnsureNoLiveSession("2", "two@x.com", "remove"); err != nil {
		t.Fatalf("the released profile's MCP sign-in blocks other accounts: %v", err)
	}
	if _, err := SwitchTo(opus, "2", true, false); err != nil {
		t.Fatalf("group cannot take another account after release: %v", err)
	}
	if live, _, _ := opus.Creds.ReadActive(); mcpTokenOf(t, live, "srv") != "mcp" || oauth.ExtractAccessToken(live) != "a2" {
		t.Fatalf("next switch lost the MCP sign-in or took the wrong account: %s", live)
	}
}

func TestGroupReleaseRefusesLiveSessions(t *testing.T) {
	s := groupFixture(t)
	fable := scope(t, s, groups.Fable)
	if _, err := SwitchTo(fable, "1", true, false); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(fable.ProfileDir(), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	row, _ := json.Marshal(map[string]any{"pid": os.Getpid()})
	if err := os.WriteFile(filepath.Join(sessions, "self.json"), row, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseGroup(fable, false); err == nil || !strings.Contains(err.Error(), "live sessions") {
		t.Fatalf("release ignored a live session: %v", err)
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != string(groups.Fable) {
		t.Fatalf("refused release dropped the claim: %+v, %v", owner, err)
	}
}

func TestGroupReleaseHoldsUnverifiedCredential(t *testing.T) {
	s := groupFixture(t)
	opus := scope(t, s, groups.Opus)
	if _, err := SwitchTo(opus, "1", true, false); err != nil {
		t.Fatal(err)
	}
	if err := opus.Creds.WriteActive(groupCreds("max", "x", "unknown-lineage")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseGroup(opus, false); err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("release saved an unverified credential: %v", err)
	}
	backup, _ := s.ReadAccountCredentials("1", "one@x.com")
	if oauth.ExtractAccessToken(backup) != "a1" {
		t.Fatal("refused release overwrote the backup")
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != string(groups.Opus) {
		t.Fatalf("refused release dropped the claim: %+v, %v", owner, err)
	}
}

func TestGroupReleaseWithoutAccountIsNoOp(t *testing.T) {
	s := groupFixture(t)
	result, err := ReleaseGroup(scope(t, s, groups.Fable), false)
	if err != nil || result["released"] != nil {
		t.Fatalf("empty release: %v, %v", result, err)
	}
}
