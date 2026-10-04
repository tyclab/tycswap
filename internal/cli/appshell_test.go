package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/switching"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/web"
)

type fakeTray struct {
	mu      sync.Mutex
	title   string
	tooltip string
	menu    []tray.Item
	notes   []string
	icons   []tray.Icon // every SetIcon, in order (A44)
	quit    bool
}

func (f *fakeTray) SetTitle(s string)     { f.mu.Lock(); f.title = s; f.mu.Unlock() }
func (f *fakeTray) SetTooltip(s string)   { f.mu.Lock(); f.tooltip = s; f.mu.Unlock() }
func (f *fakeTray) SetMenu(m []tray.Item) { f.mu.Lock(); f.menu = m; f.mu.Unlock() }
func (f *fakeTray) SetIcon(ic tray.Icon)  { f.mu.Lock(); f.icons = append(f.icons, ic); f.mu.Unlock() }
func (f *fakeTray) Notify(title, body string) error {
	f.mu.Lock()
	f.notes = append(f.notes, title+" | "+body)
	f.mu.Unlock()
	return nil
}
func (f *fakeTray) Run() error { return nil }
func (f *fakeTray) Quit()      { f.mu.Lock(); f.quit = true; f.mu.Unlock() }

// tooltipNow and notesNow read under the lock, for tests where a server
// goroutine repaints meanwhile.
func (f *fakeTray) tooltipNow() string { f.mu.Lock(); defer f.mu.Unlock(); return f.tooltip }
func (f *fakeTray) notesNow() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notes...)
}

// item finds a row by ID, inside submenus too.
func (f *fakeTray) item(id string) (tray.Item, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return findItem(f.menu, id)
}

func findItem(items []tray.Item, id string) (tray.Item, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return tray.Item{}, false
}

func acct(num int, email, alias string, active bool, fiveH, sevenD any, model string, modelPct any) map[string]any {
	usage := map[string]any{
		"fiveHour": map[string]any{"pct": fiveH, "resetsAt": "2026-09-20T12:00:00Z"},
		"sevenDay": map[string]any{"pct": sevenD, "resetsAt": "2026-09-25T00:00:00Z"},
	}
	if model != "" {
		usage["scoped"] = []any{map[string]any{"name": model, "pct": modelPct}}
	}
	return map[string]any{
		"number": num, "email": email, "alias": alias, "isActive": active,
		"switchable": true, "disabled": false, "atLimit": false, "usage": usage,
	}
}

func sampleState() web.State {
	return web.State{
		Accounts: []map[string]any{
			acct(2, "bob@example.com", "", false, 10.0, 5.0, "", nil),
			acct(1, "alice@example.com", "work", true, 45.0, 30.0, "Fable", 12.0),
		},
		Settings: []web.SettingView{
			{Key: "autoswitch.fiveHourThreshold", Value: 85.0},
			{Key: "autoswitch.sevenDayThreshold", Value: 97.0},
			{Key: "autoswitch.modelThreshold", Value: 95.0},
		},
		Auto: &web.AutoView{Running: false},
	}
}

func newTestShell(t *testing.T) (*appShell, *fakeTray, *[]string) {
	t.Helper()
	ft := &fakeTray{}
	var calls []string
	running := false
	act := shellActions{
		OpenDashboard: func() error { calls = append(calls, "open"); return nil },
		SwitchTo:      func(id string) error { calls = append(calls, "switch:"+id); return nil },
		AutoRunning:   func() bool { return running },
		AutoStart:     func() error { running = true; calls = append(calls, "auto-start"); return nil },
		AutoStop:      func() error { running = false; calls = append(calls, "auto-stop"); return nil },
		Upgrade: func() (string, error) {
			calls = append(calls, "upgrade")
			return "Updated tycswap 2.0.0 → 2.1.0", nil
		},
		Autostart: func() (bool, error) { return false, nil },
		SetAutostart: func(on bool) error {
			calls = append(calls, "autostart:"+map[bool]string{true: "on", false: "off"}[on])
			return nil
		},
		Quit: func() { calls = append(calls, "quit") },
	}
	return newAppShell(ft, act), ft, &calls
}

func TestShellTitleTooltipAndMenu(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.update(sampleState())

	if ft.title != "#1 · 45%" {
		t.Errorf("title = %q", ft.title)
	}
	if !strings.HasPrefix(ft.tooltip, "tycswap — ") || !strings.Contains(ft.tooltip, "#1 work") || !strings.Contains(ft.tooltip, "5h 45%") || !strings.Contains(ft.tooltip, "7d 30%") || !strings.Contains(ft.tooltip, "Fable 12%") {
		t.Errorf("tooltip = %q", ft.tooltip)
	}
	// accounts sorted by slot, active one checked and not clickable
	a1, ok := ft.item("switch:1")
	if !ok || !a1.Checked || !a1.Disabled || !strings.HasPrefix(a1.Title, "#1  work") {
		t.Errorf("active row = %+v", a1)
	}
	a2, ok := ft.item("switch:2")
	if !ok || a2.Checked || a2.Disabled || !strings.Contains(a2.Title, "bob@example.com") || !strings.Contains(a2.Sub, "5h 10%") || a2.Pct != 10 {
		t.Errorf("other row = %+v", a2)
	}
	ids, headers := menuShape(ft)
	if ids != "brand,open,switch:1,switch:2,auto,autostart,update,quit" {
		t.Errorf("menu order = %v", ids)
	}
	if br := ft.menu[0]; br.Kind != tray.KindBrand || br.Title != "tycswap" || !strings.Contains(br.Sub, "auto-switch off") || br.Clickable() {
		t.Errorf("brand row = %+v", br)
	}
	if headers != "Accounts,Automation,App" {
		t.Errorf("headers = %v", headers)
	}
	if a1.Kind != tray.KindGauge || a1.Pct != 45 || !strings.Contains(a1.Sub, "5h 45%") {
		t.Errorf("active gauge = %+v", a1)
	}
	if au, _ := ft.item("auto"); au.Kind != tray.KindToggle {
		t.Errorf("auto row should be a toggle: %+v", au)
	}
	if q, _ := ft.item("quit"); q.Title != "Quit tycswap" {
		t.Errorf("quit row = %+v", q)
	}
	if len(ft.notes) != 0 {
		t.Errorf("no notification expected below the threshold, got %v", ft.notes)
	}
}

func TestShellNoActiveAccount(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.update(web.State{})
	if ft.title != "" || !strings.Contains(ft.tooltip, "no active account") {
		t.Errorf("title=%q tooltip=%q", ft.title, ft.tooltip)
	}
	if _, ok := ft.item("none"); !ok {
		t.Error("empty roster should show the hint row")
	}
}

func TestShellClicks(t *testing.T) {
	sh, ft, calls := newTestShell(t)
	sh.update(sampleState())

	sh.click("open")
	sh.click("switch:2")
	sh.click("auto")
	sh.click("auto")
	sh.click("autostart")
	sh.click("install-update")
	sh.click("quit")

	want := "open,switch:2,auto-start,auto-stop,autostart:on,upgrade,quit"
	if got := strings.Join(*calls, ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}
	joined := strings.Join(ft.notes, "\n")
	if !strings.Contains(joined, "Switched to account #2") || !strings.Contains(joined, "2.0.0 → 2.1.0") || !strings.Contains(joined, "Auto-switch on |") || !strings.Contains(joined, "Auto-switch off |") {
		t.Errorf("notifications = %v", ft.notes)
	}
}

func TestShellShowsAutoModeInTitleAndMenu(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.update(sampleState())
	if ft.title != "#1 · 45%" || !strings.Contains(ft.tooltip, "auto-switch off") {
		t.Errorf("off: title=%q tooltip=%q", ft.title, ft.tooltip)
	}
	if it, _ := ft.item("auto"); it.Checked || it.Kind != tray.KindToggle || !strings.HasPrefix(it.Sub, "off") {
		t.Errorf("off: item=%+v", it)
	}
	sh.click("auto") // turns it on and repaints at once
	if ft.title != "⟳ #1 · 45%" || !strings.Contains(ft.tooltip, "auto-switch on") {
		t.Errorf("on: title=%q tooltip=%q", ft.title, ft.tooltip)
	}
	if it, _ := ft.item("auto"); !it.Checked || !strings.Contains(it.Sub, "5h 85% · 7d 97%") {
		t.Errorf("on: item=%+v", it)
	}
	sh.click("auto")
	if ft.title != "#1 · 45%" {
		t.Errorf("off again: title=%q", ft.title)
	}
}

func TestShellClickErrorsBecomeNotifications(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.act.SwitchTo = func(string) error { return errors.New("slot 9 does not exist") }
	sh.act.Upgrade = func() (string, error) { return "", errors.New("offline") }
	sh.click("switch:9")
	sh.click("install-update")
	joined := strings.Join(ft.notes, "\n")
	if !strings.Contains(joined, "Switch failed | slot 9 does not exist") || !strings.Contains(joined, "Update failed | offline") {
		t.Errorf("notifications = %v", ft.notes)
	}
}

// A failed open says where the address is, first: Windows cuts a
// notification's text at 255 characters (A41).
func TestOpenDashboardFailureNotifies(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.act.OpenDashboard = func() error {
		return errors.New("open the Dashboard address from C:\\Users\\a\\app.log in your browser (could not open a browser (ShellExecuteW: returned 31))")
	}
	sh.click("open")
	if len(ft.notes) != 1 || !strings.HasPrefix(ft.notes[0], "Could not open the dashboard | Open the Dashboard address from ") {
		t.Errorf("notifications = %v", ft.notes)
	}
}

func TestShellThresholdAlertOnce(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	st := sampleState()
	st.Accounts[1] = acct(1, "alice@example.com", "work", true, 30.0, 98.0, "", nil)
	sh.update(st)
	sh.update(st) // same state again → no second alert
	if len(ft.notes) != 1 || !strings.Contains(ft.notes[0], "Account #1 has used 98% of its week") {
		t.Fatalf("notes = %v", ft.notes)
	}
	// drops well below → re-armed, crosses again → one more
	st.Accounts[1] = acct(1, "alice@example.com", "work", true, 30.0, 50.0, "", nil)
	sh.update(st)
	st.Accounts[1] = acct(1, "alice@example.com", "work", true, 30.0, 99.0, "", nil)
	sh.update(st)
	if len(ft.notes) != 2 {
		t.Errorf("notes = %v", ft.notes)
	}
}

// Each window is judged against its own bar, and the 5h one is the LOWEST (it
// bursts, A34): 90% of the 5h window is an alert, while the same 90% of the
// week — bar 97 — is not.
func TestShellFiveHourAlertsOnItsOwnThreshold(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	st := sampleState()
	st.Accounts[1] = acct(1, "alice@example.com", "work", true, 10.0, 90.0, "", nil)
	sh.update(st)
	if len(ft.notes) != 0 {
		t.Fatalf("7d 90%% alerted below its 97%% bar: %v", ft.notes)
	}
	st.Accounts[1] = acct(1, "alice@example.com", "work", true, 90.0, 10.0, "", nil)
	sh.update(st)
	if len(ft.notes) != 1 || !strings.Contains(ft.notes[0], "has used 90% of its 5h window") {
		t.Errorf("notes = %v", ft.notes)
	}
}

func TestShellThresholdFollowsRunningEngine(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	st := sampleState()
	st.Auto = &web.AutoView{Running: true, Threshold: 25}
	sh.update(st) // the active account's week is at 30% ≥ 25
	if len(ft.notes) != 1 {
		t.Errorf("notes = %v", ft.notes)
	}
}

func TestShellAutoEvents(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.autoEvent(web.AutoEventView{Kind: "poll", Message: "polled"})
	sh.autoEvent(web.AutoEventView{Kind: "switch", Account: "3", Message: "7d window at 97%"})
	sh.autoEvent(web.AutoEventView{Kind: "account-quarantined", Account: "2", Message: "invalid_grant"})
	if len(ft.notes) != 2 || !strings.HasPrefix(ft.notes[0], "Auto-switched to account #3 |") || !strings.HasPrefix(ft.notes[1], "Account #2 quarantined |") {
		t.Errorf("notes = %v", ft.notes)
	}
}

func TestShellPctParsing(t *testing.T) {
	r := acct(1, "a@x", "", true, "88%", 12, "Fable", "100")
	if p, ok := maxWindowPct(r, true); !ok || p != 100 {
		t.Errorf("maxWindowPct = %v,%v", p, ok)
	}
	if got := windowsLine(r, true); got != "5h 88% · 7d 12% · Fable 100%" {
		t.Errorf("windowsLine = %q", got)
	}
	if _, ok := maxWindowPct(map[string]any{"usage": nil}, true); ok {
		t.Error("no usage → no pct")
	}
	// A39: with the model limit switched off, the model window is not part of
	// any figure the tray shows — the percentage is the worst of 5h and 7d.
	if p, ok := maxWindowPct(r, false); !ok || p != 88 {
		t.Errorf("maxWindowPct without models = %v,%v, want 88", p, ok)
	}
	if got := windowsLine(r, false); got != "5h 88% · 7d 12% · Fable 100% (not counted)" {
		t.Errorf("windowsLine without models = %q", got)
	}
}

// TestShellModelLimitSwitch: one switch decides what auto-switch counts and
// what every percentage in the tray reports (A39).
func TestShellModelLimitSwitch(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	var saved *bool
	sh.act.SetModelLimits = func(on bool) error { saved = &on; return nil }

	st := sampleState() // active #1: 5h 45%, 7d 30%, Fable 12% — no model setting
	sh.update(st)
	if ft.title != "#1 · 45%" {
		t.Errorf("off: title = %q, want the worst of 5h/7d", ft.title)
	}
	it, ok := ft.item("model-limits")
	if !ok || it.Checked || it.Title != "Fable limit" || !strings.Contains(it.Sub, "ignored") {
		t.Errorf("off: switch = %+v (ok=%v)", it, ok)
	}

	// Turning it on writes the setting…
	sh.click("model-limits")
	if saved == nil || !*saved {
		t.Fatalf("click should switch it on, got %v", saved)
	}
	// …and the next state document, which now carries the setting, makes the
	// model window count everywhere.
	st.Settings = append(st.Settings, web.SettingView{Key: "autoswitch.model", Value: "all"})
	st.Accounts[1] = acct(1, "alice@example.com", "work", true, 45.0, 30.0, "Fable", 97.0)
	sh.update(st)
	if ft.title != "#1 · 97%" {
		t.Errorf("on: title = %q, want the model window to count", ft.title)
	}
	if it, _ := ft.item("model-limits"); !it.Checked || !strings.Contains(it.Sub, "counts towards") {
		t.Errorf("on: switch = %+v", it)
	}
	if !strings.Contains(strings.Join(ft.notes, "\n"), "Fable limit on") {
		t.Errorf("notes = %v", ft.notes)
	}
}

func TestParseAppArgs(t *testing.T) {
	o, err := parseAppArgs([]string{"--port", "7337", "--interval=2.5", "--open", "--headless", "--debug"})
	if err != nil {
		t.Fatal(err)
	}
	if o.port != 7337 || o.interval != 2.5 || !o.open || !o.headless || !o.debug {
		t.Errorf("parsed = %+v", o)
	}
	for _, argv := range [][]string{{"--port"}, {"--port", "70000"}, {"--interval", "0"}, {"--interval", "0.5"}, {"--autostart", "maybe"}, {"--bogus"}, {"--server", "https://x"}, {"--detach"}} {
		if _, err := parseAppArgs(argv); err == nil {
			t.Errorf("%v should be rejected", argv)
		}
	}
	o, err = parseAppArgs([]string{"--autostart=status"})
	if err != nil || o.autostart != "status" {
		t.Errorf("autostart parse = %+v, %v", o, err)
	}
	if _, err := parseAppArgs([]string{"--help"}); !errors.Is(err, errHelp) {
		t.Errorf("--help should signal help, got %v", err)
	}
}

// The command's usage errors exit 2, and its help names every flag.
func TestAppHelpAndUsage(t *testing.T) {
	code, out, _ := runCLI(t, []string{"app", "--help"}, false, false)
	if code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	for _, flag := range []string{"--open", "--headless", "--no-update-check", "--port", "--interval", "--debug", "--remote", "--token-file", "--autostart", "TYCSWAP_REMOTE_TOKEN_FILE"} {
		if !strings.Contains(out, flag) {
			t.Errorf("help lacks %s", flag)
		}
	}
	if code, _, errStr := runCLI(t, []string{"app", "--bogus"}, false, false); code != 2 || !strings.Contains(errStr, "unrecognized arguments: --bogus") {
		t.Errorf("exit = %d, stderr = %q", code, errStr)
	}
}

// A33: the engine only ever switches between subscription accounts, so every
// switch notification reads the same way.
func TestShellSwitchNotifications(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.autoEvent(web.AutoEventView{Kind: "switch", Account: "2", Message: "Switched Account-1 -> Account-2 (proactive, 7d)", Fields: map[string]any{"trigger": "proactive"}})
	if len(ft.notes) != 1 || !strings.HasPrefix(ft.notes[0], "Auto-switched to account #2 |") {
		t.Errorf("notes = %v", ft.notes)
	}
}

// An API-key account changes how Claude Code authenticates: the tray asks
// first, or, without a way to ask, refuses and says where to switch (A33).
// One with a base URL (A46): the row names where its requests go, the dialog
// says it in the words every front-end uses and replaces the restart sentence
// with the endpoint's, and the notification after the switch is the one the
// CLI prints.
func TestShellAPIKeySwitchAsksFirst(t *testing.T) {
	sh, ft, calls := newTestShell(t)
	st := sampleState()
	key := acct(3, "key@example.com", "", false, nil, nil, "", nil)
	key["kind"] = "api_key"
	gw := acct(4, "gw@example.com", "", false, nil, nil, "", nil)
	gw["kind"] = "api_key"
	gw["baseUrl"] = "https://gateway.example/v1\x07"
	st.Accounts = append(st.Accounts, key, gw)
	sh.update(st)
	sh.click("switch:3")
	if len(*calls) != 0 || len(ft.notes) != 1 || !strings.HasPrefix(ft.notes[0], "Switch needs confirmation |") {
		t.Fatalf("calls = %v, notes = %v", *calls, ft.notes)
	}
	var approved []string
	var asked string
	answer := false
	sh.act.ApproveAPIKey = func(num string) { approved = append(approved, num) }
	sh.act.RestartNotice = func() string { return "2 Claude Code sessions are running." }
	sh.act.Ask = func(title, body, ok, cancel string) (bool, error) {
		*calls = append(*calls, "ask:"+title)
		asked = body
		return answer, nil
	}
	sh.click("switch:3")
	if len(approved) != 0 || strings.Contains(strings.Join(*calls, ","), "switch:3") {
		t.Errorf("a cancelled ask switched: calls %v, approved %v", *calls, approved)
	}
	answer = true
	sh.click("switch:3")
	if len(approved) != 1 || approved[0] != "3" || !strings.HasSuffix(strings.Join(*calls, ","), "ask:Switch to API-key account #3?,switch:3") {
		t.Errorf("calls %v, approved %v", *calls, approved)
	}
	if !strings.Contains(asked, "2 Claude Code sessions are running.") || strings.Contains(asked, "ANTHROPIC_BASE_URL") {
		t.Errorf("dialog without a base URL = %q", asked)
	}
	if notes := ft.notesNow(); !strings.HasSuffix(notes[len(notes)-1], "Restart running Claude Code sessions to pick it up.") {
		t.Errorf("notes = %v", notes)
	}

	// A46: the account with a base URL.
	row, ok := ft.item("switch:4")
	if !ok || !strings.Contains(row.Sub, "API key · billed per token · → gateway.example") || strings.Contains(row.Sub, "\x07") {
		t.Fatalf("row = %+v", row)
	}
	sh.act.EndpointSessionNotice = func() string { return switching.EndpointSessionNotice(2) }
	sh.click("switch:4")
	if !strings.Contains(asked, switching.EndpointNotice("https://gateway.example/v1")) ||
		!strings.Contains(asked, switching.EndpointSessionNotice(2)) ||
		strings.Contains(asked, "2 Claude Code sessions are running.") || strings.Contains(asked, "\x07") {
		t.Errorf("dialog = %q", asked)
	}
	if len(approved) != 2 || approved[1] != "4" || !strings.HasSuffix(strings.Join(*calls, ","), "ask:Switch to API-key account #4?,switch:4") {
		t.Errorf("calls %v, approved %v", *calls, approved)
	}
	want := "Switched to account #4 | " + switching.EndpointAppliedNote("gateway.example")
	if notes := ft.notesNow(); notes[len(notes)-1] != want {
		t.Errorf("notes = %v\nwant %q", notes, want)
	}
	// Without the hook the endpoint's sentence still replaces the restart one.
	sh.act.EndpointSessionNotice = nil
	sh.click("switch:4")
	if !strings.Contains(asked, switching.EndpointSessionNotice(0)) || strings.Contains(asked, "2 Claude Code sessions are running.") {
		t.Errorf("dialog without the hook = %q", asked)
	}
}

func updateShell(t *testing.T, latest string, ask func(string, string, string, string) (bool, error)) (*appShell, *fakeTray, *[]string) {
	t.Helper()
	sh, ft, calls := newTestShell(t)
	sh.act.Current = "v2.1.0"
	sh.act.LatestVersion = func() (string, error) {
		if latest == "" {
			return "", errors.New("offline")
		}
		return latest, nil
	}
	sh.act.Ask = ask
	sh.update(sampleState())
	return sh, ft, calls
}

func TestOfferUpdateAsksAndInstallsOnApproval(t *testing.T) {
	var asked []string
	sh, ft, calls := updateShell(t, "v2.2.0", func(title, body, ok, cancel string) (bool, error) {
		asked = append(asked, title+"|"+body+"|"+ok+"|"+cancel)
		return true, nil
	})
	sh.offerUpdate()
	if len(asked) != 1 || !strings.Contains(asked[0], "2.2.0 is available (you have 2.1.0)") || !strings.HasSuffix(asked[0], "|Install|Later") {
		t.Fatalf("asked = %v", asked)
	}
	if got := strings.Join(*calls, ","); !strings.Contains(got, "upgrade") {
		t.Errorf("Upgrade not run after approval: %s", got)
	}
	if _, has := ft.item("install-update"); has {
		t.Error("no pending item after a successful install")
	}
	if len(ft.notes) != 1 || !strings.Contains(ft.notes[0], "2.0.0 → 2.1.0") {
		t.Errorf("notes = %v", ft.notes)
	}
}

func TestOfferUpdateDeclinedStaysInMenuAndAsksOncePerVersion(t *testing.T) {
	asks := 0
	sh, ft, calls := updateShell(t, "v2.2.0", func(string, string, string, string) (bool, error) { asks++; return false, nil })
	sh.offerUpdate()
	sh.offerUpdate() // next periodic check, same version → no second dialog
	if asks != 1 {
		t.Errorf("asked %d times, want once per version", asks)
	}
	if strings.Contains(strings.Join(*calls, ","), "upgrade") {
		t.Error("Upgrade must not run when declined")
	}
	it, has := ft.item("install-update")
	if !has || it.Title != "Install tycswap 2.2.0…" {
		t.Errorf("pending menu item = %+v", it)
	}
	sh.click("install-update")
	if !strings.Contains(strings.Join(*calls, ","), "upgrade") {
		t.Error("menu item should install")
	}
	if _, has := ft.item("install-update"); has {
		t.Error("pending item should clear after installing")
	}
}

func TestOfferUpdateWithoutDialogNotifiesAndOffersMenu(t *testing.T) {
	sh, ft, calls := updateShell(t, "v2.2.0", nil)
	sh.offerUpdate()
	if strings.Contains(strings.Join(*calls, ","), "upgrade") {
		t.Error("must not install without approval")
	}
	if len(ft.notes) != 1 || !strings.HasPrefix(ft.notes[0], "tycswap 2.2.0 is available |") {
		t.Errorf("notes = %v", ft.notes)
	}
	if _, has := ft.item("install-update"); !has {
		t.Error("menu item missing")
	}
	// a dialog that errors (e.g. no zenity) takes the same path
	sh2, ft2, _ := updateShell(t, "v2.3.0", func(string, string, string, string) (bool, error) { return false, errors.New("no dialog") })
	sh2.offerUpdate()
	if len(ft2.notes) != 1 || !strings.Contains(ft2.notes[0], "2.3.0 is available") {
		t.Errorf("notes = %v", ft2.notes)
	}
}

// A build the tray cannot upgrade (a checkout, a go-installed binary on
// Windows, a binary it cannot write over) is never asked to install: it is
// told how, and the menu row says it too (A36).
func TestOfferUpdateTellsTheCommandWhenTheTrayCannotInstall(t *testing.T) {
	asked := false
	sh, ft, calls := updateShell(t, "v2.2.0", func(string, string, string, string) (bool, error) { asked = true; return true, nil })
	sh.act.UpgradeHint = func() string { return "go install github.com/tyclab/tycswap/cmd/tycswap@latest" }
	sh.offerUpdate()
	if asked || strings.Contains(strings.Join(*calls, ","), "upgrade") {
		t.Fatalf("asked %v, calls %v", asked, *calls)
	}
	want := "tycswap 2.2.0 is available | To update: go install github.com/tyclab/tycswap/cmd/tycswap@latest"
	if len(ft.notes) != 1 || ft.notes[0] != want {
		t.Errorf("notes = %v", ft.notes)
	}
	it, has := ft.item("install-update")
	if !has || it.Title != "tycswap 2.2.0 is available…" || !strings.Contains(it.Sub, "to update: go install") {
		t.Errorf("row = %+v", it)
	}
	sh.click("install-update")
	if strings.Contains(strings.Join(*calls, ","), "upgrade") || len(ft.notes) != 2 || ft.notes[1] != want {
		t.Errorf("click: calls %v, notes %v", *calls, ft.notes)
	}
	if v := sh.updatesView(); v.App.Hint == "" || !v.App.Available {
		t.Errorf("card = %+v", v.App)
	}
	if _, _, err := sh.upgradeApp(); err == nil || !strings.Contains(err.Error(), "update it with: go install") {
		t.Errorf("upgradeApp = %v", err)
	}
}

func TestOfferUpdateIgnoresOlderEqualAndErrors(t *testing.T) {
	for _, latest := range []string{"v2.1.0", "v2.0.9", "nightly", ""} {
		asked := false
		sh, ft, calls := updateShell(t, latest, func(string, string, string, string) (bool, error) { asked = true; return true, nil })
		sh.offerUpdate()
		if asked || len(ft.notes) != 0 || strings.Contains(strings.Join(*calls, ","), "upgrade") {
			t.Errorf("latest=%q: asked=%v notes=%v calls=%v", latest, asked, ft.notes, *calls)
		}
		if _, has := ft.item("install-update"); has {
			t.Errorf("latest=%q: unexpected pending item", latest)
		}
	}
}

func TestUpdateCheckLoopCadence(t *testing.T) {
	oldFirst, oldEvery := updateCheckFirst, updateCheckEvery
	updateCheckFirst, updateCheckEvery = 5*time.Millisecond, 10*time.Millisecond
	defer func() { updateCheckFirst, updateCheckEvery = oldFirst, oldEvery }()
	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { updateCheckLoop(ctx, func() { n.Add(1) }); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if c := n.Load(); c < 3 {
		t.Errorf("check ran %d times with a 5ms/10ms cadence", c)
	}
}

func TestInstallUpdateRestartsAfterSuccess(t *testing.T) {
	restarted := 0
	sh, ft, _ := updateShell(t, "v2.2.0", func(string, string, string, string) (bool, error) { return true, nil })
	var restartedTo string
	sh.act.Restart = func(tag string) { restarted++; restartedTo = tag }
	sh.offerUpdate()
	if restarted != 1 {
		t.Fatalf("restart called %d times, want 1", restarted)
	}
	if len(ft.notes) != 1 || !strings.Contains(ft.notes[0], "Restarting…") {
		t.Errorf("notes = %v", ft.notes)
	}
	if restartedTo != "v2.2.0" {
		t.Errorf("restart tag = %q", restartedTo)
	}
	// the restarted process must not offer the tag it was just updated to
	sh2, ft2, calls2 := updateShell(t, "v2.2.0", func(string, string, string, string) (bool, error) { return true, nil })
	sh2.act.SkipTag = "v2.2.0"
	sh2.offerUpdate()
	if len(ft2.notes) != 0 || strings.Contains(strings.Join(*calls2, ","), "upgrade") {
		t.Errorf("skip-tag guard failed: notes=%v calls=%v", ft2.notes, *calls2)
	}
	// nothing to restart when the binary was not replaced, or it failed
	sh.act.Upgrade = func() (string, error) {
		return "Installed the new tycswap, but not over /opt/x/tycswap. " + restartByHand(), nil
	}
	sh.click("install-update")
	sh.act.Upgrade = func() (string, error) { return "", errors.New("offline") }
	sh.click("install-update")
	if restarted != 1 {
		t.Errorf("restart called %d times after a no-op and a failure, want still 1", restarted)
	}
}

// The per-model week is the third bar, and it is judged on its own: the tray
// must name that window when it is the one that crossed (A34).
func TestShellModelWindowAlertsOnItsOwnThreshold(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	st := sampleState()
	st.Settings = append(st.Settings, web.SettingView{Key: "autoswitch.model", Value: "all"})
	// Fable at 96 is over the 95 model bar; 5h and 7d are cold.
	st.Accounts[1] = acct(1, "alice@example.com", "work", true, 10.0, 20.0, "Fable", 96.0)
	sh.update(st)
	if len(ft.notes) != 1 || !strings.Contains(ft.notes[0], "has used 96% of its Fable week") {
		t.Fatalf("notes = %v", ft.notes)
	}
}

// An engine out of reach (the remote tray's link to the distro, A45)
// outranks the figures: title and tooltip say so, the menu leads with the
// reason, and the rows keep the last state; back in reach, the paint is
// the usual one again.
func TestShellOfflineOutranksFigures(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	down := false
	sh.act.Offline = func() (bool, string) { return down, "engine unreachable at http://127.0.0.1:7337" }
	sh.update(sampleState())
	if ft.title != "#1 · 45%" {
		t.Fatalf("reachable: title = %q", ft.title)
	}
	if _, has := ft.item("offline"); has {
		t.Fatal("reachable: no offline row expected")
	}
	down = true
	sh.repaint()
	if ft.title != "⚠" || ft.tooltip != "tycswap — engine unreachable at http://127.0.0.1:7337" {
		t.Errorf("unreachable: title=%q tooltip=%q", ft.title, ft.tooltip)
	}
	row, has := ft.item("offline")
	if !has || !row.Disabled || row.Title != "Engine unreachable at http://127.0.0.1:7337" {
		t.Errorf("offline row = %+v (has=%v)", row, has)
	}
	if ft.menu[0].ID != "brand" || ft.menu[1].ID != "offline" || ft.menu[2].ID != "open" {
		t.Errorf("menu should lead with brand, reason, open: %v %v %v", ft.menu[0].ID, ft.menu[1].ID, ft.menu[2].ID)
	}
	if _, has := ft.item("switch:2"); !has {
		t.Error("the last state's rows should stay in the menu")
	}
	down = false
	sh.repaint()
	if ft.title != "#1 · 45%" {
		t.Errorf("reachable again: title = %q", ft.title)
	}
	if _, has := ft.item("offline"); has {
		t.Error("reachable again: offline row should be gone")
	}
}
