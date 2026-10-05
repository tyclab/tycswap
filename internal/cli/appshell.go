// appshell.go — what the menu-bar/tray app shows and does (DESIGN A35),
// separated from the platform tray so it can be tested with a fake one.
//
// The shell follows the same state documents the dashboard receives: the
// title next to the icon is the active Claude account and its fullest window,
// the menu lists every account for one-click switching — the Codex accounts
// under their own heading when the dashboard has them (DESIGN A47) — and
// notifications fire on engine switches, quarantines and when the active
// account crosses the auto-switch threshold of a window.
package cli

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/mod/semver"

	"github.com/tyclab/tycswap/internal/brand"
	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/ccversion"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/switching"
	"github.com/tyclab/tycswap/internal/termsafe"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/usage"
	"github.com/tyclab/tycswap/internal/version"
	"github.com/tyclab/tycswap/internal/web"
)

// shellActions are the things a menu click can do; the app command wires
// them to the switcher, the engine, the browser and the updater.
type shellActions struct {
	OpenDashboard func() error
	// SwitchTo switches to the account a row key names ("claude:2",
	// "codex:1", A47) and returns the codex sessions still running on the
	// old Codex account (nil for a Claude switch).
	SwitchTo func(key string) (runningPIDs []int, err error)
	// AddCurrent stores the login Claude Code is signed in with as an
	// account (A37; web.Server.AddCurrentLogin). nil hides the row.
	AddCurrent   func() (web.AddLoginResult, error)
	AutoRunning  func() bool
	AutoStart    func() error
	AutoStop     func() error
	Upgrade      func() (string, error) // returns the one-line result
	Autostart    func() (enabled bool, err error)
	SetAutostart func(enable bool) error
	Quit         func()
	// UpgradeHint says how this build is upgraded when the tray cannot do it
	// itself (a checkout build, a go-installed binary on Windows, a binary in
	// the Nix store or in a directory it cannot write). "" or nil: Upgrade
	// installs (A36).
	UpgradeHint func() string
	// LatestVersion returns the newest published tag (the release
	// endpoint's tag_name); the periodic check compares it with Current.
	LatestVersion func() (string, error)
	// Ask shows a yes/no dialog; nil or tray.ErrUnsupported → the shell falls
	// back to a notification plus an "Install tycswap X" menu item.
	Ask func(title, body, okLabel, cancelLabel string) (bool, error)
	// Current is the running build's version (v-prefixed).
	Current string
	// Restart relaunches the (now replaced) binary and ends this process; nil
	// → the user is asked to restart by hand. tag is the release just installed.
	Restart func(tag string)
	// SetModelLimits turns the per-model weekly windows on or off: it writes
	// autoswitch.model AND retargets a running engine, so the tray, the
	// dashboard and the engine agree at once (A39). nil hides the switch.
	SetModelLimits func(on bool) error
	// ApproveAPIKey records the user's approval for a switch onto an API-key
	// account, and RestartNotice is the sentence naming how many sessions must
	// be restarted (A33). Both nil → the tray refuses the switch and says where
	// to do it instead.
	ApproveAPIKey func(num string)
	RestartNotice func() string
	// EndpointSessionNotice replaces RestartNotice for an API-key account
	// with a base URL (A46): a running session takes the endpoint up when it
	// re-reads settings.json. nil → the sentence without a session count.
	EndpointSessionNotice func() string
	// RunClaudeCode runs a Claude Code upgrade command (A42). nil hides the
	// Claude Code row.
	RunClaudeCode func(c ccversion.Command, in *ccversion.Installed) (string, error)
	// CheckClaudeCode runs a full Claude Code check and returns it: the
	// periodic check, "Check for updates…", the dashboard's Check now and the
	// look after an upgrade (A42, A44). nil leaves Claude Code unchecked.
	CheckClaudeCode func() ccversion.Status
	// UpdatesChanged tells the app's dashboard that what can be updated has
	// changed, so its Updates card follows now rather than at the next poll
	// (web.Server.Refresh, A44). nil for no dashboard.
	UpdatesChanged func()
	// SkipTag is a release never to offer again in this process: the one this
	// process was just restarted into (guards against a release whose tag the
	// binary does not report as its version).
	SkipTag string
	// Offline, when set, says whether the engine this shell shows is out of
	// reach right now, and why (DESIGN A45: the remote tray's link to the
	// engine in the distro). While it is down the title and tooltip say so
	// and the menu leads with the reason; the rows keep the last state the
	// shell saw. nil → the engine is in-process and always there.
	Offline func() (down bool, why string)
}

type appShell struct {
	tray    tray.Tray
	act     shellActions
	notify  func(title, body string)
	mu      sync.Mutex
	alerted map[string]bool // Claude row key → threshold alert already shown
	last    web.State
	// pending is a newer release the user has not installed yet (menu item);
	// offered is the last tag a dialog/notification was shown for.
	pending string
	offered string
	// claude is the last Claude Code check (A42); claudeAnnounced the latest
	// version already announced.
	claude          ccversion.Status
	claudeKnown     bool
	claudeAnnounced string
	// claudeLocal is the quick look at start (no network), shown in the
	// brand row until the first full check arrives.
	claudeLocal      *ccversion.Installed
	claudeLocalKnown bool
	// appLatest is the newest release the endpoint named, releaseErr why the
	// last release check could not read it (a release found before stays
	// pending); checkedAt when a check last reached the network — the release
	// endpoint or Claude Code's newest version, not the local look at Claude
	// Code, and not a check that failed; checking how many checks run now
	// (A44).
	appLatest  string
	releaseErr error
	checkedAt  time.Time
	checking   int
	// badge is the count the icon's badge shows, 0 for none (A44); iconMu
	// keeps the decision and the SetIcon call together, so the last one wins.
	badge  int
	iconMu sync.Mutex
	// applyMu lets one update run at a time, whether the tray or the
	// dashboard started it: two would race on the same files.
	applyMu sync.Mutex
}

// errUpdateRunning: an update is already running. A lock error, so the
// dashboard answers 409 as it does to a second apply of its own.
var errUpdateRunning = cerr.Lock("another update is still running; try again when it has finished")

func newAppShell(t tray.Tray, act shellActions) *appShell {
	sh := &appShell{tray: t, act: act, alerted: map[string]bool{}}
	sh.notify = func(title, body string) { _ = t.Notify(title, body) }
	return sh
}

// update repaints title, tooltip and menu from a state document.
func (a *appShell) update(st web.State) {
	a.mu.Lock()
	a.last = st
	a.mu.Unlock()
	active, ok := activeAccount(st)
	running := a.autoRunning()
	head := brandName()
	if a.updateWaiting() {
		// Next to the name, before the per-window details: Windows cuts the
		// tooltip at 127 characters (A41), and this is the part to keep (A44).
		head += " · updates available"
	}
	title, tooltip := "", head+" — no active account"
	if ok {
		models := countedModels(st)
		title = fmt.Sprintf("#%s · %s", rowNumber(active), fmtPctShort(maxWindowPct(active, models)))
		tooltip = head + " — #" + rowNumber(active) + " " + rowName(active) + " · " + windowsLine(active, models)
	}
	// The auto-switch state is visible without opening the menu: the
	// rotation glyph leads the title while the engine runs.
	if running {
		title = strings.TrimSpace("⟳ " + title)
		tooltip += " · auto-switch on"
	} else if st.Auto != nil && st.Auto.ManagedBy != "" {
		tooltip += " · managed by " + st.Auto.ManagedBy
	} else {
		tooltip += " · auto-switch off"
	}
	// The title is Claude Code's account; the active Codex account, when
	// there is one, follows in the tooltip (A47).
	if cx, has := activeCodexAccount(st); has {
		tooltip += " · codex #" + rowNumber(cx) + " " + rowName(cx)
		if line := windowsLine(cx, nil); line != "" {
			tooltip += " · " + line
		}
	}
	// An engine out of reach outranks every figure: the figures are stale.
	if down, why := a.offline(); down {
		title, tooltip = "⚠", brandName()+" — "+why
	}
	a.tray.SetTitle(title)
	a.tray.SetTooltip(tooltip)
	a.tray.SetMenu(a.menu(st))
	a.syncIcon()
	a.thresholdAlert(st, active, ok)
}

// menu lays out the dropdown (A37): a gauge row per account (usage bar, the
// active one marked), switch rows for the automation settings, and plain
// commands. Platforms without custom menu views render the same items as text.
func (a *appShell) menu(st web.State) []tray.Item {
	running := a.autoRunning()
	state := "auto-switch off"
	if running {
		state = "auto-switch on"
	} else if st.Auto != nil && st.Auto.ManagedBy != "" {
		state = "auto-switch managed externally"
	}
	withModels := countedModels(st)
	sub := strings.TrimPrefix(version.Version, "v") + " · " + state
	if line := a.claudeCodeLine(); line != "" {
		sub += "\n" + line
	}
	items := []tray.Item{tray.Brand("brand", brand.Sanitized().DisplayName, sub)}
	if down, why := a.offline(); down {
		items = append(items, tray.Item{ID: "offline", Title: capitalizeFirst(why), Disabled: true})
	}
	items = append(items, tray.Item{ID: "open", Title: "Open dashboard", Dismiss: true})
	// What can be updated comes first, where it is seen (A44).
	items = append(items, a.updatesSection()...)
	items = append(items, tray.Separator(), tray.Header("Accounts"))
	items = append(items, a.accountRows(st, withModels)...)
	items = append(items, tray.Separator(), tray.Header("Automation"))
	// The dashboard's engines: the Claude one, and the Codex one beside it
	// when the dashboard has Codex accounts (A47).
	autoSub := "rotates " + rotatedAccounts(st) + " near the limit (" + a.thresholdLabel(st) + ")"
	if !running {
		autoSub = "off — switch accounts by hand or turn on"
	}
	managed := st.Auto != nil && st.Auto.ManagedBy != ""
	if managed {
		autoSub = "Managed by " + st.Auto.ManagedBy
	}
	items = append(items, tray.Item{Disabled: managed, ID: "auto", Kind: tray.KindToggle, Title: "Auto-switch", Sub: autoSub, Checked: running})
	if a.act.SetModelLimits != nil {
		// One switch for the per-model weekly windows: it decides what
		// auto-switch counts AND what every percentage here reports, so the
		// two can never tell different stories (A39).
		name := modelWindowLabel(st)
		sub := "off — 5h and 7d only, " + name + " ignored"
		if len(withModels) > 0 {
			sub = "counting " + strings.Join(withModels, ", ") + " — applies to percentages and switching"
		}
		items = append(items, tray.Item{ID: "model-limits", Kind: tray.KindToggle, Title: "Model limits", Sub: sub, Checked: len(withModels) > 0})
	}
	items = append(items, tray.Separator(), tray.Header("App"))
	// An update of Claude Code sits in the Updates section; its other states
	// (missing, latest, could not check) stay here.
	if it, ok := a.claudeCodeItem(); ok && !a.claudeCodeUpdate() {
		items = append(items, it)
	}
	if a.act.Autostart != nil {
		on, err := a.act.Autostart()
		items = append(items, tray.Item{ID: "autostart", Kind: tray.KindToggle, Title: "Start at login", Sub: "run in the menu bar from login on", Checked: on && err == nil, Disabled: err != nil})
	}
	items = append(items, tray.Item{ID: "update", Title: "Check for updates…"})
	items = append(items, tray.Separator(), tray.Item{ID: "quit", Title: "Quit " + brandName(), Dismiss: true})
	return items
}

// inlineAccounts is how many accounts the menu lists itself; beyond that they
// go into a submenu (A37), which keeps the menu on the screen.
const inlineAccounts = 10

// accountRows are the Accounts section: a gauge row per Claude account, the
// active one marked, or — with more than inlineAccounts — the active one and a
// submenu with all of them; then "Add current login". The Codex accounts
// follow under a Codex heading by the same rule (A47). Every row's id is
// "switch:" and its row key, since slot numbers repeat across providers.
func (a *appShell) accountRows(st web.State, withModels []string) []tray.Item {
	claude, codex := rowsByProvider(st.Accounts)
	items := gaugeGroup(claude, withModels, "accounts", "All %d accounts")
	if a.act.AddCurrent != nil {
		items = append(items, addCurrentItem(st))
	} else if len(st.Accounts) == 0 {
		items = append(items, tray.Item{ID: "none", Title: "No accounts yet — open the dashboard to add one", Disabled: true})
	}
	if len(codex) > 0 {
		items = append(items, tray.Header("Codex"))
		items = append(items, gaugeGroup(codex, withModels, "codex-accounts", "All %d Codex accounts")...)
	}
	return items
}

// rowsByProvider splits the state's rows into the Claude rows and the Codex
// rows, each in slot order.
func rowsByProvider(all []map[string]any) (claude, codex []map[string]any) {
	for _, r := range all {
		if rowProvider(r) == reporting.ProviderClaude {
			claude = append(claude, r)
		} else {
			codex = append(codex, r)
		}
	}
	for _, rows := range [][]map[string]any{claude, codex} {
		sort.SliceStable(rows, func(i, j int) bool { return rowNumberInt(rows[i]) < rowNumberInt(rows[j]) })
	}
	return claude, codex
}

// gaugeGroup is one provider's gauge rows: every row, or with more than
// inlineAccounts the active one and a submenu (id, title format) with all.
func gaugeGroup(rows []map[string]any, withModels []string, subID, subTitle string) []tray.Item {
	var gauges, active []tray.Item
	for _, r := range rows {
		pct, has := maxWindowPct(r, withModels)
		if !has {
			pct = -1
		}
		sub := windowsLine(r, withModels)
		var flags []string
		if boolOf(r["atLimit"]) {
			flags = append(flags, "at limit")
		}
		if kind, _ := r["kind"].(string); kind == "api_key" {
			flags = append(flags, "API key · billed per token")
		}
		if base := rowBaseURL(r); base != "" {
			// Where the account's requests go (A46), as the list and the
			// dashboard's chip say it.
			flags = append(flags, "→ "+endpointHost(base))
		}
		if boolOf(r["disabled"]) {
			flags = append(flags, "disabled")
		} else if !boolOf(r["switchable"]) {
			flags = append(flags, "not switchable")
		}
		if len(flags) > 0 {
			if sub != "" {
				sub += " · "
			}
			sub += strings.Join(flags, " · ")
		}
		if sub == "" {
			sub = "no usage data yet"
		}
		it := tray.Item{
			ID:       "switch:" + rowKey(r),
			Kind:     tray.KindGauge,
			Title:    "#" + rowNumber(r) + "  " + rowName(r),
			Sub:      sub,
			Pct:      pct,
			Checked:  boolOf(r["isActive"]),
			Disabled: boolOf(r["isActive"]) || !boolOf(r["switchable"]) || boolOf(r["disabled"]),
		}
		gauges = append(gauges, it)
		if it.Checked {
			active = append(active, it)
		}
	}
	if len(gauges) > inlineAccounts {
		return append(active, tray.Item{ID: subID, Title: fmt.Sprintf(subTitle, len(gauges)), Children: gauges})
	}
	return gauges
}

// addCurrentItem is "Add current login": its second line says what a click
// does now — store the login Claude Code has, or how to get another one there.
func addCurrentItem(st web.State) tray.Item {
	it := tray.Item{ID: "add-current", Title: "Add current login"}
	switch cur := st.CurrentLogin; {
	case cur != nil && !cur.Saved:
		it.Sub = cur.Email + " · not stored yet"
	case cur == nil && len(st.Accounts) == 0:
		it.Sub = "sign in to Claude Code first (/login)"
	default:
		it.Sub = "for another account: /login with it, never /logout"
	}
	return it
}

// addCurrentClick is "Add current login" (A37). The menu stays open and
// shows the new account as soon as the dashboard's state carries it.
func (a *appShell) addCurrentClick() {
	res, err := a.act.AddCurrent()
	if err != nil {
		a.notify("Login not added", capitalizeFirst(err.Error()))
		return
	}
	a.dashboardChanged()
	num, email := res.Number, res.Email
	if res.Refreshed {
		a.notify("Account #"+num+" refreshed", email+" was account #"+num+" already. To add another account, /login with it in Claude Code; don't /logout first, that ends this stored login.")
		return
	}
	a.notify("Added account #"+num, email+" is account #"+num+" now. To add another, /login with it in Claude Code (never /logout: that ends the stored login), then choose Add current login again.")
}

// apiKeyAccount reports whether Claude slot num is an API-key account, and
// the base URL its requests go to ("" for none, A46), from the last state
// document the shell painted. A Codex row with the same number is another
// CLI's account.
func (a *appShell) apiKeyAccount(num string) (apiKey bool, baseURL string) {
	a.mu.Lock()
	st := a.last
	a.mu.Unlock()
	for _, r := range st.Accounts {
		if rowProvider(r) == reporting.ProviderClaude && rowNumber(r) == num {
			kind, _ := r["kind"].(string)
			return kind == "api_key", rowBaseURL(r)
		}
	}
	return false, ""
}

// rowBaseURL is a state row's baseUrl, stripped of terminal control
// sequences: it is shown in the menu, a dialog and a notification.
func rowBaseURL(r map[string]any) string {
	base, _ := r["baseUrl"].(string)
	return termsafe.Strip(base)
}

// endpointHost is the host of a base URL, the URL itself when it has none.
func endpointHost(base string) string {
	if h := ccsettings.Host(base); h != "" {
		return h
	}
	return base
}

// thresholdLabel names every bar in force, because there is one per window:
// the week is what rotation chiefly follows, the 5h window is left alone until
// much later, and the per-model bar only applies while those windows count
// (A34).
func (a *appShell) thresholdLabel(st web.State) string {
	bars := barsOf(st)
	label := "5h " + fmtPctShort(bars.fiveHour) + " · 7d " + fmtPctShort(bars.sevenDay)
	if countsModelLimits(st) {
		label += " · " + modelWindowLabel(st) + " " + fmtPctShort(bars.model)
	}
	// The Codex engine's one bar, beside the Claude ones (A47).
	if st.Auto != nil && st.Auto.Codex != nil && st.Auto.Codex.Enabled {
		label += " · codex " + fmtPctShort(st.Auto.Codex.Threshold)
	}
	return label
}

// rotatedAccounts names what auto-switch rotates: the Claude accounts, and
// the Codex accounts too while the dashboard runs the Codex engine (A47).
func rotatedAccounts(st web.State) string {
	if st.Auto != nil && st.Auto.Codex != nil && st.Auto.Codex.Enabled {
		return "Claude and Codex accounts"
	}
	return "Claude accounts"
}

// click dispatches a menu choice. It runs off the UI thread.
func (a *appShell) click(id string) {
	switch {
	case id == "claude-code":
		a.claudeCodeClick()
	case id == "add-current":
		a.addCurrentClick()
	case id == "open":
		if err := a.act.OpenDashboard(); err != nil {
			a.notify("Could not open the dashboard", capitalizeFirst(err.Error()))
		}
	case strings.HasPrefix(id, "switch:"):
		provider, num := splitRowKey(strings.TrimPrefix(id, "switch:"))
		switch provider {
		case reporting.ProviderCodex:
			a.codexSwitchClick(num)
			return
		case reporting.ProviderClaude:
		default:
			a.notify("Switch failed", "No "+provider+" accounts here.")
			return
		}
		// An API-key account changes HOW Claude Code authenticates, and a
		// running session cannot pick that up — so it is never one click (A33).
		// One with a base URL also changes where the requests go, which a
		// running session takes up when it re-reads settings.json (A46).
		apiKey, base := a.apiKeyAccount(num)
		if apiKey {
			if a.act.ApproveAPIKey == nil || a.act.Ask == nil {
				a.notify("Switch needs confirmation", "Account #"+num+" uses an API key. Switch to it from the dashboard or the command line, where it can ask you first.")
				return
			}
			detail := "This account authenticates with a token instead of a subscription login, and its usage is billed per token. "
			if base != "" {
				detail += switching.EndpointNotice(base) + " " + a.endpointSessionNotice()
			} else {
				detail += a.act.RestartNotice()
			}
			ok, err := a.act.Ask("Switch to API-key account #"+num+"?", detail, "Switch", "Cancel")
			if err != nil || !ok {
				return
			}
			a.act.ApproveAPIKey(num)
		}
		if _, err := a.act.SwitchTo(reporting.ProviderClaude + ":" + num); err != nil {
			a.notify("Switch failed", err.Error())
			return
		}
		// The menu stays open (A37): it shows the new active account now,
		// not at the dashboard's next poll.
		a.dashboardChanged()
		if base != "" {
			a.notify("Switched to account #"+num, switching.EndpointAppliedNote(endpointHost(base)))
			return
		}
		a.notify("Switched to account #"+num, "Restart running Claude Code sessions to pick it up.")
	case id == "auto":
		a.mu.Lock()
		managed := a.last.Auto != nil && a.last.Auto.ManagedBy != ""
		a.mu.Unlock()
		if managed {
			return
		}
		var err error
		wasRunning := a.autoRunning()
		if wasRunning {
			err = a.act.AutoStop()
		} else {
			err = a.act.AutoStart()
		}
		if err != nil {
			a.notify("Auto-switch", err.Error())
			return
		}
		a.mu.Lock()
		rotated := rotatedAccounts(a.last)
		a.mu.Unlock()
		if wasRunning {
			a.notify("Auto-switch off", capitalizeFirst(rotated)+" are no longer rotated automatically.")
		} else {
			a.notify("Auto-switch on", capitalizeFirst(rotated)+" rotate automatically near the limit.")
		}
		a.repaint()
	case id == "model-limits":
		a.mu.Lock()
		st := a.last
		a.mu.Unlock()
		on := countsModelLimits(st)
		name := modelWindowLabel(st)
		if err := a.act.SetModelLimits(!on); err != nil {
			a.notify(name+" limit", err.Error())
			return
		}
		if on {
			a.notify(name+" limit off", "Auto-switch and every percentage now count the 5h and 7d windows only.")
		} else {
			a.notify("Model limits on", "Auto-switch and every percentage now count all per-model weekly windows too.")
		}
		a.repaint()
	case id == "autostart":
		on, err := a.act.Autostart()
		if err == nil {
			err = a.act.SetAutostart(!on)
		}
		if err != nil {
			a.notify("Start at login", err.Error())
		}
		a.repaint()
	case id == "install-update":
		a.installUpdate()
	case id == "update":
		a.checkForUpdatesClick()
	case id == "quit":
		a.act.Quit()
	}
}

// codexSwitchClick switches to Codex slot num, as the dashboard's Codex row
// and the terminal dashboard do, and says which codex sessions still run on
// the old account (A47). A Codex API-key login is never switchable, so its
// row is disabled and nothing here asks first.
func (a *appShell) codexSwitchClick(num string) {
	pids, err := a.act.SwitchTo(reporting.ProviderCodex + ":" + num)
	if err != nil {
		a.notify("Switch failed", err.Error())
		return
	}
	a.dashboardChanged()
	a.notify("Switched Codex to account #"+num, codexRestartSentence(pids))
}

// codexRestartSentence is what a Codex switch leaves to do: `codex switch`'s
// warning naming the running sessions, or that the next one uses it.
func codexRestartSentence(pids []int) string {
	if len(pids) == 0 {
		return "The next codex session uses it."
	}
	parts := make([]string, len(pids))
	for i, p := range pids {
		parts[i] = strconv.Itoa(p)
	}
	return "codex is running (pid " + strings.Join(parts, ", ") + ") — restart it for the new account to take effect."
}

// endpointSessionNotice is the EndpointSessionNotice hook with nil meaning
// the sentence without a session count.
func (a *appShell) endpointSessionNotice() string {
	if a.act.EndpointSessionNotice == nil {
		return switching.EndpointSessionNotice(0)
	}
	return a.act.EndpointSessionNotice()
}

// repaint redraws title, tooltip and menu from the last state now, so a
// toggle follows the click instead of the next poll tick.
func (a *appShell) repaint() {
	a.mu.Lock()
	st := a.last
	a.mu.Unlock()
	a.update(st)
}

func (a *appShell) autoRunning() bool {
	return a.act.AutoRunning != nil && a.act.AutoRunning()
}

// offline is the Offline hook with nil meaning "always reachable".
func (a *appShell) offline() (bool, string) {
	if a.act.Offline == nil {
		return false, ""
	}
	return a.act.Offline()
}

// upgradeHint is the UpgradeHint hook with nil meaning "the tray installs".
func (a *appShell) upgradeHint() string {
	if a.act.UpgradeHint == nil {
		return ""
	}
	return a.act.UpgradeHint()
}

// installUpdate runs the self-upgrade and reports the outcome. A build the
// tray cannot upgrade is told how to (A36).
func (a *appShell) installUpdate() {
	if hint := a.upgradeHint(); hint != "" {
		a.mu.Lock()
		pending := a.pending
		a.mu.Unlock()
		a.notify(releaseTitle(pending), "To update: "+hint)
		return
	}
	msg, restart, err := a.upgradeApp()
	if err != nil {
		a.notify("Update failed", err.Error())
		return
	}
	a.notify(brandName()+" update", msg)
	if restart != nil {
		restart()
	}
}

// releaseTitle is "tycswap X is available", or "tycswap update" without a
// release.
func releaseTitle(tag string) string {
	if tag == "" {
		return brandName() + " update"
	}
	return brandName() + " " + strings.TrimPrefix(tag, "v") + " is available"
}

// upgradeApp runs the self-upgrade for the tray and for the dashboard (A44)
// and says what happened. restart, when not nil, relaunches into the build
// just installed and ends this process, so the caller reports first and
// calls it last.
func (a *appShell) upgradeApp() (msg string, restart func(), err error) {
	if hint := a.upgradeHint(); hint != "" {
		return "", nil, cerr.Validation("this build is not upgraded from the tray; update it with: %s", hint)
	}
	if !a.applyMu.TryLock() {
		return "", nil, errUpdateRunning
	}
	defer a.applyMu.Unlock()
	line, err := a.act.Upgrade()
	if err != nil {
		return "", nil, err
	}
	a.mu.Lock()
	// The tag the restarted process must not offer again (A36).
	tag := a.pending
	if tag == "" {
		tag = a.offered
	}
	a.pending = ""
	a.mu.Unlock()
	a.updatesChanged()
	if strings.HasPrefix(line, "Updated") && a.act.Restart != nil {
		return strings.TrimSuffix(line, " "+restartByHand()) + " Restarting…",
			func() { a.act.Restart(tag) }, nil
	}
	return line, nil, nil
}

// restartByHand is the sentence an upgrade ends with when nothing restarts
// the app by itself.
func restartByHand() string {
	return "Quit and start " + brandName() + " again to run the new version."
}

// checkRelease asks for the newest release and records it: the dashboard and
// the badge show it as pending (A44). newer is that tag when it is newer than
// this build, "" otherwise. A check that fails is recorded too, for the
// dashboard to say so, and leaves a release found before pending: it says
// nothing about whether that release is still the newest.
func (a *appShell) checkRelease() (newer string, err error) {
	if a.act.LatestVersion == nil {
		return "", nil
	}
	done := a.beginCheck()
	defer done()
	latest, err := a.act.LatestVersion()
	if err == nil && !semver.IsValid(latest) {
		err = errors.New("the release endpoint names no valid version")
	}
	if err != nil {
		a.mu.Lock()
		a.releaseErr = err
		a.mu.Unlock()
		return "", err // done() tells the dashboard
	}
	isNewer := (!semver.IsValid(a.act.Current) || semver.Compare(latest, a.act.Current) > 0) && latest != a.act.SkipTag
	a.mu.Lock()
	a.appLatest, a.checkedAt, a.releaseErr = latest, time.Now(), nil
	a.pending = ""
	if isNewer {
		a.pending = latest
	}
	a.mu.Unlock()
	a.updatesChanged()
	if !isNewer {
		return "", nil
	}
	return latest, nil
}

// offerUpdate is the periodic check (A36): when a newer release is published
// it asks once per version — a native yes/no dialog where the platform has
// one, otherwise a notification — and installs on approval. Declined or
// unanswerable offers stay reachable as an "Install tycswap X" menu item.
func (a *appShell) offerUpdate() {
	latest, err := a.checkRelease()
	if err != nil || latest == "" {
		return
	}
	a.mu.Lock()
	already := a.offered == latest
	a.offered = latest
	a.mu.Unlock()
	if already {
		return
	}
	a.askInstall(latest)
}

// askInstall asks whether to install release latest now, and does on yes. A
// build the tray cannot upgrade is told the command instead.
func (a *appShell) askInstall(latest string) {
	shown := strings.TrimPrefix(latest, "v")
	if hint := a.upgradeHint(); hint != "" {
		a.notify(releaseTitle(latest), "To update: "+hint)
		return
	}
	if a.act.Ask != nil {
		ok, err := a.act.Ask(brandName()+" update", a.releaseQuestion(latest), "Install", "Later")
		if err == nil {
			if ok {
				a.installUpdate()
			} else {
				a.notify(brandName()+" update", "Later, then — \"Install "+brandName()+" "+shown+"…\" stays in the menu.")
			}
			return
		}
	}
	a.notify(releaseTitle(latest), "Choose \"Install "+brandName()+" "+shown+"…\" from the menu to install it.")
}

// autoEvent turns engine events the user should hear about into notifications.
// A Codex engine switch names its provider (A47).
func (a *appShell) autoEvent(ev web.AutoEventView) {
	switch ev.Kind {
	case "switch":
		if ev.Provider == reporting.ProviderCodex {
			title := "Auto-switched Codex"
			if ev.Account != "" {
				title = "Auto-switched Codex to account #" + ev.Account
			}
			a.notify(title, ev.Message)
			return
		}
		title := "Auto-switched"
		if ev.Account != "" {
			title = "Auto-switched to account #" + ev.Account
		}
		a.notify(title, ev.Message)
	case "account-quarantined":
		title := "Account quarantined"
		if ev.Account != "" {
			title = "Account #" + ev.Account + " quarantined"
		}
		a.notify(title, ev.Message)
	}
}

// thresholdAlert notifies once when a window of the active account reaches the
// threshold that governs it, and re-arms when every window is 10 points below
// its own. Each window is judged against its own bar (A34): a 5h window at 92%
// would otherwise raise this alert while the engine did nothing about it,
// because the engine does not spend a week of another account's budget to
// skip an hour of waiting.
func (a *appShell) thresholdAlert(st web.State, active map[string]any, ok bool) {
	if !ok {
		return
	}
	num, key := rowNumber(active), rowKey(active)
	bars := barsOf(st)
	pcts := classPcts(active, countedModels(st))
	// Costliest first: losing the week costs days across every model, a model's
	// week costs days for that model, the 5h window costs a wait.
	axes := []struct {
		pct     *float64
		bar     float64
		subject string
	}{
		{pcts.sevenDay, bars.sevenDay, "of its week"},
		{pcts.model, bars.model, "of its " + modelWindowLabel(st) + " week"},
		{pcts.fiveHour, bars.fiveHour, "of its 5h window"},
	}
	var over *float64
	subject, clear := "", true
	for _, ax := range axes {
		if ax.pct == nil {
			continue
		}
		if over == nil && *ax.pct >= ax.bar {
			over, subject = ax.pct, ax.subject
		}
		if *ax.pct >= ax.bar-10 {
			clear = false
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case over != nil && !a.alerted[key]:
		a.alerted[key] = true
		a.notify(fmt.Sprintf("Account #%s has used %s %s", num, fmtPctShort(*over, true), subject),
			windowsLine(active, countedModels(st))+" — the dashboard shows the next best account.")
	case clear && a.alerted[key]:
		delete(a.alerted, key)
	}
}

// ── state helpers (the same JSON-ish rows the dashboard renders) ───────────

// activeAccount is the active Claude account: the one the title, the
// threshold alert and the icon are about (A47).
func activeAccount(st web.State) (map[string]any, bool) {
	return activeRow(st, reporting.ProviderClaude)
}

// activeCodexAccount is the active Codex account, when the dashboard lists
// Codex accounts.
func activeCodexAccount(st web.State) (map[string]any, bool) {
	return activeRow(st, reporting.ProviderCodex)
}

func activeRow(st web.State, provider string) (map[string]any, bool) {
	for _, r := range st.Accounts {
		if rowProvider(r) == provider && boolOf(r["isActive"]) {
			return r, true
		}
	}
	return nil, false
}

// rowProvider is a state row's provider; a row without one is Claude's, as
// on the server (reporting.AccountSnapshot.ProviderName).
func rowProvider(r map[string]any) string {
	if p, _ := r["provider"].(string); p != "" {
		return p
	}
	return reporting.ProviderClaude
}

// rowKey is a state row's key ("claude:2", "codex:1"), what the dashboard's
// account routes take.
func rowKey(r map[string]any) string {
	if k, _ := r["key"].(string); k != "" {
		return k
	}
	return rowProvider(r) + ":" + rowNumber(r)
}

// splitRowKey reads a row key back into provider and slot; a bare slot is
// Claude's, as the terminal dashboard reads its row ids.
func splitRowKey(key string) (provider, num string) {
	if p, n, ok := strings.Cut(key, ":"); ok {
		return p, n
	}
	return reporting.ProviderClaude, key
}

func rowNumber(r map[string]any) string {
	switch v := r["number"].(type) {
	case int:
		return strconv.Itoa(v)
	case float64:
		return strconv.Itoa(int(v))
	case string:
		return v
	}
	return fmt.Sprint(r["number"])
}

func rowNumberInt(r map[string]any) int {
	n, _ := strconv.Atoi(rowNumber(r))
	return n
}

func rowName(r map[string]any) string {
	if alias, _ := r["alias"].(string); alias != "" {
		return alias
	}
	email, _ := r["email"].(string)
	return email
}

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

// pctOf reads a percentage that may arrive as a number or a string.
func pctOf(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(x), "%"), 64)
		return f, err == nil
	}
	return 0, false
}

type windowPct struct {
	label string
	pct   float64
	// counted is false for a per-model window while the model limit is
	// switched off: it is still worth showing, but it must not drive the
	// percentage the tray reports or the engine's decision (A39).
	counted bool
}

// countsModelLimits reports whether the per-model weekly windows count
// towards the figures this shell shows — the same `autoswitch.model` setting
// the engine decides with, preferring the running engine's live value so the
// tray and the engine can never disagree (A39).
func countsModelLimits(st web.State) bool { return len(countedModels(st)) > 0 }

func countedModels(st web.State) []string {
	if st.Auto != nil && st.Auto.Running && st.Auto.Settings != nil {
		if v, present := st.Auto.Settings["autoswitch.model"]; present {
			sv, _ := v.(string)
			return settings.ParseModelNames(&sv)
		}
	}
	for _, sv := range st.Settings {
		if sv.Key == "autoswitch.model" {
			s, _ := sv.Value.(string)
			return settings.ParseModelNames(&s)
		}
	}
	return nil
}

// modelWindowLabel names the per-model window the accounts actually report,
// so the switch that counts it can say so. Falls back to "model".
func modelWindowLabel(st web.State) string {
	models := countedModels(st)
	if len(models) > 0 && !countsWindow(models, "all") {
		return strings.Join(models, ", ")
	}
	for _, r := range st.Accounts {
		usage, _ := r["usage"].(map[string]any)
		if usage == nil {
			continue
		}
		scoped, _ := usage["scoped"].([]any)
		for _, x := range scoped {
			if w, _ := x.(map[string]any); w != nil {
				if name, _ := w["name"].(string); name != "" {
					return name
				}
			}
		}
	}
	return "model"
}

// windowsOf lists the 5h, 7d and — when they count — the model windows that
// have a value.
func countsWindow(models []string, name string) bool {
	for _, model := range models {
		if strings.EqualFold(model, "all") || strings.EqualFold(model, name) {
			return true
		}
	}
	return false
}

func windowsOf(r map[string]any, withModels []string) []windowPct {
	usage, _ := r["usage"].(map[string]any)
	if usage == nil {
		return nil
	}
	var out []windowPct
	if w, _ := usage["fiveHour"].(map[string]any); w != nil {
		if p, ok := pctOf(w["pct"]); ok {
			out = append(out, windowPct{"5h", p, true})
		}
	}
	if w, _ := usage["sevenDay"].(map[string]any); w != nil {
		if p, ok := pctOf(w["pct"]); ok {
			out = append(out, windowPct{"7d", p, true})
		}
	}
	if scoped, _ := usage["scoped"].([]any); scoped != nil {
		for _, s := range scoped {
			w, _ := s.(map[string]any)
			if w == nil {
				continue
			}
			if p, ok := pctOf(w["pct"]); ok {
				out = append(out, windowPct{scopedName(w), p, countsWindow(withModels, scopedName(w))})
			}
		}
	} else if scoped, _ := usage["scoped"].([]map[string]any); scoped != nil {
		for _, w := range scoped {
			if p, ok := pctOf(w["pct"]); ok {
				out = append(out, windowPct{scopedName(w), p, countsWindow(withModels, scopedName(w))})
			}
		}
	}
	return out
}

func scopedName(w map[string]any) string {
	if name, _ := w["name"].(string); name != "" {
		return name
	}
	return "model"
}

// maxWindowPct is the fullest window that COUNTS — the number the tray title
// shows.
func maxWindowPct(r map[string]any, withModels []string) (float64, bool) {
	var m float64
	var has bool
	for _, w := range windowsOf(r, withModels) {
		if !w.counted {
			continue
		}
		if !has || w.pct > m {
			m, has = w.pct, true
		}
	}
	return m, has
}

// windowClassPcts is one account's utilization per window class: the 5h rate
// window, the 7d budget, and the fullest counted per-model week. Each is nil
// when nothing of that class was reported (model is nil whenever per-model
// windows are not counted).
type windowClassPcts struct {
	fiveHour *float64
	sevenDay *float64
	model    *float64
}

// classPcts splits the counted windows into the three resources they measure.
// Each is governed by a threshold of its own, so anything that JUDGES an
// account — rather than merely reporting how full it is — needs them apart
// (DESIGN A34).
func classPcts(r map[string]any, withModels []string) windowClassPcts {
	var out windowClassPcts
	for _, w := range windowsOf(r, withModels) {
		if !w.counted {
			continue
		}
		axis := &out.model
		switch w.label {
		case usage.FiveHourLabel:
			axis = &out.fiveHour
		case usage.SevenDayLabel:
			axis = &out.sevenDay
		}
		if *axis == nil || w.pct > **axis {
			pct := w.pct
			*axis = &pct
		}
	}
	return out
}

// windowsLine names every window with its value. A window that does not count
// is still shown — hiding the model figure would answer one question by
// removing another — but it is marked, so the line explains the percentage
// next to it rather than contradicting it (A39).
func windowsLine(r map[string]any, withModels []string) string {
	wins := windowsOf(r, withModels)
	parts := make([]string, 0, len(wins))
	for _, w := range wins {
		part := w.label + " " + fmtPctShort(w.pct, true)
		if !w.counted {
			part += " (not counted)"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " · ")
}

// fmtPctShort renders "45%" (or "—" when unknown); the two-value form takes
// maxWindowPct's result directly.
func fmtPctShort(pct float64, ok ...bool) string {
	if len(ok) > 0 && !ok[0] {
		return "—"
	}
	return strconv.Itoa(int(pct+0.5)) + "%"
}

// windowBars is the threshold governing each window class (A34).
type windowBars struct {
	fiveHour float64
	sevenDay float64
	model    float64
}

// barsOf reads the three bars in force. The 7d one follows the running engine
// when a session override has moved it (the TUI's adjustment, the dashboard
// slider); the other two are settings only, so they always come from the
// file.
func barsOf(st web.State) windowBars {
	sevenDay := settingPct(st, "autoswitch.sevenDayThreshold", 97)
	if st.Auto != nil && st.Auto.Running && st.Auto.Threshold > 0 {
		sevenDay = st.Auto.Threshold
	}
	return windowBars{
		fiveHour: settingPct(st, "autoswitch.fiveHourThreshold", 85),
		sevenDay: sevenDay,
		model:    settingPct(st, "autoswitch.modelThreshold", 95),
	}
}

func settingPct(st web.State, key string, fallback float64) float64 {
	for _, s := range st.Settings {
		if s.Key == key {
			if p, ok := pctOf(s.Value); ok && p > 0 {
				return p
			}
		}
	}
	return fallback
}

// capitalizeFirst upper-cases the first letter of an error message (Go error
// strings start in lower case) for a notification body.
func capitalizeFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
