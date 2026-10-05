package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/brand"
	"github.com/tyclab/tycswap/internal/testutil"
)

// childRoleEnv makes the test binary play a child process instead of running
// tests, for the tests that need a real one: the detached start (A40/A41),
// Claude Code (installFakeClaude) and, on Windows, the console hand-off.
var childRoleEnv = brand.Sanitized().EnvPrefix + "_TEST_CHILD"

func TestMain(m *testing.M) {
	if role := os.Getenv(childRoleEnv); role != "" {
		os.Exit(runTestChild(role))
	}
	// A test binary started as `<binary> app …` is a spawn some test did not
	// stub: it must not run the suite again (which would spawn again).
	if len(os.Args) > 1 && os.Args[1] == "app" {
		fmt.Fprintln(os.Stderr, "test binary started as an app child; refusing to run the tests")
		os.Exit(3)
	}
	// The tests that run `app` through the command keep the test binary's
	// console (and its output) where it is; the hand-off itself has tests
	// of its own (console_windows_test.go, TestReleaseOwnConsoleForReal).
	releaseAppConsole = func(s ioStreams) ioStreams { return s }
	// A Git Bash running the tests (MSYSTEM set) must not turn every bare
	// invocation into a background start; the test of that rule sets it.
	msysTerminal = func() bool { return false }
	// The update restart re-runs os.Args detached: from a test binary that is
	// the whole suite again. The tests that take the hand-over stub it with a
	// recorder of their own; no other path may reach the real one.
	restartSelf = func(string) error {
		fmt.Fprintln(os.Stderr, "restartSelf called without a test stub; not restarting the test binary")
		return errors.New("restartSelf is stubbed in tests")
	}
	// Every switcher and scratch login gets its home's fake Keychain, never
	// the real login Keychain (homekeychain_test.go).
	useHomeKeychains()
	os.Exit(m.Run())
}

// runTestChild reports what the child sees, on its own stdout/stderr.
func runTestChild(role string) int {
	switch role {
	case "detached":
		n, err := os.Stdin.Read(make([]byte, 1))
		fmt.Fprintln(os.Stdout, "stdout ok")
		fmt.Fprintln(os.Stderr, "stderr ok")
		fmt.Fprintf(os.Stdout, "stdin: n=%d err=%v\n", n, err)
		fmt.Fprintf(os.Stdout, "console: %v\n", hasConsole())
		fmt.Fprintf(os.Stdout, "args: %s\n", strings.Join(os.Args[1:], " "))
		return 0
	case "release-console":
		before := consoleCountForTest()
		s := releaseOwnConsole(ioStreams{in: os.Stdin, out: os.Stdout, err: os.Stderr})
		fmt.Fprintf(s.err, "count before: %d\nreleased\nconsole: %v\n", before, hasConsole())
		return 0
	case "claude-login":
		return fakeClaudeLogin(os.Args[1:])
	case "claude-update":
		return fakeClaudeUpdater(os.Args[1:])
	}
	return 2
}

// installFakeClaude puts this test binary in dir as claude (claude.exe on
// Windows), playing role when it runs, and returns its path.
func installFakeClaude(t *testing.T, dir, role string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(dir, "claude")
	if runtime.GOOS == "windows" {
		// A symlink needs a privilege on Windows: copy instead.
		claude += ".exe"
		var raw []byte
		if raw, err = os.ReadFile(exe); err == nil {
			err = os.WriteFile(claude, raw, 0o755)
		}
	} else {
		err = os.Symlink(exe, claude)
	}
	if err != nil {
		t.Fatal(err)
	}
	testutil.Setenv(t, childRoleEnv, role)
	return claude
}

// waitExited polls a spawned child until it has exited.
func waitExited(t *testing.T, c backgroundApp) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if done, err := c.Exited(); done {
			if err != nil {
				t.Fatalf("child failed: %v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child did not exit")
}

// The real spawn the bare command uses: a detached child started as `app`,
// with stdin on the null device (EOF at once, never an error), both output
// streams in the log — and, on Windows, no console of its own (A40/A41). The child is this test binary in its child role, which
// exits by itself: nothing is left running.
func TestSpawnDetachedAppRealChild(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(childRoleEnv, "detached")
	logPath := filepath.Join(t.TempDir(), "logs", "app.log")
	child, err := spawnDetachedApp(exe, logPath)
	if err != nil {
		t.Fatal(err)
	}
	waitExited(t, child)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	for _, want := range []string{"stdout ok", "stderr ok", "stdin: n=0 err=EOF", "console: false", "args: app"} {
		if !strings.Contains(log, want) {
			t.Errorf("child log lacks %q:\n%s", want, log)
		}
	}
}
