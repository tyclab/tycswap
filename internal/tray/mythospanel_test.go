package tray

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestMythosPanelMarksLoginBesidePlan(t *testing.T) {
	p := panelFixture()
	if err := json.Unmarshal([]byte(`{"MythosLogin":"Mythos login: sign in again"}`), &p.Rows[0]); err != nil {
		t.Fatal(err)
	}
	labels := panelLabels(p.Rows[0])
	if !slices.Contains(labels, "Mythos login: sign in again") || !strings.Contains(panelSummary(p.Columns, p.Rows[0]), "Mythos login: sign in again") {
		t.Fatalf("Mythos login absent from chip or accessible name: labels %v, summary %s", labels, panelSummary(p.Columns, p.Rows[0]))
	}
}
