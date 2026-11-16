package store

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/storenames"
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

// failingSetKeychain refuses every write, as a full or locked Keychain does.
type failingSetKeychain struct{ keychain.KeychainClient }

func (failingSetKeychain) Set(string, string, string) error {
	return errors.New("keychain write refused")
}

// A refreshed login the profile cannot store is the only live copy of its
// token family: it is kept in the slot when the token response names the
// account, else in a recovery file beside the slot backup.
func TestRefreshedLoginKeptWhenProfileWriteFails(t *testing.T) {
	const login = `{"claudeAiOauth":{"accessToken":"old-access","refreshToken":"old-refresh"}}`
	const refreshed = `{"claudeAiOauth":{"accessToken":"new-access","refreshToken":"new-refresh","expiresAt":4102444800000}}`
	for name, account := range map[string]*oauth.Identity{"named": {Email: "one@example.test", UUID: "u-1"}, "unnamed": nil} {
		t.Run(name, func(t *testing.T) {
			s, _ := claimedSession(t)
			s.Platform = platform.MacOS
			s.kc = failingSetKeychain{keychain.NewFake()}
			fc := &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
				return oauth.RefreshOutcome{Credentials: refreshed, TokenAccount: account}
			}}
			err := s.Lock.With(func() error {
				_, err := s.RefreshLegacyLogin(context.Background(), fc, "1", login)
				return err
			})
			stored, _ := s.ReadAccountCredentials("1", "one@example.test")
			profile, perr := s.LegacyProfileOf("1")
			if perr != nil {
				t.Fatal(perr)
			}
			recovery := filepath.Join(s.CredentialsDir, storenames.CredsFile("1", "one@example.test")+".recovered")
			if account != nil {
				if err != nil || stored != refreshed || profile.Credential != "" || profile.Claimed {
					t.Fatalf("named: err %v, stored %q, profile %+v; want the refreshed login in the slot and the profile retired", err, stored, profile)
				}
				return
			}
			info, serr := os.Stat(recovery)
			// Windows reports no owner-only mode bits.
			if serr != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatalf("no 0600 recovery file %s: %v", recovery, serr)
			}
			raw, _ := os.ReadFile(recovery)
			if decoded, _ := base64.StdEncoding.DecodeString(string(raw)); string(decoded) != refreshed {
				t.Fatal("the recovery file does not hold the refreshed login")
			}
			if err == nil || stored != "" || profile.Credential != login || !profile.Claimed {
				t.Fatalf("unnamed: err %v, stored %q, profile %+v; want the slot untouched and the profile held", err, stored, profile)
			}
			registry, _ := groups.LoadRegistry(s.BackupDir())
			if registry.Held["1"].Digest != releaseDigest(login) {
				t.Fatal("the spent profile login is not held, so a later round would refresh it again")
			}
			if !strings.Contains(err.Error(), recovery) {
				t.Fatalf("the error for the warning does not name the recovery file: %v", err)
			}
		})
	}
}
