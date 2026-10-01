package session

import (
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/platform"
)

// TestBootstrapNeverRefreshesTheLiveLineage: a backup that shares its refresh
// token with the live default login (the slot is the current account, or its
// fingerprint matches the live credential) is seeded as stored, never
// refreshed; refreshing it would log the default login out.
func TestBootstrapNeverRefreshesTheLiveLineage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current string
		live    string
		want    int
	}{
		{"slot is the current account", "2", "", 0},
		{"live credential has the same refresh token", "", `{"claudeAiOauth":{"refreshToken":"rt-1","accessToken":"other"}}`, 0},
		{"unrelated live login", "1", `{"claudeAiOauth":{"refreshToken":"rt-other","accessToken":"x"}}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backup := t.TempDir()
			accts := newFakeAccounts(backup, platform.Linux)
			accts.add("2", "user@example.com", "org-1", oauthCreds)
			if tc.current != "" {
				c := tc.current
				accts.current = &c
			}
			accts.live = tc.live
			rr := &refreshRecorder{outcome: refreshSuccess(rotatedCreds)}
			m, _ := newSetupManager(t, accts, rr, false)
			if _, _, _, err := m.SetupSession("2", false, false); err != nil {
				t.Fatalf("SetupSession: %v", err)
			}
			if rr.called != tc.want {
				t.Fatalf("refresh called %d times, want %d", rr.called, tc.want)
			}
			if tc.want == 0 {
				if len(accts.written) != 0 {
					t.Errorf("backup rewritten: %+v", accts.written)
				}
				dir := sessionDirFor(t, backup, "2", "user@example.com")
				if got := readFileString(t, filepath.Join(dir, ".credentials.json")); got != oauthCreds {
					t.Errorf("profile seeded with %q, want the stored credential", got)
				}
			}
		})
	}
}
