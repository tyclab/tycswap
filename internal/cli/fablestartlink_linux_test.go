// Tests that a linked Fable start leaves the Fable store to the sessions on
// it and reaches Fable room through its own link (DESIGN A122).
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/testutil"
)

// The session start launches is startLoginSession, the one a resume names.
const (
	startedFableSession = startLoginSession
	otherFableSession   = "11111111-2222-4333-8444-000000000122"
)

// fableStoreOffStartLogins: the Fable store on Account-2, no start login,
// with Fable room; start logins 1 (in the Fable pool) and 3 (the default
// login's account) at their Fable limit, with 5-hour and weekly room.
func fableStoreOffStartLogins(t *testing.T) (*core.Switcher, *startRecord) {
	t.Helper()
	return fableStoreOffStartLoginsWith(t, 20, "1", "3")
}

// fableStoreOffStartLoginsWith is that fixture with the default login's
// account at weekly percent and the given start logins.
func fableStoreOffStartLoginsWith(t *testing.T, weekly float64, marked ...string) (*core.Switcher, *startRecord) {
	t.Helper()
	sw, rec := fableStartLogin(t, fableTickFixture{def: "3", live: "2", logins: []string{"1", "2"},
		usage: map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 10}, "3": {10, weekly, 100}, "4": {10, 20, 100}}}, marked...)
	startResolve = func(string) (string, error) { return "/fixture/claude", nil }
	return sw, rec
}

// startedHook runs the SessionStart hook of the session start launched last,
// as Claude Code 2.1.332 sends it: a source and a session, no model.
func startedHook(t *testing.T, rec *startRecord, source, session string) {
	t.Helper()
	for _, key := range []string{secureStorageEnv, startHoldEnv, startFamilyEnv} {
		t.Setenv(key, envValue(rec.env, key))
	}
	input := fmt.Sprintf(`{"source":%q,"session_id":%q,"hook_event_name":"SessionStart"}`, source, session)
	if code, out, msg := runCodex(t, input, "start", "--started"); code != 0 || out != "" || msg != "" {
		t.Fatalf("SessionStart hook: %d %q %q", code, out, msg)
	}
	for _, key := range []string{secureStorageEnv, startHoldEnv, startFamilyEnv} {
		testutil.Unsetenv(t, key)
	}
}

func tycswapLog(t *testing.T, sw *core.Switcher) string {
	t.Helper()
	return readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log"))
}

// requireFableStoreUntouched: the store is still on Account-2, start neither
// switched nor held it, and the other session is still linked to it.
func requireFableStoreUntouched(t *testing.T, sw *core.Switcher, other string) {
	t.Helper()
	if !strings.Contains(storeLive(t, sw), "at-f2") || strings.Contains(tycswapLog(t, sw), "Switched the Fable store") {
		t.Fatalf("the start moved the Fable store: %s\n%s", storeLive(t, sw), tycswapLog(t, sw))
	}
	if holds := holdsIn(t, fableStatePath(sw)); holds != nil {
		t.Fatalf("the start held the Fable store: %v", holds)
	}
	requireLinkTarget(t, other, fableStoreDir())
}

func TestLinkedFableStartLeavesTheFableStoreAlone(t *testing.T) {
	const notice = "tycswap start: Fable store: Account-2 is no start login; starting on the default login.\n"
	for name, args := range map[string][]string{
		"fresh start": {"start", "--", "claude", "--model", "fable"},
		"resume":      {"start", "--", "claude", "--resume", startedFableSession},
		"print mode":  {"start", "--", "claude", "-p", "--model", "fable", "say hi"},
	} {
		t.Run(name, func(t *testing.T) {
			sw, rec := fableStoreOffStartLogins(t)
			other := bindFableSession(t, sw, otherFableSession, fableStoreDir())
			source := "startup"
			if name == "resume" {
				recordedMythosSession(t, fableStoreDir())
				source = "resume"
			}
			code, _, stderr := runCLI(t, args, false, false)
			if code != 23 || !strings.HasPrefix(stderr, notice) {
				t.Fatalf("start: code=%d stderr=%q, want it to begin %q", code, stderr, notice)
			}
			link := assertNativeStore(t, rec.env, "")
			// The default login is used as it is: no selection runs there.
			if !strings.Contains(defaultLive(t, sw), "at-d3") || len(rec.selections) != 0 || envValue(rec.env, startHoldEnv) != "" || holdsIn(t, sw.StatePath()) != nil {
				t.Fatalf("default login %s, selections %q, hold %q", defaultLive(t, sw), rec.selections, envValue(rec.env, startHoldEnv))
			}
			requireFableStoreUntouched(t, sw, other)
			// The hook alone, without a status line, takes the session to Fable room.
			startedHook(t, rec, source, startedFableSession)
			requireLinkTarget(t, link, fableStoreDir())
			requireMoveLog(t, sw, startedFableSession, "hook", "default login (Account-3)", "Fable store (Account-2)")
			requireFableStoreUntouched(t, sw, other)
		})
	}
}

// The store on a start login with room serves the start itself; on one at its
// limit the session starts on the default login, and the store is not held.
func TestLinkedFableStartOnAStartLoginsStore(t *testing.T) {
	fable := []string{"start", "--", "claude", "--model", "fable"}
	t.Run("with room", func(t *testing.T) {
		sw, rec := fableStoreOffStartLogins(t)
		setRecord(t, sw, "2", "startLogin", true)
		before := readOrAbsent(t, fableStatePath(sw))
		code, _, stderr := runCLI(t, fable, false, false)
		if code != 23 || strings.Contains(stderr, "Fable store:") {
			t.Fatalf("start: code=%d stderr=%q", code, stderr)
		}
		assertNativeStore(t, rec.env, fableStoreDir())
		if !strings.Contains(storeLive(t, sw), "at-f2") || readOrAbsent(t, fableStatePath(sw)) != before {
			t.Fatalf("the start wrote the Fable store: %s", storeLive(t, sw))
		}
	})
	t.Run("at its limit", func(t *testing.T) {
		sw, rec := fableStartLogin(t, fableTickFixture{def: "3", live: "1", logins: []string{"1", "2"},
			usage: map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 10}, "3": {10, 20, 100}, "4": {10, 20, 100}}}, "1", "3")
		startResolve = func(string) (string, error) { return "/fixture/claude", nil }
		const notice = "tycswap start: Fable store: start login Account-1 is at its limit; starting on the default login.\n"
		code, _, stderr := runCLI(t, fable, false, false)
		if code != 23 || !strings.HasPrefix(stderr, notice) {
			t.Fatalf("start: code=%d stderr=%q, want it to begin %q", code, stderr, notice)
		}
		assertNativeStore(t, rec.env, "")
		if holds := holdsIn(t, fableStatePath(sw)); holds != nil || !strings.Contains(storeLive(t, sw), "at-f1") || len(rec.selections) != 0 {
			t.Fatalf("Fable store %s, holds %v, default selections %q", storeLive(t, sw), holds, rec.selections)
		}
	})
}

// What a link of its own does not change: an explicit account, and a start
// whose link fails, which nothing would move on.
func TestFableStartThatStillMovesTheFableStore(t *testing.T) {
	const switched = "tycswap start: Fable store: Switched to start login Account-1, which is at its limit; auto-switch moves on once the session is up.\n"
	t.Run("--account", func(t *testing.T) {
		sw, rec := fableStoreOffStartLogins(t)
		code, _, stderr := runCLI(t, []string{"start", "--account", "1", "--", "claude", "--model", "fable"}, false, false)
		if code != 23 || !strings.HasPrefix(stderr, switched) || !strings.Contains(storeLive(t, sw), "at-f1") {
			t.Fatalf("start: code=%d stderr=%q, Fable store %s", code, stderr, storeLive(t, sw))
		}
		assertNativeStore(t, rec.env, fableStoreDir())
	})
	t.Run("a failed link", func(t *testing.T) {
		sw, rec := fableStoreOffStartLogins(t)
		links := filepath.Join(sw.BackupDir(), "links")
		if err := os.MkdirAll(links, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(links, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(links, 0700) })
		code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false)
		if code != 23 || !strings.HasPrefix(stderr, switched) || !strings.Contains(stderr, "Session link unavailable") {
			t.Fatalf("start: code=%d stderr=%q", code, stderr)
		}
		if got := envValue(rec.env, secureStorageEnv); got != fableStoreDir() || !strings.Contains(storeLive(t, sw), "at-f1") {
			t.Fatalf("launched on %q, Fable store %s", got, storeLive(t, sw))
		}
	})
}

// With the default login on no start login with room either, nothing is
// there to come up on: the start selects on the Fable store as before.
func TestLinkedFableStartWithoutAStartLoginToComeUpOnIsAsBefore(t *testing.T) {
	const switched = "tycswap start: Fable store: Switched to start login Account-1, which is at its limit; auto-switch moves on once the session is up.\n"
	for name, fixture := range map[string]func(*testing.T) (*core.Switcher, *startRecord){
		"no start login": func(t *testing.T) (*core.Switcher, *startRecord) { return fableStoreOffStartLoginsWith(t, 20, "1") },
		"a start login at its limit": func(t *testing.T) (*core.Switcher, *startRecord) {
			return fableStoreOffStartLoginsWith(t, 100, "1", "3")
		},
	} {
		t.Run(name, func(t *testing.T) {
			sw, rec := fixture(t)
			code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false)
			if code != 23 || !strings.HasPrefix(stderr, switched) || !strings.Contains(storeLive(t, sw), "at-f1") {
				t.Fatalf("start: code=%d stderr=%q, Fable store %s", code, stderr, storeLive(t, sw))
			}
			assertNativeStore(t, rec.env, fableStoreDir())
			if !strings.Contains(defaultLive(t, sw), "at-d3") || len(rec.selections) != 0 {
				t.Fatalf("default login %s, selections %q", defaultLive(t, sw), rec.selections)
			}
		})
	}
}
