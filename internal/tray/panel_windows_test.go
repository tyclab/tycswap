package tray

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func nativePanelFixture(t *testing.T, onAction func(PanelAction) error) *windowsTray {
	t.Helper()
	runtime.LockOSThread()
	winMu.Lock()
	previous := winTray
	winMu.Unlock()
	previousTheme := readMenuTheme
	readMenuTheme = func() menuTheme { return menuDark }
	tray, err := newTray(Icon{PNG: testPNG(t)}, Options{OnPanelAction: onAction})
	if err != nil {
		t.Fatal(err)
	}
	tr := tray.(*windowsTray)
	tr.SetPanel(panelFixture())
	if err := tr.createPanel(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		tr.panel.close()
		readMenuTheme = previousTheme
		winMu.Lock()
		winTray = previous
		winMu.Unlock()
		runtime.UnlockOSThread()
	})
	return tr
}

func nativePanelResult(t *testing.T, tr *windowsTray) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		ready := len(tr.panelResults) > 0
		tr.mu.Unlock()
		if ready {
			tr.applyPanelResults()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("native panel callback did not finish")
}

func nativeEnabled(control uintptr) bool {
	enabled, _, _ := panelIsEnabled.Call(control)
	return enabled != 0
}

// nativeClick sends the parent what a clicked button sends it.
func nativeClick(control uintptr) {
	parent, _, _ := user32.NewProc("GetParent").Call(control)
	id, _, _ := panelControlID.Call(control)
	panelSend.Call(parent, wmCommand, id, control)
}

func nativeAction(t *testing.T, actions chan PanelAction) PanelAction {
	t.Helper()
	select {
	case action := <-actions:
		return action
	case <-time.After(time.Second):
		t.Fatal("no panel action")
	}
	return PanelAction{}
}

// nativeChildren lists every window inside the popup, depth first, with its
// class name.
func nativeChildren(parent uintptr) map[uintptr]string {
	out := map[uintptr]string{}
	callback := windows.NewCallback(func(hwnd, _ uintptr) uintptr {
		name := make([]uint16, 256)
		user32.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
		out[hwnd] = windows.UTF16ToString(name)
		return 1
	})
	user32.NewProc("EnumChildWindows").Call(parent, callback, 0)
	return out
}

// One Switch button per row, inside the scrolling list, its window text the
// row in words; the active row's is disabled and says "in use"; a click
// dispatches the switch.
func TestNativePanelSwitchButtonPerRow(t *testing.T) {
	actions := make(chan PanelAction, 8)
	tr := nativePanelFixture(t, func(action PanelAction) error { actions <- action; return nil })
	p := tr.panel
	for _, row := range p.panel.Rows {
		control := p.controls[p.rowIDs[row.ID]]
		if parent, _, _ := user32.NewProc("GetParent").Call(control); control == 0 || parent != p.list {
			t.Fatalf("row %s has no Switch in the list", row.ID)
		}
		if text := panelControlText(control); text != panelSummary(p.panel.Columns, row) {
			t.Fatalf("row %s button text %q", row.ID, text)
		}
		if nativeEnabled(control) != (!row.Active && !row.Disabled) {
			t.Fatalf("row %s enabled %v", row.ID, nativeEnabled(control))
		}
	}
	active := p.controls[p.rowIDs["claude:2"]]
	if !strings.HasPrefix(panelControlText(active), "#2 xxxxxxxxxxxxxxxxxxxx, in use, ") {
		t.Fatalf("active row text %q", panelControlText(active))
	}
	nativeClick(p.controls[p.rowIDs["claude:1"]])
	if action := nativeAction(t, actions); action != (PanelAction{Kind: "command", Key: "switch:claude:1"}) {
		t.Fatalf("switch dispatched %+v", action)
	}
	nativePanelResult(t, tr)
	for _, id := range []string{"claude:2", "codex:1"} {
		p.command(p.rowIDs[id], 0)
	}
	select {
	case action := <-actions:
		t.Fatalf("an active or unswitchable row dispatched %+v", action)
	default:
	}
}

// The two switches say their state in their window text and dispatch
// "auto" and "autostart"; no tab, list view, editor or combo box is left.
func TestNativePanelSwitchesAndNoSettings(t *testing.T) {
	actions := make(chan PanelAction, 8)
	tr := nativePanelFixture(t, func(action PanelAction) error { actions <- action; return nil })
	p := tr.panel
	for id, text := range map[string]string{"auto": "Auto-switch, on", "autostart": "Start at login, off", "quit": "Quit", "open": "Open dashboard"} {
		if got := panelControlText(p.controls[p.actionIDs[id]]); got != text {
			t.Fatalf("%s reads %q, want %q", id, got, text)
		}
	}
	for _, id := range []string{"auto", "autostart"} {
		nativeClick(p.controls[p.actionIDs[id]])
		if action := nativeAction(t, actions); action != (PanelAction{Kind: "command", Key: id}) {
			t.Fatalf("%s dispatched %+v", id, action)
		}
		nativePanelResult(t, tr)
	}
	for hwnd, class := range nativeChildren(p.hwnd) {
		text := panelControlText(hwnd)
		if class != "Button" && class != "BUTTON" && hwnd != p.list {
			t.Fatalf("a %s control is left in the panel", class)
		}
		for _, gone := range []string{"Accounts", "Advanced", "Save", "Reset", "Add account", "Interactive menu", "Check for updates"} {
			if strings.HasPrefix(text, gone) {
				t.Fatalf("control %q is left in the panel", text)
			}
		}
	}
}

// Tab walks the usable Switch buttons top to bottom, then the footer left to
// right.
func TestNativePanelTabOrder(t *testing.T) {
	tr := nativePanelFixture(t, func(PanelAction) error { return nil })
	p := tr.panel
	var got []uintptr
	next := uintptr(0)
	for range 12 {
		next, _, _ = panelNextTab.Call(p.hwnd, next, 0)
		if next == 0 || slices.Contains(got, next) {
			break
		}
		got = append(got, next)
	}
	want := []uintptr{p.controls[p.rowIDs["claude:1"]]}
	for _, action := range p.panel.Actions {
		want = append(want, p.controls[p.actionIDs[action.ID]])
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tab order %v, want %v", got, want)
	}
}

// Enter presses the focused Switch, Esc closes the panel (TRAY-10: Enter on
// an account did nothing).
func TestNativePanelKeyboardPressesAndCloses(t *testing.T) {
	actions := make(chan PanelAction, 8)
	tr := nativePanelFixture(t, func(action PanelAction) error { actions <- action; return nil })
	p := tr.panel
	panelShow.Call(p.hwnd, 4)
	button := p.controls[p.rowIDs["claude:1"]]
	panelSetFocus.Call(button)
	enter := msg{hwnd: windows.HWND(button), message: 0x100, wParam: 0x0d}
	if !p.dialogMessage(&enter) {
		t.Fatal("Enter was not handled")
	}
	if action := nativeAction(t, actions); action != (PanelAction{Kind: "command", Key: "switch:claude:1"}) {
		t.Fatalf("Enter dispatched %+v", action)
	}
	nativePanelResult(t, tr)
	escape := msg{hwnd: windows.HWND(button), message: 0x100, wParam: 0x1b}
	if !p.dialogMessage(&escape) {
		t.Fatal("Esc was not handled")
	}
	if visible, _, _ := panelIsVisible.Call(p.hwnd); visible != 0 {
		t.Fatal("Esc left the panel open")
	}
}

// Out of reach: no Switch and neither switch is usable; Quit and Open
// dashboard still are.
func TestNativePanelOffline(t *testing.T) {
	actions := make(chan PanelAction, 8)
	tr := nativePanelFixture(t, func(action PanelAction) error { actions <- action; return nil })
	value := panelFixture()
	value.Offline, value.Note = true, "Engine unreachable"
	tr.SetPanel(value)
	p := tr.panel
	p.update()
	for _, row := range value.Rows {
		if nativeEnabled(p.controls[p.rowIDs[row.ID]]) {
			t.Fatalf("offline Switch for %s is usable", row.ID)
		}
	}
	for id, usable := range map[string]bool{"quit": true, "open": true, "auto": false, "autostart": false} {
		if nativeEnabled(p.controls[p.actionIDs[id]]) != usable {
			t.Fatalf("offline %s usable %v", id, !usable)
		}
	}
	p.command(p.actionIDs["auto"], 0)
	p.command(p.rowIDs["claude:1"], 0)
	select {
	case action := <-actions:
		t.Fatalf("offline panel dispatched %+v", action)
	default:
	}
}

// A live update keeps every control and the focus, and a new account gets
// its Switch in row order.
func TestNativePanelUpdateKeepsControls(t *testing.T) {
	tr := nativePanelFixture(t, func(PanelAction) error { return nil })
	p := tr.panel
	first, quit := p.controls[p.rowIDs["claude:1"]], p.controls[p.actionIDs["quit"]]
	panelSetFocus.Call(first)
	value := panelFixture()
	value.Rows[0].Cells[1].Share = "55%"
	value.Rows = append(value.Rows[:1], append([]PanelRow{{ID: "claude:9", Name: "#9 x", Cells: []PanelCell{{Absent: true}, {Absent: true}, {Absent: true}}}}, value.Rows[1:]...)...)
	tr.SetPanel(value)
	p.update()
	if p.controls[p.rowIDs["claude:1"]] != first || p.controls[p.actionIDs["quit"]] != quit {
		t.Fatal("a live update replaced a control")
	}
	if focus, _, _ := panelGetFocus.Call(); focus != first {
		t.Fatal("a live update moved the focus")
	}
	if !strings.Contains(panelControlText(first), "Weekly 55%") {
		t.Fatalf("summary not updated: %q", panelControlText(first))
	}
	next, _, _ := panelNextTab.Call(p.hwnd, first, 0)
	if next != p.controls[p.rowIDs["claude:9"]] {
		t.Fatal("the new row's Switch is not next in Tab order")
	}
}

// The popup is as large as its content, every control inside it; past the
// work area the list scrolls and the footer stays inside the popup.
func TestNativePanelFitsAndScrolls(t *testing.T) {
	tr := nativePanelFixture(t, func(PanelAction) error { return nil })
	p := tr.panel
	var client menuRect
	panelGetClient.Call(p.hwnd, uintptr(unsafe.Pointer(&client)))
	if int(client.right) != p.geometry.width || int(client.bottom) != p.geometry.height {
		t.Fatalf("client %+v for geometry %dx%d", client, p.geometry.width, p.geometry.height)
	}
	inside := func(control uintptr) {
		var r menuRect
		user32.NewProc("GetWindowRect").Call(control, uintptr(unsafe.Pointer(&r)))
		user32.NewProc("MapWindowPoints").Call(0, p.hwnd, uintptr(unsafe.Pointer(&r)), 2)
		if r.left < 0 || r.top < 0 || r.right > client.right || r.bottom > client.bottom {
			t.Fatalf("control %q at %+v outside %+v", panelControlText(control), r, client)
		}
	}
	for _, action := range p.panel.Actions {
		control := p.controls[p.actionIDs[action.ID]]
		inside(control)
		var r menuRect
		user32.NewProc("GetWindowRect").Call(control, uintptr(unsafe.Pointer(&r)))
		if int(r.right-r.left) < panelFooterWidth(action, p, p.geometry.metrics) {
			t.Fatalf("%s narrower than its content", action.ID)
		}
	}
	value := panelFixture()
	for i := range 60 {
		value.Rows = append(value.Rows, PanelRow{ID: fmt.Sprintf("claude:%d", i+10), Name: fmt.Sprintf("#%d x", i+10), Cells: []PanelCell{{Absent: true}, {Absent: true}, {Absent: true}}})
	}
	tr.SetPanel(value)
	p.update()
	panelGetClient.Call(p.hwnd, uintptr(unsafe.Pointer(&client)))
	if !p.geometry.scroll || p.geometry.list.height >= p.geometry.content {
		t.Fatalf("63 rows did not scroll: %+v", p.geometry)
	}
	for _, action := range p.panel.Actions {
		inside(p.controls[p.actionIDs[action.ID]])
	}
	last := p.controls[p.rowIDs["claude:69"]]
	panelSetFocus.Call(last)
	p.reveal(last)
	if p.scroll != p.geometry.maxScroll() {
		t.Fatalf("revealing the last row scrolled to %d of %d", p.scroll, p.geometry.maxScroll())
	}
	var r menuRect
	user32.NewProc("GetWindowRect").Call(last, uintptr(unsafe.Pointer(&r)))
	user32.NewProc("MapWindowPoints").Call(0, p.list, uintptr(unsafe.Pointer(&r)), 2)
	if r.top < 0 || int(r.bottom) > p.geometry.list.height {
		t.Fatalf("revealed Switch at %+v in a list %d tall", r, p.geometry.list.height)
	}
}

// Each theme paints the rows on the panel background with the accent and
// limit colours in their bars; high contrast uses the system's colours.
func TestNativePanelThemes(t *testing.T) {
	tr := nativePanelFixture(t, func(PanelAction) error { return nil })
	p := tr.panel
	for _, theme := range []menuTheme{menuLight, menuDark, menuHighContrast} {
		readMenuTheme = func() menuTheme { return theme }
		p.refreshTheme()
		if theme == menuHighContrast {
			if p.palette.accent != panelSystemColor(8) || p.palette.background != panelSystemColor(5) {
				t.Fatal("high contrast used custom colours")
			}
			continue
		}
		var client menuRect
		panelGetClient.Call(p.list, uintptr(unsafe.Pointer(&client)))
		dc, _, _ := menuGetDC.Call(0)
		mem, _, _ := menuGDI.NewProc("CreateCompatibleDC").Call(dc)
		bmp, _, _ := menuGDI.NewProc("CreateCompatibleBitmap").Call(dc, uintptr(client.right), uintptr(client.bottom))
		old, _, _ := menuSelectObject.Call(mem, bmp)
		p.paintList(mem)
		seen := map[uintptr]int{}
		pixel := menuGDI.NewProc("GetPixel")
		for y := int32(0); y < client.bottom; y++ {
			for x := int32(0); x < client.right; x++ {
				color, _, _ := pixel.Call(mem, uintptr(x), uintptr(y))
				seen[color]++
			}
		}
		menuSelectObject.Call(mem, old)
		menuDeleteObject.Call(bmp)
		menuGDI.NewProc("DeleteDC").Call(mem)
		menuReleaseDC.Call(0, dc)
		for name, color := range map[string]uintptr{"background": p.palette.background, "accent": p.palette.accent, "limit": p.palette.limit, "track": p.palette.track} {
			if seen[color] == 0 {
				t.Fatalf("theme %d: no %s pixels", theme, name)
			}
		}
	}
}

func TestPanelWorkAreaClampAndDPIScale(t *testing.T) {
	for _, work := range []menuRect{{0, 0, 640, 480}, {-1280, 0, 0, 720}} {
		for _, anchor := range []point{{0, 0}, {work.right, work.bottom}} {
			rect := clampPanelRect(anchor, work, 1240, 1160)
			if rect.left < work.left || rect.top < work.top || rect.right > work.right || rect.bottom > work.bottom {
				t.Fatalf("popup escaped work area: %+v", rect)
			}
		}
	}
	if (&windowsPanel{dpi: 192}).scale(620) != 1240 {
		t.Fatal("panel did not scale logical dimensions for device DPI")
	}
}
