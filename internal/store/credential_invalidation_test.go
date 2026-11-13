package store

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

func claimedSession(t *testing.T) (*Store, string) {
	t.Helper()
	s := freshStore(t)
	writeSequenceRaw(t, s, `{"activeAccountNumber":null,"sequence":[1],"accounts":{"1":{"email":"one@example.test","organizationUuid":""}}}`)
	dir := s.SessionDir("1", "one@example.test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".claude.json":      `{"oauthAccount":{"emailAddress":"one@example.test","organizationUuid":""}}`,
		".credentials.json": `{"claudeAiOauth":{"accessToken":"old-access","refreshToken":"old-refresh"}}`,
		"history.jsonl":     "kept history\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Lock.With(func() error { return s.ClaimLegacyProfile("1", "one@example.test", dir) }); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestNewLoginReleasesInvalidatedSessionClaim(t *testing.T) {
	for _, liveDefault := range []bool{false, true} {
		t.Run(strconv.FormatBool(liveDefault), func(t *testing.T) {
			s, dir := claimedSession(t)
			const renewed = `{"claudeAiOauth":{"accessToken":"new-access","refreshToken":"new-refresh"}}`
			if liveDefault {
				writeGlobalConfig(t, s, `{"oauthAccount":{"emailAddress":"one@example.test","organizationUuid":""}}`)
				if err := s.Creds.WriteActive(renewed); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Lock.With(func() error { return s.WriteAccountCredentials("1", "one@example.test", renewed) }); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); !os.IsNotExist(err) {
				t.Fatalf("idle credentials survived invalidation: %v", err)
			}
			owner, err := s.CredentialOwner("1")
			wantScope := ""
			if liveDefault {
				wantScope = "default"
			}
			if err != nil || owner.Uncertain || owner.Scope != wantScope {
				t.Fatalf("new login is blocked by the invalidated session: owner=%+v, err=%v", owner, err)
			}
			if got, err := s.ReadOwnedCredentials("1", "one@example.test"); err != nil || got != renewed {
				t.Fatalf("renewed credentials are unreadable: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(dir, "history.jsonl")); err != nil || string(got) != "kept history\n" {
				t.Fatal("session history was lost")
			}
		})
	}
}

func TestBackupWriteKeepsLiveOrUnknownSessionClaim(t *testing.T) {
	for _, tc := range []struct{ name, registry, file, body string }{
		{"cli", "sessions", "self.json", `{"pid":` + strconv.Itoa(os.Getpid()) + `}`},
		{"ide", "ide", "1234.lock", `{"pid":` + strconv.Itoa(os.Getpid()) + `}`},
		{"unknown", "sessions", "partial.json", `{"pid":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := claimedSession(t)
			registry := filepath.Join(dir, tc.registry)
			if err := os.MkdirAll(registry, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(registry, tc.file), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
			if err != nil {
				t.Fatal(err)
			}
			_ = s.Lock.With(func() error { return s.WriteAccountCredentials("1", "one@example.test", "renewed") })
			after, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
			if err != nil || string(after) != string(before) {
				t.Fatal("a live or unverifiable session lost its credentials")
			}
			claims, err := groups.LoadRegistry(s.BackupDir())
			if err != nil || claims.Claims["1"].Scope != "legacy:1" {
				t.Fatalf("a live or unverifiable session lost its claim: %v", err)
			}
			if !sessprofile.IsStale(dir) {
				t.Fatal("session was not marked for deferred invalidation")
			}
		})
	}
}
