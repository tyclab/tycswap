package switching

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

func legacySwitchProfile(t *testing.T, s *store.Store, creds string) string {
	t.Helper()
	dir := s.SessionDir("2", "b@x.com")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".claude.json":      backupConfig("b@x.com", ""),
		".credentials.json": creds,
		"history.jsonl":     "preserved conversation\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Lock.With(func() error { return s.ClaimLegacyProfile("2", "b@x.com", dir) }); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertLegacySwitchHeld(t *testing.T, s *store.Store, dir, active, backup, profile string) {
	t.Helper()
	if got := readActiveCreds(t, s); got != active {
		t.Fatal("refused switch changed the default login")
	}
	if got, err := s.ReadAccountCredentials("2", "b@x.com"); err != nil || got != backup {
		t.Fatalf("refused switch changed the target backup: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, ".credentials.json")); err != nil || string(got) != profile {
		t.Fatalf("refused switch changed the profile credential: %v", err)
	}
	registry, err := groups.LoadRegistry(s.SharedRoot())
	if err != nil || registry.Claims["2"].Scope != "legacy:2" {
		t.Fatalf("refused switch released the claim: %v", err)
	}
}

func TestSwitchToReclaimsIdleLegacyLogin(t *testing.T) {
	for _, tc := range []struct {
		name, access, refresh string
		verify                bool
	}{
		{"identical", "acc-b", "ref-b", false},
		{"new-access", "new-access", "ref-b", false},
		{"rotated", "new-access", "new-refresh", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := twoAccountStore(t)
			latest := oauthCreds(tc.access, tc.refresh)
			dir := legacySwitchProfile(t, s, latest)
			queries := 0
			s.OAuth = &oauth.FakeClient{ProfileFn: func(_ context.Context, token string) *oauth.Identity {
				queries++
				if s.Lock.Held() {
					t.Error("identity request ran under the store lock")
				}
				if token != tc.access {
					t.Error("identity request used the wrong login")
				}
				return &oauth.Identity{Email: "b@x.com"}
			}}
			out, err := SwitchTo(s, "2", true, false)
			if err != nil {
				t.Fatalf("switch from idle legacy profile: %v", err)
			}
			if m := asMap(t, out); m["switched"] != true || refField(t, m, "to") != 2 {
				t.Fatal("switch did not report Account-2 as active")
			}
			if got := readActiveCreds(t, s); got != latest {
				t.Fatal("switch lost the latest profile credential")
			}
			if got, err := s.ReadAccountCredentials("2", "b@x.com"); err != nil || got != latest {
				t.Fatalf("latest profile credential was not saved: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); !os.IsNotExist(err) {
				t.Fatal("legacy profile retained a second copy of the rotating login")
			}
			if got, err := os.ReadFile(filepath.Join(dir, "history.jsonl")); err != nil || string(got) != "preserved conversation\n" {
				t.Fatal("legacy history was not preserved")
			}
			if owner, err := s.CredentialOwner("2"); err != nil || owner.Scope != "default" || owner.Uncertain {
				t.Fatalf("default login did not acquire the account: %v", err)
			}
			wantQueries := 0
			if tc.verify {
				wantQueries = 1
			}
			if queries != wantQueries {
				t.Fatalf("identity queries = %d, want %d", queries, wantQueries)
			}
		})
	}
}

func TestSwitchToPreservesUnverifiedLegacyLogin(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   *oauth.Identity
	}{
		{"unknown", nil},
		{"foreign", &oauth.Identity{Email: "a@x.com", OrgUUID: "other-org", UUID: "other-account"}},
		{"same-email-other-org", &oauth.Identity{Email: "b@x.com", OrgUUID: "other-org"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, active, backup := twoAccountStore(t)
			latest := oauthCreds("unknown-access", "unknown-refresh")
			dir := legacySwitchProfile(t, s, latest)
			s.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity { return tc.id }}
			if _, err := SwitchTo(s, "2", true, false); err == nil || !strings.Contains(err.Error(), "could not be verified") {
				t.Fatalf("unverified legacy login was not refused: %v", err)
			}
			assertLegacySwitchHeld(t, s, dir, active, backup, latest)
		})
	}
}

func TestSwitchToPreservesLiveOrUnreadableLegacySession(t *testing.T) {
	for _, tc := range []struct{ name, registry, filename, body, message string }{
		{"live-cli", "sessions", "live.json", `{"pid":` + strconv.Itoa(os.Getpid()) + `}`, "retry tycswap switch 2"},
		{"live-ide", "ide", "1234.lock", `{"pid":` + strconv.Itoa(os.Getpid()) + `}`, "retry tycswap switch 2"},
		{"unreadable", "sessions", "broken.json", `{"pid":`, "ownership is unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, active, backup := twoAccountStore(t)
			dir := legacySwitchProfile(t, s, backup)
			registry := filepath.Join(dir, tc.registry)
			if err := os.MkdirAll(registry, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(registry, tc.filename), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := SwitchTo(s, "2", true, true); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("unsafe legacy switch was not refused: %v", err)
			}
			assertLegacySwitchHeld(t, s, dir, active, backup, backup)
		})
	}
}

func TestSwitchToRechecksLegacyCredentialAfterIdentityLookup(t *testing.T) {
	s, active, backup := twoAccountStore(t)
	dir := legacySwitchProfile(t, s, oauthCreds("first-access", "first-refresh"))
	changed := oauthCreds("changed-access", "changed-refresh")
	s.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity {
		if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(changed), 0o600); err != nil {
			t.Fatal(err)
		}
		return &oauth.Identity{Email: "b@x.com"}
	}}
	if _, err := SwitchTo(s, "2", true, false); err == nil || !strings.Contains(err.Error(), "ownership changed") {
		t.Fatalf("changed legacy credential was not refused: %v", err)
	}
	assertLegacySwitchHeld(t, s, dir, active, backup, changed)
}

func TestSwitchToWaitsForLegacyCredentialLock(t *testing.T) {
	s, active, backup := twoAccountStore(t)
	dir := legacySwitchProfile(t, s, backup)
	lockDir := dir + ".lock"
	if err := os.Mkdir(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := s.Clk.Now()
	if err := os.Chtimes(lockDir, now, now); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := SwitchTo(s, "2", true, false); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("switch finished while the source credential lock was held: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	assertLegacySwitchHeld(t, s, dir, active, backup, backup)
	if err := os.Remove(lockDir); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("switch did not resume after the source credential lock was released")
	}
}
