//go:build !windows

package cli

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/tray"
)

// The program-wide SIGINT notifier Main installs, once for the test binary.
// Without the claim the two servers take, a SIGINT here ends the whole test
// run with 130, which is the failure this file guards against.
var installTestSigint = sync.OnceFunc(func() {
	var out, errb syncBuffer
	installSigint(ioStreams{in: strings.NewReader(""), out: &out, err: &errb})
})

// ctrlC sends the test process a real SIGINT, as a terminal's Ctrl-C does.
func ctrlC(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
}

// `tycswap web` ends with 0 on a real Ctrl-C with the program-wide notifier
// installed: the server claims the signal, its context ends the serve loop
// and the command returns (DESIGN A48). Before the claim the notifier's exit
// 130 won. Both commands claim before their first line, so a signal sent
// after the awaited line is always theirs.
func TestCtrlCEndsWebWithZero(t *testing.T) {
	appTestHome(t)
	installTestSigint()
	prev := webOptions
	webOptions = dashboardOptions{noUpdateSchedule: true}
	t.Cleanup(func() { webOptions = prev })
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run("tycswap", []string{"web", "--no-open", "--port", "0"}, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
	}()
	waitUntil(t, "the Dashboard line", func() bool {
		return strings.Contains(errb.String(), "Dashboard: http://127.0.0.1:")
	})
	ctrlC(t)
	if code := waitExit(t, done, &errb); code != 0 {
		t.Errorf("exit = %d, stderr = %q", code, errb.String())
	}
	if strings.Contains(out.String(), "Operation cancelled") {
		t.Errorf("the notifier's note was printed: %q", out.String())
	}
}

// The headless app on a real Ctrl-C: exit 0, and the deferred cleanup ran, so
// the remote token file is gone (A45, A48).
func TestCtrlCEndsHeadlessAppWithZeroAndRemovesTheToken(t *testing.T) {
	appTestHome(t)
	installTestSigint()
	var errb syncBuffer
	done := runApp([]string{"--headless", "--port", "0", "--no-update-check"}, &errb)
	waitUntil(t, "the remote token line", func() bool {
		return strings.Contains(errb.String(), "Remote token: ")
	})
	m := regexp.MustCompile(`Remote token: (\S+) `).FindStringSubmatch(errb.String())
	if m == nil {
		t.Fatalf("no token path in stderr: %q", errb.String())
	}
	if _, err := os.Stat(m[1]); err != nil {
		t.Fatalf("token file before the stop: %v", err)
	}
	ctrlC(t)
	if code := waitExit(t, done, &errb); code != 0 {
		t.Errorf("exit = %d, stderr = %q", code, errb.String())
	}
	if _, err := os.Stat(m[1]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("token file still there after Ctrl-C: %v", err)
	}
	if appIsRunning() {
		t.Error("app.lock is still held after Ctrl-C")
	}
}

// ctrlCTray sends the process a real Ctrl-C as Run starts and returns once
// Quit comes, or with an error after five seconds rather than hang.
type ctrlCTray struct {
	fakeTray
	quit chan struct{}
	once sync.Once
}

func (e *ctrlCTray) Run() error {
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		return err
	}
	select {
	case <-e.quit:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("no Quit after the Ctrl-C")
	}
}

func (e *ctrlCTray) Quit() { e.once.Do(func() { close(e.quit) }) }

// The remote tray (`app --remote`) on a real Ctrl-C, sent as its tray's Run
// starts: the claim hands the signal to the command's context, whose end
// quits the tray, and the command exits 0 with remote.lock free (A48).
// Without the claim the notifier's exit 130 ends the test binary.
func TestCtrlCEndsRemoteTrayWithZero(t *testing.T) {
	appLockHome(t)
	installTestSigint()
	ft := &ctrlCTray{quit: make(chan struct{})}
	prev := newTray
	newTray = func(tray.Icon, tray.Options) (tray.Tray, error) { return ft, nil }
	t.Cleanup(func() { newTray = prev })
	var errb syncBuffer
	done := runApp([]string{"--remote", "http://127.0.0.1:1", "--token-file", tokenFile(t, testRemoteToken), "--no-update-check"}, &errb)
	if code := waitExit(t, done, &errb); code != 0 {
		t.Errorf("exit = %d, stderr = %q", code, errb.String())
	}
	lock, held, err := acquireRemoteLock()
	if err != nil || !held {
		t.Fatalf("remote lock after the exit: %v, %v", held, err)
	}
	_ = lock.Release()
}
