package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/launch"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestMythosEnrollmentAndNativeStart(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("separate file-based secure storage is unavailable")
	}
	f := newLoginFixture(t)
	for _, name := range append([]string{"CLAUDE_CONFIG_DIR"}, launch.AuthOverrideEnvVars...) {
		testutil.Unsetenv(t, name)
	}
	t.Setenv("FAKE_CLAUDE_EMAIL", "a@example.com")
	def := f.liveCreds(t)
	if code, _, msg := f.run(t, "mythos", "--allow", "1"); code != 0 {
		t.Fatal(msg)
	}
	if code, _, msg := f.run(t, "add", "--login", "--lineage", "mythos", "--slot", "1"); code != 0 {
		t.Fatalf("enroll: %v", msg)
	}
	dir := store.MythosLoginDir(paths.GetBackupRoot(), "a@example.com", "")
	grant, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil || !strings.Contains(string(grant), "refresh-token-2") || f.liveCreds(t) != def {
		t.Fatalf("independent grant was not stored separately: %v", err)
	}
	prevExec, prevResolve := startExec, startResolve
	t.Cleanup(func() { startExec, startResolve = prevExec, prevResolve })
	startResolve = func(string) (string, error) { return "/verified/command", nil }
	calls := 0
	startExec = func(_ string, argv, env []string) (int, error) {
		calls++
		if envValue(env, secureStorageEnv) != dir || len(argv) != 3 || argv[2] != "mythos" || envValue(env, startHoldEnv) != "" {
			t.Fatal("the requested model and its enrolled native login must reach the same child")
		}
		return 19, nil
	}
	code, _, msg := f.run(t, "start", "command", "--model", "mythos")
	if code != 19 || calls != 1 || f.liveCreds(t) != def || msg != "" {
		t.Fatalf("native result was not passed through once: code=%d calls=%d message=%q", code, calls, msg)
	}
	if code, _, msg := f.run(t, "models", "1", "opus"); code != 0 {
		t.Fatal(msg)
	}
	code, _, msg = f.run(t, "start", "command", "--model", "mythos")
	if code != 1 || calls != 1 || !strings.Contains(msg, "operator decision needed") {
		t.Fatalf("unattended exception: code=%d calls=%d message=%q", code, calls, msg)
	}
	previousAsk := askPlacement
	t.Cleanup(func() { askPlacement = previousAsk })
	id := "12345678-1234-4123-8123-1234567890ab"
	askPlacement = func(_ ioStreams, question string, printMode bool) (bool, error) {
		if !printMode || !strings.Contains(question, id) {
			t.Fatalf("print-mode placement omitted its session ID: %s", question)
		}
		return false, nil
	}
	code, _, msg = f.run(t, "start", "command", "--model", "mythos", "--session-id", id, "-p")
	if code != 1 || calls != 1 || !strings.Contains(msg, "deferred") {
		t.Fatalf("deferred print-mode start: code=%d calls=%d message=%q", code, calls, msg)
	}
	askPlacement = func(_ ioStreams, question string, _ bool) (bool, error) {
		if !strings.Contains(question, "Account-1, allocated to opus") || !strings.Contains(question, "this Mythos session") {
			t.Fatalf("incomplete question: %s", question)
		}
		return true, nil
	}
	code, _, msg = f.run(t, "start", "command", "--model", "mythos")
	if code != 19 || calls != 2 {
		t.Fatalf("approved exception: code=%d calls=%d message=%q", code, calls, msg)
	}
	askPlacement = func(_ ioStreams, _ string, _ bool) (bool, error) {
		if code, _, msg := f.run(t, "disable", "1"); code != 0 {
			t.Fatal(msg)
		}
		return true, nil
	}
	code, _, msg = f.run(t, "start", "command", "--model", "mythos")
	if code != 1 || calls != 2 || !strings.Contains(msg, "no enabled") {
		t.Fatalf("disabled last login: code=%d calls=%d message=%q", code, calls, msg)
	}
}
