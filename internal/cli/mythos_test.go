// Tests for accounts that run Claude Mythos (DESIGN A106).
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

const familyVar = "TYCSWAP_START_FAMILY"

// mythosStart is start over countingStore: the live default session runs
// liveModel ("" for one without a record), auto-switch is on, start and
// mythos name the marked accounts, and start's child is recorded.
func mythosStart(t *testing.T, liveModel string, usage map[string][3]float64, start, mythos []string) (*core.Switcher, *startRecord) {
	t.Helper()
	startTestLaunch(t)
	testutil.Unsetenv(t, holdVar)
	testutil.Unsetenv(t, familyVar)
	testutil.Unsetenv(t, "ANTHROPIC_MODEL")
	sw := countingStore(t, liveModel, usage)
	enabledStore(t, sw.BackupDir())
	for _, num := range start {
		if err := sw.SetStartLogin(num, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, num := range mythos {
		if err := sw.SetMythos(num, true); err != nil {
			t.Fatal(err)
		}
	}
	sw.Clk = clock.NewFake(time.Now())
	useSwitcher(t, sw)
	rec := &startRecord{}
	startExec = func(_ string, args, env []string) (int, error) {
		rec.execs++
		rec.args, rec.env = args, env
		return 23, nil
	}
	return sw, rec
}

func startModel(t *testing.T, model string) string {
	t.Helper()
	code, _, stderr := runCLI(t, []string{"start", "command", "--model", model}, false, false)
	if code != 23 {
		t.Fatalf("start: code=%d stderr=%q", code, stderr)
	}
	return stderr
}

// A Mythos session starts on an account marked for Mythos, not on a start
// login, and its child learns its family; any other session starts as before.
func TestMythosSessionStartsOnAMythosAccount(t *testing.T) {
	usage := map[string][3]float64{"1": {10, 20, 30}, "2": {10, 10, 10}, "5": {10, 20, 30}}
	for model, want := range map[string]string{"claude-mythos-5-2": "5", "opus": "2"} {
		t.Run(model, func(t *testing.T) {
			sw, rec := mythosStart(t, "", usage, []string{"2"}, []string{"5"})
			stderr := startModel(t, model)
			if liveSlot(sw) != want || stderr != "tycswap start: Switched to start login Account-"+want+".\n" {
				t.Fatalf("live %s, stderr %q; want %s", liveSlot(sw), stderr, want)
			}
			if got := envValue(rec.env, familyVar); got != map[string]string{"5": "mythos", "2": ""}[want] {
				t.Fatalf("child family %q", got)
			}
		})
	}
}

// With no account marked for Mythos a Mythos session starts as any session
// does, with one notice line.
func TestMythosSessionWithoutAMarkedAccount(t *testing.T) {
	sw, rec := mythosStart(t, "", map[string][3]float64{"1": {10, 20, 30}, "2": {10, 10, 10}, "5": {10, 20, 30}}, []string{"2"}, nil)
	stderr := startModel(t, "claude-mythos-5-2")
	want := "tycswap start: no account is marked for Mythos (tycswap mythos --allow ID); selecting a start login as for any session.\n" +
		"tycswap start: Switched to start login Account-2.\n"
	if liveSlot(sw) != "2" || stderr != want {
		t.Fatalf("live %s, stderr %q", liveSlot(sw), stderr)
	}
	if envValue(rec.env, familyVar) != "mythos" {
		t.Fatalf("child family %q", envValue(rec.env, familyVar))
	}
}

// While a Mythos session runs every other session's start logins are the
// ones marked for Mythos too.
func TestStartWhileAMythosSessionRuns(t *testing.T) {
	usage := map[string][3]float64{"1": {10, 20, 30}, "2": {10, 10, 10}, "5": {10, 20, 30}}
	for live, want := range map[string]string{"claude-mythos-5-2": "5", "claude-opus-5-5": "2"} {
		t.Run(live, func(t *testing.T) {
			sw, _ := mythosStart(t, live, usage, []string{"2", "5"}, []string{"5"})
			startModel(t, "opus")
			if liveSlot(sw) != want {
				t.Fatalf("live %s, want %s", liveSlot(sw), want)
			}
		})
	}
}

// A Mythos start on a marked account at its limit: start --started records
// the session as Mythos before its tick, so that tick keeps the login on the
// marked account although an unmarked one has room.
func TestMythosStartAtItsLimitStaysOnTheMarkedAccount(t *testing.T) {
	sw, rec := mythosStart(t, "", map[string][3]float64{"1": {10, 20, 30}, "2": {10, 100, 30}, "5": {10, 10, 10}}, nil, []string{"2"})
	startModel(t, "claude-mythos-5-2")
	token := envValue(rec.env, holdVar)
	if liveSlot(sw) != "2" || !holdTokenPattern.MatchString(token) || envValue(rec.env, familyVar) != "mythos" {
		t.Fatalf("live %s, hold %q, family %q", liveSlot(sw), token, envValue(rec.env, familyVar))
	}
	t.Setenv(holdVar, token)
	t.Setenv(familyVar, "mythos")
	if code, out, errb := runCodex(t, `{"session_id":"s-live","source":"startup"}`, "start", "--started"); code != 0 || out != "" || errb != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errb)
	}
	if family, _, ok := reporting.RecordedSession(sw.BackupDir(), "s-live"); !ok || family != "mythos" {
		t.Fatalf("recorded %q %v", family, ok)
	}
	if liveSlot(sw) != "2" {
		t.Fatalf("live account %s, want the marked 2", liveSlot(sw))
	}
	if state := heldStateOf(t, sw); state["startHolds"] != nil || state["lastSwitchTo"] != "2" {
		t.Fatalf("state = %v", state)
	}
}

// Without a hold start --started records the family and does nothing else:
// no store is opened, no state written.
func TestStartedRecordsTheFamilyWithoutAHold(t *testing.T) {
	sw, _ := mythosStart(t, "", map[string][3]float64{"1": {10, 20, 30}}, nil, nil)
	before := readOrAbsent(t, sw.StatePath())
	previous := newSwitcher
	newSwitcher = func(store.Options) (*core.Switcher, error) {
		t.Fatal("start --started without a hold opened the store")
		return nil, nil
	}
	t.Cleanup(func() { newSwitcher = previous })
	t.Setenv(familyVar, "mythos")
	for _, stdin := range []string{`{"session_id":"s-clear","source":"clear"}`, `{"session_id":"s-live","source":"startup"}`} {
		if code, out, errb := runCodex(t, stdin, "start", "--started"); code != 0 || out != "" || errb != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errb)
		}
	}
	if family, _, ok := reporting.RecordedSession(sw.BackupDir(), "s-live"); !ok || family != "mythos" {
		t.Fatalf("recorded %q %v", family, ok)
	}
	if _, _, ok := reporting.RecordedSession(sw.BackupDir(), "s-clear"); ok {
		t.Fatal("a cleared session was recorded")
	}
	if readOrAbsent(t, sw.StatePath()) != before {
		t.Fatal("state written")
	}
}

// An inherited family leaves every child that is not a Mythos session,
// override paths included.
func TestStartRemovesAnInheritedFamily(t *testing.T) {
	for name, override := range map[string]bool{"selection": false, "override": true} {
		t.Run(name, func(t *testing.T) {
			_, rec := mythosStart(t, "", map[string][3]float64{"1": {10, 20, 30}}, nil, nil)
			t.Setenv(familyVar, "mythos")
			if override {
				t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			}
			before := os.Environ()
			startModel(t, "opus")
			if !reflect.DeepEqual(rec.env, withoutVar(before, familyVar)) {
				t.Fatalf("child family %q", envValue(rec.env, familyVar))
			}
		})
	}
}

// tycswap mythos --allow, --disallow and --list have start's forms and exit
// codes.
func TestMythosCommand(t *testing.T) {
	sw, _ := mythosStart(t, "", map[string][3]float64{"1": {10, 20, 30}}, nil, nil)
	for _, step := range []struct {
		args       []string
		code       int
		out, inErr string
	}{
		{[]string{"--list"}, 0, "", ""},
		{[]string{"--allow", "5"}, 0, "", ""},
		{[]string{"--allow", "2"}, 0, "", ""},
		{[]string{"--list"}, 0, "2\n5\n", ""},
		{[]string{"--disallow", "5"}, 0, "", ""},
		{[]string{"--list"}, 0, "2\n", ""},
		{[]string{"--allow", "9"}, 1, "", "tycswap mythos: "},
		{[]string{}, 2, "", "one of --allow ID, --disallow ID or --list is required"},
		{[]string{"--allow"}, 2, "", "--allow requires an account identifier"},
		{[]string{"--allow", "1", "--list"}, 2, "", "options are exclusive"},
		{[]string{"--json"}, 2, "", "unrecognized option: --json"},
	} {
		code, out, errb := runCLI(t, append([]string{"mythos"}, step.args...), false, false)
		if code != step.code || out != step.out || !strings.Contains(errb, step.inErr) || step.inErr == "" && errb != "" {
			t.Fatalf("%v: code=%d out=%q err=%q", step.args, code, out, errb)
		}
	}
	if !sw.Store.MythosCapable("2") || sw.Store.MythosCapable("5") {
		t.Fatal("marks not written")
	}
	if code, out, _ := runCLI(t, []string{"mythos", "--help"}, false, false); code != 0 || !strings.Contains(out, "mythos --allow ID | --disallow ID | --list") {
		t.Fatalf("help: %d %q", code, out)
	}
}

// The dashboard's count names the accounts a live Mythos session leaves the
// login, or Mythos unservable when none is marked; its ranking (Next best)
// leaves unmarked accounts out.
func TestAutoViewReportsMythos(t *testing.T) {
	usage := map[string][3]float64{"1": {10, 20, 30}, "2": {10, 10, 10}, "5": {10, 20, 30}}
	for name, tc := range map[string]struct {
		marks                       []string
		mythos, unservable, ranking string
	}{
		"marked":     {[]string{"5"}, "5", "", "5"},
		"not-marked": {nil, "", "Mythos", "2,5"},
	} {
		t.Run(name, func(t *testing.T) {
			sw, _ := mythosStart(t, "claude-mythos-5-2", usage, nil, tc.marks)
			a := newAutoFacade(sw, nil)
			a.managedBy = testOwner
			v := a.View()
			if v.Counting == nil || strings.Join(v.Counting.Mythos, ",") != tc.mythos || strings.Join(v.Counting.Unservable, ",") != tc.unservable {
				t.Fatalf("counting = %+v", v.Counting)
			}
			if got := strings.Join(v.Ranking, ","); got != tc.ranking {
				t.Fatalf("ranking %q, want %q", got, tc.ranking)
			}
		})
	}
}

// A switch by strategy best takes the account the engine ranks first, so
// while a Mythos session runs a marked one (DESIGN A99, A106).
func TestSwitchBestWhileAMythosSessionRuns(t *testing.T) {
	usage := map[string][3]float64{"1": {10, 70, 30}, "2": {10, 10, 10}, "5": {10, 20, 30}}
	for name, tc := range map[string]struct {
		marks []string
		want  string
	}{
		"marked":     {[]string{"5"}, "5"},
		"not-marked": {nil, "2"},
	} {
		t.Run(name, func(t *testing.T) {
			sw, _ := mythosStart(t, "claude-mythos-5-2", usage, nil, tc.marks)
			code, out, errb := runCLI(t, []string{"switch", "--strategy", "best", "--json"}, false, false)
			var res map[string]any
			if err := json.Unmarshal([]byte(out), &res); err != nil || code != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errb)
			}
			wantLanding(t, "CLI", "best", res, tc.want)
			if liveSlot(sw) != tc.want {
				t.Fatalf("live %s", liveSlot(sw))
			}
		})
	}
}

// The tray's row tag and menu line name the mark, as they name a start login.
func TestTrayMythosTag(t *testing.T) {
	sh, _, _ := newTestShell(t)
	claude := panelRow("claude", 1, "work", "", nil, nil, nil)
	plain := panelRow("claude", 2, "home", "", nil, nil, nil)
	claude["startLogin"], claude["mythos"] = true, true
	p := sh.panelAt(panelState(claude, plain), panelNow)
	if got := rowByID(t, p, "claude:1").Tag; got != "start login · Mythos" {
		t.Fatalf("marked tag %q", got)
	}
	if got := rowByID(t, p, "claude:2").Tag; got != "" {
		t.Fatalf("unmarked tag %q", got)
	}
	items := gaugeGroup([]map[string]any{claude}, nil, "all", "Accounts")
	if len(items) != 1 || !strings.Contains(items[0].Sub, "Mythos") {
		t.Fatalf("menu tag %+v", items)
	}
}

// The dashboard cli serves sets the mark through the store (DESIGN A106).
func TestDashboardSetsTheMythosMark(t *testing.T) {
	sw, _ := mythosStart(t, "", map[string][3]float64{"1": {10, 20, 30}}, nil, nil)
	_, post := serveDashboard(t, sw, testOwner)
	if status, res := post("/api/accounts/claude:2/mythos/allow", ""); status != 200 {
		t.Fatalf("allow: %d %v", status, res)
	}
	if !sw.Store.MythosCapable("2") {
		t.Fatal("mark not set")
	}
	if status, res := post("/api/accounts/claude:2/mythos/disallow", ""); status != 200 || sw.Store.MythosCapable("2") {
		t.Fatalf("disallow: %d %v", status, res)
	}
}

// A scheduled tick with the live login at its 5-hour limit and an unmarked
// account with the most room: a live Mythos session keeps the login among the
// marked accounts; with no account marked the tick is the one without the
// session, and its poll event names Mythos unservable (DESIGN A106).
func TestAutoOnceWithAMythosSession(t *testing.T) {
	usage := map[string][3]float64{"1": {100, 50, 30}, "2": {5, 10, 30}, "5": {20, 40, 30}}
	for name, tc := range map[string]struct {
		live       string
		marks      []string
		want       string
		unservable string
	}{
		"marked":       {"claude-mythos-5-2", []string{"1", "5"}, "switch to 5", ""},
		"not-marked":   {"claude-mythos-5-2", nil, "switch to 2", `["Mythos"]`},
		"opus-session": {"claude-opus-5-5", []string{"1", "5"}, "switch to 2", ""},
	} {
		t.Run(name, func(t *testing.T) {
			sw, _ := mythosStart(t, tc.live, usage, nil, tc.marks)
			useCodexSource(t, &fakeCodexSource{})
			_, out, errb := runCodex(t, "", "auto", "--once", "--dry-run", "--json")
			moved, unservable := "", ""
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				ev := decodeJSON(t, line)
				switch ev["event"] {
				case "poll":
					if ev["unservable"] != nil {
						raw, _ := json.Marshal(ev["unservable"])
						unservable = string(raw)
					}
				case "switch":
					to, _ := ev["to"].(map[string]any)
					moved = fmt.Sprintf("switch to %v", to["number"])
				}
			}
			if moved != tc.want || unservable != tc.unservable {
				t.Fatalf("%q, unservable %s; want %q, %s (stdout %q stderr %q)", moved, unservable, tc.want, tc.unservable, out, errb)
			}
			if liveSlot(sw) != "1" {
				t.Fatalf("dry run moved the login to %s", liveSlot(sw))
			}
		})
	}
}
