package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/web"
)

// panelRefresh is how old a countdown on an open panel can get: without a
// newer state the panel is laid out again after it.
const panelRefresh = time.Minute

// panel is the Windows tray's daily view (DESIGN A84).
func (a *appShell) panel(st web.State) tray.Panel { return a.panelAt(st, time.Now()) }

// panelAt lays the panel out as of now; clock texts are in now's zone.
func (a *appShell) panelAt(st web.State, now time.Time) tray.Panel {
	p := tray.Panel{Title: brandName(), Empty: "No accounts yet. Add one in the dashboard."}
	if st.Auto == nil && len(st.Accounts) == 0 {
		p.Empty = "No data yet"
	}
	a.mu.Lock()
	p.Note, p.Alert = a.note, a.noteAlert
	a.mu.Unlock()
	down, why := a.offline()
	if down {
		p.Offline, p.Note, p.Alert = true, capitalizeFirst(why), true
	}
	p.Columns = []tray.Column{{ID: "account", Label: "Account"}, {ID: "fiveHour", Label: "5-hour"}, {ID: "sevenDay", Label: "Weekly"}}
	for _, name := range panelModels(st) {
		p.Columns = append(p.Columns, tray.Column{ID: "model:" + name, Label: name})
	}
	bars := barsOf(st)
	claude, codex := rowsByProvider(st.Accounts)
	for _, row := range append(claude, codex...) {
		r := tray.PanelRow{ID: rowKey(row), Name: "#" + rowNumber(row) + " " + rowName(row),
			Active: boolOf(row["isActive"]), Disabled: !boolOf(row["switchable"])}
		if rowProvider(row) == "codex" {
			r.Name = "Codex " + r.Name
		}
		r.Plan, _ = row["plan"].(string)
		if boolOf(row["disabled"]) {
			r.Tag = "not rotated"
		}
		if status, _ := row["usageStatus"].(string); status != "" && status != "ok" {
			r.Warning = status
		}
		windows := panelWindows(row)
		for _, column := range p.Columns[1:] {
			bar := bars.model
			switch column.ID {
			case "fiveHour":
				bar = bars.fiveHour
			case "sevenDay":
				bar = bars.sevenDay
			}
			cell := panelLimit(windows[column.ID], column.ID == "fiveHour", bar, now)
			if column.ID == "model:Fable" && !panelRunsFable(r.Plan) {
				cell = tray.PanelCell{Absent: true}
			}
			r.Cells = append(r.Cells, cell)
		}
		p.Rows = append(p.Rows, r)
	}
	p.Actions = append(p.Actions, tray.Item{ID: "quit", Title: "Quit"})
	for _, action := range a.updatesSection() {
		switch action.ID {
		case "install-update":
			action.Title = "Update " + brandName()
		case "claude-code":
			action.Title = "Update Claude"
		default:
			continue
		}
		action.Sub = ""
		p.Actions = append(p.Actions, action)
	}
	p.Actions = append(p.Actions, tray.Item{ID: "auto", Title: "Auto-switch", Kind: tray.KindToggle, Checked: a.autoRunning(),
		Disabled: down || a.act.AutoStart == nil || a.act.AutoStop == nil})
	if a.act.Autostart != nil {
		on, err := a.act.Autostart()
		p.Actions = append(p.Actions, tray.Item{ID: "autostart", Title: "Start at login", Kind: tray.KindToggle, Checked: on && err == nil, Disabled: down || err != nil})
	}
	p.Actions = append(p.Actions, tray.Item{ID: "open", Title: "Open dashboard"})
	return p
}

// panelRunsFable is false only for a plan known not to run Fable (Pro,
// free); an unknown plan shows what the account reports.
func panelRunsFable(plan string) bool {
	entitled := groups.Entitlement(groups.Fable, groups.Continue, plan, nil, nil)
	return !entitled.Known || entitled.Allowed
}

// panelLimit is one window's cell: a dash when it is not reported, else its
// share, a bar in the limit colour at or over bar, and its own reset.
func panelLimit(w map[string]any, fiveHour bool, bar float64, now time.Time) tray.PanelCell {
	pct, ok := pctOf(w["pct"])
	if w == nil || !ok {
		return tray.PanelCell{Absent: true}
	}
	reset, _ := w["resetsAt"].(string)
	return tray.PanelCell{Share: fmtPctShort(pct), Pct: min(100, max(0, pct)), Limit: pct >= bar, Reset: panelResetLine(reset, fiveHour, now)}
}

func panelModels(st web.State) []string {
	found := map[string]bool{}
	for _, row := range st.Accounts {
		for key := range panelWindows(row) {
			if strings.HasPrefix(key, "model:") {
				found[strings.TrimPrefix(key, "model:")] = true
			}
		}
	}
	var out []string
	for name := range found {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i] == "Fable" || out[j] == "Fable" {
			return out[i] == "Fable"
		}
		return out[i] < out[j]
	})
	return out
}

func panelWindows(row map[string]any) map[string]map[string]any {
	usage, _ := row["usage"].(map[string]any)
	out := map[string]map[string]any{}
	for _, key := range []string{"fiveHour", "sevenDay"} {
		if w, _ := usage[key].(map[string]any); w != nil {
			out[key] = w
		}
	}
	add := func(w map[string]any) {
		if w == nil {
			return
		}
		name := scopedName(w)
		for _, base := range []string{"Fable", "Opus", "Sonnet", "Haiku"} {
			if strings.EqualFold(name, base) || strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)+" ") {
				name = base
				break
			}
		}
		key := "model:" + name
		if previous := out[key]; previous != nil {
			old, oldOK := pctOf(previous["pct"])
			value, valid := pctOf(w["pct"])
			if oldOK && (!valid || old >= value) {
				return
			}
		}
		out[key] = w
	}
	switch scoped := usage["scoped"].(type) {
	case []any:
		for _, v := range scoped {
			w, _ := v.(map[string]any)
			add(w)
		}
	case []map[string]any:
		for _, w := range scoped {
			add(w)
		}
	}
	return out
}

// panelResetLine is a limit's own reset, small under its bar: the clock time
// and the time left, "Sat 10:00 · 2d 22h". "" when the reset is unknown or
// past, which includes a window that has not started.
func panelResetLine(value string, fiveHour bool, now time.Time) string {
	left := panelCountdown(value, now)
	if left == "" {
		return ""
	}
	stamp, _ := time.Parse(time.RFC3339, value)
	return panelClock(stamp.In(now.Location()), fiveHour, now) + " · " + left
}

// panelClock is the 5-hour window's time of day; any other window's names
// the day, "Today 15:04" or "Mon 15:04", and past six days the date alone,
// where a weekday would name this week's.
func panelClock(at time.Time, fiveHour bool, now time.Time) string {
	if fiveHour {
		return at.Format("15:04")
	}
	day := func(t time.Time) time.Time { y, m, d := t.Date(); return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	switch days := int(day(at).Sub(day(now)).Hours() / 24); {
	case days == 0:
		return "Today " + at.Format("15:04")
	case days <= 6:
		return at.Format("Mon 15:04")
	}
	return at.Format("Mon 2 Jan")
}

// panelCountdown is the short time to a reset ("8m", "5h 2m", "5d 5h"); ""
// when it is unknown or due.
func panelCountdown(value string, now time.Time) string {
	stamp, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	d := stamp.Sub(now)
	switch {
	case d <= 0:
		return ""
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// panelAction runs a command from the panel; the panel sends nothing else.
func (a *appShell) panelAction(action tray.PanelAction) error {
	if action.Kind != "command" {
		return fmt.Errorf("unknown panel action %q", action.Kind)
	}
	a.click(action.Key)
	return nil
}

// setNote is the panel's line under the list: the last action's result.
func (a *appShell) setNote(text string, alert bool) {
	a.mu.Lock()
	a.note, a.noteAlert = text, alert
	a.mu.Unlock()
}

// armPanelRefresh lays the panel out again once its countdowns are a
// refresh old, unless a newer state comes first. Only a tray with a panel
// shows countdowns.
func (a *appShell) armPanelRefresh() {
	if _, ok := a.tray.(tray.PanelTray); !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refresh != nil {
		a.refresh.Stop()
	}
	every := a.panelRefresh
	if every <= 0 {
		every = panelRefresh
	}
	a.refresh = time.AfterFunc(every, a.repaint)
}
