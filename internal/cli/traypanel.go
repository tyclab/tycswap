package cli

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/version"
	"github.com/tyclab/tycswap/internal/web"
)

func (a *appShell) panel(st web.State) tray.Panel {
	p := tray.Panel{Title: brandName(), Version: version.Version, Status: "Auto-switch off"}
	a.mu.Lock()
	p.SortColumn, p.SortDescending = a.panelOrder, a.panelDescending
	a.mu.Unlock()
	if a.autoRunning() {
		p.Status = "Auto-switch on"
	} else if st.Auto != nil && st.Auto.ManagedBy != "" {
		manager := st.Auto.ManagedBy
		if manager == "flakelab-tycswap-autoswitch.timer" {
			manager = "Flakelab"
		}
		p.Status = "Auto-switch managed by " + manager
	}
	if down, why := a.offline(); down {
		p.Offline, p.Status = true, capitalizeFirst(why)
	}
	p.Columns = []tray.Column{{ID: "account", Label: "Account"}, {ID: "fiveHour", Label: "5-hour"}, {ID: "sevenDay", Label: "Weekly"}}
	models := panelModels(st)
	for _, name := range models {
		p.Columns = append(p.Columns, tray.Column{ID: "model:" + name, Label: name})
	}
	p.Columns = append(p.Columns, tray.Column{ID: "owner", Label: "Used by"})
	claude, codex := rowsByProvider(st.Accounts)
	for _, row := range append(claude, codex...) {
		windows := panelWindows(row)
		r := tray.PanelRow{ID: rowKey(row), Title: "#" + rowNumber(row) + " " + rowName(row)}
		if rowProvider(row) == "codex" {
			r.Title = "Codex " + r.Title
		}
		owners := panelOwners(st, row)
		r.Active = len(owners) > 0
		r.Cells = []string{r.Title, panelPct(windows["fiveHour"]), panelPct(windows["sevenDay"])}
		for _, model := range models {
			r.Cells = append(r.Cells, panelPct(windows["model:"+model]))
		}
		owner := strings.Join(owners, ", ")
		if owner == "" {
			owner = "—"
		}
		r.Cells = append(r.Cells, owner)
		for _, col := range p.Columns[1 : len(p.Columns)-1] {
			w := windows[col.ID]
			if w == nil {
				continue
			}
			line := col.Label + ": " + panelPct(w) + " used"
			if reset, _ := w["resetsAt"].(string); reset != "" {
				line += " · " + panelReset(reset, time.Now())
			}
			r.Details = append(r.Details, line)
		}
		if rowProvider(row) == "claude" && windows["model:Opus"] == nil {
			r.Details = append(r.Details, "No separate Opus limit reported; shared limits are shown above.")
		}
		if fetched, _ := row["usageFetchedAt"].(string); fetched != "" {
			if stamp, err := time.Parse(time.RFC3339, fetched); err == nil {
				r.Details = append(r.Details, "Usage fetched "+stamp.Local().Format("02 Jan 2006 15:04:05"))
			}
		}
		if status, _ := row["usageStatus"].(string); status != "" && status != "ok" {
			r.Details = append(r.Details, "Usage: "+status)
		}
		if boolOf(row["disabled"]) {
			r.Details = append(r.Details, "Excluded from automatic rotation; manual switching is allowed.")
		}
		if len(r.Details) == 0 {
			r.Details = []string{"No usage data reported yet."}
		}
		r.Targets = panelTargets(st, row)
		p.Rows = append(p.Rows, r)
	}
	sortPanelRows(&p)
	p.Settings = []tray.SettingScope{{Label: "Shared defaults", Settings: panelSettings(st.Settings)}}
	for _, group := range st.Groups {
		p.Settings = append(p.Settings, tray.SettingScope{ID: group.ID, Label: group.Label, Settings: panelSettings(group.SettingViews)})
	}
	p.Actions = []tray.Item{{ID: "open", Title: "Open dashboard"}, {ID: "update", Title: "Check for updates"}}
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
	if st.Auto == nil || st.Auto.ManagedBy == "" {
		p.Actions = append(p.Actions, tray.Item{ID: "auto", Title: "Auto-switch", Kind: tray.KindToggle, Checked: a.autoRunning(), Disabled: a.act.AutoStart == nil || a.act.AutoStop == nil})
	}
	if a.act.Autostart != nil {
		on, err := a.act.Autostart()
		p.Actions = append(p.Actions, tray.Item{ID: "autostart", Title: "Start at login", Kind: tray.KindToggle, Checked: on, Disabled: err != nil})
	}
	p.Actions = append(p.Actions, tray.Item{ID: "quit", Title: "Quit"})
	return p
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

func panelPct(w map[string]any) string {
	return fmtPctShort(pctOf(w["pct"]))
}

func panelReset(value string, now time.Time) string {
	stamp, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "reset time unknown"
	}
	d := stamp.Sub(now)
	if d <= 0 {
		return "reset due; awaiting fresh usage"
	}
	if d < time.Hour {
		return fmt.Sprintf("resets in %dm", max(1, int(d.Minutes())))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("resets in %dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("resets in %dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

func panelOwners(st web.State, row map[string]any) []string {
	var out []string
	if rowProvider(row) == "codex" {
		if boolOf(row["isActive"]) {
			return []string{"Codex"}
		}
		return nil
	}
	for _, group := range st.Groups {
		if group.ActiveNumber == rowNumber(row) {
			out = append(out, fmt.Sprintf("%s · %d", group.Label, group.LiveSessions))
		}
	}
	if boolOf(row["isActive"]) {
		out = append(out, "Existing / default")
	}
	return out
}

func panelTargets(st web.State, row map[string]any) []tray.Target {
	common := ""
	if !boolOf(row["switchable"]) {
		common = "Account is not switchable"
	}
	label := "Existing / default"
	if rowProvider(row) == "codex" {
		label = "Codex (resume sessions to use it)"
	}
	root := tray.Target{ID: "switch:" + rowKey(row), Label: label, Reason: common}
	if boolOf(row["isActive"]) {
		root.Reason = "Already active"
	}
	if rowProvider(row) == "claude" {
		for _, group := range st.Groups {
			if group.ActiveNumber == rowNumber(row) {
				root.Reason = "In use by " + group.Label
			}
		}
	}
	root.Disabled = root.Reason != ""
	out := []tray.Target{root}
	if rowProvider(row) != "claude" {
		return out
	}
	for _, group := range st.Groups {
		reason := common
		if group.Blocker != "" {
			reason = group.Blocker
		}
		if blocker := group.AccountBlockers[rowNumber(row)]; blocker != "" {
			reason = blocker
		}
		if group.ActiveNumber == rowNumber(row) {
			reason = "Already active for " + group.Label
		}
		out = append(out, tray.Target{ID: "group:" + group.ID + ":" + rowNumber(row), Label: group.Label, Reason: reason, Disabled: reason != ""})
	}
	return out
}

func panelSettings(views []web.SettingView) []tray.PanelSetting {
	out := make([]tray.PanelSetting, 0, len(views))
	for _, v := range views {
		label := v.Label
		if label == "" {
			label = v.Key
		}
		value := ""
		if v.Value != nil {
			value = settings.FormatSettingValue(v.Value)
		}
		source := v.Source
		if source == "" {
			source = "custom"
			if v.IsDefault {
				source = "default"
			}
		}
		out = append(out, tray.PanelSetting{Key: v.Key, Label: label, Value: value, Kind: v.Kind, Description: v.Description,
			Applies: v.Applies, Source: source, ReadOnly: v.ReadOnly, Choices: v.Choices, Min: v.Min, Max: v.Max, IsDefault: v.IsDefault})
	}
	return out
}

func (a *appShell) panelAction(action tray.PanelAction) error {
	switch action.Kind {
	case "sort":
		a.mu.Lock()
		if a.panelOrder == action.Key {
			a.panelDescending = !a.panelDescending
		} else {
			a.panelOrder, a.panelDescending = action.Key, false
		}
		a.mu.Unlock()
		a.repaint()
	case "command":
		a.click(action.Key)
	case "setting-set", "setting-reset":
		if a.act.SaveSetting == nil {
			return fmt.Errorf("settings are unavailable")
		}
		if down, why := a.offline(); down {
			return fmt.Errorf("%s", why)
		}
		var value *string
		if action.Kind == "setting-set" {
			value = &action.Value
		}
		if err := a.act.SaveSetting(action.Scope, action.Key, value); err != nil {
			return err
		}
		a.dashboardChanged()
	}
	return nil
}

func sortPanelRows(p *tray.Panel) {
	primary := slices.IndexFunc(p.Columns, func(c tray.Column) bool { return c.ID == p.SortColumn })
	if primary < 0 {
		return
	}
	indices := []int{primary}
	for _, key := range []string{"sevenDay", "model:Fable", "model:Opus", "fiveHour"} {
		idx := slices.IndexFunc(p.Columns, func(c tray.Column) bool { return c.ID == key })
		if idx >= 0 && idx != primary {
			indices = append(indices, idx)
		}
	}
	sort.SliceStable(p.Rows, func(i, j int) bool {
		a, b := p.Rows[i], p.Rows[j]
		ap, _, _ := strings.Cut(a.ID, ":")
		bp, _, _ := strings.Cut(b.ID, ":")
		if ap != bp {
			return ap < bp
		}
		for _, idx := range indices {
			if idx >= len(a.Cells) || idx >= len(b.Cells) {
				continue
			}
			x, y := a.Cells[idx], b.Cells[idx]
			if x == y {
				continue
			}
			if x == "—" || y == "—" {
				return y == "—"
			}
			comparison := strings.Compare(strings.ToLower(x), strings.ToLower(y))
			if xv, ok := pctOf(x); ok {
				if yv, valid := pctOf(y); valid {
					switch {
					case xv < yv:
						comparison = -1
					case xv > yv:
						comparison = 1
					default:
						comparison = 0
					}
				}
			}
			if comparison == 0 {
				continue
			}
			if p.SortDescending {
				return comparison > 0
			}
			return comparison < 0
		}
		return false
	})
}
