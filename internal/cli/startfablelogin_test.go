// Tests that a Fable session starts on a start login on the Fable store too
// (DESIGN A105, F2-1).
package cli

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/launch"
	"github.com/tyclab/tycswap/internal/testutil"
)

const fableStartNotice = "tycswap start: no start login has a Fable login; starting on the default login.\n"

// fableStartLogin builds fx with marks on the given accounts and makes start
// record its child and its default selections.
func fableStartLogin(t *testing.T, fx fableTickFixture, marked ...string) (*core.Switcher, *startRecord) {
	t.Helper()
	sw, _ := fx.build(t)
	for _, num := range marked {
		setRecord(t, sw, num, "startLogin", true)
	}
	resetFableSessionProcess(t)
	for _, key := range append([]string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_MODEL", holdVar}, launch.AuthOverrideEnvVars...) {
		testutil.Unsetenv(t, key)
	}
	rec := &startRecord{}
	prevExec, prevResolve, prevDefault, prevSelect := startExec, startResolve, startResolveDefault, startSelect
	t.Cleanup(func() {
		startExec, startResolve, startResolveDefault, startSelect = prevExec, prevResolve, prevDefault, prevSelect
	})
	startExec = func(_ string, args, env []string) (int, error) {
		rec.execs++
		rec.args, rec.env = args, env
		return 23, nil
	}
	startResolve = func(string) (string, error) { return "/verified/command", nil }
	startResolveDefault = func() (string, error) { return "/verified/claude", nil }
	startSelect = func(account, family string, stderr io.Writer) string {
		rec.selections = append(rec.selections, account)
		return prevSelect(account, family, stderr)
	}
	t.Chdir(t.TempDir())
	return sw, rec
}

func startFable(t *testing.T, args ...string) string {
	t.Helper()
	code, _, stderr := runCLI(t, append(append([]string{"start"}, args...), "--", "command", "--model", "fable"), false, false)
	if code != 23 {
		t.Fatalf("start: code=%d stderr=%q", code, stderr)
	}
	return stderr
}

func holdsIn(t *testing.T, path string) map[string]any {
	t.Helper()
	state := map[string]any{}
	if raw := readOrAbsent(t, path); raw != "<absent>" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatal(err)
		}
	}
	holds, _ := state["startHolds"].(map[string]any)
	return holds
}

func defaultLive(t *testing.T, sw *core.Switcher) string {
	return readOrAbsent(t, filepath.Join(sw.Home, ".claude", ".credentials.json"))
}

// The Fable store sits on unmarked account 2; the one marked account, 1, has a
// Fable login at its Fable limit. start moves the Fable store to 1 and holds
// the Fable tick, the default login stays put, and once the session is
// up the Fable tick moves on (the reviewer's probe turned around).
func TestFableSessionStartsOnAStartLogin(t *testing.T) {
	fx := fableTickFixture{def: "3", live: "2", logins: []string{"1", "2"},
		usage: map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 10}, "3": {10, 20, 30}, "4": {10, 20, 100}}}
	sw, rec := fableStartLogin(t, fx, "1")
	setRecord(t, sw, "2", "subscriptionType", "enterprise", "fableContinue", true)
	stderr := startFable(t)
	want := "tycswap start: Fable store: Switched to start login Account-1, which is at its limit; auto-switch moves on once the session is up.\n" + signIn34Notice
	if stderr != want {
		t.Fatalf("stderr %q, want %q", stderr, want)
	}
	token := envValue(rec.env, holdVar)
	if !holdTokenPattern.MatchString(token) || envValue(rec.env, secureStorageVar) != fableStoreDir() {
		t.Fatalf("child: hold token %v, on the Fable store %v", holdTokenPattern.MatchString(token), envValue(rec.env, secureStorageVar) == fableStoreDir())
	}
	if !strings.Contains(storeLive(t, sw), "at-f1") || !strings.Contains(defaultLive(t, sw), "at-d3") || len(rec.selections) != 0 {
		t.Fatalf("Fable store %s, default login %s, default selections %q", storeLive(t, sw), defaultLive(t, sw), rec.selections)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(readOrAbsent(t, fableStatePath(sw))), &state); err != nil || state["lastSwitchTo"] != "1" || state["lastSwitchBy"] != nil {
		t.Fatalf("Fable state %v (%v)", state, err)
	}
	if holds := holdsIn(t, fableStatePath(sw)); !reflect.DeepEqual(holds, map[string]any{token: clock.Seconds(sw.Clk) + 30}) || holdsIn(t, sw.StatePath()) != nil {
		t.Fatalf("holds: Fable %v, default %v", holds, holdsIn(t, sw.StatePath()))
	}
	log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log"))
	if !strings.Contains(log, "Switched the Fable store to account 1 by start") {
		t.Fatalf("log:\n%s", log)
	}
	// A tick before the session is up leaves the Fable store where it started.
	_, out, _ := runCodex(t, "", "auto", "--once", "--json")
	if _, fable, _ := splitByStore(t, out); eventOf(fable, "switch") != nil || !strings.Contains(storeLive(t, sw), "at-f1") {
		t.Fatalf("the Fable tick moved the store during the start hold: %v", fable)
	} else if ns := eventOf(fable, "no-switch"); ns == nil || ns["reason"] != "cooldown" || !strings.HasPrefix(ns["detail"].(string), "a session is starting") {
		t.Fatalf("Fable events %v", fable)
	}
	t.Setenv(holdVar, token)
	if code, out, errb := runCodex(t, `{"source":"startup"}`, "start", "--started"); code != 0 || out != "" || errb != "" {
		t.Fatalf("--started: code=%d out=%q err=%q", code, out, errb)
	}
	if !strings.Contains(storeLive(t, sw), "at-f2") || holdsIn(t, fableStatePath(sw)) != nil {
		t.Fatalf("after --started: Fable store %s, holds %v", storeLive(t, sw), holdsIn(t, fableStatePath(sw)))
	}
}

// A marked Fable login with room ranks before one at its limit and is held
// for nothing; one the Fable store already sits on is left, held only at its
// limit.
func TestFableStartLoginRanksAndHolds(t *testing.T) {
	usage := map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 10}, "3": {10, 20, 30}, "4": {10, 20, 30}}
	t.Run("room first", func(t *testing.T) {
		sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "4", logins: []string{"1", "2", "4"}, usage: usage}, "1", "2")
		if stderr := startFable(t); stderr != "tycswap start: Fable store: Switched to start login Account-2.\ntycswap start: Account-3 needs a Fable sign-in: tycswap fable --login\n" {
			t.Fatalf("stderr %q", stderr)
		}
		if !strings.Contains(storeLive(t, sw), "at-f2") || envValue(rec.env, holdVar) != "" || holdsIn(t, fableStatePath(sw)) != nil {
			t.Fatalf("Fable store %s, hold %q", storeLive(t, sw), envValue(rec.env, holdVar))
		}
	})
	for name, live := range map[string]string{"current with room": "2", "current at its limit": "1"} {
		t.Run(name, func(t *testing.T) {
			sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: live, logins: []string{"1", "2"}, usage: usage}, "1", "2")
			before := readOrAbsent(t, fableStatePath(sw))
			if stderr := startFable(t); stderr != signIn34Notice {
				t.Fatalf("stderr %q", stderr)
			}
			token := envValue(rec.env, holdVar)
			if !strings.Contains(storeLive(t, sw), "at-f"+live) {
				t.Fatalf("Fable store %s, want it left on %s", storeLive(t, sw), live)
			}
			if live == "2" && (token != "" || readOrAbsent(t, fableStatePath(sw)) != before) {
				t.Fatalf("with room: hold %q, Fable state %s", token, readOrAbsent(t, fableStatePath(sw)))
			}
			if live == "1" && (!holdTokenPattern.MatchString(token) || holdsIn(t, fableStatePath(sw))[token] == nil) {
				t.Fatalf("at its limit: hold %q, Fable holds %v", token, holdsIn(t, fableStatePath(sw)))
			}
		})
	}
}

// Marks without a usable Fable login among them: the session starts on the
// default login, which the default selection put on a start login, and says
// why; the Fable store stays.
func TestFableStartWithoutAMarkedFableLogin(t *testing.T) {
	for name, prepare := range map[string]func(*testing.T, *core.Switcher){
		"no Fable login": nil,
		"quarantined": func(t *testing.T, sw *core.Switcher) {
			writeFile(t, fableStatePath(sw), `{"schemaVersion":1,"quarantine":{"1":{"email":"acct1@example.com","reason":"invalid_grant"}}}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			logins := []string{"2"}
			if prepare != nil {
				logins = []string{"1", "2"}
			}
			sw, rec := fableStartLogin(t, fableTickFixture{def: "3", live: "2", logins: logins,
				usage: map[string][3]float64{"1": {10, 20, 10}, "2": {10, 20, 10}, "3": {10, 20, 30}, "4": {10, 20, 30}}}, "1")
			if prepare != nil {
				prepare(t, sw)
			}
			live := storeLive(t, sw)
			stderr := startFable(t)
			if stderr != fableStartNotice+signIn134Notice+"tycswap start: Switched to start login Account-1.\n" {
				t.Fatalf("stderr %q", stderr)
			}
			if envValue(rec.env, secureStorageVar) != "" || !strings.Contains(defaultLive(t, sw), "at-d1") || storeLive(t, sw) != live {
				t.Fatalf("on the Fable store %q, default login %s", envValue(rec.env, secureStorageVar), defaultLive(t, sw))
			}
			if log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")); !strings.Contains(log, "start: no start login has a Fable login; starting on the default login.") {
				t.Fatalf("log:\n%s", log)
			}
		})
	}
}

// storeFiles is every file under the backup root and the home with its
// content.
func storeFiles(t *testing.T, sw *core.Switcher) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range []string{sw.BackupDir(), sw.Home} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				raw, _ := os.ReadFile(path)
				out[path] = string(raw)
			}
			return nil
		})
	}
	return out
}

// No marks, or auto-switch off: a Fable session starts on the Fable store
// where it sits, and start writes nothing, as before.
func TestFableStartWithoutMarksOrAutoSwitchIsAsBefore(t *testing.T) {
	usage := map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 10}, "3": {10, 20, 30}, "4": {10, 20, 30}}
	for name, tc := range map[string]struct {
		marked []string
		off    bool
	}{"no marks": {}, "auto-switch off": {marked: []string{"2"}, off: true}} {
		t.Run(name, func(t *testing.T) {
			sw, rec := fableStartLogin(t, fableTickFixture{def: "3", live: "1", logins: []string{"1", "2"}, usage: usage, off: tc.off}, tc.marked...)
			before, env := storeFiles(t, sw), os.Environ()
			if stderr := startFable(t); stderr != signIn34Notice {
				t.Fatalf("stderr %q", stderr)
			}
			if !reflect.DeepEqual(rec.env, withFableStore(env, fableStoreDir())) {
				t.Fatalf("child environment differs in %q", envDiff(rec.env, withFableStore(env, fableStoreDir())))
			}
			if after := storeFiles(t, sw); !reflect.DeepEqual(after, before) {
				t.Fatal("start wrote a file")
			}
		})
	}
}

// The default login is idle throughout this dedicated launch.
func TestFableStartHoldsOnlyItsOwnStore(t *testing.T) {
	fx := fableTickFixture{def: "3", live: "2", logins: []string{"1", "2"},
		usage: map[string][3]float64{"1": {10, 100, 100}, "2": {10, 20, 10}, "3": {10, 20, 30}, "4": {10, 20, 30}}}
	sw, rec := fableStartLogin(t, fx, "1")
	startFable(t)
	token := envValue(rec.env, holdVar)
	want := map[string]any{token: clock.Seconds(sw.Clk) + 30}
	if !holdTokenPattern.MatchString(token) || len(holdsIn(t, sw.StatePath())) != 0 || !reflect.DeepEqual(holdsIn(t, fableStatePath(sw)), want) {
		t.Fatalf("holds: default %v, Fable %v", holdsIn(t, sw.StatePath()), holdsIn(t, fableStatePath(sw)))
	}
	if !strings.Contains(storeLive(t, sw), "at-f1") || !strings.Contains(defaultLive(t, sw), "at-d3") {
		t.Fatal("expected only the Fable login to move")
	}
	t.Setenv(holdVar, token)
	if code, out, errb := runCodex(t, `{"source":"startup"}`, "start", "--started"); code != 0 || out != "" || errb != "" {
		t.Fatalf("--started: code=%d out=%q err=%q", code, out, errb)
	}
	if holdsIn(t, sw.StatePath()) != nil || holdsIn(t, fableStatePath(sw)) != nil || strings.Contains(storeLive(t, sw), "at-f1") || strings.Contains(defaultLive(t, sw), "at-d1") {
		t.Fatalf("after --started: holds %v / %v, Fable store %s, default %s", holdsIn(t, sw.StatePath()), holdsIn(t, fableStatePath(sw)), storeLive(t, sw), defaultLive(t, sw))
	}
}
