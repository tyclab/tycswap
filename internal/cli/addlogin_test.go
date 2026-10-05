package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/tyclab/tycswap/internal/session"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

// fakeClaudeLogin stands in for Claude Code (installFakeClaude's
// "claude-login" role). It refuses anything but `auth login`, records its
// arguments (one per line) and the CLAUDE_CONFIG_DIR it was given, and then —
// per FAKE_CLAUDE_MODE — writes a subscription login, writes an API-key login,
// or fails having written nothing.
func fakeClaudeLogin(args []string) int {
	if len(args) < 2 || args[0] != "auth" || args[1] != "login" {
		return 64
	}
	log, dir := os.Getenv("FAKE_CLAUDE_LOG"), os.Getenv("CLAUDE_CONFIG_DIR")
	write := func(path, content string) bool { return os.WriteFile(path, []byte(content), 0o600) == nil }
	if !write(log+".args", strings.Join(args[2:], "\n")) || !write(log+".dir", dir) {
		return 70
	}
	email := os.Getenv("FAKE_CLAUDE_EMAIL")
	account := fmt.Sprintf(`{"oauthAccount":{"emailAddress":"%s","organizationUuid":"","accountUuid":"uuid-%s"}}`, email, email)
	creds := ""
	switch os.Getenv("FAKE_CLAUDE_MODE") {
	case "fail":
		return 1
	case "keychain":
		// macOS: the credential goes to the Keychain, not a file.
	case "apikey":
		creds = "sk-ant-api03-test-key-1"
	default:
		account = fmt.Sprintf(`{"oauthAccount":{"emailAddress":"%s","organizationUuid":"%s","organizationName":"%s","accountUuid":"uuid-%s"}}`,
			email, os.Getenv("FAKE_CLAUDE_ORG"), os.Getenv("FAKE_CLAUDE_ORGNAME"), email)
		creds = fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"%s","refreshToken":"%s","expiresAt":4102444800000,"scopes":["user:inference"]}}`,
			os.Getenv("FAKE_CLAUDE_TOKEN"), os.Getenv("FAKE_CLAUDE_REFRESH"))
	}
	if !write(filepath.Join(dir, ".claude.json"), account) {
		return 70
	}
	if creds != "" && !write(filepath.Join(dir, ".credentials.json"), creds) {
		return 70
	}
	return 0
}

// loginFixture is a home with one managed, live account (a@example.com in
// slot 1) and a fake claude first on PATH, in bin, that logs in as
// b@example.com.
type loginFixture struct {
	home, log, bin string
	out            *bytes.Buffer
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	cleanHome(t)
	home := os.Getenv("HOME")

	bin := filepath.Join(t.TempDir(), "bin")
	installFakeClaude(t, bin, "claude-login")
	testutil.Setenv(t, "PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(t.TempDir(), "claude")
	testutil.Setenv(t, "FAKE_CLAUDE_LOG", log)
	testutil.Setenv(t, "FAKE_CLAUDE_MODE", "ok")
	testutil.Setenv(t, "FAKE_CLAUDE_EMAIL", "b@example.com")
	testutil.Setenv(t, "FAKE_CLAUDE_ORG", "")
	testutil.Setenv(t, "FAKE_CLAUDE_ORGNAME", "")
	testutil.Setenv(t, "FAKE_CLAUDE_TOKEN", "sk-ant-oat01-test-token-2")
	testutil.Setenv(t, "FAKE_CLAUDE_REFRESH", "refresh-token-2")

	// No network: the switcher gets no OAuth client, and the home's fake
	// Keychain.
	prevNew := newSwitcher
	newSwitcher = func(opts store.Options) (*core.Switcher, error) {
		opts.OAuth, opts.Keychain = nil, homeKeychain()
		return core.New(opts)
	}
	t.Cleanup(func() { newSwitcher = prevNew })

	out := &bytes.Buffer{}
	prevOut := lifecycle.Output
	lifecycle.Output = out
	t.Cleanup(func() { lifecycle.Output = prevOut })

	f := &loginFixture{home: home, log: log, bin: bin, out: out}
	f.writeLive(t, "a@example.com", "sk-ant-oat01-test-token-1", "refresh-token-1")
	if code, _, errb := f.run(t, "add"); code != 0 {
		t.Fatalf("seeding add: exit %d, stderr %q", code, errb)
	}
	return f
}

func (f *loginFixture) writeLive(t *testing.T, email, token, refresh string) {
	t.Helper()
	cfg := `{"oauthAccount":{"emailAddress":"` + email + `","organizationUuid":"","accountUuid":"uuid-` + email + `"}}`
	if err := os.WriteFile(filepath.Join(f.home, ".claude.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	creds := `{"claudeAiOauth":{"accessToken":"` + token + `","refreshToken":"` + refresh + `","expiresAt":4102444800000,"scopes":["user:inference"]}}`
	if err := os.MkdirAll(filepath.Join(f.home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, ".claude", ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *loginFixture) run(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	f.out.Reset()
	var out, errb bytes.Buffer
	code := run("tycswap", argv, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
	return code, f.out.String() + out.String(), errb.String()
}

// sequence is sequence.json as the test needs it.
type sequence struct {
	Active   *int                         `json:"activeAccountNumber"`
	Sequence []int                        `json:"sequence"`
	Accounts map[string]map[string]string `json:"accounts"`
}

func (f *loginFixture) sequence(t *testing.T) sequence {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(paths.GetBackupRoot(), "sequence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var seq sequence
	if err := json.Unmarshal(raw, &seq); err != nil {
		t.Fatalf("sequence.json: %v", err)
	}
	return seq
}

func (f *loginFixture) storedCreds(t *testing.T, num, email string) string {
	t.Helper()
	sw, err := core.New(store.Options{Keychain: homeKeychain()})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := sw.Store.ReadAccountCredentials(num, email)
	if err != nil {
		t.Fatal(err)
	}
	return creds
}

func (f *loginFixture) liveCreds(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.home, ".claude", ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// loginDirArg is what the fake claude recorded as its CLAUDE_CONFIG_DIR.
func (f *loginFixture) loginDirArg(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(f.log + ".dir")
	if err != nil {
		t.Fatalf("fake claude never ran: %v", err)
	}
	return string(raw)
}

func (f *loginFixture) loginArgs(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(f.log + ".args")
	if err != nil {
		t.Fatalf("fake claude never ran: %v", err)
	}
	return strings.Split(string(raw), "\n")
}

// assertNoScratch fails if any login.* scratch profile survived.
func assertNoScratch(t *testing.T) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(paths.GetBackupRoot(), "login.*"))
	if len(left) != 0 {
		t.Errorf("scratch profile(s) left behind: %v", left)
	}
}

func TestAddLoginStoresTheNewAccountAndLeavesTheLiveOneAlone(t *testing.T) {
	f := newLoginFixture(t)
	liveBefore := f.liveCreds(t)

	code, out, errb := f.run(t, "add", "--login", "--", "--email", "b@example.com", "--sso", "two words")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if !strings.Contains(out, "Added Account 2: b@example.com [personal] (from login)") {
		t.Errorf("output = %q", out)
	}
	if got, want := f.loginArgs(t), []string{"--claudeai", "--email", "b@example.com", "--sso", "two words"}; !slices.Equal(got, want) {
		t.Errorf("claude auth login got passthrough %q, want %q", got, want)
	}
	if dir := f.loginDirArg(t); filepath.Dir(dir) != paths.GetBackupRoot() || !strings.HasPrefix(filepath.Base(dir), "login.") {
		t.Errorf("login ran with CLAUDE_CONFIG_DIR=%q, want a login.* scratch under %q", dir, paths.GetBackupRoot())
	}
	assertNoScratch(t)

	seq := f.sequence(t)
	if seq.Active == nil || *seq.Active != 1 {
		t.Errorf("activeAccountNumber = %v, want 1 (the live login did not change)", seq.Active)
	}
	if got := seq.Accounts["2"]["email"]; got != "b@example.com" {
		t.Errorf("slot 2 = %v", seq.Accounts["2"])
	}
	if got := f.storedCreds(t, "2", "b@example.com"); !strings.Contains(got, "sk-ant-oat01-test-token-2") {
		t.Errorf("stored credential for slot 2 = %q", got)
	}
	if f.liveCreds(t) != liveBefore {
		t.Errorf("the live credential changed")
	}
	cfg, _ := os.ReadFile(filepath.Join(f.home, ".claude.json"))
	if !strings.Contains(string(cfg), "a@example.com") {
		t.Errorf("the live config changed: %s", cfg)
	}
}

func TestAddLoginSwitchMakesTheNewAccountLive(t *testing.T) {
	f := newLoginFixture(t)

	code, _, errb := f.run(t, "add", "--login", "--switch")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	assertNoScratch(t)
	seq := f.sequence(t)
	if seq.Active == nil || *seq.Active != 2 {
		t.Errorf("activeAccountNumber = %v, want 2", seq.Active)
	}
	if live := f.liveCreds(t); !strings.Contains(live, "sk-ant-oat01-test-token-2") {
		t.Errorf("live credential after --switch = %q", live)
	}
	cfg, _ := os.ReadFile(filepath.Join(f.home, ".claude.json"))
	if !strings.Contains(string(cfg), "b@example.com") {
		t.Errorf("live config after --switch: %s", cfg)
	}
	if got := f.storedCreds(t, "1", "a@example.com"); !strings.Contains(got, "sk-ant-oat01-test-token-1") {
		t.Errorf("outgoing account 1 backup = %q", got)
	}
}

func TestAddLoginOverridesTheParentsConfigDir(t *testing.T) {
	f := newLoginFixture(t)
	custom := filepath.Join(t.TempDir(), "custom-profile")
	if err := os.MkdirAll(custom, 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.Setenv(t, "CLAUDE_CONFIG_DIR", custom)

	code, _, errb := f.run(t, "add", "--login")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if dir := f.loginDirArg(t); dir == custom || !strings.HasPrefix(filepath.Base(dir), "login.") {
		t.Errorf("login ran with CLAUDE_CONFIG_DIR=%q, want the scratch, not the parent's %q", dir, custom)
	}
	if entries, _ := os.ReadDir(custom); len(entries) != 0 {
		t.Errorf("the login wrote into the parent's profile: %v", entries)
	}
	if os.Getenv("CLAUDE_CONFIG_DIR") != custom {
		t.Errorf("the parent's own CLAUDE_CONFIG_DIR was changed")
	}
	assertNoScratch(t)
}

func TestAddLoginRefreshesAnAlreadyStoredIdentity(t *testing.T) {
	f := newLoginFixture(t)
	testutil.Setenv(t, "FAKE_CLAUDE_EMAIL", "a@example.com")
	testutil.Setenv(t, "FAKE_CLAUDE_TOKEN", "sk-ant-oat01-test-token-3")
	testutil.Setenv(t, "FAKE_CLAUDE_REFRESH", "refresh-token-3")

	code, out, errb := f.run(t, "add", "--login")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if !strings.Contains(out, "Updated credentials for Account 1 (a@example.com [personal]).") {
		t.Errorf("output = %q", out)
	}
	seq := f.sequence(t)
	if len(seq.Sequence) != 1 || seq.Active == nil || *seq.Active != 1 {
		t.Errorf("sequence = %v active = %v, want one account, active 1", seq.Sequence, seq.Active)
	}
	if got := f.storedCreds(t, "1", "a@example.com"); !strings.Contains(got, "sk-ant-oat01-test-token-3") {
		t.Errorf("slot 1 was not refreshed: %q", got)
	}
	assertNoScratch(t)
}

func TestAddLoginAppliesAliasAndSlot(t *testing.T) {
	f := newLoginFixture(t)

	code, out, errb := f.run(t, "add", "--login", "--alias", "Work", "--slot", "5")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if !strings.Contains(out, "Added Account 5: b@example.com") {
		t.Errorf("output = %q", out)
	}
	seq := f.sequence(t)
	if rec := seq.Accounts["5"]; rec["email"] != "b@example.com" || rec["alias"] != "work" {
		t.Errorf("slot 5 = %v", rec)
	}
	if seq.Active == nil || *seq.Active != 1 {
		t.Errorf("activeAccountNumber = %v, want 1", seq.Active)
	}
	assertNoScratch(t)
}

func TestAddLoginFailureStoresNothing(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"fail", "Error: claude's login did not complete; nothing stored, the live login untouched"},
		{"apikey", "Error: claude's login made an API key, which is a different auth axis: use tycswap --add-token"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newLoginFixture(t)
			testutil.Setenv(t, "FAKE_CLAUDE_MODE", tc.mode)
			before := f.sequence(t)

			code, _, errb := f.run(t, "add", "--login", "--switch")
			if code != 1 {
				t.Fatalf("exit %d, want 1 (stderr %q)", code, errb)
			}
			if !strings.Contains(errb, tc.want) {
				t.Errorf("stderr = %q, want %q", errb, tc.want)
			}
			after := f.sequence(t)
			if len(after.Accounts) != len(before.Accounts) || after.Active == nil || *after.Active != 1 {
				t.Errorf("roster changed: %v active %v", after.Accounts, after.Active)
			}
			if !strings.Contains(f.liveCreds(t), "sk-ant-oat01-test-token-1") {
				t.Errorf("the live login changed")
			}
			assertNoScratch(t)
		})
	}
}

func TestAddRefusesLoginOnlyArguments(t *testing.T) {
	cleanHome(t)
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"add", "--switch"}, "Error: --switch goes with --login: the live login is already the one add stores"},
		{[]string{"add", "--slot", "2", "--switch"}, "Error: --switch goes with --login: the live login is already the one add stores"},
		{[]string{"add", "--", "--sso"}, "Error: arguments after -- go with --login: they are claude's login arguments"},
		{[]string{"--add-account", "--", "--sso"}, "Error: arguments after -- go with --login: they are claude's login arguments"},
	} {
		var out, errb bytes.Buffer
		code := run("tycswap", tc.argv, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
		if code != 1 || !strings.Contains(errb.String(), tc.want) {
			t.Errorf("%v: exit %d stderr %q, want 1 and %q", tc.argv, code, errb.String(), tc.want)
		}
	}
	assertNoScratch(t)
}

func TestAddLoginKeepsAddsFlagRefusals(t *testing.T) {
	cleanHome(t)
	var out, errb bytes.Buffer
	code := run("tycswap", []string{"add", "--login", "--json"}, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
	if code != 2 || !strings.Contains(errb.String(), "--json can only be used with") {
		t.Errorf("exit %d stderr %q, want add's --json refusal", code, errb.String())
	}
}

func TestLoginEnvReplacesConfigDir(t *testing.T) {
	got := loginEnv([]string{"A=1", "CLAUDE_CONFIG_DIR=/pinned", "B=2"}, "/scratch")
	want := []string{"A=1", "B=2", "CLAUDE_CONFIG_DIR=/scratch"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("loginEnv = %v, want %v", got, want)
	}
}

func TestSplitAddArgs(t *testing.T) {
	a := splitAddArgs([]string{"--login", "--alias", "w", "--switch", "--", "--switch", "--email", "x"})
	if !a.login || !a.switchAfter || strings.Join(a.rest, " ") != "--alias w" || strings.Join(a.tail, " ") != "--switch --email x" {
		t.Errorf("splitAddArgs = %+v", a)
	}
}

// loginKeychainFake stands in for the macOS Keychain a scratch login writes
// to. It holds one credential, served only under the service Claude Code
// derives from the CLAUDE_CONFIG_DIR the fake claude recorded — so a read
// keyed by any other dir finds nothing — and records every delete.
type loginKeychainFake struct {
	keychain.Fake
	log, value string

	mu      sync.Mutex
	deletes []string
}

func (k *loginKeychainFake) Get(service, account string) (string, bool, error) {
	dir, err := os.ReadFile(k.log + ".dir")
	if err != nil || k.value == "" || account != keychain.AccountName() ||
		service != lifecycle.LoginKeychainService(string(dir)) {
		return "", false, nil
	}
	return k.value, true, nil
}

func (k *loginKeychainFake) Delete(service, account string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.deletes = append(k.deletes, service)
	return nil
}

func (k *loginKeychainFake) deleted() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.deletes...)
}

// useLoginKeychain makes the login's Keychain seam return kc, as on macOS.
func useLoginKeychain(t *testing.T, kc keychain.KeychainClient) {
	t.Helper()
	prev := loginKeychain
	loginKeychain = func() keychain.KeychainClient { return kc }
	t.Cleanup(func() { loginKeychain = prev })
}

// assertScratchItemDeleted checks the scratch dir's Keychain item was deleted.
func assertScratchItemDeleted(t *testing.T, f *loginFixture, kc *loginKeychainFake) {
	t.Helper()
	want := lifecycle.LoginKeychainService(f.loginDirArg(t))
	for _, got := range kc.deleted() {
		if got == want {
			return
		}
	}
	t.Errorf("Keychain deletes = %v, want %q (the scratch dir's item)", kc.deleted(), want)
}

func TestAddLoginReadsTheKeychainWhenNoCredentialFile(t *testing.T) {
	f := newLoginFixture(t)
	testutil.Setenv(t, "FAKE_CLAUDE_MODE", "keychain")
	kc := &loginKeychainFake{log: f.log,
		value: `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-test-token-4","refreshToken":"refresh-token-4"}}`}
	useLoginKeychain(t, kc)

	code, out, errb := f.run(t, "add", "--login")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if !strings.Contains(out, "Added Account 2: b@example.com [personal] (from login)") {
		t.Errorf("output = %q", out)
	}
	if got := f.storedCreds(t, "2", "b@example.com"); !strings.Contains(got, "sk-ant-oat01-test-token-4") {
		t.Errorf("stored credential = %q, want the Keychain item's", got)
	}
	assertScratchItemDeleted(t, f, kc)
	assertNoScratch(t)
}

func TestAddLoginKeychainFailuresStoreNothingAndDeleteTheItem(t *testing.T) {
	for _, tc := range []struct{ name, mode, value string }{
		{"no item", "keychain", ""},
		{"login failed", "fail", `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-test-token-5"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoginFixture(t)
			testutil.Setenv(t, "FAKE_CLAUDE_MODE", tc.mode)
			kc := &loginKeychainFake{log: f.log, value: tc.value}
			useLoginKeychain(t, kc)

			code, _, errb := f.run(t, "add", "--login")
			want := "Error: claude's login did not complete; nothing stored, the live login untouched"
			if code != 1 || !strings.Contains(errb, want) {
				t.Fatalf("exit %d stderr %q, want 1 and %q", code, errb, want)
			}
			if seq := f.sequence(t); len(seq.Accounts) != 1 {
				t.Errorf("roster changed: %v", seq.Accounts)
			}
			assertScratchItemDeleted(t, f, kc)
			assertNoScratch(t)
		})
	}
}

// TestLoginScratchCleanupOnInterrupt: the SIGINT path (lifecycle.RunCleanups)
// deletes the scratch's Keychain item and the directory, and a later remove
// does neither again.
func TestLoginScratchCleanupOnInterrupt(t *testing.T) {
	root := t.TempDir()
	kc := &loginKeychainFake{}
	dir, remove, err := makeLoginScratch(root, kc)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700) {
		t.Fatalf("scratch %q: %v %v", dir, fi, err)
	}
	lifecycle.RunCleanups()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("scratch survived the interrupt cleanup: %v", err)
	}
	if got := kc.deleted(); len(got) != 1 || got[0] != lifecycle.LoginKeychainService(dir) {
		t.Errorf("Keychain deletes = %v", got)
	}
	remove()
	if got := kc.deleted(); len(got) != 1 {
		t.Errorf("remove after the cleanup deleted again: %v", got)
	}
}

// TestAddLoginScrubsAuthOverrides: the variables that make claude skip the
// account login are removed from the login's environment, as run/env do.
func TestAddLoginScrubsAuthOverrides(t *testing.T) {
	env := loginEnv(session.ScrubAuthOverrides([]string{"A=1", "ANTHROPIC_API_KEY=sk", "CLAUDE_CODE_OAUTH_TOKEN=t", "B=2"}), "/scratch")
	for _, kv := range env {
		for _, v := range session.AuthOverrideEnvVars {
			if strings.HasPrefix(kv, v+"=") {
				t.Errorf("%s survived: %v", v, env)
			}
		}
	}
	if len(env) != 3 || env[2] != "CLAUDE_CONFIG_DIR=/scratch" {
		t.Errorf("env = %v", env)
	}
}

// TestAddLoginSwitchKeepsTheLiveMCPLogins: the scratch profile a login runs in
// never has mcpOAuth, so activating the new account used to wipe the seat's MCP
// server logins. The switch carries the live mcpOAuth over the new account.
func TestAddLoginSwitchKeepsTheLiveMCPLogins(t *testing.T) {
	f := newLoginFixture(t)
	live := `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-test-token-1","refreshToken":"refresh-token-1","expiresAt":4102444800000,"scopes":["user:inference"]},"mcpOAuth":{"srv|1111":{"accessToken":"mcp-live","refreshToken":"mcp-live-refresh"}}}`
	if err := os.WriteFile(filepath.Join(f.home, ".claude", ".credentials.json"), []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, errb := f.run(t, "add", "--login", "--switch")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	after := f.liveCreds(t)
	if !strings.Contains(after, "sk-ant-oat01-test-token-2") {
		t.Errorf("live credential after --switch = %q, want the new account", after)
	}
	if !strings.Contains(after, `"mcp-live"`) {
		t.Errorf("live credential after --switch lost the MCP server login: %q", after)
	}
	if got := f.storedCreds(t, "2", "b@example.com"); strings.Contains(got, "mcp-live") {
		t.Errorf("stored credential for slot 2 carries the seat's MCP login: %q", got)
	}
}
