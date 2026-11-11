package cli

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/web"
)

type panelCaptureTray struct {
	*fakeTray
	panel tray.Panel
	sets  int
}

func (t *panelCaptureTray) SetPanel(p tray.Panel) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.panel = p
	t.sets++
}

func (t *panelCaptureTray) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sets
}

// panelNow is a Wednesday afternoon two hours east of UTC: every clock text
// must come out in the zone "now" is given in, not in UTC.
var panelNow = time.Date(2026, 11, 11, 14, 30, 0, 0, time.FixedZone("east", 2*3600))

func resetIn(d time.Duration) string { return panelNow.Add(d).UTC().Format(time.RFC3339) }

func limitWindow(pct any, reset string) map[string]any {
	w := map[string]any{"pct": pct, "resetsAt": nil}
	if reset != "" {
		w["resetsAt"] = reset
	}
	return w
}

// panelRow is a state row with exactly the windows given; models are
// "Name" → window.
func panelRow(provider string, num int, alias, plan string, five, week map[string]any, models map[string]map[string]any) map[string]any {
	usage := map[string]any{}
	if five != nil {
		usage["fiveHour"] = five
	}
	if week != nil {
		usage["sevenDay"] = week
	}
	var scoped []any
	for name, w := range models {
		w["name"] = name
		scoped = append(scoped, w)
	}
	if scoped != nil {
		usage["scoped"] = scoped
	}
	r := map[string]any{"number": num, "alias": alias, "email": alias + "@example.test", "isActive": false, "switchable": true,
		"disabled": false, "usageStatus": "ok", "usage": usage, "provider": provider, "key": provider + ":" + strconv.Itoa(num)}
	if plan != "" {
		r["plan"] = plan
	}
	return r
}

func panelState(rows ...map[string]any) web.State {
	st := sampleState()
	st.Accounts = rows
	return st
}

func cellsOf(p tray.Panel, row tray.PanelRow) map[string]tray.PanelCell {
	out := map[string]tray.PanelCell{}
	for i, cell := range row.Cells {
		out[p.Columns[i+1].ID] = cell
	}
	return out
}

func rowByID(t *testing.T, p tray.Panel, id string) tray.PanelRow {
	t.Helper()
	for _, row := range p.Rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no row %s in %+v", id, p.Rows)
	return tray.PanelRow{}
}

// The daily view (DESIGN A84): Claude rows by slot, then Codex rows; the
// limit columns 5-hour, Weekly and every model window, Fable first; a dash
// where a window is not reported and in Fable for a plan that cannot run it.
func TestTrayPanelRowsColumnsAndDashes(t *testing.T) {
	sh, _, _ := newTestShell(t)
	week := limitWindow(54.0, resetIn(70*time.Hour))
	ultra := panelRow("claude", 1, "first", "Ultra", nil, week, map[string]map[string]any{"Fable": limitWindow(50.0, resetIn(70*time.Hour))})
	pro := panelRow("claude", 2, "second", "Pro", limitWindow(0.0, ""), limitWindow(100.0, resetIn(12*time.Hour)), map[string]map[string]any{"Fable": limitWindow(0.0, "")})
	free := panelRow("claude", 6, "sixth", "free", limitWindow(0.0, ""), limitWindow(10.0, ""), map[string]map[string]any{"Fable": limitWindow(3.0, "")})
	unknown := panelRow("claude", 4, "fourth", "", limitWindow(0.0, ""), limitWindow(100.0, resetIn(10*time.Hour)), map[string]map[string]any{"Fable": limitWindow(90.0, "")})
	opus := panelRow("claude", 3, "third", "Ultra", limitWindow(1.0, ""), limitWindow(2.0, ""), map[string]map[string]any{"Opus": limitWindow(7.0, "")})
	codex := panelRow("codex", 1, "cx", "promax", nil, limitWindow(100.0, resetIn(7*time.Hour)), nil)
	codex["isActive"] = true
	ultra["disabled"] = true
	pro["usageStatus"] = "token_expired"
	opus["switchable"] = false
	opus["isActive"] = true
	p := sh.panelAt(panelState(codex, unknown, pro, opus, ultra, free), panelNow)

	var columns, labels []string
	for _, column := range p.Columns {
		columns, labels = append(columns, column.ID), append(labels, column.Label)
	}
	if !slices.Equal(columns, []string{"account", "fiveHour", "sevenDay", "model:Fable", "model:Opus"}) ||
		!slices.Equal(labels, []string{"Account", "5-hour", "Weekly", "Fable", "Opus"}) {
		t.Fatalf("columns %v %v", columns, labels)
	}
	var order, names []string
	for _, row := range p.Rows {
		order, names = append(order, row.ID), append(names, row.Name)
		if len(row.Cells) != len(p.Columns)-1 {
			t.Fatalf("row %s has %d cells for %d limit columns", row.ID, len(row.Cells), len(p.Columns)-1)
		}
	}
	if !slices.Equal(order, []string{"claude:1", "claude:2", "claude:3", "claude:4", "claude:6", "codex:1"}) {
		t.Fatalf("row order %v", order)
	}
	if names[0] != "#1 first" || names[5] != "Codex #1 cx" {
		t.Fatalf("names %v", names)
	}

	first := cellsOf(p, p.Rows[0])
	if !first["fiveHour"].Absent || first["sevenDay"].Absent || first["sevenDay"].Share != "54%" || first["sevenDay"].Pct != 54 ||
		first["model:Fable"].Share != "50%" || !first["model:Opus"].Absent {
		t.Fatalf("an unreported window is not a dash, or a reported one is: %+v", first)
	}
	if cells := cellsOf(p, rowByID(t, p, "claude:2")); !cells["model:Fable"].Absent || cells["fiveHour"].Absent || cells["fiveHour"].Share != "0%" {
		t.Fatalf("a Pro plan's Fable share is drawn: %+v", cells)
	}
	if cells := cellsOf(p, rowByID(t, p, "claude:6")); !cells["model:Fable"].Absent {
		t.Fatalf("a free plan's Fable share is drawn: %+v", cells)
	}
	if cells := cellsOf(p, rowByID(t, p, "claude:4")); cells["model:Fable"].Absent || cells["model:Fable"].Share != "90%" {
		t.Fatalf("an unknown plan hides the reported share: %+v", cells)
	}

	if row := rowByID(t, p, "claude:1"); row.Plan != "Ultra" || row.Tag != "not rotated" || row.Warning != "" || row.Active || row.Disabled {
		t.Fatalf("an account excluded from rotation: %+v", row)
	}
	if row := rowByID(t, p, "claude:2"); row.Plan != "Pro" || row.Tag != "" || row.Warning != "token expired" {
		t.Fatalf("an expired token: %+v", row)
	}
	if row := rowByID(t, p, "claude:4"); row.Plan != "" || row.Warning != "" {
		t.Fatalf("no plan recorded: %+v", row)
	}
	if row := rowByID(t, p, "claude:3"); !row.Active || !row.Disabled {
		t.Fatalf("the active, unswitchable row: %+v", row)
	}
	if row := rowByID(t, p, "codex:1"); !row.Active || row.Disabled || row.Plan != "promax" {
		t.Fatalf("the active Codex row: %+v", row)
	}
	if p.Title != "tycswap" || p.Offline || p.Note != "" {
		t.Fatalf("title %q, offline %v, note %q", p.Title, p.Offline, p.Note)
	}
}

// A failed reading is said in the dashboard's words; an API-key account,
// which reports no usage by design, has no warning.
func TestTrayPanelWarningIsWords(t *testing.T) {
	sh, _, _ := newTestShell(t)
	for status, want := range map[string]string{"ok": "", "api_key": "", "token_expired": "token expired", "relogin_required": "re-login needed",
		"keychain_unavailable": "keychain unavailable", "no_credentials": "no credentials", "unavailable": "usage unavailable", "some_new_state": "some new state"} {
		row := panelRow("claude", 1, "a", "Ultra", nil, limitWindow(5.0, ""), nil)
		row["usageStatus"] = status
		if got := sh.panelAt(panelState(row), panelNow).Rows[0].Warning; got != want {
			t.Errorf("status %s warns %q, want %q", status, got, want)
		}
	}
}

// A Codex row is coloured against the Codex engine's one bar, not Claude's.
func TestTrayPanelCodexRowsUseTheCodexBar(t *testing.T) {
	sh, _, _ := newTestShell(t)
	claude := panelRow("claude", 1, "a", "Ultra", nil, limitWindow(92.0, ""), nil)
	codex := panelRow("codex", 1, "b", "", limitWindow(92.0, ""), limitWindow(92.0, ""), nil)
	st := panelState(claude, codex)
	st.Auto = &web.AutoView{Codex: &web.CodexAutoView{Enabled: true, Threshold: 90}}
	p := sh.panelAt(st, panelNow)
	if cells := cellsOf(p, rowByID(t, p, "claude:1")); cells["sevenDay"].Limit {
		t.Fatal("a Claude week at 92% took the limit colour under a 97% bar")
	}
	if cells := cellsOf(p, rowByID(t, p, "codex:1")); !cells["sevenDay"].Limit || !cells["fiveHour"].Limit {
		t.Fatalf("a Codex row at 92%% is under the Codex bar of 90: %+v", cells)
	}
}

// Each limit carries its own reset under the bar: clock time and time left,
// the 5-hour one as a plain time, the others with the day.
func TestTrayPanelResetLines(t *testing.T) {
	sh, _, _ := newTestShell(t)
	resets := map[string]time.Duration{
		"today":     2 * time.Hour,                 // 16:30 Wed
		"midnight":  9*time.Hour + 45*time.Minute,  // 00:15 Thu here, still Wed in UTC
		"thursday":  20 * time.Hour,                // 10:30 Thu
		"saturday":  70 * time.Hour,                // 12:30 Sat
		"sixDays":   6*24*time.Hour + 2*time.Hour,  // 16:30 Tue
		"sevenDays": 6*24*time.Hour + 20*time.Hour, // 10:30 Wed next week
	}
	want := map[string]string{
		"today":     "Today 16:30 · 2h 0m",
		"midnight":  "Thu 00:15 · 9h 45m",
		"thursday":  "Thu 10:30 · 20h 0m",
		"saturday":  "Sat 12:30 · 2d 22h",
		"sixDays":   "Tue 16:30 · 6d 2h",
		"sevenDays": "Wed 18 Nov · 6d 20h",
	}
	for name, d := range resets {
		row := panelRow("claude", 1, "a", "Ultra", limitWindow(3.0, resetIn(3*time.Hour+52*time.Minute)), limitWindow(1.0, resetIn(d)), nil)
		p := sh.panelAt(panelState(row), panelNow)
		cells := cellsOf(p, p.Rows[0])
		if got := cells["sevenDay"].Reset; got != want[name] {
			t.Errorf("%s: weekly reset %q, want %q", name, got, want[name])
		}
		if got := cells["fiveHour"].Reset; got != "18:22 · 3h 52m" {
			t.Errorf("5-hour reset %q", got)
		}
	}
	for _, reset := range []string{"", "not a time", resetIn(-time.Minute)} {
		row := panelRow("claude", 1, "a", "Ultra", limitWindow(0.0, reset), limitWindow(5.0, reset), nil)
		p := sh.panelAt(panelState(row), panelNow)
		if cells := cellsOf(p, p.Rows[0]); cells["fiveHour"].Reset != "" || cells["sevenDay"].Reset != "" || cells["sevenDay"].Share != "5%" {
			t.Errorf("reset %q drew a reset line: %+v", reset, cells)
		}
	}
}

// Share and bar turn the limit colour at each window's own auto-switch bar
// (85 / 97 / 95 in the sample state), judged on the share as it is shown:
// "97%" is never drawn below a 97 bar.
func TestTrayPanelLimitLevelFollowsEachBar(t *testing.T) {
	sh, _, _ := newTestShell(t)
	for _, check := range []struct {
		five, week, fable float64
		limit             bool
	}{{85, 97, 95, true}, {84.5, 96.5, 94.5, true}, {84.4, 96.4, 94.4, false}} {
		row := panelRow("claude", 1, "a", "Ultra", limitWindow(check.five, ""), limitWindow(check.week, ""), map[string]map[string]any{"Fable": limitWindow(check.fable, "")})
		p := sh.panelAt(panelState(row), panelNow)
		for id, cell := range cellsOf(p, p.Rows[0]) {
			if cell.Limit != check.limit {
				t.Errorf("%s at %s: limit %v, want %v", id, cell.Share, cell.Limit, check.limit)
			}
		}
	}
}

func footerIDs(p tray.Panel) []string {
	var ids []string
	for _, action := range p.Actions {
		ids = append(ids, action.ID)
	}
	return ids
}

// The footer: Quit (and the update buttons while one waits) on the left,
// the two switches and Open dashboard on the right, in Tab order.
func TestTrayPanelFooterActions(t *testing.T) {
	sh, ft, rig := updatesShell(t, false)
	pt := &panelCaptureTray{fakeTray: ft}
	sh.tray = pt
	sh.act.Autostart = func() (bool, error) { return true, nil }
	sh.repaint()
	if ids := footerIDs(pt.panel); !slices.Equal(ids, []string{"quit", "auto", "autostart", "open"}) {
		t.Fatalf("footer without an update %v", ids)
	}
	for _, action := range pt.panel.Actions {
		want := map[string]string{"quit": "Quit", "auto": "Auto-switch", "autostart": "Start at login", "open": "Open dashboard"}[action.ID]
		toggle := action.ID == "auto" || action.ID == "autostart"
		if action.Title != want || (action.Kind == tray.KindToggle) != toggle || action.Disabled {
			t.Fatalf("footer action %+v", action)
		}
		if action.ID == "autostart" && !action.Checked || action.ID == "auto" && action.Checked {
			t.Fatalf("switch state %+v", action)
		}
	}
	rig.latest = "v2.2.0"
	rig.claude = brewSeat("2.1.280", "2.1.281")
	if _, err := sh.checkRelease(); err != nil {
		t.Fatal(err)
	}
	sh.checkClaudeCode(false)
	sh.repaint()
	if ids := footerIDs(pt.panel); !slices.Equal(ids, []string{"quit", "install-update", "claude-code", "auto", "autostart", "open"}) {
		t.Fatalf("footer with updates %v", ids)
	}
	if pt.panel.Actions[1].Title != "Update tycswap" || pt.panel.Actions[2].Title != "Update Claude" {
		t.Fatalf("update buttons %+v", pt.panel.Actions[1:3])
	}
}

// Out of reach, the note says why and Auto-switch is off limits; Start at
// login acts on this machine and stays usable. The rows keep the last state.
// Before any state the list says "No data yet".
func TestTrayPanelOfflineAndBeforeTheFirstState(t *testing.T) {
	sh, _, _ := newTestShell(t)
	sh.act.Offline = func() (bool, string) { return true, "engine unreachable at http://127.0.0.1:7337" }
	p := sh.panelAt(sampleState(), panelNow)
	if !p.Offline || p.Note != "Engine unreachable at http://127.0.0.1:7337" || !p.Alert || len(p.Rows) != 2 {
		t.Fatalf("offline panel %+v", p)
	}
	for _, action := range p.Actions {
		if (action.ID == "auto") != action.Disabled {
			t.Fatalf("offline action %+v", action)
		}
	}
	if p := sh.panelAt(web.State{}, panelNow); p.Empty != "No data yet" || len(p.Rows) != 0 {
		t.Fatalf("before the first state %+v", p)
	}
	st := sampleState()
	st.Accounts = nil
	if p := sh.panelAt(st, panelNow); p.Empty == "No data yet" || p.Empty == "" {
		t.Fatalf("an empty store says %q", p.Empty)
	}
}

// Countdowns on an open panel are never more than a refresh old: without a
// new state the panel is laid out again.
func TestTrayPanelRefreshesCountdowns(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	pt := &panelCaptureTray{fakeTray: ft}
	sh.tray = pt
	sh.panelRefresh = 5 * time.Millisecond
	t.Cleanup(func() {
		sh.mu.Lock()
		sh.panelRefresh = time.Hour
		sh.refresh.Stop()
		sh.mu.Unlock()
	})
	sh.update(sampleState())
	deadline := time.Now().Add(2 * time.Second)
	for pt.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pt.count() < 3 {
		t.Fatalf("panel set %d times without a new state", pt.count())
	}
}

// ---- switching from the tray (DESIGN A84) ----

// switchShell is a shell with a Claude and a Codex row and auto-switch as
// given; calls lists every hook run, in order.
func switchShell(t *testing.T, on, dryRun bool) (*appShell, *fakeTray, *[]string) {
	t.Helper()
	sh, ft, calls := newTestShell(t)
	st := codexState()
	st.Auto = &web.AutoView{Running: on, DryRun: dryRun, Codex: &web.CodexAutoView{Enabled: true, Threshold: 90}}
	if on {
		_ = sh.act.AutoStart()
	}
	sh.update(st)
	*calls, ft.notes = nil, nil
	return sh, ft, calls
}

func lastNote(ft *fakeTray) string {
	notes := ft.notesNow()
	if len(notes) == 0 {
		return ""
	}
	return notes[len(notes)-1]
}

func TestTraySwitchTurnsAutoSwitchOff(t *testing.T) {
	sh, ft, calls := switchShell(t, true, false)
	sh.click("switch:claude:2")
	if got := strings.Join(*calls, ","); got != "auto-stop,switch:claude:2" {
		t.Fatalf("calls %s", got)
	}
	p := sh.panel(sh.last)
	if p.Note != "Switched to #2. Auto-switch is now off, so the login stays there." || p.Alert {
		t.Fatalf("note %q", p.Note)
	}
	if note := lastNote(ft); !strings.HasPrefix(note, "Switched to account #2 |") || !strings.Contains(note, "Auto-switch is now off") {
		t.Fatalf("notification %q", note)
	}
	if auto := p.Actions[slices.IndexFunc(p.Actions, func(a tray.Item) bool { return a.ID == "auto" })]; auto.Checked {
		t.Fatal("the auto-switch switch still reads on")
	}
	sh.click("autostart")
	if p := sh.panel(sh.last); p.Note != "" {
		t.Fatalf("a click on a switch kept the note %q", p.Note)
	}
}

func TestTraySwitchWithAutoSwitchOffLeavesItAlone(t *testing.T) {
	sh, ft, calls := switchShell(t, false, false)
	sh.click("switch:claude:2")
	if got := strings.Join(*calls, ","); got != "switch:claude:2" {
		t.Fatalf("calls %s", got)
	}
	if p := sh.panel(sh.last); p.Note != "Switched to #2." {
		t.Fatalf("note %q", p.Note)
	}
	if note := lastNote(ft); note != "Switched to account #2 | Restart running Claude Code sessions to pick it up." {
		t.Fatalf("notification %q", note)
	}
}

func TestTraySwitchIsNotMadeWhenAutoSwitchStaysOn(t *testing.T) {
	sh, ft, calls := switchShell(t, true, false)
	sh.act.AutoStop = func() error { *calls = append(*calls, "auto-stop"); return errors.New("settings.json is read-only") }
	sh.click("switch:claude:2")
	if got := strings.Join(*calls, ","); got != "auto-stop" {
		t.Fatalf("calls %s", got)
	}
	p := sh.panel(sh.last)
	if !strings.Contains(p.Note, "settings.json is read-only") || !strings.Contains(p.Note, "Not switched") || !p.Alert {
		t.Fatalf("note %q", p.Note)
	}
	if note := lastNote(ft); !strings.HasPrefix(note, "Switch failed |") || !strings.Contains(note, "settings.json is read-only") {
		t.Fatalf("notification %q", note)
	}
}

func TestTraySwitchFailureTurnsAutoSwitchBackOn(t *testing.T) {
	sh, ft, calls := switchShell(t, true, false)
	sh.act.SwitchTo = func(key string) ([]int, error) {
		*calls = append(*calls, "switch:"+key)
		return nil, errors.New("slot 2 has no stored login")
	}
	sh.click("switch:claude:2")
	if got := strings.Join(*calls, ","); got != "auto-stop,switch:claude:2,auto-start" {
		t.Fatalf("calls %s", got)
	}
	if p := sh.panel(sh.last); p.Note != "Switch failed: slot 2 has no stored login" || !p.Alert {
		t.Fatalf("note %q", p.Note)
	}
	if note := lastNote(ft); note != "Switch failed | slot 2 has no stored login" {
		t.Fatalf("notification %q", note)
	}
}

func TestTraySwitchFailureSaysAutoSwitchStaysOff(t *testing.T) {
	sh, ft, calls := switchShell(t, true, false)
	sh.act.SwitchTo = func(key string) ([]int, error) {
		*calls = append(*calls, "switch:"+key)
		return nil, errors.New("slot 2 has no stored login")
	}
	sh.act.AutoStart = func() error { *calls = append(*calls, "auto-start"); return errors.New("settings.json is read-only") }
	sh.click("switch:claude:2")
	if got := strings.Join(*calls, ","); got != "auto-stop,switch:claude:2,auto-start" {
		t.Fatalf("calls %s", got)
	}
	want := "slot 2 has no stored login; auto-switch stays off: settings.json is read-only"
	if p := sh.panel(sh.last); p.Note != "Switch failed: "+want || !p.Alert {
		t.Fatalf("note %q", p.Note)
	}
	if note := lastNote(ft); note != "Switch failed | "+want {
		t.Fatalf("notification %q", note)
	}
}

// Two Switch clicks run one after the other: a first that fails cannot turn
// auto-switch back on behind the second.
func TestTraySwitchClicksRunOneAtATime(t *testing.T) {
	sh, _, calls := switchShell(t, true, false)
	var mu sync.Mutex
	record := func(call string) { mu.Lock(); *calls = append(*calls, call); mu.Unlock() }
	running := false
	sh.act.AutoRunning = func() bool { mu.Lock(); defer mu.Unlock(); return running }
	running = true
	sh.act.AutoStop = func() error { record("auto-stop"); mu.Lock(); running = false; mu.Unlock(); return nil }
	sh.act.AutoStart = func() error { record("auto-start"); mu.Lock(); running = true; mu.Unlock(); return nil }
	inFirst, release := make(chan struct{}), make(chan struct{})
	sh.act.SwitchTo = func(key string) ([]int, error) {
		record("switch:" + key)
		if key == "claude:2" {
			close(inFirst)
			<-release
			return nil, errors.New("slot 2 has no stored login")
		}
		return nil, nil
	}
	done := make(chan struct{}, 2)
	go func() { sh.click("switch:claude:2"); done <- struct{}{} }()
	<-inFirst
	go func() { sh.click("switch:codex:3"); done <- struct{}{} }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	<-done
	<-done
	mu.Lock()
	got, on := strings.Join(*calls, ","), running
	mu.Unlock()
	if got != "auto-stop,switch:claude:2,auto-start,auto-stop,switch:codex:3" || on {
		t.Fatalf("calls %s, auto-switch on %v", got, on)
	}
	if p := sh.panel(sh.last); p.Note != "Switched to Codex #3. Auto-switch is now off, so the login stays there." {
		t.Fatalf("note %q", p.Note)
	}
}

// The API-key approval is recorded only for a switch that is made.
func TestTraySwitchApprovesAnAPIKeyOnlyWhenItSwitches(t *testing.T) {
	sh, _, calls := switchShell(t, true, false)
	st := sh.last
	key := acct(4, "key@example.com", "", false, nil, nil, "", nil)
	key["kind"] = "api_key"
	st.Accounts = append(st.Accounts, key)
	sh.update(st)
	var approved []string
	sh.act.ApproveAPIKey = func(num string) { approved = append(approved, num); *calls = append(*calls, "approve:"+num) }
	sh.act.RestartNotice = func() string { return "" }
	sh.act.Ask = func(string, string, string, string) (bool, error) { return true, nil }
	sh.act.AutoStop = func() error { *calls = append(*calls, "auto-stop"); return errors.New("settings.json is read-only") }
	sh.click("switch:claude:4")
	if len(approved) != 0 {
		t.Fatalf("approved %v without a switch (calls %v)", approved, *calls)
	}
	sh.act.AutoStop = func() error { *calls = append(*calls, "auto-stop"); return nil }
	*calls = nil
	sh.click("switch:claude:4")
	if got := strings.Join(*calls, ","); got != "auto-stop,approve:4,switch:claude:4" {
		t.Fatalf("calls %s", got)
	}
}

// The note is the last panel action's result or error, whichever action it
// was (DESIGN A84); the notification stays.
func TestTrayPanelNoteTellsEveryAction(t *testing.T) {
	sh, ft, rig := updatesShell(t, false)
	note := func() (string, bool) { p := sh.panel(sh.last); return p.Note, p.Alert }
	sh.act.OpenDashboard = func() error { return errors.New("no browser") }
	sh.click("open")
	if text, alert := note(); text != "Could not open the dashboard: no browser" || !alert {
		t.Fatalf("open: %q %v", text, alert)
	}
	sh.click("switch:gemini:1")
	if text, alert := note(); text != "Switch failed: no gemini accounts here." || !alert {
		t.Fatalf("unknown provider: %q %v", text, alert)
	}
	st := sh.last
	key := acct(3, "key@example.com", "", false, nil, nil, "", nil)
	key["kind"] = "api_key"
	st.Accounts = append(st.Accounts, key)
	sh.update(st)
	sh.act.Ask = nil
	sh.click("switch:claude:3")
	if text, alert := note(); !strings.HasPrefix(text, "Not switched: account #3 uses an API key") || !alert {
		t.Fatalf("API key without a question: %q %v", text, alert)
	}
	rig.latest = "v2.2.0"
	if _, err := sh.checkRelease(); err != nil {
		t.Fatal(err)
	}
	sh.act.Upgrade = func() (string, error) { return "", errors.New("offline") }
	sh.click("install-update")
	if text, alert := note(); text != "Update failed: offline" || !alert {
		t.Fatalf("failed update: %q %v", text, alert)
	}
	sh.act.Upgrade = func() (string, error) { return "Updated tycswap 2.1.0 → 2.2.0", nil }
	sh.click("install-update")
	if text, alert := note(); text != "Updated tycswap 2.1.0 → 2.2.0" || alert {
		t.Fatalf("update: %q %v", text, alert)
	}
	rig.claude = brewSeat("2.1.280", "2.1.281")
	sh.checkClaudeCode(false)
	sh.click("claude-code")
	if text, alert := note(); text != "Claude Code updated. Restart running Claude Code sessions to use the new version." || alert {
		t.Fatalf("Claude Code update: %q %v", text, alert)
	}
	if notes := strings.Join(ft.notesNow(), "\n"); !strings.Contains(notes, "Could not open the dashboard | ") || !strings.Contains(notes, "Update failed | offline") {
		t.Fatalf("notifications %q", notes)
	}
}

func TestTraySwitchLeavesADryRunAlone(t *testing.T) {
	sh, _, calls := switchShell(t, true, true)
	sh.click("switch:claude:2")
	if got := strings.Join(*calls, ","); got != "switch:claude:2" {
		t.Fatalf("calls %s", got)
	}
	if p := sh.panel(sh.last); p.Note != "Switched to #2." {
		t.Fatalf("note %q", p.Note)
	}
}

// Without Codex rotation nothing would move a Codex login, so a Codex
// switch leaves auto-switch on.
func TestTraySwitchCodexWithoutCodexRotationLeavesAutoSwitchOn(t *testing.T) {
	sh, _, calls := switchShell(t, true, false)
	sh.last.Auto.Codex.Enabled = false
	sh.click("switch:codex:3")
	if got := strings.Join(*calls, ","); got != "switch:codex:3" {
		t.Fatalf("calls %s", got)
	}
	if p := sh.panel(sh.last); p.Note != "Switched to Codex #3." {
		t.Fatalf("note %q", p.Note)
	}
}

func TestTraySwitchCodexTurnsAutoSwitchOff(t *testing.T) {
	sh, ft, calls := switchShell(t, true, false)
	sh.click("switch:codex:3")
	if got := strings.Join(*calls, ","); got != "auto-stop,switch:codex:3" {
		t.Fatalf("calls %s", got)
	}
	if p := sh.panel(sh.last); p.Note != "Switched to Codex #3. Auto-switch is now off, so the login stays there." {
		t.Fatalf("note %q", p.Note)
	}
	if note := lastNote(ft); note != "Switched Codex to account #3 | The next codex session uses it. Auto-switch is now off, so the login stays there." {
		t.Fatalf("notification %q", note)
	}
	sh, _, calls = switchShell(t, false, false)
	sh.click("switch:codex:3")
	if got := strings.Join(*calls, ","); got != "switch:codex:3" {
		t.Fatalf("calls with auto-switch off %s", got)
	}
	if p := sh.panel(sh.last); p.Note != "Switched to Codex #3." {
		t.Fatalf("note %q", p.Note)
	}
}

// A panel action is a command; nothing else reaches the shell.
func TestTrayPanelActionRunsCommands(t *testing.T) {
	sh, _, calls := newTestShell(t)
	sh.update(sampleState())
	*calls = nil
	sh.panelAction(tray.PanelAction{Kind: "command", Key: "switch:claude:2"})
	sh.panelAction(tray.PanelAction{Kind: "sort", Key: "switch:claude:1"})
	if got := strings.Join(*calls, ","); got != "switch:claude:2" {
		t.Fatalf("calls %s", got)
	}
}

// orderedPanelTray records every menu and panel it is given, in order: two
// repaints overlapping show as a menu followed by another menu before its
// panel.
type orderedPanelTray struct {
	*fakeTray
	order []string
	panel tray.Panel
}

func (t *orderedPanelTray) SetMenu(m []tray.Item) {
	t.mu.Lock()
	t.menu = m
	t.order = append(t.order, "menu")
	t.mu.Unlock()
}

func (t *orderedPanelTray) SetPanel(p tray.Panel) {
	t.mu.Lock()
	t.panel = p
	t.order = append(t.order, "panel")
	t.mu.Unlock()
}

func TestTrayPanelRepaintsDoNotInterleave(t *testing.T) {
	sh, ft, rig := updatesShell(t, false)
	pt := &orderedPanelTray{fakeTray: ft}
	sh.tray = pt
	// Autostart is read while the panel is laid out; a pause there is the
	// window in which the other check's repaint could land first.
	sh.act.Autostart = func() (bool, error) { time.Sleep(2 * time.Millisecond); return false, nil }
	rig.latest = "v2.2.0"
	rig.claude = brewSeat("2.1.280", "2.1.281")
	sh.click("update")
	pt.mu.Lock()
	order, panel := pt.order, pt.panel
	pt.mu.Unlock()
	if len(order) < 4 {
		t.Fatalf("expected a repaint per check, got %v", order)
	}
	for i, step := range order {
		if want := []string{"menu", "panel"}[i%2]; step != want {
			t.Fatalf("repaints interleaved: %v", order)
		}
	}
	for _, id := range []string{"install-update", "claude-code"} {
		if !slices.ContainsFunc(panel.Actions, func(a tray.Item) bool { return a.ID == id && !a.Disabled }) {
			t.Fatalf("%s missing after both checks: %v", id, order)
		}
	}
}
