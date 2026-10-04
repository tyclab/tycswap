// End-to-end tests of `tycswap app` through the command (DESIGN A35–A45):
// the headless app's start and stop, the app without a tray, and the update
// restart's hand-over of the single-instance lock. Every switcher uses fake
// secrets, PATH holds no Claude Code, and every server is on loopback under a
// temp HOME; nothing is left running.
package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/update"
	"github.com/tyclab/tycswap/internal/wincred"
)

// appTestHome is appLockHome plus what an app run needs to stay offline and
// private: switchers with fake secrets and a PATH without Claude Code.
func appTestHome(t *testing.T) string {
	t.Helper()
	home := appLockHome(t)
	testutil.Setenv(t, "PATH", t.TempDir())
	testutil.Setenv(t, "XDG_RUNTIME_DIR", t.TempDir())
	prev := newSwitcher
	newSwitcher = func(opts store.Options) (*core.Switcher, error) {
		opts.Keychain = keychain.NewFake()
		opts.OAuth = &oauth.FakeClient{}
		opts.WinCred = wincred.NewFake()
		return core.New(opts)
	}
	t.Cleanup(func() { newSwitcher = prev })
	return home
}

// runApp starts `tycswap app argv…` on a goroutine; the channel gets its exit
// code.
func runApp(argv []string, errb *syncBuffer) chan int {
	done := make(chan int, 1)
	var out syncBuffer
	go func() {
		done <- run("tycswap", append([]string{"app"}, argv...), ioStreams{in: strings.NewReader(""), out: &out, err: errb}, false, false)
	}()
	return done
}

func waitExit(t *testing.T, done chan int, errb *syncBuffer) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(15 * time.Second):
		t.Fatalf("the app did not exit; stderr:\n%s", errb.String())
	}
	return -1
}

// The headless app prints its dashboard, holds the single-instance lock while
// it runs, and ends with 0 on Ctrl-C / SIGTERM (the notify-context seam),
// leaving the lock free.
func TestHeadlessAppStartsAndStops(t *testing.T) {
	appTestHome(t)
	stop := stopViaNotifyContext(t)
	var errb syncBuffer
	done := runApp([]string{"--headless", "--port", "0", "--no-update-check"}, &errb)
	waitUntil(t, "the Dashboard line", func() bool {
		return strings.Contains(errb.String(), "Dashboard: http://127.0.0.1:")
	})
	if !strings.Contains(errb.String(), "Running without a tray (--headless).") {
		t.Errorf("stderr = %q", errb.String())
	}
	if !appIsRunning() {
		t.Error("the running app does not hold app.lock")
	}
	stop()
	if code := waitExit(t, done, &errb); code != 0 {
		t.Errorf("exit = %d, stderr = %q", code, errb.String())
	}
	if appIsRunning() {
		t.Error("app.lock is still held after the app stopped")
	}
}

// Without a tray (a Linux session without a StatusNotifierWatcher) the app
// serves the dashboard alone and says where a remote tray finds the token.
func TestAppWithoutATrayServesTheDashboard(t *testing.T) {
	appTestHome(t)
	prev := newTray
	newTray = func(tray.Icon, tray.Options) (tray.Tray, error) { return nil, tray.ErrUnsupported }
	t.Cleanup(func() { newTray = prev })
	stop := stopViaNotifyContext(t)
	var errb syncBuffer
	done := runApp([]string{"--port", "0", "--no-update-check"}, &errb)
	waitUntil(t, "the remote token line", func() bool {
		return strings.Contains(errb.String(), "Remote token: ")
	})
	for _, want := range []string{"Dashboard: http://127.0.0.1:", "No system tray is available here; running the dashboard server only."} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr lacks %q: %q", want, errb.String())
		}
	}
	stop()
	if code := waitExit(t, done, &errb); code != 0 {
		t.Errorf("exit = %d, stderr = %q", code, errb.String())
	}
}

// releaseEndpoint serves a release with tag for the update check.
func releaseEndpoint(t *testing.T, tag string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `"}`))
	}))
	t.Cleanup(srv.Close)
	prev := update.Endpoint
	update.Endpoint = srv.URL
	t.Cleanup(func() { update.Endpoint = prev })
}

// stubRestart takes the update restart's hand-over: the successor is not
// started, the test learns the tag and whether the lock was free by then.
func stubRestart(t *testing.T, lockPath string) (tags *[]string, lockFree *[]bool) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	var free []bool
	prev := restartSelf
	restartSelf = func(tag string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, tag)
		l, held, err := acquireLockAt(lockPath)
		free = append(free, err == nil && held)
		if held {
			_ = l.Release()
		}
		return nil
	}
	t.Cleanup(func() { restartSelf = prev })
	return &got, &free
}

// installWithoutClicking makes the tray's update→restart path run on its own:
// the test hooks for the first check and the dialog, an upgrade that reports
// the running binary replaced, and a build the tray may upgrade. It counts
// how often the app works out how this build is upgraded.
func installWithoutClicking(t *testing.T) *atomic.Int32 {
	t.Helper()
	testutil.Setenv(t, "TYCSWAP_TEST_UPDATE_FIRST", "20ms")
	testutil.Setenv(t, "TYCSWAP_TEST_AUTO_APPROVE", "1")
	prevFirst, prevCC := updateCheckFirst, claudeCodeCheckFirst
	prevUp, prevHint := upgradeForShell, appUpgradeHint
	claudeCodeCheckFirst = time.Hour
	upgradeForShell = func() (string, error) { return "Updated tycswap.", nil }
	var hints atomic.Int32
	appUpgradeHint = func() string { hints.Add(1); return "" }
	t.Cleanup(func() {
		updateCheckFirst, claudeCodeCheckFirst = prevFirst, prevCC
		upgradeForShell, appUpgradeHint = prevUp, prevHint
	})
	return &hints
}

// The update restart hands the single-instance lock over and ends with 0: the
// successor (stubbed here) must find app.lock free, and the deferred release
// must not touch the lock it handed over — the nil-lock panic that used to
// end every update restart. The remote token goes with it.
func TestAppRestartHandOverReleasesTheLockOnce(t *testing.T) {
	appTestHome(t)
	releaseEndpoint(t, "v99.0.0")
	hints := installWithoutClicking(t)
	tags, free := stubRestart(t, appLockPath())
	ft := &blockingTray{done: make(chan struct{})}
	prev := newTray
	newTray = func(tray.Icon, tray.Options) (tray.Tray, error) { return ft, nil }
	t.Cleanup(func() { newTray = prev })
	stopViaNotifyContext(t)
	var errb syncBuffer
	done := runApp([]string{"--port", "0"}, &errb)
	if code := waitExit(t, done, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errb.String())
	}
	if len(*tags) != 1 || (*tags)[0] != "v99.0.0" {
		t.Fatalf("restart tags = %v, stderr = %q", *tags, errb.String())
	}
	if !(*free)[0] {
		t.Error("the successor would have found app.lock still held")
	}
	// The hint probes the binary's directory: once, at the start, however
	// often the check, the menu and the card ask.
	if n := hints.Load(); n != 1 {
		t.Errorf("the upgrade hint was worked out %d times, want once", n)
	}
	if appIsRunning() {
		t.Error("app.lock is held after the hand-over")
	}
	if notes := strings.Join(ft.notesNow(), "\n"); !strings.Contains(notes, "Restarting…") {
		t.Errorf("notes = %s", notes)
	}
	for _, line := range strings.Split(errb.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, "Remote token: "); ok {
			t.Errorf("a tray app printed the remote token line: %q", rest)
		}
	}
}

// The remote tray takes the same hand-over with its own lock.
func TestRemoteRestartHandOverReleasesTheLockOnce(t *testing.T) {
	appTestHome(t)
	fx := startRemoteServer(t, testRemoteToken, "127.0.0.1:0")
	releaseEndpoint(t, "v99.0.0")
	hints := installWithoutClicking(t)
	tags, free := stubRestart(t, remoteLockPath())
	ft := &blockingTray{done: make(chan struct{})}
	prev := newTray
	newTray = func(tray.Icon, tray.Options) (tray.Tray, error) { return ft, nil }
	t.Cleanup(func() { newTray = prev })
	stopViaNotifyContext(t)
	var errb syncBuffer
	done := runApp([]string{"--remote", fx.base, "--token-file", tokenFile(t, testRemoteToken)}, &errb)
	if code := waitExit(t, done, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errb.String())
	}
	if len(*tags) != 1 || (*tags)[0] != "v99.0.0" || !(*free)[0] {
		t.Fatalf("restart tags = %v, lock free = %v, stderr = %q", *tags, *free, errb.String())
	}
	if n := hints.Load(); n != 1 {
		t.Errorf("the upgrade hint was worked out %d times, want once", n)
	}
	lock, held, err := acquireRemoteLock()
	if err != nil || !held {
		t.Fatalf("remote lock after the hand-over: %v, %v", held, err)
	}
	_ = lock.Release()
}

// The local app wires the tray: the shell paints the store's state, and
// "Open dashboard" opens a one-time URL of the app's own server.
func TestAppRunsTheTray(t *testing.T) {
	appTestHome(t)
	ft := &blockingTray{done: make(chan struct{})}
	var opts *tray.Options
	var mu sync.Mutex
	prev := newTray
	newTray = func(_ tray.Icon, o tray.Options) (tray.Tray, error) {
		mu.Lock()
		opts = &o
		mu.Unlock()
		return ft, nil
	}
	t.Cleanup(func() { newTray = prev })
	opened := stubOpener(t, "windows")
	stopViaNotifyContext(t)
	var errb syncBuffer
	done := runApp([]string{"--port", "0", "--no-update-check"}, &errb)
	waitUntil(t, "the first menu", func() bool {
		_, ok := ft.item("open")
		return ok
	})
	if br, _ := ft.item("brand"); br.Title != "tycswap" {
		t.Errorf("brand row = %+v", br)
	}
	if _, ok := ft.item("add-current"); !ok {
		t.Error("the local app's menu lacks Add current login")
	}
	mu.Lock()
	o := opts
	mu.Unlock()
	o.OnClick("open")
	if len(*opened) != 1 || !strings.HasPrefix((*opened)[0], "http://127.0.0.1:") || !strings.Contains((*opened)[0], "/?token=") {
		t.Errorf("browser got %q", *opened)
	}
	o.OnClick("quit")
	if code := waitExit(t, done, &errb); code != 0 {
		t.Errorf("exit = %d, stderr = %q", code, errb.String())
	}
	if _, err := os.Stat(appLockPath()); err != nil {
		t.Logf("lock file: %v", err)
	}
	if appIsRunning() {
		t.Error("app.lock is held after Quit")
	}
}
