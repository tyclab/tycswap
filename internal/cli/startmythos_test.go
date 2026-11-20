package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/launch"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestResumeKeepsIndependentIdentityAfterModelChange(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("separate file-based secure storage is unavailable")
	}
	f := newLoginFixture(t)
	for _, key := range append([]string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_MODEL"}, launch.AuthOverrideEnvVars...) {
		testutil.Unsetenv(t, key)
	}
	run := func(args ...string) {
		t.Helper()
		if code, _, msg := f.run(t, args...); code != 0 {
			t.Fatalf("%v: %s", args, msg)
		}
	}
	t.Setenv("FAKE_CLAUDE_EMAIL", "a@example.com")
	run("mythos", "--allow", "1")
	run("add", "--login", "--lineage", "mythos", "--slot", "1")
	f.writeLive(t, "b@example.com", "fixture-default", "fixture-refresh")
	run("add")
	t.Setenv("FAKE_CLAUDE_EMAIL", "b@example.com")
	t.Setenv("FAKE_CLAUDE_TOKEN", "fixture-independent-b")
	t.Setenv("FAKE_CLAUDE_REFRESH", "fixture-refresh-b")
	run("mythos", "--allow", "2")
	run("add", "--login", "--lineage", "mythos", "--slot", "2")
	root := paths.GetBackupRoot()
	dir := store.MythosLoginDir(root, "a@example.com", "")
	const id = "11111111-2222-4333-8444-555555555555"
	observed := time.Now()
	for _, family := range []string{"mythos", "opus"} {
		if _, err := reporting.RecordDefaultSessionModel(root, id, family, "mythos/"+filepath.Base(dir), observed); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reporting.RecordDefaultSessionModel(root, "old-shared-session", "opus", "", observed); err != nil {
		t.Fatal(err)
	}
	if _, err := reporting.RecordDefaultSessionModel(root, "new-shared-session", "opus", "", observed.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, recorded := reporting.RecordedSession(root, "old-shared-session"); recorded {
		t.Fatal("an inactive shared session kept its transient status")
	}
	oldExec, oldResolve, oldAsk := startExec, startResolve, askPlacement
	t.Cleanup(func() { startExec, startResolve, askPlacement = oldExec, oldResolve, oldAsk })
	startResolve = func(string) (string, error) { return "/verified/claude", nil }
	calls := 0
	startExec = func(_ string, args, env []string) (int, error) {
		calls++
		if got := envValue(env, secureStorageEnv); got != dir {
			t.Fatalf("explicit resume used %q, want its recorded dedicated identity %q", got, dir)
		}
		if strings.Join(args, " ") != "claude --resume "+id || envValue(env, startHoldEnv) != "" {
			t.Fatalf("resume arguments or hold changed: %v", args)
		}
		return 19, nil
	}
	resume := []string{"start", "claude", "--resume", id}
	if code, _, msg := f.run(t, resume...); code != 19 || calls != 1 {
		t.Fatalf("native status: code=%d calls=%d %s", code, calls, msg)
	}
	run("models", "1", "mythos")
	question := ""
	askPlacement = func(_ ioStreams, q string, _ bool) (bool, error) { question = q; return false, nil }
	for _, family := range []string{"Opus", "Fable"} {
		args := resume
		if family == "Fable" {
			args = append(args, "--model", "fable")
		}
		if code, _, msg := f.run(t, args...); code != 1 || calls != 1 ||
			!strings.Contains(question, "this "+family+" session") || !strings.Contains(question, id) ||
			!strings.Contains(question, "Account-1, allocated to mythos") {
			t.Fatalf("allocation on resume: code=%d calls=%d question=%q %s", code, calls, question, msg)
		}
	}
	run("disable", "1")
	if code, _, msg := f.run(t, resume...); code != 1 || calls != 1 || !strings.Contains(msg, "recorded login is unavailable") {
		t.Fatalf("disabled resume: code=%d calls=%d %s", code, calls, msg)
	}
	sw, err := newSwitcher(store.Options{SkipMigrations: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.RemoveAccount("1", true); err != nil {
		t.Fatal(err)
	}
	if code, _, msg := f.run(t, resume...); code != 1 || calls != 1 || !strings.Contains(msg, "recorded login is unavailable") {
		t.Fatalf("removed identity with another login available: code=%d calls=%d %s", code, calls, msg)
	}
	if err := os.Rename(store.MythosLoginRoot(root), filepath.Join(root, "removed-test-logins")); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.run(t, resume...); code != 1 || calls != 1 {
		t.Fatalf("removed native store: code=%d calls=%d", code, calls)
	}
}

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
