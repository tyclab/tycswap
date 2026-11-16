package store

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/sessprofile"
)

// A drop needs its proof under the locks: anything else keeps login and claim.
func TestDiscardLegacyLoginRefusals(t *testing.T) {
	const login = `{"claudeAiOauth":{"accessToken":"old-access","refreshToken":"old-refresh"}}`
	for _, tc := range []struct {
		name     string
		setup    func(t *testing.T, dir string)
		expected string
		drops    bool
	}{
		{"neither live nor stale", func(*testing.T, string) {}, login, false},
		{"stale", func(_ *testing.T, dir string) { sessprofile.MarkStale(dir) }, login, true},
		{"changed meanwhile", func(_ *testing.T, dir string) { sessprofile.MarkStale(dir) }, "another login", false},
		{"other identity", func(t *testing.T, dir string) {
			sessprofile.MarkStale(dir)
			if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"other@example.test","organizationUuid":""}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, login, false},
		{"running", func(t *testing.T, dir string) {
			sessprofile.MarkStale(dir)
			if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sessions", "live.json"), []byte(`{"pid":`+strconv.Itoa(os.Getpid())+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, login, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := claimedSession(t)
			tc.setup(t, dir)
			err := s.Lock.With(func() error { return s.DiscardLegacyLogin("1", tc.expected) })
			if (err == nil) != tc.drops {
				t.Fatalf("drop error = %v, want a drop %v", err, tc.drops)
			}
			profile, perr := s.LegacyProfileOf("1")
			if perr != nil {
				t.Fatal(perr)
			}
			if tc.drops != (!profile.Claimed && profile.Credential == "" && !profile.Stale) {
				t.Fatalf("after the drop: %+v", profile)
			}
			if !tc.drops && profile.Credential != login {
				t.Fatal("a refused drop changed the profile's login")
			}
		})
	}
}

// SES-6: a profile's session record whose PID now names a process with
// another start time does not hold the profile.
func TestReusedPIDDoesNotHoldProfile(t *testing.T) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skip("no /proc on this system")
	}
	stat := string(raw)
	start := strings.Fields(stat[strings.LastIndexByte(stat, ')')+1:])[19]
	pid := strconv.Itoa(os.Getpid())
	for procStart, want := range map[string]int{"1": 0, start: 1} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sessions", pid+".json"), []byte(`{"pid":`+pid+`,"procStart":"`+procStart+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if pids, err := LiveProfilePIDs(dir); err != nil || len(pids) != want {
			t.Fatalf("procStart %s: live PIDs %v, %v; want %d", procStart, pids, err, want)
		}
	}
}
