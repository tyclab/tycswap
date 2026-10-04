package tray

import (
	"reflect"
	"testing"
)

// A submenu becomes an open/close pair around its children, the closing
// step carrying MF_POPUP and the label; every leaf's command ID minus one
// resolves through idAt to that leaf, also inside the submenu.
func TestMenuPlanNestsSubmenusAndNumbersByTag(t *testing.T) {
	items := []Item{
		{ID: "open", Title: "Open"},
		{ID: "more", Title: "More", Children: []Item{
			{ID: "a", Title: "A", Checked: true},
			Separator(),
			{ID: "b", Title: "B", Disabled: true},
		}},
		{ID: "quit", Title: "Quit"},
	}
	got := menuPlan(items)
	want := []menuStep{
		{op: opRow, flags: mfString, cmd: 1, title: "Open"},
		{op: opOpen, flags: mfString | mfPopup, title: "More"},
		{op: opRow, flags: mfString | mfChecked, cmd: 3, title: "A"},
		{op: opSeparator, flags: mfSeparator},
		{op: opRow, flags: mfString | mfGrayed, cmd: 5, title: "B"},
		{op: opClose, flags: mfString | mfPopup, title: "More"},
		{op: opRow, flags: mfString, cmd: 6, title: "Quit"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan =\n%+v\nwant\n%+v", got, want)
	}

	var m menuModel
	m.set(items)
	for cmd, id := range map[uintptr]string{1: "open", 3: "a", 6: "quit"} {
		if got, ok := m.idAt(int(cmd) - 1); !ok || got != id {
			t.Errorf("command %d = %q,%v, want %q", cmd, got, ok, id)
		}
	}
	if _, ok := m.idAt(1); ok {
		t.Error("the submenu row itself must not resolve to an ID")
	}
}
