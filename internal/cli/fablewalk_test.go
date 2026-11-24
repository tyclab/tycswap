// Tests for `tycswap fable --login` (DESIGN A114): the Fable sign-in of every
// account without a usable Fable login, one after another, and the menu's
// entry for it.
package cli

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

const (
	walkPrompt = "Enter to open the browser, s to skip, q to stop: "
	walkAgain  = "Run tycswap fable --login again for the rest.\n"
	walkCtrlC  = "Fable sign-ins stopped. Run tycswap fable --login again for the rest."
)

// newFableWalkFixture is newFableLoginFixture plus Account-3
// (c@example.com): three accounts on a max plan without a Fable login.
func newFableWalkFixture(t *testing.T) *loginFixture {
	t.Helper()
	f := newFableLoginFixture(t)
	addByLogin(t, f, "c@example.com", "default-3")
	t.Cleanup(func() { setSigintNote("") })
	return f
}

func addByLogin(t *testing.T, f *loginFixture, email, grant string) {
	t.Helper()
	signInAs(t, email, grant)
	if code, _, errb := f.run(t, "add", "--login"); code != 0 {
		t.Fatalf("adding %s: exit %d, stderr %q", email, code, errb)
	}
}

// signInForFable stores num's Fable login with add --login --lineage fable.
func signInForFable(t *testing.T, f *loginFixture, num, email string) {
	t.Helper()
	signInAs(t, email, "fable-"+num)
	if code, _, errb := f.run(t, "add", "--login", "--lineage", "fable", "--slot", num); code != 0 {
		t.Fatalf("Fable sign-in of Account-%s: exit %d, stderr %q", num, code, errb)
	}
}

// walk runs `tycswap fable --login` with stdin's answers, on a terminal or not.
func (f *loginFixture) walk(t *testing.T, stdin string, terminal bool) (int, string, string) {
	t.Helper()
	testutil.Setenv(t, "NO_COLOR", "1")
	f.out.Reset()
	var out, errb bytes.Buffer
	code := run("tycswap", []string{"fable", "--login"}, ioStreams{in: strings.NewReader(stdin), out: &out, err: &errb}, terminal, terminal)
	return code, out.String(), errb.String()
}

var signInAsLine = regexp.MustCompile(`Sign in as (\S+) in the browser\.`)

type walkLogins struct{ emails, notes []string }

// walkSignIns makes each claude login sign in, with a grant of its own, as the
// account its sign-in named ("Sign in as <email> in the browser."), as
// wrong[that email] instead, or fail where that is "fail". It records the
// emails signed in as and the Ctrl-C note at each start, and fails the test
// when a login starts while an earlier one's scratch profile is still there.
// The fake gets no stdin, so the walk's answers stay with the walk.
func walkSignIns(t *testing.T, wrong map[string]string) *walkLogins {
	t.Helper()
	rec := &walkLogins{}
	var scratches []string
	prev := startClaudeLogin
	startClaudeLogin = func(binary string, args, env []string, s ioStreams) (*loginProcess, error) {
		for _, dir := range scratches {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("sign-in %d started while %s was still there (%v)", len(scratches)+1, dir, err)
			}
		}
		scratches = append(scratches, envValue(env, "CLAUDE_CONFIG_DIR"))
		named := signInAsLine.FindAllStringSubmatch(s.out.(*bytes.Buffer).String(), -1)
		if len(named) == 0 {
			t.Fatal("a claude login started before its account was named")
		}
		email := named[len(named)-1][1]
		if as, ok := wrong[email]; ok {
			email = as
		}
		mode := "ok"
		if email == "fail" {
			mode = "fail"
		}
		rec.emails = append(rec.emails, email)
		rec.notes = append(rec.notes, currentSigintNote())
		grant := "walk-" + strconv.Itoa(len(rec.emails))
		for key, value := range map[string]string{"FAKE_CLAUDE_EMAIL": email, "FAKE_CLAUDE_MODE": mode,
			"FAKE_CLAUDE_TOKEN": "access-" + grant, "FAKE_CLAUDE_REFRESH": "refresh-" + grant} {
			env = setVar(env, key, key, value)
		}
		return prev(binary, args, env, ioStreams{in: strings.NewReader(""), out: s.out, err: s.err})
	}
	t.Cleanup(func() { startClaudeLogin = prev })
	return rec
}

// inOrder fails unless each part appears in out after the one before it.
func inOrder(t *testing.T, out string, parts ...string) {
	t.Helper()
	rest := out
	for _, part := range parts {
		i := strings.Index(rest, part)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", part, out)
		}
		rest = rest[i+len(part):]
	}
}

// fableLoginSlots is those of nums that hold a Fable login.
func fableLoginSlots(t *testing.T, nums ...string) []string {
	t.Helper()
	sw, err := newSwitcher(store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var held []string
	for _, num := range nums {
		if sw.Store.HasFableLogin(num) {
			held = append(held, num)
		}
	}
	return held
}

var walkHeader = regexp.MustCompile(`(?m)^Fable sign-in .*$`)

// Three accounts without a Fable login: asked in roster order, each signed in
// by the single Fable sign-in for its slot, one after the other.
func TestFableLoginWalkSignsInEachAccountInTurn(t *testing.T) {
	f := newFableWalkFixture(t)
	logins := walkSignIns(t, nil)
	code, out, errb := f.walk(t, "\n\n\n", true)
	if code != 0 || strings.Contains(errb, "Error") {
		t.Fatalf("exit %d, stderr %q, out:\n%s", code, errb, out)
	}
	if want := []string{"a@example.com", "b@example.com", "c@example.com"}; !reflect.DeepEqual(logins.emails, want) {
		t.Fatalf("signed in as %q, want %q", logins.emails, want)
	}
	inOrder(t, out,
		"Fable sign-in 1 of 3: a@example.com (Account-1).\n"+walkPrompt, "Sign in as a@example.com in the browser.",
		"Fable sign-in 2 of 3: b@example.com (Account-2).\n"+walkPrompt, "Sign in as b@example.com in the browser.",
		"Fable sign-in 3 of 3: c@example.com (Account-3).\n"+walkPrompt, "Sign in as c@example.com in the browser.",
		"Signed in for Fable: a@example.com (Account-1), b@example.com (Account-2), c@example.com (Account-3).\n")
	if strings.Contains(out, "Skipped") || strings.Contains(out, "Failed") || strings.Contains(out, walkAgain) {
		t.Fatalf("summary of a complete walk:\n%s", out)
	}
	if got := fableLoginSlots(t, "1", "2", "3"); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("Fable logins stored for %v", got)
	}
	for _, note := range logins.notes {
		if note != walkCtrlC {
			t.Fatalf("Ctrl-C during a sign-in would say %q", note)
		}
	}
	assertNoScratch(t)
}

// s skips an account, q stops the walk: no sign-in for the rest, which the
// summary names with the line to go on.
func TestFableLoginWalkSkipsAndStops(t *testing.T) {
	f := newFableWalkFixture(t)
	logins := walkSignIns(t, nil)
	code, out, errb := f.walk(t, "\ns\nq\n", true)
	if code != 0 || !reflect.DeepEqual(logins.emails, []string{"a@example.com"}) {
		t.Fatalf("exit %d, signed in as %q, stderr %q", code, logins.emails, errb)
	}
	inOrder(t, out, "Fable sign-in 1 of 3", "Sign in as a@example.com",
		"Fable sign-in 2 of 3: b@example.com (Account-2).\n"+walkPrompt,
		"Fable sign-in 3 of 3: c@example.com (Account-3).\n"+walkPrompt,
		"Signed in for Fable: a@example.com (Account-1).\n"+
			"Skipped: b@example.com (Account-2), c@example.com (Account-3).\n"+walkAgain)
	if !strings.HasSuffix(out, walkAgain) || strings.Contains(out, "Sign in as b@") || strings.Contains(out, "Sign in as c@") {
		t.Fatalf("out:\n%s", out)
	}
	if got := fableLoginSlots(t, "1", "2", "3"); !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("Fable logins stored for %v", got)
	}
}

// A sign-in that names the wrong account or does not complete prints its
// reason; the next account is still asked, and the walk exits 1.
func TestFableLoginWalkGoesOnAfterAFailedSignIn(t *testing.T) {
	for _, tc := range []struct {
		name, as, reason string
		emails           []string
	}{
		{"the wrong account", "c@example.com", "This sign-in is c@example.com, not Account-2 (b@example.com); nothing stored.",
			[]string{"a@example.com", "c@example.com", "c@example.com"}},
		{"the login fails", "fail", "claude's login did not complete; nothing stored, the live login untouched",
			[]string{"a@example.com", "fail", "c@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFableWalkFixture(t)
			logins := walkSignIns(t, map[string]string{"b@example.com": tc.as})
			code, out, errb := f.walk(t, "\n\n\n", true)
			if code != 1 || !strings.Contains(errb, "Error: "+tc.reason) || !reflect.DeepEqual(logins.emails, tc.emails) {
				t.Fatalf("exit %d, signed in as %q, stderr %q", code, logins.emails, errb)
			}
			inOrder(t, out, "Fable sign-in 2 of 3", "Sign in as b@example.com",
				"Fable sign-in 3 of 3: c@example.com (Account-3).\n"+walkPrompt, "Sign in as c@example.com",
				"Signed in for Fable: a@example.com (Account-1), c@example.com (Account-3).\n"+
					"Failed: b@example.com (Account-2): "+tc.reason+"\n"+walkAgain)
			if got := fableLoginSlots(t, "1", "2", "3"); !reflect.DeepEqual(got, []string{"1", "3"}) {
				t.Fatalf("Fable logins stored for %v", got)
			}
			assertNoScratch(t)
		})
	}
}

// quarantineFableLogin makes num's Fable login read "sign in again".
func quarantineFableLogin(t *testing.T, num, email string) {
	t.Helper()
	sw, err := newSwitcher(store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := sw.Store.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	state := `{"quarantine":{"` + num + `":{"email":"` + email + `","reason":"invalid_grant","at":"t"}}}`
	if err := os.WriteFile(f.StatePath(), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Asked: an enabled OAuth account that runs Fable without a Fable login, and
// one whose Fable login reads "sign in again". Not asked: one with a usable
// Fable login, a disabled one, an API key, a plan without Fable.
func TestFableLoginWalkAsksOnlyAccountsWithoutAUsableFableLogin(t *testing.T) {
	f := newFableLoginFixture(t)
	t.Cleanup(func() { setSigintNote("") })
	signInForFable(t, f, "1", "a@example.com")
	signInForFable(t, f, "2", "b@example.com")
	quarantineFableLogin(t, "2", "b@example.com")
	addByLogin(t, f, "c@example.com", "default-3")
	if code, _, errb := f.run(t, "disable", "3"); code != 0 {
		t.Fatalf("disable 3: exit %d, stderr %q", code, errb)
	}
	if code, _, errb := f.run(t, "add-token", "sk-ant-api03-test-key-4", "--email", "k@example.com", "--slot", "4"); code != 0 {
		t.Fatalf("seeding Account-4: exit %d, stderr %q", code, errb)
	}
	testutil.Setenv(t, "FAKE_CLAUDE_SUBSCRIPTION", "pro")
	addByLogin(t, f, "p@example.com", "default-5")
	testutil.Setenv(t, "FAKE_CLAUDE_SUBSCRIPTION", "max")
	addByLogin(t, f, "e@example.com", "default-6")

	logins := walkSignIns(t, nil)
	code, out, errb := f.walk(t, "s\ns\n", true)
	if code != 0 || len(logins.emails) != 0 {
		t.Fatalf("exit %d, signed in as %q, stderr %q", code, logins.emails, errb)
	}
	want := []string{"Fable sign-in 1 of 2: b@example.com (Account-2).", "Fable sign-in 2 of 2: e@example.com (Account-6)."}
	if got := walkHeader.FindAllString(out, -1); !reflect.DeepEqual(got, want) {
		t.Fatalf("asked %q, want %q; out:\n%s", got, want, out)
	}
	inOrder(t, out, "Skipped: b@example.com (Account-2), e@example.com (Account-6).\n"+walkAgain)
}

// Every account that runs Fable holds a usable Fable login: one line, exit 0,
// no question, with or without a terminal.
func TestFableLoginWalkWithEveryLoginInPlace(t *testing.T) {
	f := newFableLoginFixture(t)
	signInForFable(t, f, "1", "a@example.com")
	signInForFable(t, f, "2", "b@example.com")
	logins := walkSignIns(t, nil)
	for _, terminal := range []bool{true, false} {
		code, out, errb := f.walk(t, "\n", terminal)
		if code != 0 || out != "Every account that runs Fable has a Fable login.\n" || errb != "" {
			t.Fatalf("terminal %v: exit %d out %q stderr %q", terminal, code, out, errb)
		}
	}
	if len(logins.emails) != 0 {
		t.Fatalf("signed in as %q", logins.emails)
	}
}

// Without a terminal, on macOS, with --json or with another action, nothing
// is asked or started; help names the action.
func TestFableLoginWalkRefusals(t *testing.T) {
	t.Run("no terminal", func(t *testing.T) {
		f := newFableWalkFixture(t)
		logins := walkSignIns(t, nil)
		before := defaultFiles(t, f.home)
		code, out, errb := f.walk(t, "\n\n\n", false)
		want := "Error: tycswap fable --login needs a terminal on stdin and stdout: it asks before each browser sign-in"
		if code != 1 || out != "" || strings.TrimSpace(errb) != want || len(logins.emails) != 0 {
			t.Fatalf("exit %d out %q stderr %q, signed in as %q", code, out, errb, logins.emails)
		}
		if !sameStrings(before, defaultFiles(t, f.home)) {
			t.Error("the refusal changed the default login's files")
		}
		if _, err := os.Stat(paths.FableCredentialsDir(paths.GetBackupRoot())); !os.IsNotExist(err) {
			t.Errorf("the refusal created credentials-fable/ (%v)", err)
		}
	})
	t.Run("macOS", func(t *testing.T) {
		f := newFableWalkFixture(t)
		onPlatform(t, platform.MacOS)
		logins := walkSignIns(t, nil)
		code, out, errb := f.walk(t, "\n\n\n", true)
		want := "Error: The Fable store needs a file-based credential store; macOS keeps Claude Code's login in the Keychain"
		if code != 1 || out != "" || strings.TrimSpace(errb) != want || len(logins.emails) != 0 {
			t.Fatalf("exit %d out %q stderr %q, signed in as %q", code, out, errb, logins.emails)
		}
	})
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"fable", "--login", "--json"}, "--json goes with --list"},
		{[]string{"fable", "--login", "--allow", "1"}, "--allow, --disallow, --list and --login are exclusive"},
		{[]string{"fable", "--disallow", "1", "--login"}, "--allow, --disallow, --list and --login are exclusive"},
		{[]string{"fable", "--list", "--login"}, "--allow, --disallow, --list and --login are exclusive"},
	} {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			f := newFableWalkFixture(t)
			logins := walkSignIns(t, nil)
			code, out, errb := f.run(t, tc.argv...)
			if code != 2 || out != "" || !strings.HasSuffix(strings.TrimSpace(errb), "tycswap fable: error: "+tc.want) || len(logins.emails) != 0 {
				t.Fatalf("exit %d out %q stderr %q", code, out, errb)
			}
		})
	}
	t.Run("help", func(t *testing.T) {
		f := newFableWalkFixture(t)
		_, out, _ := f.run(t, "fable", "--help")
		var main bytes.Buffer
		renderMainHelp("tycswap", &main)
		if !strings.Contains(out, "| --login") || !strings.Contains(main.String(), "tycswap fable --login") {
			t.Fatalf("fable --help:\n%s\nhelp:\n%s", out, main.String())
		}
	})
}

// storeMenuRows serves the menu's Fable rows from the test's store, as the
// command does; TestMain stubs them.
func storeMenuRows(t *testing.T) {
	t.Helper()
	old := menuFableRows
	t.Cleanup(func() { menuFableRows = old })
	menuFableRows = func(s ioStreams) ([]fableRow, error) {
		sw, err := constructSwitcher(false, s.err)
		if err != nil {
			return nil, err
		}
		return fableRows(sw)
	}
}

// With two or more accounts without a Fable login the menu's Fable sign-in
// offers all of them first and runs the walk; with one or none the picker is
// as before.
func TestMenuFableSignInOffersTheWalk(t *testing.T) {
	signIn := func(t *testing.T, pick string, signedIn ...string) (map[string][]string, *[][]string) {
		t.Helper()
		f := newFableWalkFixture(t)
		emails := map[string]string{"1": "a@example.com", "2": "b@example.com", "3": "c@example.com"}
		for _, num := range signedIn {
			signInForFable(t, f, num, emails[num])
		}
		fableMenuPlatform(t, true)
		storeMenuRows(t)
		commands := workflowCommands(t)
		offered := menuScript(t, "add --login --lineage fable", pick)
		menuAdd(ioStreams{err: io.Discard})
		return offered, commands
	}
	const question = "Sign in for Fable as which account?"
	all := "All 3 accounts without a Fable login, one after another"
	t.Run("three without, the walk", func(t *testing.T) {
		offered, commands := signIn(t, "All 3")
		if want := []string{all, "#1  a@example.com", "#2  b@example.com", "#3  c@example.com"}; !reflect.DeepEqual(offered[question], want) {
			t.Fatalf("accounts = %q, want %q", offered[question], want)
		}
		if want := [][]string{{"fable", "--login"}}; !reflect.DeepEqual(*commands, want) {
			t.Fatalf("commands = %v", *commands)
		}
	})
	t.Run("three without, one account", func(t *testing.T) {
		_, commands := signIn(t, "#2")
		if want := [][]string{fableSignInArgs("2")}; !reflect.DeepEqual(*commands, want) {
			t.Fatalf("commands = %v", *commands)
		}
	})
	for _, tc := range []struct {
		name     string
		signedIn []string
		want     []string
	}{
		{"one without", []string{"1", "2"}, []string{"#3  c@example.com", "#1  a@example.com · sign in again", "#2  b@example.com · sign in again"}},
		{"none without", []string{"1", "2", "3"}, []string{"#1  a@example.com · sign in again", "#2  b@example.com · sign in again", "#3  c@example.com · sign in again"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			offered, commands := signIn(t, "#1", tc.signedIn...)
			if !reflect.DeepEqual(offered[question], tc.want) {
				t.Fatalf("accounts = %q, want %q", offered[question], tc.want)
			}
			if want := [][]string{fableSignInArgs("1")}; !reflect.DeepEqual(*commands, want) {
				t.Fatalf("commands = %v", *commands)
			}
		})
	}
}

// A second run after a partial first asks only for the accounts still
// missing, a failed one included; once none is left it says so.
func TestFableLoginWalkAgainAsksOnlyTheRest(t *testing.T) {
	f := newFableWalkFixture(t)
	wrong := map[string]string{"b@example.com": "fail"}
	logins := walkSignIns(t, wrong)
	if code, out, errb := f.walk(t, "\n\nq\n", true); code != 1 || !strings.HasSuffix(out, walkAgain) {
		t.Fatalf("first run: exit %d stderr %q out:\n%s", code, errb, out)
	}
	delete(wrong, "b@example.com")
	code, out, errb := f.walk(t, "\n\n", true)
	if code != 0 || !reflect.DeepEqual(logins.emails, []string{"a@example.com", "fail", "b@example.com", "c@example.com"}) {
		t.Fatalf("second run: exit %d, signed in as %q, stderr %q", code, logins.emails, errb)
	}
	want := []string{"Fable sign-in 1 of 2: b@example.com (Account-2).", "Fable sign-in 2 of 2: c@example.com (Account-3)."}
	if got := walkHeader.FindAllString(out, -1); !reflect.DeepEqual(got, want) || strings.Contains(out, walkAgain) {
		t.Fatalf("second run asked %q, want %q; out:\n%s", got, want, out)
	}
	if code, out, _ := f.walk(t, "", true); code != 0 || out != "Every account that runs Fable has a Fable login.\n" {
		t.Fatalf("third run: exit %d out %q", code, out)
	}
}

// q stops the walk at once: the accounts after it are not asked, none is
// signed in, and each is named as skipped once.
func TestFableLoginWalkStopsAtQWithAccountsLeft(t *testing.T) {
	f := newFableWalkFixture(t)
	logins := walkSignIns(t, nil)
	code, out, errb := f.walk(t, "q\n\n\n", true)
	if code != 0 || len(logins.emails) != 0 {
		t.Fatalf("exit %d, signed in as %q, stderr %q", code, logins.emails, errb)
	}
	if strings.Count(out, walkPrompt) != 1 || strings.Contains(out, "Fable sign-in 2 of 3") {
		t.Fatalf("asked after q:\n%s", out)
	}
	want := "Skipped: a@example.com (Account-1), b@example.com (Account-2), c@example.com (Account-3).\n" + walkAgain
	if !strings.HasSuffix(out, want) {
		t.Fatalf("out:\n%s", out)
	}
	if got := fableLoginSlots(t, "1", "2", "3"); len(got) != 0 {
		t.Fatalf("Fable logins stored for %v", got)
	}
}
