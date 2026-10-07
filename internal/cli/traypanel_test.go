package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/web"
)

type panelCaptureTray struct {
	*fakeTray
	panel tray.Panel
}

func (t *panelCaptureTray) SetPanel(p tray.Panel) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.panel = p
}

func TestTrayPanelSeparatesQuotaAndSessionOwnership(t *testing.T) {
	sh, ft, calls := newTestShell(t)
	pt := &panelCaptureTray{fakeTray: ft}
	sh.tray = pt
	st := sampleState()
	st.Accounts[1] = acct(1, "work@example.test", "Work", true, 0, 65, "Fable 5", 100)
	st.Groups = []web.GroupView{{ID: "fable", Label: "Fable", ActiveNumber: "2", LiveSessions: 2,
		AccountBlockers: map[string]string{"1": "Fable quota exhausted"}}, {ID: "opus", Label: "Opus / other"}}
	st.Auto.ManagedBy = "flakelab-tycswap-autoswitch.timer"
	sh.update(st)
	p := pt.panel
	if len(p.Rows) != 2 || p.Rows[0].Cells[1] != "0%" || p.Rows[0].Cells[2] != "65%" || p.Rows[0].Cells[3] != "100%" {
		t.Fatalf("limits were collapsed or rows duplicated: %#v", p.Rows)
	}
	if !p.Rows[0].Active || !p.Rows[1].Active || !strings.Contains(p.Rows[1].Cells[4], "Fable") {
		t.Fatalf("active profiles not visible: %#v", p.Rows)
	}
	if p.Rows[1].Cells[3] != "—" || !strings.Contains(p.Status, "managed by Flakelab") {
		t.Fatalf("unknown quota or external automation misreported: %#v", p)
	}
	for _, row := range p.Rows {
		if len(row.Cells) != len(p.Columns) {
			t.Fatalf("columns misaligned: %#v", row)
		}
	}
	if !p.Rows[1].Targets[0].Disabled || !strings.Contains(p.Rows[1].Targets[0].Reason, "Fable") {
		t.Fatal("default target offered another group's account")
	}
	if !p.Rows[0].Targets[1].Disabled || p.Rows[0].Targets[1].Reason != "Fable quota exhausted" {
		t.Fatal("group target lost its compatibility blocker")
	}
	if len(*calls) != 0 {
		t.Fatal("rendering performed an account mutation")
	}
	active, ok := ft.item("switch:claude:1")
	if !ok || !active.Active || !active.Disabled {
		t.Fatal("active account lost emphasis or became switchable")
	}
	if toggle, ok := ft.item("auto"); ok && toggle.Active {
		t.Fatal("automation toggle got active-account styling")
	}
}

func TestTrayPanelSettingsUseExplicitScopeAndPreserveReset(t *testing.T) {
	sh, _, _ := newTestShell(t)
	var calls []string
	sh.act.SaveSetting = func(scope, key string, value *string) error {
		v := "reset"
		if value != nil {
			v = *value
		}
		calls = append(calls, scope+":"+key+":"+v)
		if v == "101" {
			return errors.New("threshold must not exceed 100")
		}
		return nil
	}
	sh.panelAction(tray.PanelAction{Kind: "setting-set", Scope: "fable", Key: "autoswitch.fiveHourThreshold", Value: "90"})
	sh.panelAction(tray.PanelAction{Kind: "setting-reset", Scope: "opus", Key: "autoswitch.fiveHourThreshold"})
	err := sh.panelAction(tray.PanelAction{Kind: "setting-set", Key: "autoswitch.fiveHourThreshold", Value: "101"})
	want := []string{"fable:autoswitch.fiveHourThreshold:90", "opus:autoswitch.fiveHourThreshold:reset", ":autoswitch.fiveHourThreshold:101"}
	if !slices.Equal(calls, want) || err == nil || !strings.Contains(err.Error(), "100") {
		t.Fatalf("settings dispatch %v, error %v", calls, err)
	}
}

func TestTrayPanelKeepsPendingLocalUpdatesReachable(t *testing.T) {
	sh, ft, rig := updatesShell(t, false)
	pt := &panelCaptureTray{fakeTray: ft}
	sh.tray = pt
	rig.latest = "v2.2.0"
	rig.claude = brewSeat("2.1.280", "2.1.281")
	sh.click("update")
	for _, id := range []string{"install-update", "claude-code"} {
		if !slices.ContainsFunc(pt.panel.Actions, func(action tray.Item) bool { return action.ID == id && !action.Disabled }) {
			t.Fatalf("pending %s action missing from panel", id)
		}
	}
	if len(ft.notes) != 1 || !strings.Contains(ft.notes[0], "update buttons in the tray panel") {
		t.Fatalf("update notification points outside panel: %v", ft.notes)
	}
}

func TestTrayPanelSettingsShowDefaultAndOverrideSources(t *testing.T) {
	fields := panelSettings([]web.SettingView{{IsDefault: true}, {}, {Source: "group"}, {Source: "session"}})
	for i, want := range []string{"default", "custom", "group", "session"} {
		if fields[i].Source != want {
			t.Fatalf("source %d = %q, want %q", i, fields[i].Source, want)
		}
	}
}

func TestTrayPanelRotationExclusionStillAllowsExplicitSwitch(t *testing.T) {
	sh, _, _ := newTestShell(t)
	st := sampleState()
	st.Accounts[0]["disabled"] = true
	p := sh.panel(st)
	row := p.Rows[1]
	if row.ID != "claude:2" || row.Targets[0].Disabled || !strings.Contains(strings.Join(row.Details, " "), "Excluded from automatic rotation") {
		t.Fatalf("manual and automatic eligibility confused: %+v", row)
	}
}

func TestTrayPanelSortUsesSecondaryLimitsAndLeavesUnknownLast(t *testing.T) {
	sh, _, _ := newTestShell(t)
	st := sampleState()
	st.Accounts = []map[string]any{
		acct(1, "a", "A", false, 10, 60, "Fable", 20),
		acct(2, "b", "B", false, 10, 40, "Fable", 80),
		acct(3, "c", "C", false, nil, nil, "", nil),
		acct(4, "d", "D", false, 10, 40, "Fable", 30),
	}
	sh.update(st)
	sh.panelAction(tray.PanelAction{Kind: "sort", Key: "fiveHour"})
	p := sh.panel(st)
	var ids []string
	for _, r := range p.Rows {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []string{"claude:4", "claude:2", "claude:1", "claude:3"}) {
		t.Fatalf("hierarchical order %v", ids)
	}
	sh.panelAction(tray.PanelAction{Kind: "sort", Key: "fiveHour"})
	p = sh.panel(st)
	if !p.SortDescending || p.Rows[len(p.Rows)-1].ID != "claude:3" {
		t.Fatal("unknown usage sorted as available")
	}
}

func TestPanelResetDoesNotPromiseQuotaBeforeRefresh(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if got := panelReset(now.Add(-time.Minute).Format(time.RFC3339), now); got != "reset due; awaiting fresh usage" {
		t.Fatal(got)
	}
	if got := panelReset(now.Add(90*time.Minute).Format(time.RFC3339), now); got != "resets in 1h 30m" {
		t.Fatal(got)
	}
}
