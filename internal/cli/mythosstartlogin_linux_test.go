// Tests that a Mythos start, fresh or a resume, begins on a start login and
// leaves it from its SessionStart hook on (DESIGN A119).
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

const (
	spentStartLoginNotice = "tycswap start: Starting on start login Account-2, which is at its limit; auto-switch moves the session to a Mythos login with room once it is up.\n"
	startLoginSession     = "11111111-2222-4333-8444-000000000119"
)

var lateMythosReset = time.Date(2026, 11, 28, 9, 59, 0, 0, time.Local).UTC().Format(time.RFC3339)

// mythosStartLoginFixture is the box of 2026-11-25 in small: Account-2 (8
// there) a start login whose Mythos login is at its limit, Account-3 (10) an
// unmarked Mythos login with room and the selected one, accounts 1 and 4
// start logins without a ready Mythos login. It returns both Mythos logins.
func mythosStartLoginFixture(t *testing.T, off bool) (*core.Switcher, *startRecord, string, string) {
	t.Helper()
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", off: off, usage: map[string][3]float64{
		"1": {5, 15, 25}, "2": {5, 15, 100}, "3": {5, 15, 30}, "4": {1, 1, 1},
	}}, "1", "2", "4")
	spent, room := addMythosTestLogins(t, sw)
	if err := store.SelectMythosLogin(sw.BackupDir(), room); err != nil {
		t.Fatal(err)
	}
	startResolve = func(name string) (string, error) { return filepath.Join("/fixture", name), nil }
	return sw, rec, spent, room
}

// selectedUnmarkedMythosLogin gives Account-4 a Mythos login with room,
// unmarks it as a start login and selects it; it returns the login.
func selectedUnmarkedMythosLogin(t *testing.T, sw *core.Switcher) string {
	t.Helper()
	dir := store.MythosLoginDir(sw.BackupDir(), fableEmail("4"), "")
	writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("mythos-4", "refresh-4"))
	setRecord(t, sw, "4", "startLogin", false)
	writeMythosTestUsage(t, sw, "4", 10, 10, lateMythosReset, lateMythosReset)
	if err := store.SelectMythosLogin(sw.BackupDir(), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func loggedStart(t *testing.T, sw *core.Switcher, notice string) bool {
	t.Helper()
	line := strings.TrimSuffix(strings.TrimPrefix(notice, "tycswap start: "), "\n")
	return strings.Contains(readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")), "start: "+line)
}

func TestMythosStartBeginsOnAStartLoginAtItsLimit(t *testing.T) {
	for _, route := range []struct {
		name   string
		args   []string
		queued bool
	}{
		{"start --mythos", []string{"--mythos"}, false},
		{"launcher", []string{"--", "claude", "--model", fixtureMythosModel}, false},
		{"next start", nil, true},
	} {
		t.Run(route.name, func(t *testing.T) {
			sw, rec, spent, _ := mythosStartLoginFixture(t, false)
			if route.queued {
				expectStartCode(t, 0, "--next", "mythos")
			}
			msg := expectStartCode(t, 23, route.args...)
			assertNativeStore(t, rec.env, spent)
			if msg != spentStartLoginNotice || !loggedStart(t, sw, spentStartLoginNotice) {
				t.Fatalf("start notice %q, logged %t; want %q", msg, loggedStart(t, sw, spentStartLoginNotice), spentStartLoginNotice)
			}
			if envValue(rec.env, startFamilyEnv) != mythosFamily || envValue(rec.env, startHoldEnv) != "" {
				t.Fatalf("child environment: family %q, hold %q", envValue(rec.env, startFamilyEnv), envValue(rec.env, startHoldEnv))
			}
		})
	}
}

// startOnStartLogin starts a Mythos session with args (default --mythos),
// checks that its link is on login and returns the link.
func startOnStartLogin(t *testing.T, rec *startRecord, login string, args ...string) string {
	t.Helper()
	if args == nil {
		args = []string{"--mythos"}
	}
	expectStartCode(t, 23, args...)
	return assertNativeStore(t, rec.env, login)
}

// sessionStartHook runs the SessionStart hook of the session on link, as
// Claude Code reports source, session and model ("" for none).
func sessionStartHook(t *testing.T, rec *startRecord, link, source, session, model string) {
	t.Helper()
	t.Setenv(secureStorageEnv, link)
	t.Setenv(startFamilyEnv, envValue(rec.env, startFamilyEnv))
	input := fmt.Sprintf(`{"source":%q,"session_id":%q}`, source, session)
	if model != "" {
		input = fmt.Sprintf(`{"source":%q,"session_id":%q,"model":%q}`, source, session, model)
	}
	if code, out, msg := runCodex(t, input, "start", "--started"); code != 0 || out != "" || msg != "" {
		t.Fatalf("SessionStart hook: %d %q %q", code, out, msg)
	}
	testutil.Unsetenv(t, secureStorageEnv)
	testutil.Unsetenv(t, startFamilyEnv)
}

func tickMovedSession(t *testing.T, out string) bool {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if event := decodeJSON(t, line); event["event"] == "switch" && event["sessionId"] == startLoginSession {
			return true
		}
	}
	return false
}

// movedByHook: the log has the hook's move of startLoginSession off Account-2.
func movedByHook(t *testing.T, sw *core.Switcher, to string) bool {
	t.Helper()
	line := "Session " + startLoginSession[:8] + " moved from Mythos login (Account-2) to Mythos login (Account-" + to + ") by hook"
	return strings.Contains(readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")), line)
}

func TestMythosSessionLeavesItsStartLoginAtItsSessionStartHook(t *testing.T) {
	t.Run("nothing moves it before the hook; the hook moves it", func(t *testing.T) {
		sw, rec, spent, room := mythosStartLoginFixture(t, false)
		link := startOnStartLogin(t, rec, spent)
		for range 2 {
			if _, out, _ := runCodex(t, "", "auto", "--once", "--json"); tickMovedSession(t, out) {
				t.Fatalf("a tick moved the session before its SessionStart hook: %s", out)
			}
			requireLinkTarget(t, link, spent)
		}
		if code, out, msg := runCodex(t, "", "switch", "--store", "mythos", "3", "--json"); code != 0 || decodeJSON(t, out)["movedSessions"] != 0.0 {
			t.Fatalf("switch --store mythos: %d %s %s", code, out, msg)
		}
		requireLinkTarget(t, link, spent)
		sessionStartHook(t, rec, link, "startup", startLoginSession, fixtureMythosModel)
		requireLinkTarget(t, link, room)
		if !movedByHook(t, sw, "3") {
			t.Fatalf("no hook move in the log: %s", readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")))
		}
	})
	t.Run("a hook without a model leaves the move to the status line", func(t *testing.T) {
		_, rec, spent, room := mythosStartLoginFixture(t, false)
		link := startOnStartLogin(t, rec, spent)
		sessionStartHook(t, rec, link, "startup", startLoginSession, "")
		requireLinkTarget(t, link, spent)
		sessionStatus(t, link, startLoginSession, fixtureMythosModel, true)
		requireLinkTarget(t, link, room)
	})
}

// A print-mode session draws no status line: the hook alone moves it, else
// the tick once a login has room. No status line runs here.
func TestMythosPrintModeSessionLeavesItsStartLogin(t *testing.T) {
	printMode := []string{"--", "claude", "-p", "--model", fixtureMythosModel, "say hi"}
	t.Run("its SessionStart hook moves it", func(t *testing.T) {
		_, rec, spent, room := mythosStartLoginFixture(t, false)
		link := startOnStartLogin(t, rec, spent, printMode...)
		sessionStartHook(t, rec, link, "startup", startLoginSession, fixtureMythosModel)
		requireLinkTarget(t, link, room)
	})
	t.Run("the tick moves it once a login has room", func(t *testing.T) {
		sw, rec, spent, room := mythosStartLoginFixture(t, false)
		link := startOnStartLogin(t, rec, spent, printMode...)
		writeMythosTestUsage(t, sw, "3", 53, 100, lateMythosReset, lateMythosReset)
		sessionStartHook(t, rec, link, "startup", startLoginSession, fixtureMythosModel)
		requireLinkTarget(t, link, spent)
		writeMythosTestUsage(t, sw, "3", 53, 30, lateMythosReset, lateMythosReset)
		if _, out, msg := runCodex(t, "", "auto", "--once", "--json"); !tickMovedSession(t, out) {
			t.Fatalf("the tick did not move the session: %s %s", out, msg)
		}
		requireLinkTarget(t, link, room)
	})
}

const previousStartLoginMythosEnv = "TYCSWAP_TEST_PREVIOUS_START_LOGIN_MYTHOS"

// TestMythosStartLoginPreviousSession is the first run of startLoginSession
// for recordedMythosSession, in a child process; alone it does nothing.
func TestMythosStartLoginPreviousSession(t *testing.T) {
	dir := os.Getenv(previousStartLoginMythosEnv)
	if dir == "" {
		return
	}
	root := paths.GetBackupRoot()
	link, err := reporting.CreateSessionLink(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporting.BindSessionLink(root, startLoginSession, link, mythosFamily, "mythos/"+filepath.Base(dir), time.Now()); err != nil {
		t.Fatal(err)
	}
}

// recordedMythosSession leaves startLoginSession as its first run on the
// Mythos login dir leaves it: bound there by a process that has ended.
func recordedMythosSession(t *testing.T, dir string) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestMythosStartLoginPreviousSession$")
	child.Env = append(os.Environ(), previousStartLoginMythosEnv+"="+dir)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("previous session: %s %v", out, err)
	}
}

func TestMythosResumeBeginsOnAStartLogin(t *testing.T) {
	const fork = "11111111-2222-4333-8444-000000000120"
	for _, route := range []struct {
		name     string
		args     []string
		recorded bool
		hookID   string
	}{
		{"--resume ID with --model", []string{"--resume", startLoginSession, "--model", fixtureMythosModel}, true, startLoginSession},
		{"--resume ID without --model", []string{"--resume", startLoginSession}, true, startLoginSession},
		{"--resume ID without a recorded login", []string{"--resume", startLoginSession, "--model", fixtureMythosModel}, false, startLoginSession},
		{"the picker", []string{"--resume", "--model", fixtureMythosModel}, true, startLoginSession},
		{"--continue", []string{"--continue", "--model", fixtureMythosModel}, true, startLoginSession},
		{"--fork-session", []string{"--resume", startLoginSession, "--fork-session", "--model", fixtureMythosModel}, true, fork},
		{"--from-pr", []string{"--from-pr", "7", "--model", fixtureMythosModel}, false, startLoginSession},
	} {
		t.Run(route.name, func(t *testing.T) {
			sw, rec, spent, room := mythosStartLoginFixture(t, false)
			if route.recorded {
				recordedMythosSession(t, room)
			}
			msg := expectStartCode(t, 23, append([]string{"--", "claude"}, route.args...)...)
			link := assertNativeStore(t, rec.env, spent)
			if msg != spentStartLoginNotice || !loggedStart(t, sw, spentStartLoginNotice) {
				t.Fatalf("start notice %q; want %q", msg, spentStartLoginNotice)
			}
			sessionStartHook(t, rec, link, "resume", route.hookID, fixtureMythosModel)
			requireLinkTarget(t, link, room)
		})
	}
}

func TestMythosResumeReturnsToItsRecordedLogin(t *testing.T) {
	resume := []string{"--", "claude", "--resume", startLoginSession, "--model", fixtureMythosModel}
	t.Run("the recorded login has room", func(t *testing.T) {
		sw, rec, spent, room := mythosStartLoginFixture(t, false)
		selectedUnmarkedMythosLogin(t, sw)
		recordedMythosSession(t, room)
		link := startOnStartLogin(t, rec, spent, resume...)
		sessionStartHook(t, rec, link, "resume", startLoginSession, fixtureMythosModel)
		requireLinkTarget(t, link, room)
		if !movedByHook(t, sw, "3") {
			t.Fatalf("no hook move in the log: %s", readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")))
		}
	})
	t.Run("the recorded login has no room", func(t *testing.T) {
		sw, rec, spent, room := mythosStartLoginFixture(t, false)
		other := selectedUnmarkedMythosLogin(t, sw)
		recordedMythosSession(t, room)
		writeMythosTestUsage(t, sw, "3", 53, 100, lateMythosReset, lateMythosReset)
		if msg := expectStartCode(t, 23, resume...); msg != spentStartLoginNotice {
			t.Fatalf("start notice %q; want %q", msg, spentStartLoginNotice)
		}
		link := assertNativeStore(t, rec.env, spent)
		sessionStartHook(t, rec, link, "resume", startLoginSession, fixtureMythosModel)
		requireLinkTarget(t, link, other)
	})
	t.Run("a resume that ended on the start login before its return", func(t *testing.T) {
		_, rec, spent, room := mythosStartLoginFixture(t, false)
		recordedMythosSession(t, room)
		recordedMythosSession(t, spent)
		if msg := expectStartCode(t, 23, resume...); msg != spentStartLoginNotice {
			t.Fatalf("start notice %q; want %q", msg, spentStartLoginNotice)
		}
		link := assertNativeStore(t, rec.env, spent)
		sessionStartHook(t, rec, link, "resume", startLoginSession, fixtureMythosModel)
		requireLinkTarget(t, link, room)
	})
	t.Run("auto-switch off", func(t *testing.T) {
		sw, rec, spent, room := mythosStartLoginFixture(t, true)
		recordedMythosSession(t, room)
		const plain = "tycswap start: Starting on start login Account-2, which is at its limit.\n"
		if msg := expectStartCode(t, 23, resume...); msg != plain || !loggedStart(t, sw, plain) {
			t.Fatalf("start notice %q; want %q", msg, plain)
		}
		link := assertNativeStore(t, rec.env, spent)
		sessionStartHook(t, rec, link, "resume", startLoginSession, fixtureMythosModel)
		requireLinkTarget(t, link, room)
	})
}

func TestMythosStartLoginExplicitAccount(t *testing.T) {
	t.Run("a start login at its limit while another has room", func(t *testing.T) {
		sw, rec, spent, _ := mythosStartLoginFixture(t, false)
		if msg := expectStartCode(t, 23, "--mythos", "--account", "2"); msg != spentStartLoginNotice || !loggedStart(t, sw, spentStartLoginNotice) {
			t.Fatalf("notice %q", msg)
		}
		assertNativeStore(t, rec.env, spent)
	})
	t.Run("a start login at its limit while none has room", func(t *testing.T) {
		sw, rec, _, _ := mythosStartLoginFixture(t, false)
		writeMythosTestUsage(t, sw, "2", 100, 100, lateMythosReset, lateMythosReset)
		writeMythosTestUsage(t, sw, "3", 53, 100, lateMythosReset, lateMythosReset)
		want := "tycswap start: no available Mythos login has quota for mythos; Account-2 returns Nov 28 09:59\n"
		if msg := expectStartCode(t, 1, "--mythos", "--account", "2"); msg != want || rec.execs != 0 {
			t.Fatalf("refusal %q, %d launches; want %q", msg, rec.execs, want)
		}
	})
	t.Run("an account that is no start login", func(t *testing.T) {
		sw, rec, _, room := mythosStartLoginFixture(t, false)
		if msg := expectStartCode(t, 23, "--mythos", "--account", "3"); msg != "" {
			t.Fatalf("notice %q", msg)
		}
		assertNativeStore(t, rec.env, room)
		writeMythosTestUsage(t, sw, "3", 53, 100, lateMythosReset, lateMythosReset)
		want := "tycswap start: no available Mythos login has quota for mythos; Account-3 returns Nov 28 09:59\n"
		if msg := expectStartCode(t, 1, "--mythos", "--account", "3"); msg != want {
			t.Fatalf("refusal %q; want %q", msg, want)
		}
	})
	t.Run("auto-switch off", func(t *testing.T) {
		sw, rec, spent, _ := mythosStartLoginFixture(t, true)
		const plain = "tycswap start: Starting on start login Account-2, which is at its limit.\n"
		if msg := expectStartCode(t, 23, "--mythos", "--account", "2"); msg != plain || !loggedStart(t, sw, plain) {
			t.Fatalf("notice %q; want %q", msg, plain)
		}
		assertNativeStore(t, rec.env, spent)
	})
}

func TestMythosStartWithoutAStartLoginChangeIsAsBefore(t *testing.T) {
	resume := []string{"--", "claude", "--resume", startLoginSession, "--model", fixtureMythosModel}
	unmark := func(t *testing.T, sw *core.Switcher) { setRecord(t, sw, "2", "startLogin", false) }
	for _, tc := range []struct {
		name     string
		off      bool
		setup    func(*testing.T, *core.Switcher)
		args     []string
		recorded bool
	}{
		{"no Mythos login is a start login", false, unmark, nil, false},
		{"no start login at all", false, func(t *testing.T, sw *core.Switcher) {
			for _, num := range []string{"1", "2", "4"} {
				setRecord(t, sw, num, "startLogin", false)
			}
		}, nil, false},
		{"the start login is allocated away from Mythos", false, func(t *testing.T, sw *core.Switcher) { setRecord(t, sw, "2", store.ModelsKey, []string{"opus"}) }, nil, false},
		{"the ranked choice is a start login", false, func(t *testing.T, sw *core.Switcher) { setRecord(t, sw, "3", "startLogin", true) }, nil, false},
		{"auto-switch off", true, nil, nil, false},
		{"a resume while no Mythos login is a start login", false, unmark, resume, true},
		{"a resume without a recorded login, auto-switch off", true, nil, resume, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, rec, _, room := mythosStartLoginFixture(t, tc.off)
			if tc.setup != nil {
				tc.setup(t, sw)
			}
			if tc.recorded {
				recordedMythosSession(t, room)
			}
			args := tc.args
			if args == nil {
				args = []string{"--mythos"}
			}
			if msg := expectStartCode(t, 23, args...); msg != "" {
				t.Fatalf("notice %q", msg)
			}
			assertNativeStore(t, rec.env, room)
		})
	}
	t.Run("a resume recorded on a start login", func(t *testing.T) {
		sw, rec, spent, _ := mythosStartLoginFixture(t, false)
		writeMythosTestUsage(t, sw, "2", 53, 60, lateMythosReset, lateMythosReset)
		recordedMythosSession(t, spent)
		if msg := expectStartCode(t, 23, resume...); msg != "" {
			t.Fatalf("notice %q", msg)
		}
		assertNativeStore(t, rec.env, spent)
		writeMythosTestUsage(t, sw, "2", 100, 100, lateMythosReset, lateMythosReset)
		want := "tycswap start: no available Mythos login has quota for mythos; Account-2 returns Nov 28 09:59\n"
		if msg := expectStartCode(t, 1, resume...); msg != want {
			t.Fatalf("at its limit: %q; want %q", msg, want)
		}
	})
	t.Run("no Mythos login has room", func(t *testing.T) {
		sw, rec, _, room := mythosStartLoginFixture(t, false)
		writeMythosTestUsage(t, sw, "2", 100, 100, lateMythosReset, lateMythosReset)
		early := time.Date(2026, 11, 26, 8, 15, 0, 0, time.Local).UTC().Format(time.RFC3339)
		writeMythosTestUsage(t, sw, "3", 53, 100, lateMythosReset, early)
		want := "tycswap start: no available Mythos login has quota for mythos; Account-3 returns Nov 26 08:15\n"
		for _, args := range [][]string{{"--mythos"}, resume} {
			if msg := expectStartCode(t, 1, args...); msg != want || rec.execs != 0 {
				t.Fatalf("%v: refusal %q, %d launches; want %q", args, msg, rec.execs, want)
			}
		}
		recordedMythosSession(t, room)
		if msg := expectStartCode(t, 1, resume...); msg != want || rec.execs != 0 {
			t.Fatalf("recorded resume: refusal %q, %d launches; want %q", msg, rec.execs, want)
		}
	})
	t.Run("the only room asks an allocation question", func(t *testing.T) {
		sw, rec, _, room := mythosStartLoginFixture(t, false)
		setRecord(t, sw, "3", store.ModelsKey, []string{"opus"})
		asked := 0
		previous := askPlacement
		t.Cleanup(func() { askPlacement = previous })
		askPlacement = func(ioStreams, string, string, bool) (bool, error) { asked++; return true, nil }
		if msg := expectStartCode(t, 23, "--mythos"); strings.Contains(msg, "start login") || asked != 1 {
			t.Fatalf("notice %q, %d questions; want the allocation question and no start login", msg, asked)
		}
		assertNativeStore(t, rec.env, room)
	})
}

// Without a selected Mythos login the start seeds the selection with the
// login it would have taken, so the hook moves the session off its start login.
func TestMythosStartLoginSeedsTheSelectionWithItsRankedChoice(t *testing.T) {
	sw, rec, spent, room := mythosStartLoginFixture(t, false)
	if err := os.Remove(filepath.Join(sw.BackupDir(), "mythos", "selected")); err != nil {
		t.Fatal(err)
	}
	writeMythosTestUsage(t, sw, "2", 53, 60, lateMythosReset, lateMythosReset)
	link := startOnStartLogin(t, rec, spent)
	if selected, err := store.SelectedMythosDigest(sw.BackupDir()); err != nil || selected != filepath.Base(room) {
		t.Fatalf("selection %q (%v); want the ranked choice %q", selected, err, filepath.Base(room))
	}
	sessionStartHook(t, rec, link, "startup", startLoginSession, fixtureMythosModel)
	requireLinkTarget(t, link, room)
}

// A failed link launches directly on the ranked login: nothing would move a
// direct launch off a start login.
func TestMythosStartWithAFailedLinkTakesNoStartLogin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		recorded bool
	}{
		{"a fresh start", []string{"--mythos"}, false},
		{"a resume with a recorded login", []string{"--", "claude", "--resume", startLoginSession, "--model", fixtureMythosModel}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, rec, _, room := mythosStartLoginFixture(t, false)
			if tc.recorded {
				recordedMythosSession(t, room)
			}
			links := filepath.Join(sw.BackupDir(), "links")
			if err := os.MkdirAll(links, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(links, 0500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(links, 0700) })
			msg := expectStartCode(t, 23, tc.args...)
			if got := envValue(rec.env, secureStorageEnv); got != room || strings.Contains(msg, "start login") || !strings.Contains(msg, "Session link unavailable") {
				t.Fatalf("launched on %q with %q; want %q and the link notice", got, msg, room)
			}
		})
	}
}

// A start login with room comes first; the session still moves to the
// selected Mythos login at its SessionStart hook.
func TestMythosStartTakesAStartLoginWithRoomFirst(t *testing.T) {
	sw, rec, spent, room := mythosStartLoginFixture(t, false)
	writeMythosTestUsage(t, sw, "2", 53, 60, lateMythosReset, lateMythosReset)
	const notice = "tycswap start: Starting on start login Account-2.\n"
	if msg := expectStartCode(t, 23, "--mythos"); msg != notice || !loggedStart(t, sw, notice) {
		t.Fatalf("notice %q; want %q", msg, notice)
	}
	link := assertNativeStore(t, rec.env, spent)
	sessionStartHook(t, rec, link, "startup", startLoginSession, fixtureMythosModel)
	requireLinkTarget(t, link, room)
}
