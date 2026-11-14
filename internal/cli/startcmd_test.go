package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/session"
	"github.com/tyclab/tycswap/internal/store"
)

func startTestLaunch(t *testing.T) *int {
	t.Helper()
	cleanHome(t)
	for _, key := range append([]string{"CLAUDE_CONFIG_DIR"}, session.AuthOverrideEnvVars...) {
		value, exists := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
	previousExec, previousResolve, previousDefault := startExec, startResolve, startResolveDefault
	t.Cleanup(func() { startExec, startResolve, startResolveDefault = previousExec, previousResolve, previousDefault })
	calls := new(int)
	startExec = func(string, []string, []string) (int, error) { *calls++; return 23, nil }
	startResolve = func(string) (string, error) { return "/verified/command", nil }
	startResolveDefault = func() (string, error) { return "/verified/claude", nil }
	return calls
}

func TestStartPassesArgvAndEnvThroughFrontController(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "pinned"}[pinned], func(t *testing.T) {
			calls := startTestLaunch(t)
			if pinned {
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(paths.GetBackupRoot(), "sessions", "1-test"))
			}
			t.Setenv("START_TEST_VALUE", "spaces = unicode λ\nretained")
			before := os.Environ()
			want := []string{"custom", "--account", "child-option", "", "space argument", "--", "-x"}
			startExec = func(bin string, args, env []string) (int, error) {
				*calls++
				if bin != "/verified/command" || !reflect.DeepEqual(args, want) || !reflect.DeepEqual(env, before) {
					t.Fatalf("bin=%s args=%q env preserved=%v", bin, args, reflect.DeepEqual(env, before))
				}
				return 23, nil
			}
			code, out, stderr := runCLI(t, append([]string{"start", "--"}, want...), false, false)
			if code != 23 || *calls != 1 || out != "" || stderr != "" {
				t.Fatalf("code=%d calls=%d out=%q stderr=%q", code, *calls, out, stderr)
			}
		})
	}
}

func TestStartOverridesDoNotOpenStore(t *testing.T) {
	keys := append([]string{"CLAUDE_CONFIG_DIR"}, session.AuthOverrideEnvVars...)
	keys = append(keys, "--bare")
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			calls := startTestLaunch(t)
			previous := newSwitcher
			newSwitcher = func(store.Options) (*core.Switcher, error) {
				t.Fatal("override accessed the account store")
				return nil, nil
			}
			t.Cleanup(func() { newSwitcher = previous })
			argv := []string{"start", "--account", "2", "--", "command"}
			if key == "--bare" {
				argv = append(argv, "--bare")
			} else {
				t.Setenv(key, "")
			}
			code, out, stderr := runCLI(t, argv, false, false)
			if code != 23 || *calls != 1 || out != "" || !strings.Contains(stderr, "Skipping the requested") {
				t.Fatalf("code=%d calls=%d out=%q stderr=%q", code, *calls, out, stderr)
			}
		})
	}
}

func TestStartUsageErrorsRunNothing(t *testing.T) {
	calls := startTestLaunch(t)
	for _, args := range [][]string{{"--account"}, {"--allow", "1", "--list"}, {"--allow", "1", "command"}, {"--list", "--"}, {"--account", "1", "--disallow", "2"}, {"--unknown"}} {
		code, _, _ := runCLI(t, append([]string{"start"}, args...), false, false)
		if code != 2 || *calls != 0 {
			t.Fatalf("args=%q code=%d calls=%d", args, code, *calls)
		}
	}
}

func TestStartResolveFailureDoesNotSelect(t *testing.T) {
	calls := startTestLaunch(t)
	previous := newSwitcher
	newSwitcher = func(store.Options) (*core.Switcher, error) {
		t.Fatal("missing executable selected an account")
		return nil, nil
	}
	t.Cleanup(func() { newSwitcher = previous })
	startResolve = func(string) (string, error) { return "", exec.ErrNotFound }
	code, out, stderr := runCLI(t, []string{"start", "missing-command"}, false, false)
	if code != 127 || *calls != 0 || out != "" || strings.Count(stderr, "tycswap start:") != 1 {
		t.Fatalf("code=%d calls=%d out=%q stderr=%q", code, *calls, out, stderr)
	}
}

func TestStartFallsBackAndStillRuns(t *testing.T) {
	for _, failure := range []string{"bad-root", "unknown-account", "corrupt-roster", "corrupt-settings", "constructor"} {
		t.Run(failure, func(t *testing.T) {
			calls := startTestLaunch(t)
			argv := []string{"start", "command"}
			root := paths.GetBackupRoot()
			if failure == "bad-root" {
				if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if failure == "unknown-account" {
				argv = []string{"start", "--account", "unknown", "command"}
			} else if failure == "constructor" {
				previous := newSwitcher
				newSwitcher = func(store.Options) (*core.Switcher, error) { return nil, errors.New("unavailable") }
				t.Cleanup(func() { newSwitcher = previous })
			} else {
				if err := os.MkdirAll(root, 0o700); err != nil {
					t.Fatal(err)
				}
				name := "sequence.json"
				if failure == "corrupt-settings" {
					name = "settings.json"
				}
				if err := os.WriteFile(filepath.Join(root, name), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code, out, stderr := runCLI(t, argv, false, false)
			if code != 23 || *calls != 1 || out != "" || strings.Count(stderr, "tycswap start:") != 1 {
				t.Fatalf("code=%d calls=%d out=%q stderr=%q", code, *calls, out, stderr)
			}
		})
	}
}

func TestStartNoMarksLeavesNoStore(t *testing.T) {
	calls := startTestLaunch(t)
	code, out, stderr := runCLI(t, []string{"start", "command"}, false, false)
	if code != 23 || *calls != 1 || out != "" || stderr != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(paths.GetBackupRoot()); !os.IsNotExist(err) {
		t.Fatalf("no-op created store: %v", err)
	}
}

func TestStartExecFailureNeverRetries(t *testing.T) {
	calls := startTestLaunch(t)
	startExec = func(string, []string, []string) (int, error) { *calls++; return 127, os.ErrNotExist }
	code, _, stderr := runCLI(t, []string{"start", "command"}, false, false)
	if code != 127 || *calls != 1 || strings.Count(stderr, "tycswap start:") != 1 {
		t.Fatalf("code=%d calls=%d stderr=%q", code, *calls, stderr)
	}
}

func TestStartCurrentOwnershipMustBeCached(t *testing.T) {
	startTestLaunch(t)
	sw, err := core.New(store.Options{SkipMigrations: true, Keychain: keychain.NewFake(), OAuth: &oauth.FakeClient{
		ProfileFn: func(context.Context, string) *oauth.Identity {
			t.Fatal("cache-only selection requested a profile")
			return nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sw.BackupDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	roster := `{"sequence":[1],"accounts":{"1":{"email":"marked@example.com","organizationUuid":"org-one","startLogin":true}}}`
	if err := os.WriteFile(sw.SequenceFile, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sw.BackupDir(), "settings.json"), []byte(`{"autoswitch":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sw.GlobalConfigPath(), []byte(`{"oauthAccount":{"emailAddress":"marked@example.com","organizationUuid":"org-one"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sw.WriteAccountCredentials("1", "marked@example.com", `{"claudeAiOauth":{"accessToken":"fake-backup","refreshToken":"fake-backup-refresh","expiresAt":4102444800000}}`); err != nil {
		t.Fatal(err)
	}
	if err := sw.Creds.WriteActive(`{"claudeAiOauth":{"accessToken":"fake-rotated","refreshToken":"fake-rotated-refresh","expiresAt":4102444800000}}`); err != nil {
		t.Fatal(err)
	}
	_, err = readStartSnapshot(sw, "")
	if err == nil || !strings.Contains(err.Error(), "could not be verified") {
		t.Fatalf("ownership uncertainty = %v", err)
	}
	if _, err := os.Stat(filepath.Join(sw.BackupDir(), ".autoswitch_state.lock")); !os.IsNotExist(err) {
		t.Fatalf("uncertain no-op took a state lock: %v", err)
	}
}
