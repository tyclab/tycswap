// Tests for the store tycswap start gives a session (DESIGN A103).
package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/launch"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
)

type startRecord struct {
	args, env  []string
	execs      int
	selections []string
}

// fableStart prepares a start run over the fixture store with the Fable store
// in state: "on" (alice's Fable login live), "unknown" (a live login no Fable
// login or chain record names), "off" (empty), "unusable" (alice's live login
// quarantined as dead) or "none" (no Fable login). The profile endpoint fails
// the test.
func fableStart(t *testing.T, state string) (*core.Switcher, *startRecord) {
	t.Helper()
	startTestLaunch(t)
	resetFableSessionProcess(t)
	var sw *core.Switcher
	if state == "none" {
		sw = fixtureSwitcher(t)
		useSwitcher(t, sw)
	} else {
		sw = fableCLISwitcher(t)
	}
	live := filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json")
	sw.Store.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity {
		t.Fatal("start asked the profile endpoint")
		return nil
	}}
	switch state {
	case "unknown":
		if err := os.WriteFile(live, []byte(fableLogin("elsewhere")), 0o600); err != nil {
			t.Fatal(err)
		}
	case "off":
		if err := os.Remove(live); err != nil {
			t.Fatal(err)
		}
	case "unusable":
		f, err := sw.Store.ForFable()
		if err != nil {
			t.Fatal(err)
		}
		fp := *oauth.CredentialFingerprint(fableLogin("f1"))
		dead := `{"quarantine":{"1":{"email":"alice@example.com","reason":"invalid_grant","at":"t","refreshTokenFingerprint":"` + fp + `"}}}`
		if err := os.WriteFile(f.StatePath(), []byte(dead), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(t.TempDir())
	rec := &startRecord{}
	startExec = func(_ string, args, env []string) (int, error) {
		rec.execs++
		rec.args, rec.env = args, env
		return 23, nil
	}
	prev := startSelect
	startSelect = func(account string, stderr io.Writer) string {
		rec.selections = append(rec.selections, account)
		return prev(account, stderr)
	}
	t.Cleanup(func() { startSelect = prev })
	return sw, rec
}

func fableStoreDir() string { return filepath.Clean(paths.FableStoreDir(paths.GetBackupRoot())) }

// withoutVar is env without key's entries.
func withoutVar(env []string, key string) []string {
	out := []string{}
	for _, entry := range env {
		if k, _, _ := strings.Cut(entry, "="); k != key {
			out = append(out, entry)
		}
	}
	return out
}

// setVar is env with the entry named old replaced in place by key=value, or
// key=value appended when there is none.
func setVar(env []string, old, key, value string) []string {
	out := append([]string(nil), env...)
	for i, entry := range out {
		if strings.HasPrefix(entry, old+"=") {
			out[i] = key + "=" + value
			return out
		}
	}
	return append(out, key+"="+value)
}

func TestStartPicksTheStoreBySessionModel(t *testing.T) {
	const noUsable = "tycswap start: the Fable store has no usable login (%s); starting on the default login.\n"
	for _, tc := range []struct {
		name, state      string
		args             []string
		inherit          bool
		wantVar, selects bool
		notice           string
	}{
		{name: "fable, on", state: "on", args: []string{"--model", "fable"}, wantVar: true},
		{name: "fable, on, inherited value kept", state: "on", args: []string{"--model", "fable"}, inherit: true, wantVar: true},
		{name: "fable, live slot unknown counts as usable", state: "unknown", args: []string{"--model", "fable"}, wantVar: true},
		{name: "fable, off", state: "off", args: []string{"--model", "fable"}, selects: true,
			notice: strings.Replace(noUsable, "%s", "the store is empty", 1)},
		{name: "fable, off, inherited value removed", state: "off", args: []string{"--model=claude-fable-5-1[1m]"}, inherit: true, selects: true,
			notice: strings.Replace(noUsable, "%s", "the store is empty", 1)},
		{name: "fable, unusable", state: "unusable", args: []string{"--model", "fable"}, inherit: true, selects: true,
			notice: strings.Replace(noUsable, "%s", "Account-1's Fable login: sign in again", 1)},
		{name: "opus, on", state: "on", args: []string{"--model", "opus"}, selects: true},
		{name: "opus, on, inherited value removed", state: "on", args: []string{"--model", "opus"}, inherit: true, selects: true},
		{name: "no family, on, inherited value removed", state: "on", inherit: true, selects: true},
		{name: "fable, no Fable logins", state: "none", args: []string{"--model", "fable"}, selects: true},
		{name: "fable, no Fable logins, inherited value removed", state: "none", args: []string{"--model", "fable"}, inherit: true, selects: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, rec := fableStart(t, tc.state)
			storeDir := fableStoreDir()
			if tc.inherit {
				t.Setenv(secureStorageVar, storeDir+string(filepath.Separator))
			}
			before := os.Environ()
			liveBefore, _ := os.ReadFile(filepath.Join(storeDir, ".credentials.json"))
			code, out, stderr := runCLI(t, append([]string{"start", "--", "command"}, tc.args...), false, false)
			if code != 23 || rec.execs != 1 || out != "" || stderr != tc.notice {
				t.Fatalf("code=%d execs=%d out=%q stderr=%q, want notice %q", code, rec.execs, out, stderr, tc.notice)
			}
			if want := append([]string{"command"}, tc.args...); !reflect.DeepEqual(rec.args, want) {
				t.Fatalf("args = %q, want %q", rec.args, want)
			}
			want := withoutVar(before, secureStorageVar)
			if tc.wantVar {
				want = setVar(before, secureStorageVar, secureStorageVar, storeDir)
			}
			if !reflect.DeepEqual(rec.env, want) {
				t.Fatalf("child environment differs:\n got %q\nwant %q", rec.env, want)
			}
			if wantSel := map[bool]int{true: 1, false: 0}[tc.selects]; len(rec.selections) != wantSel || wantSel == 1 && rec.selections[0] != "" {
				t.Fatalf("selections = %q, want %d", rec.selections, wantSel)
			}
			if after, _ := os.ReadFile(filepath.Join(storeDir, ".credentials.json")); string(after) != string(liveBefore) {
				t.Fatal("start changed the Fable store")
			}
			if tc.state == "none" {
				for _, dir := range []string{paths.FableScopeDir(sw.BackupDir()), paths.FableCredentialsDir(sw.BackupDir())} {
					if _, err := os.Stat(dir); !os.IsNotExist(err) {
						t.Fatalf("start created %s: %v", dir, err)
					}
				}
			}
		})
	}
}

func TestStartWithoutFableLoginsPassesTheEnvironmentUnchanged(t *testing.T) {
	for name, args := range map[string][]string{"fable": {"--model", "fable"}, "opus": {"--model", "opus"}, "none": nil} {
		t.Run(name, func(t *testing.T) {
			_, rec := fableStart(t, "none")
			t.Setenv("ANTHROPIC_MODEL", "fable")
			before := os.Environ()
			code, _, stderr := runCLI(t, append([]string{"start", "command"}, args...), false, false)
			if code != 23 || stderr != "" || !reflect.DeepEqual(rec.env, before) {
				t.Fatalf("code=%d stderr=%q env preserved=%v", code, stderr, reflect.DeepEqual(rec.env, before))
			}
		})
	}
}

func TestStartOverridesKeepAFableSessionUnchanged(t *testing.T) {
	cases := map[string]func(t *testing.T) []string{
		"--bare": func(*testing.T) []string { return []string{"--bare"} },
		"foreign " + secureStorageVar: func(t *testing.T) []string {
			t.Setenv(secureStorageVar, filepath.Join(t.TempDir(), "mine"))
			return nil
		},
		"empty " + secureStorageVar: func(t *testing.T) []string {
			t.Setenv(secureStorageVar, "")
			return nil
		},
		"CLAUDE_CONFIG_DIR beside the Fable store's value": func(t *testing.T) []string {
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			t.Setenv(secureStorageVar, fableStoreDir())
			return nil
		},
	}
	for _, key := range append([]string{"CLAUDE_CONFIG_DIR"}, launch.AuthOverrideEnvVars...) {
		if key != secureStorageVar {
			cases[key] = func(t *testing.T) []string { t.Setenv(key, "x"); return nil }
		}
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			_, rec := fableStart(t, "on")
			previous := newSwitcher
			newSwitcher = func(store.Options) (*core.Switcher, error) {
				t.Fatal("an override opened the account store")
				return nil, nil
			}
			t.Cleanup(func() { newSwitcher = previous })
			args := append([]string{"--model", "fable"}, set(t)...)
			before := os.Environ()
			code, _, stderr := runCLI(t, append([]string{"start", "--account", "2", "--", "command"}, args...), false, false)
			if code != 23 || !reflect.DeepEqual(rec.env, before) || len(rec.selections) != 0 || !strings.Contains(stderr, "Skipping the requested") {
				t.Fatalf("code=%d env preserved=%v selections=%q stderr=%q", code, reflect.DeepEqual(rec.env, before), rec.selections, stderr)
			}
		})
	}
}

func TestStartAccountWithAFableSession(t *testing.T) {
	for state, suffix := range map[string]string{"on": " (Account-1)", "unknown": ""} {
		t.Run(state, func(t *testing.T) {
			sw, rec := fableStart(t, state)
			sw.Store.OAuth = &oauth.FakeClient{}
			storeDir := fableStoreDir()
			liveBefore, err := os.ReadFile(filepath.Join(storeDir, ".credentials.json"))
			if err != nil {
				t.Fatal(err)
			}
			chainBefore, _ := os.ReadFile(filepath.Join(paths.FableScopeDir(paths.GetBackupRoot()), "live_lineage.json"))
			code, _, stderr := runCLI(t, []string{"start", "--account", "2", "--", "command", "--model", "fable"}, false, false)
			notice := "tycswap start: --account selects the default login; this Fable session starts on the Fable store" + suffix + ".\n"
			if code != 23 || strings.Count(stderr, notice) != 1 || strings.Count(stderr, "Fable") != 2 {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			if !reflect.DeepEqual(rec.selections, []string{"2"}) {
				t.Fatalf("selections = %q, want the default login's selection of account 2", rec.selections)
			}
			if got := envValue(rec.env, secureStorageVar); got != storeDir {
				t.Fatalf("%s = %q, want %q", secureStorageVar, got, storeDir)
			}
			if after, _ := os.ReadFile(filepath.Join(storeDir, ".credentials.json")); string(after) != string(liveBefore) {
				t.Fatal("--account moved the Fable store")
			}
			if after, _ := os.ReadFile(filepath.Join(paths.FableScopeDir(paths.GetBackupRoot()), "live_lineage.json")); string(after) != string(chainBefore) {
				t.Fatal("--account rewrote the Fable store's chain record")
			}
		})
	}
}

func TestStartOnWindowsMatchesTheVariableCaseInsensitively(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		env     map[string]string
		wantVar bool
	}{
		{name: "fable session", args: []string{"--model", "fable"}, wantVar: true},
		{name: "fable by anthropic_model", env: map[string]string{"anthropic_model": "fable"}, wantVar: true},
		{name: "opus session", args: []string{"--model", "opus"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, rec := fableStart(t, "on")
			prev := startWindows
			startWindows = func() bool { return true }
			t.Cleanup(func() { startWindows = prev })
			const key = "claude_SecureStorage_config_dir"
			t.Setenv(key, strings.ToUpper(fableStoreDir()))
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			before := os.Environ()
			code, _, stderr := runCLI(t, append([]string{"start", "command"}, tc.args...), false, false)
			if code != 23 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			want := withoutVar(before, key)
			if tc.wantVar {
				want = setVar(before, key, secureStorageVar, fableStoreDir())
			}
			if !reflect.DeepEqual(rec.env, want) {
				t.Fatalf("child environment differs:\n got %q\nwant %q", rec.env, want)
			}
			if wantSel := map[bool]int{true: 0, false: 1}[tc.wantVar]; len(rec.selections) != wantSel {
				t.Fatalf("selections = %q", rec.selections)
			}
		})
	}
}

func TestStartOnMacOSLeavesAFableSessionAsBefore(t *testing.T) {
	sw, rec := fableStart(t, "on")
	sw.Store.Platform = platform.MacOS
	before := os.Environ()
	code, _, stderr := runCLI(t, []string{"start", "command", "--model", "fable"}, false, false)
	if code != 23 || stderr != "" || !reflect.DeepEqual(rec.env, before) || len(rec.selections) != 1 {
		t.Fatalf("code=%d stderr=%q env preserved=%v selections=%q", code, stderr, reflect.DeepEqual(rec.env, before), rec.selections)
	}
}

func TestStartHelpNamesTheFableStore(t *testing.T) {
	cleanHome(t)
	code, out, _ := runCLI(t, []string{"start", "--help"}, false, false)
	if code != 0 || !strings.Contains(out, "\nSessions on a Fable model start on the Fable store when one is on (tycswap add --login --lineage fable).\n") {
		t.Fatalf("code=%d help=%q", code, out)
	}
}

// The help says what passes unchanged: the arguments, and the environment but
// for the Fable store's variable (DESIGN A103) and the start hold's (A105).
func TestStartHelpNamesTheVariablesItChanges(t *testing.T) {
	cleanHome(t)
	code, out, _ := runCLI(t, []string{"start", "--help"}, false, false)
	for _, want := range []string{
		"\nSelection failures launch on the current login. Arguments pass unchanged.\n",
		"\nThe environment passes unchanged, except CLAUDE_SECURESTORAGE_CONFIG_DIR: start sets it to the Fable store\n" +
			"for a Fable session while that store is usable, and removes it from any other session when it names that store;\n" +
			"and TYCSWAP_START_HOLD: start sets it for a session it holds a login at its limit for, and removes it from any other.\n",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "environment pass unchanged") {
		t.Errorf("help still says the environment passes unchanged:\n%s", out)
	}
}
