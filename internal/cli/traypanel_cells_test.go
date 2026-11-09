package cli

import (
	"strings"
	"testing"
	"time"
)

// Every cell carries the reset countdown (A80), the row title the plan
// (A79), and sorting still reads the share before the countdown.
func TestTrayPanelCellsShowResetWithoutASelection(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	pt := &panelCaptureTray{fakeTray: ft}
	sh.tray = pt
	st := sampleState()
	soon := time.Now().Add(8 * time.Minute).UTC().Format(time.RFC3339)
	week := time.Now().Add(5*24*time.Hour + 5*time.Hour).UTC().Format(time.RFC3339)
	st.Accounts[0] = acct(1, "a@example.test", "", true, 55, 15, "Fable", 21)
	st.Accounts[0]["plan"] = "Ultra"
	st.Accounts[0]["usage"].(map[string]any)["fiveHour"] = map[string]any{"pct": 55, "resetsAt": soon}
	st.Accounts[0]["usage"].(map[string]any)["sevenDay"] = map[string]any{"pct": 15, "resetsAt": week}
	st.Accounts[1] = acct(2, "b@example.test", "", false, 7, 85, "Fable", 100)
	delete(st.Accounts[1]["usage"].(map[string]any), "fiveHour")
	sh.update(st)
	p := pt.panel
	var first, second = p.Rows[0], p.Rows[1]
	if first.ID != "claude:1" {
		first, second = p.Rows[1], p.Rows[0]
	}
	if first.Title != "#1 a@example.test · Ultra" || first.Cells[0] != first.Title {
		t.Errorf("title = %q", first.Title)
	}
	if first.Cells[1] != "55% · 8m" && first.Cells[1] != "55% · 7m" {
		t.Errorf("5h cell = %q", first.Cells[1])
	}
	if first.Cells[2] != "15% · 5d 5h" && first.Cells[2] != "15% · 5d 4h" {
		t.Errorf("7d cell = %q", first.Cells[2])
	}
	if second.Cells[1] != "—" || !strings.HasPrefix(second.Cells[2], "85%") || strings.Contains(second.Title, "·") {
		t.Errorf("row 2 = %q %q", second.Title, second.Cells)
	}
	if cellLead(first.Cells[1]) != "55%" || cellLead("—") != "—" {
		t.Error("cellLead keeps the share only")
	}
	if panelCountdown("not a time", time.Now()) != "" || panelCountdown(time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), time.Now()) != "" {
		t.Error("an unknown or due reset adds nothing")
	}
}
