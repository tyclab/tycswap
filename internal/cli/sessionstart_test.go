package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
)

func assertNativeStore(t *testing.T, env []string, want string) string {
	t.Helper()
	dir := envValue(env, secureStorageEnv)
	if runtime.GOOS == "linux" {
		if filepath.Dir(dir) != filepath.Join(paths.GetBackupRoot(), "links") {
			t.Fatalf("expected a session link under the fixture root, got %q", dir)
		}
		got, err := os.Readlink(dir)
		if want == "" {
			want = paths.GetClaudeConfigHome()
		}
		if err != nil || got != want {
			t.Fatalf("session link points to %q, want %q: %v", got, want, err)
		}
	} else if dir != want {
		t.Fatalf("secure storage = %q, want %q", dir, want)
	}
	return dir
}

func linkedTestStart(t *testing.T, rec *startRecord, id, model string) string {
	t.Helper()
	startResolve = func(string) (string, error) { return "/fixture/claude", nil }
	if code, _, msg := runCLI(t, []string{"start", "--", "claude", "--model", model}, false, false); code != 23 {
		t.Fatalf("native launch: %d %s", code, msg)
	}
	path := envValue(rec.env, secureStorageEnv)
	if filepath.Dir(path) != filepath.Join(paths.GetBackupRoot(), "links") {
		t.Fatalf("native launch supplied %q, expected an individual link", path)
	}
	for _, key := range []string{secureStorageEnv, startHoldEnv, startFamilyEnv, recoveredFableEnv} {
		t.Setenv(key, envValue(rec.env, key))
	}
	input := fmt.Sprintf(`{"source":"startup","session_id":%q,"model":%q}`, id, model)
	if code, out, msg := runCodex(t, input, "start", "--started"); code != 0 || out != "" || msg != "" {
		t.Fatalf("SessionStart hook: %d %q %q", code, out, msg)
	}
	return path
}

func TestNativeSessionLinkLaunchHookNestedStartAndResume(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux session links")
	}
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	const id = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	first := linkedTestStart(t, rec, id, "fable")
	assertNativeStore(t, rec.env, fableStoreDir())
	if family, scope, ok := reporting.RecordedSession(sw.BackupDir(), id); !ok || family != "fable" || scope != "fable" {
		t.Fatalf("SessionStart record = %q/%q, present %t", family, scope, ok)
	}
	t.Setenv(secureStorageEnv, first)
	if code, _, msg := runCLI(t, []string{"start", "--", "claude", "--model", "opus"}, false, false); code != 23 {
		t.Fatalf("nested native launch: %d %s", code, msg)
	}
	nested := assertNativeStore(t, rec.env, "")
	if nested == first {
		t.Fatal("nested launch reused its parent's link")
	}
	if got, err := os.Readlink(first); err != nil || got != fableStoreDir() {
		t.Fatalf("parent link after child launch = %q, %v", got, err)
	}
	t.Setenv(secureStorageEnv, first)
	if code, _, msg := runCLI(t, []string{"start", "--", "claude", "--bare"}, false, false); code != 23 {
		t.Fatalf("nested bare launch: %d %s", code, msg)
	}
	if got := envValue(rec.env, secureStorageEnv); got != fableStoreDir() {
		t.Fatalf("bare child should inherit the current login directly, got %q", got)
	}

	if code, _, msg := runCLI(t, []string{"start", "--", "claude", "--resume", id}, false, false); code != 23 {
		t.Fatalf("resume exit %d: %s", code, msg)
	}
	if resumed := assertNativeStore(t, rec.env, fableStoreDir()); resumed == first || resumed == nested {
		t.Fatal("resume reused a previous launch's link")
	}
	for _, path := range []string{first, nested} {
		t.Setenv(secureStorageEnv, path)
		if code, _, msg := runSub(t, "--version"); code != 0 {
			t.Fatalf("nested cli: %d %s", code, msg)
		}
		if _, set := os.LookupEnv(secureStorageEnv); set || fableSessionProcess != (path == first) || mythosSessionDir != "" {
			t.Fatal("tycswap used the session's secure storage or misidentified its store")
		}
		if got := cclock.CredentialsLockDir(); got != sw.DefaultProfileDir()+".lock" {
			t.Fatalf("nested cli credential lock = %q", got)
		}
	}
}

func TestNativeSessionLinkFailureLaunchesWithOriginalStore(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux session links")
	}
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	startResolve = func(string) (string, error) { return "/fixture/claude", nil }
	writeFile(t, filepath.Join(sw.BackupDir(), "links"), "fixture: cannot create links inside a file")
	for _, model := range []string{"fable", "opus"} {
		want := ""
		if model == "fable" {
			want = fableStoreDir()
		}
		code, _, msg := runCLI(t, []string{"start", "--", "claude", "--model", model}, false, false)
		if code != 23 || envValue(rec.env, secureStorageEnv) != want || strings.Count(msg, "\n") != 1 || !strings.Contains(strings.ToLower(msg), "link") {
			t.Fatalf("%s fallback got exit %d, dir %q, notice %q", model, code, envValue(rec.env, secureStorageEnv), msg)
		}
	}
}

func TestNativeContinueRestoresPendingLoginWhenHookIdentifiesSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux session links")
	}
	const id = "d34392e9-b782-4fa3-8018-222c1c377314"
	if os.Getenv("TYCSWAP_TEST_PENDING_FABLE") == "yes" {
		root := paths.GetBackupRoot()
		link, err := reporting.CreateSessionLink(root, paths.FableStoreDir(root))
		if err != nil {
			t.Fatal(err)
		}
		if err := reporting.BindSessionLink(root, id, link, "opus", "fable", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := reporting.UpdateSessionLink(root, id, link, time.Now(), func(session *reporting.SessionLink) error {
			session.Wanted = "fable"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	child := exec.Command(os.Args[0], "-test.run=^TestNativeContinueRestoresPendingLoginWhenHookIdentifiesSession$")
	child.Env = append(os.Environ(), "TYCSWAP_TEST_PENDING_FABLE=yes")
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("previous session: %s, %v", out, err)
	}
	startResolve = func(string) (string, error) { return "/fixture/claude", nil }
	if code, _, msg := runCLI(t, []string{"start", "--", "claude", "--continue", "--model", "opus"}, false, false); code != 23 {
		t.Fatalf("continue: %d %s", code, msg)
	}
	path := assertNativeStore(t, rec.env, "")
	t.Setenv(secureStorageEnv, path)
	if code, out, msg := runCodex(t, `{"source":"resume","session_id":"`+id+`","model":"opus"}`, "start", "--started"); code != 0 || out != "" || msg != "" {
		t.Fatalf("resume hook: %d %q %q", code, out, msg)
	}
	saved, ok, err := reporting.LookupSessionLink(sw.BackupDir(), id)
	if err != nil || !ok || saved.Wanted != "fable" || saved.Token != filepath.Base(path) {
		t.Fatalf("resumed session: %+v, %v", saved, err)
	}
	assertNativeStore(t, rec.env, fableStoreDir())
}
