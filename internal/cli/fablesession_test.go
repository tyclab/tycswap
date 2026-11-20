// Tests for the Fable store's variable in tycswap's own processes (DESIGN A100).
package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/testutil"
)

const secureStorageVar = "CLAUDE_SECURESTORAGE_CONFIG_DIR"

func resetFableSessionProcess(t *testing.T) {
	t.Helper()
	prev := fableSessionProcess
	fableSessionProcess = false
	t.Cleanup(func() { fableSessionProcess = prev })
}

func TestRunDropsTheFableStoreVariable(t *testing.T) {
	for name, value := range map[string]func(string) string{
		"exact":          func(dir string) string { return dir },
		"trailing slash": func(dir string) string { return dir + string(filepath.Separator) },
		"not clean":      func(dir string) string { return filepath.Join(dir, "..", "store") },
	} {
		t.Run(name, func(t *testing.T) {
			cleanHome(t)
			resetFableSessionProcess(t)
			home, _ := os.UserHomeDir()
			testutil.Setenv(t, secureStorageVar, value(paths.FableStoreDir(paths.GetBackupRoot())))
			if code, _, errb := runSub(t, "--version"); code != 0 {
				t.Fatalf("exit %d, stderr %q", code, errb)
			}
			if v, ok := os.LookupEnv(secureStorageVar); ok {
				t.Fatalf("%s is still %q", secureStorageVar, v)
			}
			if got, want := cclock.CredentialsLockDir(), filepath.Join(home, ".claude.lock"); got != want {
				t.Fatalf("CredentialsLockDir = %s, want %s", got, want)
			}
			if !fableSessionProcess {
				t.Fatal("fableSessionProcess is false")
			}
		})
	}
}

// start inside a Fable-store session selects the default login under the
// default store's Claude Code locks, while the child keeps its environment.
func TestStartDropsTheFableStoreVariableBeforeItSelects(t *testing.T) {
	for _, args := range [][]string{
		{"start", "--", "command", "--model", "opus"},
		{"start", "--account", "2", "--", "command", "--model", "opus"},
		{"start", "--account", "2", "--", "command", "--model", "fable"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			sw, rec := fableStart(t, "on")
			sw.Store.OAuth = &oauth.FakeClient{}
			// Account 2's plan offers Fable: --account makes it the Fable store's start login too.
			setRecord(t, sw, "2", "subscriptionType", "max")
			testutil.Setenv(t, secureStorageVar, fableStoreDir())
			home, _ := os.UserHomeDir()
			var cred, storage string
			prev := startSelect
			startSelect = func(account, family string, stderr io.Writer) string {
				cred, storage = cclock.CredentialsLockDir(), cclock.StorageWriteLockDir()
				return prev(account, family, stderr)
			}
			t.Cleanup(func() { startSelect = prev })
			if code, _, errb := runCLI(t, args, false, false); code != 23 || rec.execs != 1 {
				t.Fatalf("exit %d, execs %d, stderr %q", code, rec.execs, errb)
			}
			fable := args[len(args)-1] == "fable"
			if fable {
				if cred != "" || storage != "" {
					t.Fatal("dedicated start unexpectedly selected the default login")
				}
			} else {
				if want := filepath.Join(home, ".claude.lock"); cred != want {
					t.Errorf("credential lock %s, want %s", cred, want)
				}
				if want := filepath.Join(home, ".claude", ".storage-write.lock"); storage != want {
					t.Errorf("storage lock %s, want %s", storage, want)
				}
			}
			if got := envValue(rec.env, secureStorageVar); (got == fableStoreDir()) != fable {
				t.Errorf("child %s = %q, Fable session %v", secureStorageVar, got, fable)
			}
		})
	}
}

func TestRunKeepsAnotherSecureStorageDir(t *testing.T) {
	for name, value := range map[string]func(string) string{
		"user's own":   func(string) string { return filepath.Join(t.TempDir(), "mine") },
		"empty":        func(string) string { return "" },
		"fable parent": func(dir string) string { return filepath.Dir(dir) },
	} {
		t.Run(name, func(t *testing.T) {
			cleanHome(t)
			resetFableSessionProcess(t)
			want := value(paths.FableStoreDir(paths.GetBackupRoot()))
			testutil.Setenv(t, secureStorageVar, want)
			lock := cclock.CredentialsLockDir()
			if code, _, errb := runSub(t, "--version"); code != 0 {
				t.Fatalf("exit %d, stderr %q", code, errb)
			}
			if v, ok := os.LookupEnv(secureStorageVar); !ok || v != want {
				t.Fatalf("%s = %q (%v), want %q", secureStorageVar, v, ok, want)
			}
			if cclock.CredentialsLockDir() != lock || fableSessionProcess {
				t.Fatalf("lock %s, flag %v", cclock.CredentialsLockDir(), fableSessionProcess)
			}
		})
	}
}

// One helper says whether a value names the Fable store, for cli.run and for
// start alike; on Windows it compares without case (DESIGN A100, A103).
func TestNamesFableStoreOnEachPlatform(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fable", "store")
	upper := strings.ToUpper(dir)
	for _, tc := range []struct {
		value   string
		windows bool
		want    bool
	}{
		{dir, false, true},
		{dir + string(filepath.Separator), false, true},
		{filepath.Join(dir, "..", "store"), false, true},
		{upper, false, upper == dir},
		{upper, true, true},
		{filepath.Dir(dir), true, false},
		{"", true, false},
	} {
		if got := namesFableStore(tc.value, dir, tc.windows); got != tc.want {
			t.Errorf("namesFableStore(%q, windows=%v) = %v, want %v", tc.value, tc.windows, got, tc.want)
		}
	}
}

func TestRunDropsTheFableStoreVariableWithoutCaseOnWindows(t *testing.T) {
	cleanHome(t)
	resetFableSessionProcess(t)
	prev := startWindows
	startWindows = func() bool { return true }
	t.Cleanup(func() { startWindows = prev })
	testutil.Setenv(t, secureStorageVar, strings.ToUpper(paths.FableStoreDir(paths.GetBackupRoot())))
	if code, _, errb := runSub(t, "--version"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if v, ok := os.LookupEnv(secureStorageVar); ok || !fableSessionProcess {
		t.Fatalf("%s is still %q (flag %v)", secureStorageVar, v, fableSessionProcess)
	}
}
