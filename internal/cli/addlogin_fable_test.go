// Tests for `add --login --lineage fable` (DESIGN A101) and the renewal
// decision from the resolved live slot.
package cli

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

// countLogins counts the claude logins started and records what the user had
// been shown when each started.
func countLogins(t *testing.T, f *loginFixture) *[]string {
	t.Helper()
	var shown []string
	prev := startClaudeLogin
	startClaudeLogin = func(binary string, args, env []string, s ioStreams) (*loginProcess, error) {
		text := f.out.String()
		if b, ok := s.out.(*bytes.Buffer); ok {
			text += b.String()
		}
		shown = append(shown, text)
		return prev(binary, args, env, s)
	}
	t.Cleanup(func() { startClaudeLogin = prev })
	return &shown
}

// onPlatform builds every switcher of the test for plat.
func onPlatform(t *testing.T, plat platform.Platform) {
	t.Helper()
	prev := newSwitcher
	newSwitcher = func(opts store.Options) (*core.Switcher, error) {
		sw, err := prev(opts)
		if sw != nil {
			sw.Store.Platform = plat
		}
		return sw, err
	}
	t.Cleanup(func() { newSwitcher = prev })
}

// newFableLoginFixture: Account-1 (a@example.com, live) and Account-2
// (b@example.com), both on a max plan, no Fable login; the fake claude signs
// in with a new grant.
func newFableLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	f := newLoginFixture(t)
	onPlatform(t, platform.Linux)
	creds := `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-test-token-1","refreshToken":"refresh-token-1","expiresAt":4102444800000,"scopes":["user:inference"],"subscriptionType":"max"}}`
	if err := os.WriteFile(filepath.Join(f.home, ".claude", ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.Setenv(t, "FAKE_CLAUDE_SUBSCRIPTION", "max")
	for _, argv := range [][]string{{"add"}, {"add", "--login"}} {
		if code, _, errb := f.run(t, argv...); code != 0 {
			t.Fatalf("%v: exit %d, stderr %q", argv, code, errb)
		}
	}
	signInAs(t, "a@example.com", "fable-1")
	return f
}

func signInAs(t *testing.T, email, grant string) {
	t.Helper()
	testutil.Setenv(t, "FAKE_CLAUDE_EMAIL", email)
	testutil.Setenv(t, "FAKE_CLAUDE_TOKEN", "access-"+grant)
	testutil.Setenv(t, "FAKE_CLAUDE_REFRESH", "refresh-"+grant)
}

// defaultFiles is every file of the default login: the roster, the default
// backups and the home's Claude Code files.
func defaultFiles(t *testing.T, home string) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := paths.GetBackupRoot()
	for _, p := range []string{filepath.Join(root, "sequence.json"), filepath.Join(root, "credentials"), filepath.Join(root, "configs"),
		filepath.Join(home, ".claude", ".credentials.json"), filepath.Join(home, ".claude.json")} {
		_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				raw, _ := os.ReadFile(path)
				out[path] = string(raw)
			}
			return nil
		})
	}
	return out
}

func fableState(t *testing.T) store.FableState {
	t.Helper()
	sw, err := newSwitcher(store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return sw.Store.FableStore()
}

func TestAddLoginLineageRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		plat platform.Platform
		argv []string
		code int
		want string
	}{
		{"unknown value", platform.Linux, []string{"add", "--login", "--lineage", "other"}, 2, "tycswap: error: unknown lineage: other (default, fable or mythos)"},
		{"unknown value, = form", platform.Linux, []string{"add", "--login", "--lineage=other"}, 2, "tycswap: error: unknown lineage: other (default, fable or mythos)"},
		{"no value", platform.Linux, []string{"add", "--login", "--lineage"}, 2, "tycswap: error: argument --lineage: expected one argument"},
		{"without --login", platform.Linux, []string{"add", "--lineage", "fable"}, 1, "Error: --lineage goes with --login"},
		{"without --login, = form", platform.Linux, []string{"add", "--lineage=default"}, 1, "Error: --lineage goes with --login"},
		{"macOS", platform.MacOS, []string{"add", "--login", "--lineage", "fable"}, 1, "Error: The Fable store needs a file-based credential store; macOS keeps Claude Code's login in the Keychain"},
		{"an alias", platform.Linux, []string{"add", "--login", "--lineage=fable", "--alias", "w"}, 1, "Error: --alias names a new account; a Fable login is stored for an account already added"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFableLoginFixture(t)
			onPlatform(t, tc.plat)
			started := countLogins(t, f)
			before := defaultFiles(t, f.home)
			code, _, errb := f.run(t, tc.argv...)
			if code != tc.code || !strings.HasSuffix(strings.TrimSpace(errb), tc.want) {
				t.Fatalf("exit %d stderr %q, want %d and %q", code, errb, tc.code, tc.want)
			}
			if len(*started) != 0 {
				t.Errorf("claude's login started %d times", len(*started))
			}
			if after := defaultFiles(t, f.home); !sameStrings(before, after) {
				t.Error("the refusal changed the default login's files")
			}
			if _, err := os.Stat(paths.FableCredentialsDir(paths.GetBackupRoot())); !os.IsNotExist(err) {
				t.Errorf("the refusal created credentials-fable/ (%v)", err)
			}
		})
	}
}

// sortedLines is out's lines, sorted and joined with |: the store's lines and
// the command's own go to two writers in the tests.
func sortedLines(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "|")
}

func sameStrings(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestAddLoginLineageDefaultIsTodaysAdd(t *testing.T) {
	f := newLoginFixture(t)
	code, out, errb := f.run(t, "add", "--login", "--lineage", "default")
	if code != 0 || !strings.Contains(out, "Added Account 2: b@example.com [personal] (from login)") {
		t.Fatalf("exit %d out %q stderr %q", code, out, errb)
	}
	if _, err := os.Stat(paths.FableCredentialsDir(paths.GetBackupRoot())); !os.IsNotExist(err) {
		t.Errorf("credentials-fable/ exists (%v)", err)
	}
}

// The first Fable login turns the store on with a switch by hand; the default
// login's files stay byte-identical.
func TestAddFableLoginFirstTurnsTheStoreOn(t *testing.T) {
	f := newFableLoginFixture(t)
	started := countLogins(t, f)
	before := defaultFiles(t, f.home)

	code, out, errb := f.run(t, "add", "--login", "--lineage", "fable", "--slot", "1")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if got := sortedLines(out); got != "Sign in as a@example.com in the browser.|Stored the Fable login for Account-1 (a@example.com).|The Fable store now uses Account-1." {
		t.Errorf("output = %q", out)
	}
	if len(*started) != 1 {
		t.Fatalf("claude's login started %d times", len(*started))
	}
	if !sameStrings(before, defaultFiles(t, f.home)) {
		t.Error("the default login's files changed")
	}
	st := fableState(t)
	if !st.On || !st.Usable || st.Live.Slot != "1" {
		t.Fatalf("FableStore = %+v, want on with Account-1", st)
	}
	raw, err := os.ReadFile(filepath.Join(paths.FableScopeDir(paths.GetBackupRoot()), "autoswitch_state.json"))
	var state map[string]any
	if err != nil || json.Unmarshal(raw, &state) != nil || state["lastSwitchBy"] != "hand" || state["lastSwitchTo"] != "1" {
		t.Errorf("Fable state = %s (%v), want the hold of a switch by hand to 1", raw, err)
	}
	assertNoScratch(t)
}

func TestAddFableLoginSecondSwitchesOnlyWithSwitch(t *testing.T) {
	f := newFableLoginFixture(t)
	if code, _, errb := f.run(t, "add", "--login", "--lineage=fable"); code != 0 {
		t.Fatalf("first: exit %d, stderr %q", code, errb)
	}
	signInAs(t, "b@example.com", "fable-2")
	code, out, errb := f.run(t, "add", "--login", "--lineage=fable")
	if code != 0 || out != "Stored the Fable login for Account-2 (b@example.com).\n" {
		t.Fatalf("second: exit %d out %q stderr %q", code, out, errb)
	}
	if st := fableState(t); st.Live.Slot != "1" {
		t.Fatalf("after a second login the store is on %+v, want Account-1", st.Live)
	}
	signInAs(t, "b@example.com", "fable-2b")
	code, out, errb = f.run(t, "add", "--login", "--lineage=fable", "--switch")
	if code != 0 || sortedLines(out) != "Stored the Fable login for Account-2 (b@example.com).|The Fable store now uses Account-2." {
		t.Fatalf("--switch: exit %d out %q stderr %q", code, out, errb)
	}
	if st := fableState(t); st.Live.Slot != "2" {
		t.Fatalf("after --switch the store is on %+v, want Account-2", st.Live)
	}
}

func TestAddFableLoginNamesTheAccountBeforeTheBrowser(t *testing.T) {
	f := newFableLoginFixture(t)
	shown := countLogins(t, f)
	signInAs(t, "b@example.com", "fable-2")
	if code, _, errb := f.run(t, "add", "--login", "--lineage", "fable", "--slot", "2"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if len(*shown) != 1 || (*shown)[0] != "Sign in as b@example.com in the browser.\n" {
		t.Fatalf("shown before the login started: %q", *shown)
	}

	signInAs(t, "a@example.com", "fable-1")
	if code, _, errb := f.run(t, "add", "--login", "--lineage", "fable"); code != 0 {
		t.Fatalf("without --slot: exit %d, stderr %q", code, errb)
	}
	if len(*shown) != 2 || (*shown)[1] != "" {
		t.Fatalf("without --slot, shown before the login: %q", (*shown)[1:])
	}

	code, _, errb := f.run(t, "add", "--login", "--lineage", "fable", "--slot", "7")
	if code != 1 || !strings.Contains(errb, "Account-7 does not exist") || len(*shown) != 2 {
		t.Fatalf("an empty slot: exit %d stderr %q, %d logins", code, errb, len(*shown))
	}
}

// A Fable session wrote Account-2 into ~/.claude.json while the default
// credential is still Account-1's: a default `add --login` of Account-2
// stores it and leaves the default login on Account-1 (DESIGN A101).
func TestAddLoginDecidesRenewalFromTheResolvedLiveSlot(t *testing.T) {
	f := newLoginFixture(t)
	if code, _, errb := f.run(t, "add", "--login"); code != 0 {
		t.Fatalf("seeding Account-2: exit %d, stderr %q", code, errb)
	}
	cfg := `{"oauthAccount":{"emailAddress":"b@example.com","organizationUuid":"","accountUuid":"uuid-b@example.com"}}`
	if err := os.WriteFile(filepath.Join(f.home, ".claude.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	liveBefore := f.liveCreds(t)
	renewAs(t, "b@example.com")

	code, _, errb := f.run(t, "add", "--login")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if got := f.storedCreds(t, "2", "b@example.com"); !strings.Contains(got, "renewed-access") {
		t.Errorf("Account-2's backup = %q, want the new login", got)
	}
	if f.liveCreds(t) != liveBefore {
		t.Errorf("the default login was switched: %q", f.liveCreds(t))
	}
	if seq := f.sequence(t); seq.Active == nil || *seq.Active != 1 {
		t.Errorf("activeAccountNumber = %v, want 1", seq.Active)
	}
}
