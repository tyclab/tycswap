package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/testutil"
)

// appLockHome points the backup root at a temp dir so the lock file is ours.
func appLockHome(t *testing.T) string {
	t.Helper()
	home := testutil.IsolateHome(t)
	testutil.Setenv(t, "NO_COLOR", "1")
	prev := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = prev })
	if err := os.MkdirAll(filepath.Dir(appLockPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestAppLockIsExclusive(t *testing.T) {
	appLockHome(t)
	if appIsRunning() {
		t.Fatal("no app has the lock yet")
	}
	lock, got, err := acquireAppLock()
	if err != nil || !got {
		t.Fatalf("first acquire = %v, %v", got, err)
	}
	if !appIsRunning() {
		t.Error("a held lock must read as running")
	}
	if _, second, err := acquireAppLock(); err != nil || second {
		t.Errorf("second acquire = %v, %v — want refused", second, err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if appIsRunning() {
		t.Error("a released lock must read as free")
	}
	relock, again, err := acquireAppLock()
	if err != nil || !again {
		t.Errorf("acquire after release = %v, %v", again, err)
	}
	// Release it: Windows cannot delete a file that is still open, and the
	// test's temp dir cleanup would fail on the lock file (A41).
	if relock != nil {
		_ = relock.Release()
	}
}

// TestPurgeRefusedWhileAppRuns: purge must not delete the accounts under a
// running tray (A38).
func TestPurgeRefusedWhileAppRuns(t *testing.T) {
	appTestHome(t)
	lock, got, err := acquireAppLock()
	if err != nil || !got {
		t.Fatalf("acquire = %v, %v", got, err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	var out, errb bytes.Buffer
	code := run("tycswap", []string{"purge"}, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
	if code == 0 {
		t.Fatalf("purge should refuse, exit = %d (stdout=%q)", code, out.String())
	}
	if !strings.Contains(errb.String(), "app is running") {
		t.Errorf("stderr = %q, want a running-app message", errb.String())
	}
}

// Asking whether an app runs creates nothing: a purge, cancelled or not,
// leaves no app.lock and no backup root behind (A38).
func TestPurgeCreatesNoLock(t *testing.T) {
	testutil.IsolateHome(t)
	if appIsRunning() {
		t.Fatal("no app has run here")
	}
	if _, err := os.Stat(filepath.Dir(appLockPath())); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("asking created the backup root: %v", err)
	}
	appLockHome(t)
	withPrompter(t, &fakePrompter{answer: "n", ok: true}) // purge asks through the prompter, not s.in
	var out, errb bytes.Buffer
	_ = run("tycswap", []string{"purge"}, ioStreams{in: strings.NewReader("n\n"), out: &out, err: &errb}, false, false)
	if _, err := os.Stat(appLockPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("purge created %s: %v (stderr %q)", appLockPath(), err, errb.String())
	}
}

// A second app on the same machine refuses to start, before it builds a tray
// or a dashboard.
func TestSecondAppRefusesToStart(t *testing.T) {
	appLockHome(t)
	lock, got, err := acquireAppLock()
	if err != nil || !got {
		t.Fatalf("acquire = %v, %v", got, err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	code, _, errStr := runCLI(t, []string{"app", "--headless", "--no-update-check"}, false, false)
	if code != 1 || !strings.Contains(errStr, "tycswap app is already running") {
		t.Errorf("exit = %d, stderr = %q", code, errStr)
	}
}
