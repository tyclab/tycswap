package switching

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/store"
)

func groupCreds(plan, access, refresh string) string {
	var obj map[string]any
	_ = json.Unmarshal([]byte(oauthCreds(access, refresh)), &obj)
	obj["claudeAiOauth"].(map[string]any)["subscriptionType"] = plan
	raw, _ := json.Marshal(obj)
	return string(raw)
}

func groupFixture(t *testing.T) *store.Store {
	t.Helper()
	return groupFixtureWith(t, nil)
}

func groupFixtureWith(t *testing.T, client oauth.Client) *store.Store {
	t.Helper()
	s := newTestStore(t, client)
	writeSeq(t, s, seqData(ptrInt(3), []int{1, 2, 3}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": "one@x.com", "organizationUuid": ""}),
		"2": record(map[string]any{"email": "two@x.com", "organizationUuid": ""}),
		"3": record(map[string]any{"email": "default@x.com", "organizationUuid": ""}),
	}))
	seedBackup(t, s, "1", "one@x.com", groupCreds("max", "a1", "r1"), "")
	seedBackup(t, s, "2", "two@x.com", groupCreds("max", "a2", "r2"), "")
	seedBackup(t, s, "3", "default@x.com", groupCreds("pro", "a3", "r3"), "")
	seedLive(t, s, "default@x.com", "", groupCreds("pro", "a3", "r3"))
	return s
}

func scope(t *testing.T, s *store.Store, id groups.ID) *store.Store {
	t.Helper()
	group, err := s.ForGroup(id)
	if err != nil {
		t.Fatal(err)
	}
	return group
}

// seedGroupHolding leaves the files a finished group switch onto number left
// before session groups retired: the claim, the active record, the profile's
// credential and its config identity, and the group's control files.
func seedGroupHolding(t *testing.T, s *store.Store, id groups.ID, number, creds string) *store.Store {
	t.Helper()
	g := scope(t, s, id)
	account := groups.Account{Number: number, Email: s.AccountEmail(number)}
	registry, err := groups.LoadRegistry(s.SharedRoot())
	if err != nil {
		t.Fatal(err)
	}
	registry.Claims[number] = groups.Owner{Scope: string(id), ProfileDir: g.ProfileDir(), Account: account}
	if err := registry.Save(s.SharedRoot()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(g.ProfileDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := groups.WriteJSON(groups.ActivePath(s.SharedRoot(), id), account); err != nil {
		t.Fatal(err)
	}
	if err := g.Creds.WriteActive(creds); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"oauthAccount": map[string]any{"emailAddress": account.Email, "organizationUuid": ""}}
	if err := groups.WriteJSON(g.GlobalConfigPath(), config); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hooks.json", "autoswitch_state.json"} {
		if err := groups.WriteJSON(filepath.Join(groups.ScopeDir(s.SharedRoot(), id), name), map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func liveSessionIn(t *testing.T, profile string) {
	t.Helper()
	sessions := filepath.Join(profile, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	row, _ := json.Marshal(map[string]any{"pid": os.Getpid()})
	if err := os.WriteFile(filepath.Join(sessions, "self.json"), row, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pending(t *testing.T, s *store.Store, id groups.ID) bool {
	t.Helper()
	got, err := groups.Pending(s.SharedRoot(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A retired group hands its newest credential back to the account's slot,
// keeps its profile's MCP sign-in and conversations, and leaves nothing for
// the next tick to do (DESIGN A78).
func TestRetireReturnsNewestCredentialToPool(t *testing.T) {
	s := groupFixture(t)
	opus := seedGroupHolding(t, s, groups.Opus, "1", groupCreds("max", "a1", "r1"))
	if got := strings.Join(s.SwitchableAccountNumbers(), ","); got != "2,3" {
		t.Fatalf("claimed account offered to the default login: %s", got)
	}
	rotated := withMCPOAuth(t, groupCreds("max", "a1-new", "r1"), "srv", "mcp")
	if err := opus.Creds.WriteActive(rotated); err != nil {
		t.Fatal(err)
	}
	if !pending(t, s, groups.Opus) {
		t.Fatal("a holding group is not pending retirement")
	}
	released, err := RetireGroup(opus)
	if err != nil || released == nil || released.Email != "one@x.com" {
		t.Fatalf("retire = %+v, %v", released, err)
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
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != "" {
		t.Fatalf("released account still owned: %+v, %v", owner, err)
	}
	if got := strings.Join(s.SwitchableAccountNumbers(), ","); got != "1,2,3" {
		t.Fatalf("released account not offered to the default login: %s", got)
	}
	if pending(t, s, groups.Opus) {
		t.Fatal("a retired group is still pending")
	}
	if _, err := os.Stat(opus.ProfileDir()); err != nil {
		t.Fatalf("retirement removed the profile: %v", err)
	}
	if _, err := SwitchTo(opus, "2", true, false); err == nil {
		t.Fatal("a retired group switched accounts")
	}
}

func TestRetireWaitsForLiveSessions(t *testing.T) {
	s := groupFixture(t)
	fable := seedGroupHolding(t, s, groups.Fable, "1", groupCreds("max", "a1", "r1"))
	liveSessionIn(t, fable.ProfileDir())
	if _, err := RetireGroup(fable); !errors.Is(err, ErrGroupBusy) {
		t.Fatalf("retire under a live session = %v, want ErrGroupBusy", err)
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != string(groups.Fable) {
		t.Fatalf("a busy group lost its claim: %+v, %v", owner, err)
	}
	if !pending(t, s, groups.Fable) {
		t.Fatal("a busy group stopped pending")
	}
}

func TestRetireHoldsUnverifiedCredential(t *testing.T) {
	s := groupFixture(t)
	opus := seedGroupHolding(t, s, groups.Opus, "1", groupCreds("max", "x", "unknown-lineage"))
	if _, err := RetireGroup(opus); err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("retire saved an unverified credential: %v", err)
	}
	if backup, _ := s.ReadAccountCredentials("1", "one@x.com"); oauth.ExtractAccessToken(backup) != "a1" {
		t.Fatal("refused retirement overwrote the backup")
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != string(groups.Opus) {
		t.Fatalf("refused retirement dropped the claim: %+v, %v", owner, err)
	}
	if !pending(t, s, groups.Opus) {
		t.Fatal("a held group stopped pending")
	}
}

func TestRetireWithoutAccountDropsControlFiles(t *testing.T) {
	s := groupFixture(t)
	fable := scope(t, s, groups.Fable)
	if pending(t, s, groups.Fable) {
		t.Fatal("a group that never ran is pending")
	}
	if err := groups.WriteJSON(filepath.Join(groups.ScopeDir(s.SharedRoot(), groups.Fable), "hooks.json"), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	released, err := RetireGroup(fable)
	if err != nil || released != nil {
		t.Fatalf("retire without an account = %+v, %v", released, err)
	}
	if pending(t, s, groups.Fable) {
		t.Fatal("control files survived retirement")
	}
}

// Conversations are copied into the default profile; nothing there is replaced.
func TestRetireCopiesHistoryWithoutReplacing(t *testing.T) {
	s := groupFixture(t)
	opus := seedGroupHolding(t, s, groups.Opus, "1", groupCreds("max", "a1", "r1"))
	write := func(dir, rel, body string) {
		path := filepath.Join(dir, "projects", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(opus.ProfileDir(), "-work/group.jsonl", "group")
	write(opus.ProfileDir(), "-work/group/subagents/agent.jsonl", "agent")
	write(opus.ProfileDir(), "-work/both.jsonl", "group copy")
	write(s.DefaultProfileDir(), "-work/both.jsonl", "default copy")
	if _, err := RetireGroup(opus); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"-work/group.jsonl": "group", "-work/group/subagents/agent.jsonl": "agent", "-work/both.jsonl": "default copy"} {
		got, err := os.ReadFile(filepath.Join(s.DefaultProfileDir(), "projects", rel))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", rel, got, err, want)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(opus.ProfileDir(), "projects", "-work/group.jsonl")); string(got) != "group" {
		t.Fatal("retirement moved the group's history instead of copying it")
	}
}

func expiredGroupCreds(access, refresh string) string {
	var obj map[string]any
	_ = json.Unmarshal([]byte(groupCreds("max", access, refresh)), &obj)
	obj["claudeAiOauth"].(map[string]any)["expiresAt"] = 1
	raw, _ := json.Marshal(obj)
	return string(raw)
}

// groupLoginClient refreshes only the group's own refresh token and resolves
// only the token that refresh issues: the expired one answers 401.
func groupLoginClient(grant bool) (*oauth.FakeClient, *int) {
	refreshes := 0
	return &oauth.FakeClient{
		RefreshFn: func(_ context.Context, creds string) oauth.RefreshOutcome {
			refreshes++
			if !grant || oauth.ExtractAccessToken(creds) != "a1-group" {
				return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
			}
			return oauth.RefreshOutcome{Credentials: groupCreds("max", "a1-fresh", "r1-next")}
		},
		ProfileFn: func(_ context.Context, token string) *oauth.Identity {
			if token == "a1-fresh" {
				return &oauth.Identity{Email: "one@x.com"}
			}
			return nil
		},
	}, &refreshes
}

// heldAndDecayed replays 2026-11-04: the Opus group holds Account-1 after its
// sessions ended, its access token has expired, and the default login re-added
// the same account with a new lineage.
func heldAndDecayed(t *testing.T, grant bool) (*store.Store, *store.Store, *int) {
	t.Helper()
	client, refreshes := groupLoginClient(grant)
	s := groupFixtureWith(t, client)
	opus := seedGroupHolding(t, s, groups.Opus, "1", expiredGroupCreds("a1-group", "r1-group"))
	seedBackup(t, s, "1", "one@x.com", groupCreds("max", "a1-default", "r1-default"), "")
	return s, opus, refreshes
}

func TestRetireRefreshesAnExpiredLogin(t *testing.T) {
	s, opus, refreshes := heldAndDecayed(t, true)
	if _, err := RetireGroup(opus); err != nil {
		t.Fatalf("retire of a group with an expired login: %v", err)
	}
	if *refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", *refreshes)
	}
	if backup, _ := s.ReadAccountCredentials("1", "one@x.com"); oauth.ExtractAccessToken(backup) != "a1-fresh" {
		t.Fatalf("backup did not take the refreshed group login: %s", backup)
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != "" {
		t.Fatalf("released account still owned: %+v, %v", owner, err)
	}
}

func TestRetireKeepsADeadLoginHeld(t *testing.T) {
	s, opus, refreshes := heldAndDecayed(t, false)
	if _, err := RetireGroup(opus); err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("retire with a dead group login: %v", err)
	}
	if *refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", *refreshes)
	}
	if backup, _ := s.ReadAccountCredentials("1", "one@x.com"); oauth.ExtractAccessToken(backup) != "a1-default" {
		t.Fatalf("refused retirement overwrote the backup: %s", backup)
	}
	if live, _, _ := opus.Creds.ReadActive(); oauth.ExtractAccessToken(live) != "a1-group" {
		t.Fatalf("failed refresh rewrote the group login: %s", live)
	}
}

func TestRetireNeverRefreshesUnderALiveSession(t *testing.T) {
	_, opus, refreshes := heldAndDecayed(t, true)
	liveSessionIn(t, opus.ProfileDir())
	if _, err := RetireGroup(opus); !errors.Is(err, ErrGroupBusy) {
		t.Fatalf("retire ignored a live session: %v", err)
	}
	if *refreshes != 0 {
		t.Fatalf("refreshed a login a live session owns: %d", *refreshes)
	}
}

// The default login signed in to the held account again: two owners, so usage
// and switching hold, until the retired group lets go.
func TestRetireEndsTheDefaultLoginConflict(t *testing.T) {
	s, opus, _ := heldAndDecayed(t, true)
	seedLive(t, s, "one@x.com", "", groupCreds("max", "a1-default", "r1-default"))
	if _, err := s.CredentialOwner("1"); err == nil || !strings.Contains(err.Error(), "conflicting credential owners") {
		t.Fatalf("fixture does not reproduce the conflict: %v", err)
	}
	if released, err := RetireGroup(opus); err != nil || released == nil {
		t.Fatalf("retire = %+v, %v", released, err)
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != "default" || owner.Uncertain {
		t.Fatalf("default login does not own the account alone: %+v, %v", owner, err)
	}
}
