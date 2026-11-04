package switching

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/store"
)

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
	opus := scope(t, s, groups.Opus)
	if _, err := SwitchTo(opus, "1", true, false); err != nil {
		t.Fatal(err)
	}
	if err := opus.Creds.WriteActive(expiredGroupCreds("a1-group", "r1-group")); err != nil {
		t.Fatal(err)
	}
	seedBackup(t, s, "1", "one@x.com", groupCreds("max", "a1-default", "r1-default"), "")
	return s, opus, refreshes
}

func ageGroupActivity(t *testing.T, s *store.Store, by time.Duration) {
	t.Helper()
	old := s.Clk.Now().Add(-by)
	paths := []string{groups.ActivePath(s.SharedRoot(), s.GroupID()), filepath.Join(s.ProfileDir(), ".credentials.json")}
	for _, dir := range []string{filepath.Join(s.ProfileDir(), "sessions"), filepath.Join(groups.ScopeDir(s.SharedRoot(), s.GroupID()), "launches")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, dir)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	for _, p := range paths {
		if err := os.Chtimes(p, old, old); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

func TestGroupReleaseRefreshesAnExpiredLogin(t *testing.T) {
	s, opus, refreshes := heldAndDecayed(t, true)
	if _, err := ReleaseGroup(opus, false); err != nil {
		t.Fatalf("release of an idle group with an expired login: %v", err)
	}
	if *refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", *refreshes)
	}
	backup, _ := s.ReadAccountCredentials("1", "one@x.com")
	if oauth.ExtractAccessToken(backup) != "a1-fresh" {
		t.Fatalf("backup did not take the refreshed group login: %s", backup)
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != "" {
		t.Fatalf("released account still owned: %+v, %v", owner, err)
	}
}

func TestGroupReleaseKeepsADeadLoginHeld(t *testing.T) {
	s, opus, refreshes := heldAndDecayed(t, false)
	if _, err := ReleaseGroup(opus, false); err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("release with a dead group login: %v", err)
	}
	if *refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", *refreshes)
	}
	if backup, _ := s.ReadAccountCredentials("1", "one@x.com"); oauth.ExtractAccessToken(backup) != "a1-default" {
		t.Fatalf("refused release overwrote the backup: %s", backup)
	}
	if live, _, _ := opus.Creds.ReadActive(); oauth.ExtractAccessToken(live) != "a1-group" {
		t.Fatalf("failed refresh rewrote the group login: %s", live)
	}
}

func TestGroupReleaseNeverRefreshesUnderALiveSession(t *testing.T) {
	_, opus, refreshes := heldAndDecayed(t, true)
	sessions := filepath.Join(opus.ProfileDir(), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	row, _ := json.Marshal(map[string]any{"pid": os.Getpid()})
	if err := os.WriteFile(filepath.Join(sessions, "self.json"), row, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseGroup(opus, false); err == nil || !strings.Contains(err.Error(), "live sessions") {
		t.Fatalf("release ignored a live session: %v", err)
	}
	if *refreshes != 0 {
		t.Fatalf("refreshed a login a live session owns: %d", *refreshes)
	}
}

func TestIdleGroupReleasesAfterTheGrace(t *testing.T) {
	s, opus, _ := heldAndDecayed(t, true)
	ageGroupActivity(t, opus, 10*time.Minute)
	released, err := ReleaseIdleGroup(opus, 5*time.Minute)
	if err != nil || released == nil || released.Number != "1" {
		t.Fatalf("idle release = %+v, %v", released, err)
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != "" {
		t.Fatalf("released account still owned: %+v, %v", owner, err)
	}
}

// The default login signed in to the held account again: two owners, so usage
// and switching hold, until the idle group lets go.
func TestIdleGroupReleaseEndsTheDefaultLoginConflict(t *testing.T) {
	s, opus, _ := heldAndDecayed(t, true)
	seedLive(t, s, "one@x.com", "", groupCreds("max", "a1-default", "r1-default"))
	if _, err := s.CredentialOwner("1"); err == nil || !strings.Contains(err.Error(), "conflicting credential owners") {
		t.Fatalf("fixture does not reproduce the conflict: %v", err)
	}
	ageGroupActivity(t, opus, 10*time.Minute)
	if released, err := ReleaseIdleGroup(opus, 5*time.Minute); err != nil || released == nil {
		t.Fatalf("idle release = %+v, %v", released, err)
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != "default" || owner.Uncertain {
		t.Fatalf("default login does not own the account alone: %+v, %v", owner, err)
	}
}

func TestIdleGroupKeepsItsAccountInsideTheGrace(t *testing.T) {
	s, opus, refreshes := heldAndDecayed(t, true)
	ageGroupActivity(t, opus, 10*time.Minute)
	now := s.Clk.Now()
	if err := os.Chtimes(filepath.Join(opus.ProfileDir(), "sessions"), now, now); err != nil {
		t.Fatal(err)
	}
	released, err := ReleaseIdleGroup(opus, 5*time.Minute)
	if err != nil || released != nil || *refreshes != 0 {
		t.Fatalf("released inside the grace: %+v, %v, refreshes %d", released, err, *refreshes)
	}
	if owner, _ := s.CredentialOwner("1"); owner.Scope != string(groups.Opus) {
		t.Fatalf("claim dropped inside the grace: %+v", owner)
	}
}

// A launch reserves its routing session under the same lock that hands the
// group its account, before Claude registers a PID in the profile.
func TestIdleGroupKeepsItsAccountForALaunch(t *testing.T) {
	s, opus, _ := heldAndDecayed(t, true)
	ageGroupActivity(t, opus, 10*time.Minute)
	err := routingstate.With(s.SharedRoot(), func(state *routingstate.State) error {
		state.Sessions[routingstate.Key(string(groups.Opus), "launch-1")] = routingstate.Session{Group: string(groups.Opus), ID: "launch-1", PID: os.Getpid(), Model: "opus"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	released, err := ReleaseIdleGroup(opus, 5*time.Minute)
	if err != nil || released != nil {
		t.Fatalf("released under a launch: %+v, %v", released, err)
	}
	if owner, _ := s.CredentialOwner("1"); owner.Scope != string(groups.Opus) {
		t.Fatalf("claim dropped under a launch: %+v", owner)
	}
}
