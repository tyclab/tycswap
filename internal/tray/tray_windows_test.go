package tray

import (
	"reflect"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
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

// When Explorer restarts it broadcasts "TaskbarCreated" and every icon is
// gone; the tray adds its icon again, with the current tooltip, icon and
// click callback (DESIGN A52). Before, the icon stayed away until the app
// restarted. Other messages add nothing.
func TestTaskbarCreatedAddsTheIconAgain(t *testing.T) {
	m, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16z("TaskbarCreated"))))
	if m == 0 {
		t.Fatal("RegisterWindowMessage(TaskbarCreated) failed")
	}
	type call struct {
		op       uintptr
		flags    uint32
		callback uint32
		icon     windows.Handle
		tip      string
	}
	var calls []call
	prevShell, prevMsg := shellNotifyIcon, wmTaskbarCreated
	shellNotifyIcon = func(op uintptr, nid *notifyIconData) bool {
		calls = append(calls, call{op, nid.uFlags, nid.uCallbackMessage, nid.hIcon, windows.UTF16ToString(nid.szTip[:])})
		return true
	}
	wmTaskbarCreated = uint32(m)
	tr := &windowsTray{tooltip: "first", hicon: windows.Handle(7)}
	winMu.Lock()
	prevTray := winTray
	winTray = tr
	winMu.Unlock()
	t.Cleanup(func() {
		shellNotifyIcon, wmTaskbarCreated = prevShell, prevMsg
		winMu.Lock()
		winTray = prevTray
		winMu.Unlock()
	})

	tr.mu.Lock()
	tr.tooltip = "Account-2 · 5h 41%"
	tr.mu.Unlock()
	if r := trayWndProc(0, uint32(m), 0, 0); r != 0 {
		t.Errorf("TaskbarCreated returned %d, want 0", r)
	}
	want := call{nimAdd, nifMessage | nifIcon | nifTip, wmTrayIcon, windows.Handle(7), "Account-2 · 5h 41%"}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("shell calls = %+v, want one %+v", calls, want)
	}

	// The tray's own messages are not a re-add.
	calls = nil
	trayWndProc(0, wmTrayUpdate, 0, 0)
	for _, c := range calls {
		if c.op == nimAdd {
			t.Errorf("a tooltip update added the icon: %+v", calls)
		}
	}
}
