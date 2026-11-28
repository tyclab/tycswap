// Tests that a linked start comes up on a start login's own login where it
// would otherwise switch or hold a shared login (DESIGN A125).
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
)

const onOwnLogin = "; starting on start login Account-4's Mythos login.\n"

// ownLoginOf marks num as a start login and gives it a ready login of its own.
func ownLoginOf(t *testing.T, sw *core.Switcher, num string) string {
	t.Helper()
	if err := sw.SetMythos(num, true); err != nil {
		t.Fatal(err)
	}
	setRecord(t, sw, num, "startLogin", true)
	dir := store.MythosLoginDir(sw.BackupDir(), fableEmail(num), "")
	writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("mythos-"+num, "refresh-"+num))
	return dir
}

// requireSharedLoginsUntouched: the default login is still on Account-3 and
// the Fable store on Account-2, and start neither switched nor held either.
func requireSharedLoginsUntouched(t *testing.T, sw *core.Switcher, rec *startRecord) {
	t.Helper()
	log := tycswapLog(t, sw)
	if !strings.Contains(defaultLive(t, sw), "at-d3") || !strings.Contains(storeLive(t, sw), "at-f2") || strings.Contains(log, "Switched") {
		t.Fatalf("the start moved a shared login: default %s, Fable store %s\n%s", defaultLive(t, sw), storeLive(t, sw), log)
	}
	if hold := envValue(rec.env, startHoldEnv); hold != "" || holdsIn(t, sw.StatePath()) != nil || holdsIn(t, fableStatePath(sw)) != nil {
		t.Fatalf("the start held a shared login: %q, %v, %v", hold, holdsIn(t, sw.StatePath()), holdsIn(t, fableStatePath(sw)))
	}
}

// defaultLoginStates are the two states in which a start cannot come up on
// the default login as it is: its account is no start login, or one at its limit.
var defaultLoginStates = map[string]struct {
	weekly float64
	marked []string
	why    string
}{
	"on no start login":             {20, []string{"1"}, "Account-3 is no start login"},
	"on a start login at its limit": {100, []string{"1", "3"}, "start login Account-3 is at its limit"},
}

func TestLinkedFableStartComesUpOnAStartLoginsOwnLogin(t *testing.T) {
	const notice = "tycswap start: Fable store: Account-2 is no start login" + onOwnLogin
	for state, fx := range defaultLoginStates {
		for route, args := range map[string][]string{
			"fresh start": {"start", "--", "claude", "--model", "fable"},
			"resume":      {"start", "--", "claude", "--resume", startedFableSession},
			"print mode":  {"start", "--", "claude", "-p", "--model", "fable", "say hi"},
		} {
			t.Run(state+"/"+route, func(t *testing.T) {
				sw, rec := fableStoreOffStartLoginsWith(t, fx.weekly, fx.marked...)
				own := ownLoginOf(t, sw, "4")
				other := bindFableSession(t, sw, otherFableSession, fableStoreDir())
				source := "startup"
				if route == "resume" {
					recordedMythosSession(t, fableStoreDir())
					source = "resume"
				}
				code, _, stderr := runCLI(t, args, false, false)
				if code != 23 || !strings.HasPrefix(stderr, notice) {
					t.Fatalf("start: code=%d stderr=%q, want it to begin %q", code, stderr, notice)
				}
				link := assertNativeStore(t, rec.env, own)
				if len(rec.selections) != 0 {
					t.Fatalf("a selection ran on the default login: %q", rec.selections)
				}
				requireSharedLoginsUntouched(t, sw, rec)
				startedHook(t, rec, source, startedFableSession)
				requireLinkTarget(t, link, fableStoreDir())
				requireMoveLog(t, sw, startedFableSession, "hook", "Mythos login (Account-4)", "Fable store (Account-2)")
				requireSharedLoginsUntouched(t, sw, rec)
				requireLinkTarget(t, other, fableStoreDir())
			})
		}
	}
}

func TestLinkedStartLeavesTheDefaultLoginAlone(t *testing.T) {
	for state, fx := range defaultLoginStates {
		for route, args := range map[string][]string{
			"fresh start": {"start", "--", "claude", "--model", "opus"},
			"resume":      {"start", "--", "claude", "--resume", startLoginSession, "--model", "opus"},
			"print mode":  {"start", "--", "claude", "-p", "--model", "opus", "say hi"},
		} {
			t.Run(state+"/"+route, func(t *testing.T) {
				sw, rec := fableStoreOffStartLoginsWith(t, fx.weekly, fx.marked...)
				own := ownLoginOf(t, sw, "4")
				source := "startup"
				if route == "resume" {
					recordedMythosSession(t, paths.GetClaudeConfigHome())
					source = "resume"
				}
				notice := "tycswap start: " + fx.why + onOwnLogin
				code, _, stderr := runCLI(t, args, false, false)
				if code != 23 || stderr != notice {
					t.Fatalf("start: code=%d stderr=%q, want %q", code, stderr, notice)
				}
				link := assertNativeStore(t, rec.env, own)
				requireSharedLoginsUntouched(t, sw, rec)
				startedHook(t, rec, source, startLoginSession)
				requireLinkTarget(t, link, paths.GetClaudeConfigHome())
				requireMoveLog(t, sw, startLoginSession, "hook", "Mythos login (Account-4)", "default login (Account-3)")
				if fx.weekly < 100 {
					requireSharedLoginsUntouched(t, sw, rec)
				} else if log := tycswapLog(t, sw); strings.Contains(defaultLive(t, sw), "at-d3") || strings.Contains(log, "by start") {
					// The session left for a login at its limit: the hook's tick moves that login on.
					t.Fatalf("default login %s after the hook\n%s", defaultLive(t, sw), log)
				}
			})
		}
	}
}

// A Fable start the room rule sends to the default login (A112) runs that
// login's selection, and a Mythos resume of a session recorded on the default
// login runs it among the accounts marked for Mythos: neither moves it.
func TestStartRoutedToTheDefaultLoginComesUpOnAnOwnLogin(t *testing.T) {
	t.Run("Fable by the room rule", func(t *testing.T) {
		sw, rec := fableStartLogin(t, fableTickFixture{def: "3", live: "2", logins: []string{"1", "2"},
			usage: map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 100}, "3": {10, 20, 10}, "4": {10, 20, 100}}}, "1")
		startResolve = func(string) (string, error) { return "/fixture/claude", nil }
		own := ownLoginOf(t, sw, "4")
		code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false)
		if code != 23 || !strings.Contains(stderr, "starting on the default login.") || !strings.HasSuffix(stderr, "tycswap start: Account-3 is no start login"+onOwnLogin) {
			t.Fatalf("start: code=%d stderr=%q", code, stderr)
		}
		link := assertNativeStore(t, rec.env, own)
		requireSharedLoginsUntouched(t, sw, rec)
		startedHook(t, rec, "startup", startedFableSession)
		requireLinkTarget(t, link, paths.GetClaudeConfigHome())
		requireSharedLoginsUntouched(t, sw, rec)
	})
	t.Run("Mythos resume", func(t *testing.T) {
		sw, rec := fableStoreOffStartLoginsWith(t, 20, "1")
		own := ownLoginOf(t, sw, "4")
		recordedMythosSession(t, paths.GetClaudeConfigHome())
		notice := "tycswap start: Account-3 is not marked for Mythos" + onOwnLogin
		code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--resume", startLoginSession, "--model", fixtureMythosModel}, false, false)
		if code != 23 || stderr != notice {
			t.Fatalf("start: code=%d stderr=%q, want %q", code, stderr, notice)
		}
		assertNativeStore(t, rec.env, own)
		if envValue(rec.env, startFamilyEnv) != mythosFamily {
			t.Fatalf("family %q", envValue(rec.env, startFamilyEnv))
		}
		requireSharedLoginsUntouched(t, sw, rec)
	})
}

// The own login with room comes first, else the first at its limit.
func TestWhichOwnLoginAStartComesUpOn(t *testing.T) {
	opus := []string{"start", "--", "claude", "--model", "opus"}
	t.Run("with room first", func(t *testing.T) {
		sw, rec := fableStartLogin(t, fableTickFixture{def: "3", live: "2", logins: []string{"1", "2"},
			usage: map[string][3]float64{"1": {10, 100, 100}, "2": {10, 20, 10}, "3": {10, 20, 100}, "4": {10, 20, 100}}})
		startResolve = func(string) (string, error) { return "/fixture/claude", nil }
		ownLoginOf(t, sw, "1")
		own := ownLoginOf(t, sw, "4")
		code, _, stderr := runCLI(t, opus, false, false)
		if code != 23 || stderr != "tycswap start: Account-3 is no start login"+onOwnLogin {
			t.Fatalf("start: code=%d stderr=%q", code, stderr)
		}
		assertNativeStore(t, rec.env, own)
	})
	t.Run("at its limit", func(t *testing.T) {
		sw, rec := fableStartLogin(t, fableTickFixture{def: "3", live: "2", logins: []string{"1", "2"},
			usage: map[string][3]float64{"1": {10, 100, 100}, "2": {10, 20, 10}, "3": {10, 20, 100}, "4": {10, 100, 100}}})
		startResolve = func(string) (string, error) { return "/fixture/claude", nil }
		own := ownLoginOf(t, sw, "1")
		ownLoginOf(t, sw, "4")
		code, _, stderr := runCLI(t, opus, false, false)
		if code != 23 || stderr != "tycswap start: Account-3 is no start login; starting on start login Account-1's Mythos login.\n" {
			t.Fatalf("start: code=%d stderr=%q", code, stderr)
		}
		assertNativeStore(t, rec.env, own)
		requireSharedLoginsUntouched(t, sw, rec)
	})
}

// A start login that lost its own login (disabled, or signed out) leaves the
// selection on the shared login as before, after one notice: why the start
// would have come up on one, that this start switches or holds that login for
// every session on it, and the sign-in that gives one back (DESIGN A126).
func TestStartWithoutAnOwnLoginSaysItMovesTheSharedLogin(t *testing.T) {
	missing := func(login, verb string) string {
		return "; no start login has a Mythos login of its own, so this start " + verb + " " + login +
			" for every session on it. Sign one in with tycswap add --login --lineage mythos --slot N.\n"
	}
	for name, spoil := range map[string]func(*testing.T, *core.Switcher, string){
		"disabled": func(t *testing.T, sw *core.Switcher, _ string) { setRecord(t, sw, "4", "disabled", true) },
		"signed out": func(t *testing.T, _ *core.Switcher, dir string) {
			if err := os.Remove(filepath.Join(dir, ".credentials.json")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name+"/default login", func(t *testing.T) {
			sw, rec := fableStoreOffStartLoginsWith(t, 20, "1")
			spoil(t, sw, ownLoginOf(t, sw, "4"))
			notice := "tycswap start: Account-3 is no start login" + missing("the default login", "switches")
			code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--model", "opus"}, false, false)
			if want := notice + "tycswap start: Switched to start login Account-1.\n"; code != 23 || stderr != want || !strings.Contains(defaultLive(t, sw), "at-d1") {
				t.Fatalf("start: code=%d stderr=%q, want %q; default login %s", code, stderr, want, defaultLive(t, sw))
			}
			assertNativeStore(t, rec.env, "")
			if log := tycswapLog(t, sw); !strings.Contains(log, "start: "+strings.TrimSuffix(strings.TrimPrefix(notice, "tycswap start: "), "\n")) {
				t.Fatalf("log lacks the notice:\n%s", log)
			}
		})
		t.Run(name+"/Fable store", func(t *testing.T) {
			sw, rec := fableStoreOffStartLoginsWith(t, 20, "1")
			spoil(t, sw, ownLoginOf(t, sw, "4"))
			want := "tycswap start: Fable store: Account-2 is no start login" + missing("the Fable store", "switches") +
				"tycswap start: Fable store: Switched to start login Account-1, which is at its limit; auto-switch moves on once the session is up.\n"
			code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false)
			if code != 23 || !strings.HasPrefix(stderr, want) || !strings.Contains(storeLive(t, sw), "at-f1") {
				t.Fatalf("start: code=%d stderr=%q, want it to begin %q; Fable store %s", code, stderr, want, storeLive(t, sw))
			}
			assertNativeStore(t, rec.env, fableStoreDir())
		})
	}
	t.Run("holding the default login", func(t *testing.T) {
		sw, rec := fableStoreOffStartLoginsWith(t, 100, "1", "3")
		ownLoginOf(t, sw, "4")
		setRecord(t, sw, "4", "disabled", true)
		want := "tycswap start: start login Account-3 is at its limit" + missing("the default login", "holds")
		code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--model", "opus"}, false, false)
		if code != 23 || stderr != want {
			t.Fatalf("start: code=%d stderr=%q, want %q", code, stderr, want)
		}
		assertNativeStore(t, rec.env, "")
		if !strings.Contains(defaultLive(t, sw), "at-d3") || envValue(rec.env, startHoldEnv) == "" {
			t.Fatalf("default login %s, hold %q", defaultLive(t, sw), envValue(rec.env, startHoldEnv))
		}
	})
}

// What an own login does not change: a default login that already sits on a
// start login with room, an explicit account, and auto-switch off. The own
// login of an account that is no start login is not taken, and with no start
// login marked for Mythos nothing is said (DESIGN A126).
func TestStartThatTakesNoOwnLogin(t *testing.T) {
	opus := []string{"start", "--", "claude", "--model", "opus"}
	t.Run("of no start login", func(t *testing.T) {
		sw, rec := fableStoreOffStartLoginsWith(t, 20, "1")
		ownLoginOf(t, sw, "4")
		setRecord(t, sw, "4", "startLogin", false)
		code, _, stderr := runCLI(t, opus, false, false)
		if code != 23 || stderr != "tycswap start: Switched to start login Account-1.\n" {
			t.Fatalf("start: code=%d stderr=%q", code, stderr)
		}
		assertNativeStore(t, rec.env, "")
	})
	t.Run("the default login on a start login with room", func(t *testing.T) {
		sw, rec := fableStoreOffStartLogins(t)
		ownLoginOf(t, sw, "4")
		code, _, stderr := runCLI(t, opus, false, false)
		if code != 23 || stderr != "" {
			t.Fatalf("start: code=%d stderr=%q", code, stderr)
		}
		assertNativeStore(t, rec.env, "")
		requireSharedLoginsUntouched(t, sw, rec)
	})
	t.Run("--account", func(t *testing.T) {
		sw, rec := fableStoreOffStartLoginsWith(t, 20, "1")
		ownLoginOf(t, sw, "4")
		code, _, stderr := runCLI(t, []string{"start", "--account", "1", "--", "claude", "--model", "opus"}, false, false)
		if code != 23 || !strings.HasPrefix(stderr, "tycswap start: Switched to start login Account-1") || !strings.Contains(defaultLive(t, sw), "at-d1") {
			t.Fatalf("start: code=%d stderr=%q, default login %s", code, stderr, defaultLive(t, sw))
		}
		assertNativeStore(t, rec.env, "")
	})
	t.Run("auto-switch off", func(t *testing.T) {
		sw, rec := fableStartLogin(t, fableTickFixture{def: "3", live: "2", logins: []string{"1", "2"}, off: true,
			usage: map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 10}, "3": {10, 20, 100}, "4": {10, 20, 100}}}, "1")
		startResolve = func(string) (string, error) { return "/fixture/claude", nil }
		ownLoginOf(t, sw, "4")
		code, _, stderr := runCLI(t, opus, false, false)
		if code != 23 || stderr != "" {
			t.Fatalf("start: code=%d stderr=%q", code, stderr)
		}
		assertNativeStore(t, rec.env, "")
		requireSharedLoginsUntouched(t, sw, rec)
	})
}
