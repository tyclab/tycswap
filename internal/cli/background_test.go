package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeChild is a spawned app that has or has not exited.
type fakeChild struct {
	exited bool
	err    error
}

func (c fakeChild) Exited() (bool, error) { return c.exited, c.err }

// stubBackground replaces the binary path and the spawner for one test; spawn
// is what the "child" does when started.
func stubBackground(t *testing.T, spawn func(exe, logPath string) (backgroundApp, error)) {
	t.Helper()
	prevExe, prevSpawn := exePath, spawnBackgroundApp
	prevWait, prevPoll := backgroundStartWait, backgroundPoll
	exePath = func() string { return "/opt/tools/tycswap" }
	spawnBackgroundApp = spawn
	backgroundStartWait, backgroundPoll = 2*time.Second, 5*time.Millisecond
	t.Cleanup(func() {
		exePath, spawnBackgroundApp = prevExe, prevSpawn
		backgroundStartWait, backgroundPoll = prevWait, prevPoll
	})
}

func runBare(t *testing.T) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run("tycswap", []string{}, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, true, true)
	return code, out.String(), errb.String()
}

// Bare tycswap in a terminal starts `app` detached and returns as soon as the
// child holds the single-instance lock (A40).
func TestBareTTYStartsAppInBackground(t *testing.T) {
	appLockHome(t)
	var gotExe, gotLog string
	stubBackground(t, func(exe, logPath string) (backgroundApp, error) {
		gotExe, gotLog = exe, logPath
		lock, held, err := acquireAppLock() // what `app` does first
		if err != nil || !held {
			t.Fatalf("child lock = %v, %v", held, err)
		}
		t.Cleanup(func() { _ = lock.Release() })
		return fakeChild{}, nil
	})
	code, out, errStr := runBare(t)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errStr)
	}
	if gotExe != "/opt/tools/tycswap" || gotLog != appLogPath() {
		t.Errorf("spawned %q logging to %q", gotExe, gotLog)
	}
	if !strings.Contains(out, "running in the background") || !strings.Contains(out, "Log: "+appLogPath()) {
		t.Errorf("stdout = %q", out)
	}
}

// A second bare start finds the app running and starts nothing (A38).
func TestBareTTYWhenAppAlreadyRuns(t *testing.T) {
	appLockHome(t)
	lock, held, err := acquireAppLock()
	if err != nil || !held {
		t.Fatalf("acquire = %v, %v", held, err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	stubBackground(t, func(string, string) (backgroundApp, error) {
		t.Fatal("spawned although the app is running")
		return nil, nil
	})
	code, out, _ := runBare(t)
	if code != 0 || !strings.Contains(out, "already running") {
		t.Errorf("exit = %d, stdout = %q", code, out)
	}
}

// A child that dies before taking the lock is reported with the end of its
// log, not as a success.
func TestBareTTYReportsAChildThatDies(t *testing.T) {
	appLockHome(t)
	stubBackground(t, func(_, logPath string) (backgroundApp, error) {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(logPath, []byte("Dashboard: x\nError: could not bind\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return fakeChild{exited: true, err: errors.New("exit status 1")}, nil
	})
	code, _, errStr := runBare(t)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{"stopped right after starting (exit status 1)", "Error: could not bind", "Full log: " + appLogPath()} {
		if !strings.Contains(errStr, want) {
			t.Errorf("stderr = %q, want %q in it", errStr, want)
		}
	}
}

// A spawn failure is an error that points at the foreground command.
func TestBareTTYSpawnFailure(t *testing.T) {
	appLockHome(t)
	stubBackground(t, func(string, string) (backgroundApp, error) {
		return nil, errors.New("permission denied")
	})
	code, _, errStr := runBare(t)
	if code != 1 || !strings.Contains(errStr, "permission denied") || !strings.Contains(errStr, "tycswap app") {
		t.Errorf("exit = %d, stderr = %q", code, errStr)
	}
}

// A bare invocation from a script or pipe is still the usage error, not a
// resident process nobody asked for (A40).
func TestBareNonTTYSpawnsNothing(t *testing.T) {
	appLockHome(t)
	stubBackground(t, func(string, string) (backgroundApp, error) {
		t.Fatal("spawned from a pipe")
		return nil, nil
	})
	if code, _, errStr := runCLI(t, []string{}, false, true); code != 2 || !strings.Contains(errStr, "no command given") {
		t.Errorf("exit = %d, stderr = %q", code, errStr)
	}
}

// The log is rotated once it grows past maxAppLog, never truncated in place.
func TestOpenAppLogRotates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logs", "app.log")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), maxAppLog+1), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openAppLog(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if fi, err := os.Stat(p + ".1"); err != nil || fi.Size() != maxAppLog+1 {
		t.Fatalf("rotated log = %v, %v", fi, err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() != 0 {
		t.Errorf("fresh log = %v, %v", fi, err)
	}
}

// Git Bash hands programs pipes, not a console; a person typing the bare
// command there still gets the background app, not "no command given" (A41).
func TestBareMSYSTerminalStartsApp(t *testing.T) {
	appLockHome(t)
	prev := msysTerminal
	msysTerminal = func() bool { return true }
	t.Cleanup(func() { msysTerminal = prev })
	spawned := false
	stubBackground(t, func(string, string) (backgroundApp, error) {
		spawned = true
		lock, held, err := acquireAppLock()
		if err != nil || !held {
			t.Fatalf("child lock = %v, %v", held, err)
		}
		t.Cleanup(func() { _ = lock.Release() })
		return fakeChild{}, nil
	})
	code, _, errStr := runCLI(t, []string{}, false, false)
	if code != 0 || !spawned {
		t.Errorf("exit = %d, spawned = %v, stderr = %q", code, spawned, errStr)
	}
}
