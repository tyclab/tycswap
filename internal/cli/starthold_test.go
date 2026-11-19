// Tests for a start login at its limit, the start hold and start --started
// (DESIGN A105).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

const holdVar = "TYCSWAP_START_HOLD"

var holdTokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// heldStart: account 2, the one marked for start, at its weekly limit (with
// room when room is set), account 5 with the most room, the default login on
// 1, auto-switch on, a fake clock, and start's child recorded.
func heldStart(t *testing.T, room bool) (*core.Switcher, *clock.Fake, *startRecord) {
	t.Helper()
	startTestLaunch(t)
	testutil.Unsetenv(t, holdVar)
	week := 100.0
	if room {
		week = 20
	}
	sw := countingStore(t, "claude-fable-5-1", map[string][3]float64{"1": {10, 20, 30}, "2": {10, week, 30}, "5": {10, 10, 10}})
	enabledStore(t, sw.BackupDir())
	if err := sw.SetStartLogin("2", true); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Now())
	sw.Clk = clk
	useSwitcher(t, sw)
	rec := &startRecord{}
	startExec = func(_ string, args, env []string) (int, error) {
		rec.execs++
		rec.args, rec.env = args, env
		return 23, nil
	}
	return sw, clk, rec
}

func heldStateOf(t *testing.T, sw *core.Switcher) map[string]any {
	t.Helper()
	state := map[string]any{}
	if raw := readOrAbsent(t, sw.StatePath()); raw != "<absent>" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func liveSlot(sw *core.Switcher) string {
	if num := sw.CurrentAccountNumber(); num != nil {
		return *num
	}
	return ""
}

// startHeld runs start on heldStart's store and returns the hold token the
// child got.
func startHeld(t *testing.T, rec *startRecord) string {
	t.Helper()
	if code, _, stderr := runCLI(t, []string{"start", "command"}, false, false); code != 23 {
		t.Fatalf("start: code=%d stderr=%q", code, stderr)
	}
	return envValue(rec.env, holdVar)
}

func TestStartAtItsLimitHoldsTheEngineForTheSession(t *testing.T) {
	sw, clk, rec := heldStart(t, false)
	before := os.Environ()
	code, out, stderr := runCLI(t, []string{"start", "command"}, false, false)
	notice := "tycswap start: Switched to start login Account-2, which is at its limit; auto-switch moves on once the session is up.\n"
	if code != 23 || rec.execs != 1 || out != "" || stderr != notice {
		t.Fatalf("code=%d execs=%d out=%q stderr=%q", code, rec.execs, out, stderr)
	}
	token := envValue(rec.env, holdVar)
	if !holdTokenPattern.MatchString(token) || !reflect.DeepEqual(rec.env, append(append([]string(nil), before...), holdVar+"="+token)) {
		t.Fatalf("child environment: token %q, rest preserved %v", token, reflect.DeepEqual(withoutVar(rec.env, holdVar), before))
	}
	if liveSlot(sw) != "2" {
		t.Fatalf("live account %s, want the marked 2", liveSlot(sw))
	}
	state := heldStateOf(t, sw)
	if state["lastSwitchTo"] != "2" || !reflect.DeepEqual(state["startHolds"], map[string]any{token: clock.Seconds(clk) + 30}) {
		t.Fatalf("state = %v", state)
	}
}

// A start on a login with room is v0.16.0's: the notice, the log lines, the
// state file and the child's environment, less an inherited hold.
func TestStartWithRoomRecordsNoHold(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "inherited-hold"}[inherited], func(t *testing.T) {
			sw, clk, rec := heldStart(t, true)
			if inherited {
				t.Setenv(holdVar, strings.Repeat("0", 32))
			}
			logPath := filepath.Join(sw.BackupDir(), "tycswap.log")
			logBefore := strings.TrimPrefix(readOrAbsent(t, logPath), "<absent>")
			before := os.Environ()
			code, out, stderr := runCLI(t, []string{"start", "command"}, false, false)
			if code != 23 || out != "" || stderr != "tycswap start: Switched to start login Account-2.\n" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			if !reflect.DeepEqual(rec.env, withoutVar(before, holdVar)) || inherited == reflect.DeepEqual(rec.env, before) {
				t.Fatalf("child environment: hold %q, inherited one removed %v", envValue(rec.env, holdVar), !inherited || !reflect.DeepEqual(rec.env, before))
			}
			want := `{
  "lastSwitchAt": ` + jsonNum(clock.Seconds(clk)) + `,
  "lastSwitchTo": "2",
  "schemaVersion": 1
}`
			if got := readOrAbsent(t, sw.StatePath()); got != want {
				t.Fatalf("state file %q, want %q", got, want)
			}
			var logged []string
			for _, line := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(readOrAbsent(t, logPath), logBefore), "\n"), "\n") {
				_, message, _ := strings.Cut(line, " - ")
				logged = append(logged, message)
			}
			wantLog := []string{
				"INFO - Backed up account 1 (lineage differs from the stored backup and ownership could not be verified — pre-fix backup)",
				"INFO - Wrote target credentials", "INFO - Updated config file", "INFO - Switched from account 1 to 2",
				"INFO - start: Switched to start login Account-2.", "INFO - Switched to account 2 by start",
			}
			if !reflect.DeepEqual(logged, wantLog) {
				t.Fatalf("log lines %q, want %q", logged, wantLog)
			}
		})
	}
}

func jsonNum(v float64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// start --started releases the session's hold and runs one tick: the login
// leaves the start login at its limit for the account with room.
func TestStartedMovesTheLoginOnOnceTheSessionIsUp(t *testing.T) {
	for _, tc := range []struct {
		name, stdin string
		moves       bool
	}{
		{"startup", `{"session_id":"s-1","source":"startup"}`, true},
		{"resume", `{"session_id":"s-1","source":"resume"}`, true},
		{"empty stdin", "", true},
		{"unreadable stdin", "not json", true},
		{"clear", `{"session_id":"s-1","source":"clear"}`, false},
		{"compact", `{"session_id":"s-1","source":"compact"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, _, rec := heldStart(t, false)
			t.Setenv(holdVar, startHeld(t, rec))
			before := readOrAbsent(t, sw.StatePath())
			code, out, errb := runCodex(t, tc.stdin, "start", "--started")
			if code != 0 || out != "" || errb != "" {
				t.Fatalf("code=%d out=%q err=%q", code, out, errb)
			}
			if !tc.moves {
				if liveSlot(sw) != "2" || readOrAbsent(t, sw.StatePath()) != before {
					t.Fatalf("source %s: live %s, state %v", tc.name, liveSlot(sw), heldStateOf(t, sw))
				}
				return
			}
			if liveSlot(sw) != "5" {
				t.Fatalf("live account %s, want 5 after the session is up", liveSlot(sw))
			}
			if state := heldStateOf(t, sw); state["startHolds"] != nil || state["lastSwitchTo"] != "5" {
				t.Fatalf("state = %v", state)
			}
			if log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")); strings.Count(log, "Switched to account 5 by engine:at-limit") != 1 {
				t.Fatalf("log:\n%s", log)
			}
			// No Fable login: the Fable store's state is read for a hold, never created.
			for _, dir := range []string{paths.FableScopeDir(sw.BackupDir()), paths.FableCredentialsDir(sw.BackupDir())} {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("%s exists (%v)", dir, err)
				}
			}
		})
	}
}

// Without the variable start --started opens no store and reads or writes
// no state.
func TestStartedWithoutItsHoldDoesNothing(t *testing.T) {
	sw, _, rec := heldStart(t, false)
	startHeld(t, rec)
	path := sw.StatePath()
	before := readOrAbsent(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	previous := newSwitcher
	newSwitcher = func(store.Options) (*core.Switcher, error) {
		t.Fatal("start --started without a hold opened the store")
		return nil, nil
	}
	t.Cleanup(func() { newSwitcher = previous })
	code, out, errb := runCodex(t, `{"source":"startup"}`, "start", "--started")
	later, _ := os.Stat(path)
	if code != 0 || out != "" || errb != "" || liveSlot(sw) != "2" || readOrAbsent(t, path) != before || !later.ModTime().Equal(info.ModTime()) {
		t.Fatalf("code=%d out=%q err=%q live=%s state %v", code, out, errb, liveSlot(sw), heldStateOf(t, sw))
	}
}

// A session whose hold is no longer there, ended or released before, still
// gets its one tick: one that took longer than the bound to come up needs the
// login moved most.
func TestStartedTicksAfterItsHoldEnded(t *testing.T) {
	for _, ended := range []bool{true, false} {
		t.Run(map[bool]string{true: "ended", false: "released-before"}[ended], func(t *testing.T) {
			sw, clk, rec := heldStart(t, false)
			token := startHeld(t, rec)
			if ended {
				clk.Advance(31 * time.Second)
			} else if err := autoswitch.ReleaseStartHold(sw.StatePath(), clk, token); err != nil {
				t.Fatal(err)
			}
			t.Setenv(holdVar, token)
			if code, out, errb := runCodex(t, `{"source":"startup"}`, "start", "--started"); code != 0 || out != "" || errb != "" {
				t.Fatalf("code=%d out=%q err=%q", code, out, errb)
			}
			if state := heldStateOf(t, sw); liveSlot(sw) != "5" || state["startHolds"] != nil || state["lastSwitchTo"] != "5" {
				t.Fatalf("live %s, state %v", liveSlot(sw), state)
			}
			if log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")); strings.Count(log, "Switched to account 5 by engine:at-limit") != 1 {
				t.Fatalf("log:\n%s", log)
			}
		})
	}
}

// sleepClock is a fake clock that runs hook on each sleep.
type sleepClock struct {
	*clock.Fake
	hook func()
}

func (c *sleepClock) Sleep(d time.Duration) {
	c.Fake.Sleep(d)
	if c.hook != nil {
		c.hook()
	}
}

// Two starts at once: the second finds the start login current and at its
// limit, holds the engine itself and switches nothing; the first session's
// --started waits until the second reported, or its hold expired, and one
// tick then moves the login on.
func TestStartedWaitsForAnotherStartingSession(t *testing.T) {
	for _, reported := range []bool{true, false} {
		t.Run(map[bool]string{true: "reported", false: "expired"}[reported], func(t *testing.T) {
			sw, clk, rec := heldStart(t, false)
			first := startHeld(t, rec)
			stamp := heldStateOf(t, sw)["lastSwitchAt"]
			code, _, stderr := runCLI(t, []string{"start", "command"}, false, false)
			second := envValue(rec.env, holdVar)
			if code != 23 || stderr != "" || !holdTokenPattern.MatchString(second) || second == first || liveSlot(sw) != "2" {
				t.Fatalf("second start: code=%d stderr=%q token=%q live=%s", code, stderr, second, liveSlot(sw))
			}
			now := clock.Seconds(clk)
			if state := heldStateOf(t, sw); state["lastSwitchAt"] != stamp || !reflect.DeepEqual(state["startHolds"], map[string]any{first: now + 30, second: now + 30}) {
				t.Fatalf("state after the second start = %v", state)
			}
			waiting := &sleepClock{Fake: clk}
			sw.Clk = waiting
			sleeps := 0
			waiting.hook = func() {
				sleeps++
				if sleeps == 1 && liveSlot(sw) != "2" {
					t.Error("the tick ran before the other session was up")
				}
				if reported && sleeps == 1 {
					if err := autoswitch.ReleaseStartHold(sw.StatePath(), waiting, second); err != nil {
						t.Errorf("the second session's release: %v", err)
					}
				}
			}
			t.Setenv(holdVar, first)
			if code, out, errb := runCodex(t, `{"source":"startup"}`, "start", "--started"); code != 0 || out != "" || errb != "" {
				t.Fatalf("code=%d out=%q err=%q", code, out, errb)
			}
			if liveSlot(sw) != "5" || sleeps == 0 {
				t.Fatalf("live %s after %d sleeps, want 5 after a wait", liveSlot(sw), sleeps)
			}
			if reported && (sleeps != 1 || heldStateOf(t, sw)["startHolds"] != nil) {
				t.Fatalf("sleeps %d, state %v", sleeps, heldStateOf(t, sw))
			}
			if !reported && clock.Seconds(clk) < now+30 {
				t.Fatalf("ticked %vs into the second hold", clock.Seconds(clk)-now)
			}
			if log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")); strings.Count(log, "by engine:") != 1 {
				t.Fatalf("log:\n%s", log)
			}
		})
	}
}

// start --started prints nothing and exits 0 whatever happens.
func TestStartedNeverFails(t *testing.T) {
	t.Run("store missing", func(t *testing.T) {
		cleanHome(t)
		t.Setenv(holdVar, strings.Repeat("a", 32))
		if code, out, errb := runCodex(t, `{"source":"startup"}`, "start", "--started"); code != 0 || out != "" || errb != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errb)
		}
		if _, err := os.Stat(paths.GetBackupRoot()); !os.IsNotExist(err) {
			t.Fatalf("created the store: %v", err)
		}
	})
	t.Run("corrupt state", func(t *testing.T) {
		sw, _, rec := heldStart(t, false)
		t.Setenv(holdVar, startHeld(t, rec))
		if err := os.WriteFile(sw.StatePath(), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, out, errb := runCodex(t, `{"source":"startup"}`, "start", "--started"); code != 0 || out != "" || errb != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errb)
		}
		// A corrupt state holds nothing; the tick reads it as empty, as every tick does.
		if liveSlot(sw) != "5" {
			t.Fatalf("live %s, state %q", liveSlot(sw), readOrAbsent(t, sw.StatePath()))
		}
	})
	t.Run("tick fails", func(t *testing.T) {
		sw, _, rec := heldStart(t, false)
		t.Setenv(holdVar, startHeld(t, rec))
		previous := defaultSessionPolicy
		defaultSessionPolicy = func(*store.Store, settings.AutoSwitchSettings, time.Time) reporting.PolicyResult {
			panic("policy unreadable")
		}
		t.Cleanup(func() { defaultSessionPolicy = previous })
		if code, out, errb := runCodex(t, `{"source":"startup"}`, "start", "--started"); code != 0 || out != "" || errb != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errb)
		}
		if state := heldStateOf(t, sw); liveSlot(sw) != "2" || state["startHolds"] != nil {
			t.Fatalf("live %s, state %v", liveSlot(sw), state)
		}
		if log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")); !strings.Contains(log, "policy unreadable") {
			t.Fatalf("log:\n%s", log)
		}
	})
}

func TestStartStartedIsAManagementForm(t *testing.T) {
	calls := startTestLaunch(t)
	for _, args := range [][]string{{"--started", "--list"}, {"--started", "command"}, {"--account", "1", "--started"}, {"--started", "--", "command"}} {
		if code, _, _ := runCLI(t, append([]string{"start"}, args...), false, false); code != 2 || *calls != 0 {
			t.Fatalf("args=%q code=%d calls=%d", args, code, *calls)
		}
	}
}

func TestStartHelpNamesStarted(t *testing.T) {
	cleanHome(t)
	code, out, _ := runCLI(t, []string{"start", "--help"}, false, false)
	for _, want := range []string{
		"\n       tycswap start --started\n",
		"\nStart on a marked login, then launch once (default: real Claude). A marked login with room\n" +
			"comes first; one at its limit is still used, and auto-switch leaves it there until the session\n" +
			"is up: --started, run as Claude Code's SessionStart hook, says so, else the hold ends after 30 s.\n",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
}
