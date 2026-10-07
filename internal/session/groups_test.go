package session_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/session"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

type groupRunner struct {
	argv, env []string
	cwd       string
}

func (*groupRunner) LookPath(string) (string, error)                              { return "/bin/claude", nil }
func (*groupRunner) Probe([]string, []string, time.Duration) (string, int, error) { return "", 1, nil }
func (r *groupRunner) Exec(_ string, argv, env []string) error {
	r.argv = argv
	r.env = env
	return nil
}
func (r *groupRunner) ExecInDir(_ string, argv, env []string, cwd string) error {
	r.argv = argv
	r.env = env
	r.cwd = cwd
	return nil
}

func sessionFixture(t *testing.T) (*core.Switcher, *session.Manager, *groupRunner, string) {
	t.Helper()
	testutil.IsolateHome(t)
	sw, err := core.New(store.Options{Stderr: io.Discard, Keychain: keychain.NewFake()})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	data := &store.SequenceData{Sequence: []int{1, 2}, Accounts: map[string]json.RawMessage{
		"1": json.RawMessage(`{"email":"fable@example.test","organizationUuid":""}`),
		"2": json.RawMessage(`{"email":"opus@example.test","organizationUuid":""}`),
	}}
	if err := sw.WriteSequence(data); err != nil {
		t.Fatal(err)
	}
	for number, email := range map[string]string{"1": "fable@example.test", "2": "opus@example.test"} {
		creds := `{"claudeAiOauth":{"accessToken":"fixture-` + number + `","refreshToken":"refresh-` + number + `","subscriptionType":"max"}}`
		if err := sw.WriteAccountCredentials(number, email, creds); err != nil {
			t.Fatal(err)
		}
		if err := sw.Store.WriteAccountConfig(number, email, `{"oauthAccount":{"emailAddress":"`+email+`","organizationUuid":""}}`); err != nil {
			t.Fatal(err)
		}
	}
	runner := &groupRunner{}
	manager := session.NewManager(sw, session.Options{Runner: runner, Stdout: io.Discard, Keychain: sw.Keychain(), HookExecutable: "/tmp/fixture tycswap"})
	return sw, manager, runner, t.TempDir()
}

func transcript(t *testing.T, profile, id, cwd string) string {
	t.Helper()
	path := filepath.Join(profile, "projects", "fixture-project", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"type": "user", "cwd": cwd, "sessionId": id, "message": map[string]string{"role": "user", "content": "preserve this saved history"}})
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func envValue(env []string, key string) string {
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			return strings.TrimPrefix(entry, key+"=")
		}
	}
	return ""
}

func TestManagedGroupSettingsAndEnvironmentAreIndependent(t *testing.T) {
	sw, manager, _, cwd := sessionFixture(t)
	if err := os.MkdirAll(sw.DefaultProfileDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sw.DefaultProfileDir(), "settings.json")
	original := `{"permissions":{"defaultMode":"plan"},"apiKeyHelper":"private-helper","env":{"ANTHROPIC_AUTH_TOKEN":"private-token","KEEP":"value"},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"custom-user-hook"}]}]}}`
	if err := os.WriteFile(source, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "do-not-transfer")
	t.Setenv("ANTHROPIC_BASE_URL", "https://secret.invalid")
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/inherited-profile")
	launch, err := manager.PrepareGroup("fable", "1", "fable", "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CLAUDE_CONFIG_DIR") != "/tmp/inherited-profile" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "do-not-transfer" {
		t.Fatal("group preparation changed process environment")
	}
	if envValue(launch.Env, "CLAUDE_CONFIG_DIR") != launch.ProfileDir || envValue(launch.Env, "ANTHROPIC_AUTH_TOKEN") != "" || envValue(launch.Env, "ANTHROPIC_BASE_URL") != "" {
		t.Fatal("launch environment is not isolated")
	}
	for _, entry := range launch.Env {
		if strings.HasPrefix(entry, "CLAUDE_SECURESTORAGE_CONFIG_DIR=") {
			t.Fatal("secure-storage override survived")
		}
	}
	if envValue(launch.Env, "TYCSWAP_GROUP_MODEL") != "fable" || !strings.Contains(strings.Join(launch.Args, " "), "--session-id") {
		t.Fatal("managed model/session context is missing")
	}
	if got, _ := os.ReadFile(source); string(got) != original {
		t.Fatal("group hooks modified the global settings")
	}
	settingsPath := filepath.Join(launch.ProfileDir, "settings.json")
	info, err := os.Lstat(settingsPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("group settings are not an independent file")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := manager.PrepareGroup("fable", "1", "fable", "", cwd); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(settingsPath)
	if strings.Contains(string(data), "private-helper") || strings.Contains(string(data), "private-token") || !strings.Contains(string(data), "custom-user-hook") {
		t.Fatal("user hooks/settings preservation or auth override scrub failed")
	}
	if strings.Count(string(data), "groups guard --group fable") != 1 {
		t.Fatal("model guard installation is not idempotent")
	}
	if strings.Count(string(data), "recovery record --group fable") != 5 {
		t.Fatal("lifecycle hook installation is not idempotent")
	}
	if !strings.Contains(string(data), "TYCSWAP_GROUP_MODEL") {
		t.Fatal("hooks lost per-session model attribution")
	}
	record, _ := os.ReadFile(filepath.Join(sw.ScopeRoot(), "groups", "fable", "launches", launch.LaunchID+".json"))
	if strings.Contains(string(record), "do-not-transfer") {
		t.Fatal("persisted launch exposed authentication environment")
	}
}

func TestFirstResumeForksSourceAndRecordsActualNativeID(t *testing.T) {
	sw, manager, _, cwd := sessionFixture(t)
	sourceID := "01234567-89ab-4cde-8012-3456789abcde"
	destinationID := "12345678-90ab-4cde-8123-456789abcdef"
	source := transcript(t, sw.DefaultProfileDir(), sourceID, cwd)
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	canonicalCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(source)
	launch, err := manager.PrepareGroup("fable", "1", "fable", sourceID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !launch.Migrating || launch.SessionID != "" {
		t.Fatalf("unexpected migration state: migrating=%t sessionID=%q", launch.Migrating, launch.SessionID)
	}
	if launch.SourceTranscript != canonicalSource {
		t.Fatalf("migration source=%q, want %q", launch.SourceTranscript, canonicalSource)
	}
	launchCWD, err := filepath.EvalSymlinks(launch.CWD)
	if err != nil || launchCWD != canonicalCWD {
		t.Fatalf("migration cwd=%q, want %q (resolve error: %v)", launch.CWD, canonicalCWD, err)
	}
	if strings.Join(launch.Args, " ") != "--model fable --resume "+canonicalSource+" --fork-session" {
		t.Fatalf("unsafe resume args: %v", launch.Args)
	}
	if err := session.RecordGroupSession(sw.SharedRoot(), groups.Fable, launch.LaunchID, destinationID, cwd); err != nil {
		t.Fatal(err)
	}
	transcript(t, launch.ProfileDir, destinationID, cwd)
	for _, resume := range []string{sourceID, source, destinationID} {
		next, err := manager.PrepareGroup("fable", "1", "fable", resume, "")
		if err != nil {
			t.Fatal(err)
		}
		if next.Migrating || next.SessionID != destinationID || strings.Contains(strings.Join(next.Args, " "), "--fork-session") {
			t.Fatalf("second resume did not stay in group: migrating=%t sessionID=%q args=%v", next.Migrating, next.SessionID, next.Args)
		}
	}
	if after, _ := os.ReadFile(source); string(after) != string(before) {
		t.Fatal("source transcript changed during migration")
	}
}

func TestMigrationRejectsSourceChangesAndLiveSource(t *testing.T) {
	sw, manager, _, cwd := sessionFixture(t)
	sourceID := "01234567-89ab-4cde-8012-3456789abcde"
	source := transcript(t, sw.DefaultProfileDir(), sourceID, cwd)
	sessionDir := filepath.Join(sw.DefaultProfileDir(), "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	row, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": sourceID})
	pidFile := filepath.Join(sessionDir, "self.json")
	if err := os.WriteFile(pidFile, row, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareGroup("fable", "1", "fable", sourceID, ""); err == nil {
		t.Fatal("live source was silently migrated")
	}
	if err := os.Remove(pidFile); err != nil {
		t.Fatal(err)
	}
	launch, err := manager.PrepareGroup("fable", "1", "fable", sourceID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("changed source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.RecordGroupSession(sw.SharedRoot(), groups.Fable, launch.LaunchID, "12345678-90ab-4cde-8123-456789abcdef", cwd); err == nil {
		t.Fatal("changed source gained a migration mapping")
	}
}

func TestForwardedResumeGetsForkedAndGuardBypassesAreRejected(t *testing.T) {
	sw, manager, runner, cwd := sessionFixture(t)
	sourceID := "01234567-89ab-4cde-8012-3456789abcde"
	source := transcript(t, sw.DefaultProfileDir(), sourceID, cwd)
	if err := manager.RunGroup("fable", "1", "", "", cwd, []string{"--resume", source, "--model=fable", "--permission-mode", "plan"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(runner.argv, " "), "--fork-session") {
		t.Fatal("forwarded resume bypassed native fork")
	}
	for _, args := range [][]string{{"--model", "opus"}, {"--continue"}, {"--settings={}"}, {"--setting-sources", ""}, {"--fork-session"}, {"--session-id", "x"}, {"--fallback-model", "opus"}} {
		if err := manager.RunGroup("fable", "1", "", "", cwd, args); err == nil {
			t.Fatalf("managed launch accepted unsafe args %v", args)
		}
	}
}

func TestGroupSettingsSymlinkCannotWriteGlobalFile(t *testing.T) {
	sw, manager, _, cwd := sessionFixture(t)
	if _, err := manager.PrepareGroup("fable", "1", "fable", "", cwd); err != nil {
		t.Fatal(err)
	}
	group, _ := sw.ForGroup(groups.Fable)
	global := filepath.Join(sw.DefaultProfileDir(), "settings.json")
	if err := os.MkdirAll(filepath.Dir(global), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{"keep":"global"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(group.SettingsPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(global, group.SettingsPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareGroup("fable", "1", "fable", "", cwd); err == nil {
		t.Fatal("symlink settings were accepted")
	}
	if raw, _ := os.ReadFile(global); string(raw) != `{"keep":"global"}` {
		t.Fatal("global settings were modified through a group symlink")
	}
}

func TestManagedLaunchReconcilesInitialClaimBeforeSelection(t *testing.T) {
	sw, manager, _, cwd := sessionFixture(t)
	group, err := sw.Store.ForGroup(groups.Fable)
	if err != nil {
		t.Fatal(err)
	}
	creds, _ := group.ReadAccountCredentials("1", "fable@example.test")
	if err := sw.Lock.With(func() error {
		_, err := group.BeginGroupSwitch(groups.Account{Number: "1", Email: "fable@example.test"}, creds)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	launch, err := manager.PrepareGroup("fable", "", "fable", "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if launch.AccountNumber != "1" {
		t.Fatal("initial claim crash stranded the only selected account")
	}
	if journal, err := groups.LoadJournal(sw.SharedRoot(), groups.Fable); err != nil || journal != nil {
		t.Fatal("initial claim journal was not reconciled")
	}
}
