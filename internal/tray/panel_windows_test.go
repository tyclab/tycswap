package tray

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
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

func nativeSelectPanelRow(hwnd uintptr, index int) {
	clear := panelListItem{stateMask: 3}
	panelSend.Call(hwnd, panelListBase+43, ^uintptr(0), uintptr(unsafe.Pointer(&clear)))
	state := panelListItem{state: 3, stateMask: 3}
	panelSend.Call(hwnd, panelListBase+43, uintptr(index), uintptr(unsafe.Pointer(&state)))
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

func TestNativePanelFocusDraftScopeAndErrors(t *testing.T) {
	actions := make(chan PanelAction, 8)
	tr := nativePanelFixture(t, func(action PanelAction) error {
		actions <- action
		if action.Kind == "setting-set" && action.Value == "94" {
			return errors.New("Synthetic setting rejected")
		}
		return nil
	})
	p := tr.panel
	nativeSelectPanelRow(p.controls[panelAccounts], 1)
	if p.model.rowID != "claude:2" {
		t.Fatal("native table did not focus selected row")
	}
	select {
	case action := <-actions:
		t.Fatalf("row focus executed action %+v", action)
	default:
	}
	p.command(panelSettingsTab, 0)
	panelSend.Call(p.controls[panelScopes], panelComboSelect, 1, 0)
	p.command(panelScopes, 1)
	nativeSelectPanelRow(p.controls[panelFields], 1)
	if p.model.fieldKey != "model" {
		t.Fatal("native setting table did not select group model")
	}
	p.command(panelSave, 0)
	select {
	case action := <-actions:
		t.Fatalf("read-only model sent action %+v", action)
	default:
	}
	nativeSelectPanelRow(p.controls[panelFields], 0)
	p.setText(panelEdit, "90.0")
	panelSend.Call(p.controls[panelEdit], 0xb1, 4, 4)
	button := p.controls[p.actionControls["quit"]]
	value := clonePanel(tr.panelData)
	value.Status = "live usage update"
	tr.SetPanel(value)
	p.update()
	var start, end uint32
	panelSend.Call(p.controls[panelEdit], 0xb0, uintptr(unsafe.Pointer(&start)), uintptr(unsafe.Pointer(&end)))
	if p.model.value() != "90.0" || panelControlText(p.controls[panelEdit]) != "90.0" || start != 4 || end != 4 || p.controls[p.actionControls["quit"]] != button {
		t.Fatal("live update changed draft, caret or command control identity")
	}
	p.command(panelSave, 0)
	action := <-actions
	if action.Kind != "setting-set" || action.Scope != "fable" || action.Key != "threshold" || action.Value != "90.0" {
		t.Fatalf("wrong scoped save: %+v", action)
	}
	nativePanelResult(t, tr)
	value.Settings[1].Settings[0].Value = "90"
	tr.SetPanel(value)
	p.update()
	if p.model.value() != "90" || len(p.model.drafts) != 0 {
		t.Fatal("server acknowledgment did not clear canonical draft")
	}
	p.setText(panelEdit, "94")
	p.command(panelSave, 0)
	<-actions
	nativePanelResult(t, tr)
	if p.model.value() != "94" || !strings.Contains(panelControlText(p.controls[227]), "Synthetic setting rejected") {
		t.Fatal("failed save lost attempted draft or readable error")
	}
	note := panelListNotification{header: panelHeader{hwnd: p.controls[panelAccounts], code: 0xffffff94}, subItem: 1}
	p.notification(uintptr(unsafe.Pointer(&note)))
	action = <-actions
	if action.Kind != "sort" || action.Key != "weekly" {
		t.Fatalf("wrong native sort action: %+v", action)
	}
	nativePanelResult(t, tr)
	value.Actions = []Item{{ID: "open", Title: "Open dashboard"}, {ID: "update", Title: "Check updates"}, {ID: "quit", Title: "Quit"}}
	panelSetFocus.Call(button)
	tr.SetPanel(value)
	p.update()
	if p.controls[p.actionControls["quit"]] != button {
		t.Fatal("inserting another action changed the existing command control")
	}
	focus, _, _ := user32.NewProc("GetFocus").Call()
	if focus != button {
		t.Fatal("live update moved keyboard focus off retained command")
	}
	p.command(p.actionControls["quit"], 0)
	action = <-actions
	if action.Key != "quit" {
		t.Fatal("a retained command button changed its action")
	}
	nativePanelResult(t, tr)
	value.Offline = true
	tr.SetPanel(value)
	p.update()
	p.command(panelSave, 0)
	select {
	case action := <-actions:
		t.Fatalf("offline editor sent mutation %+v", action)
	default:
	}
	if p.model.value() != "94" {
		t.Fatal("offline update erased unsaved draft")
	}
}

func TestNativePanelActiveAccentAndDeviceThemes(t *testing.T) {
	tr := nativePanelFixture(t, func(PanelAction) error { return nil })
	p := tr.panel
	dc, _, _ := menuGetDC.Call(0)
	defer menuReleaseDC.Call(0, dc)
	mem, _, _ := menuGDI.NewProc("CreateCompatibleDC").Call(dc)
	defer menuGDI.NewProc("DeleteDC").Call(mem)
	bmp, _, _ := menuGDI.NewProc("CreateCompatibleBitmap").Call(dc, 400, 100)
	defer menuDeleteObject.Call(bmp)
	old, _, _ := menuSelectObject.Call(mem, bmp)
	defer menuSelectObject.Call(mem, old)
	for _, theme := range []menuTheme{menuLight, menuDark, menuHighContrast} {
		readMenuTheme = func() menuTheme { return theme }
		p.refreshTheme()
		draw := panelListDraw{custom: panelCustomDraw{header: panelHeader{hwnd: p.controls[panelAccounts]}, stage: 0x30001, dc: mem, item: 0}}
		if p.drawList(&draw) != 4 {
			t.Fatal("active account cell did not draw")
		}
		if theme == menuHighContrast {
			if p.palette.accent != panelSystemColor(8) {
				t.Fatal("high contrast used custom branding color")
			}
			continue
		}
		pixels := 0
		pixel := menuGDI.NewProc("GetPixel")
		for y := uintptr(0); y < 100; y++ {
			for x := uintptr(0); x < 400; x++ {
				color, _, _ := pixel.Call(mem, x, y)
				if color == themeAccent(theme) {
					pixels++
				}
			}
		}
		if pixels == 0 {
			t.Fatalf("theme %d active account has no shared accent pixels", theme)
		}
		for index := 0; index < 2; index++ {
			draw.custom.stage, draw.custom.item = 0x10001, uintptr(index)
			p.drawList(&draw)
			bounds := menuRect{}
			panelSend.Call(p.controls[panelAccounts], panelListBase+14, uintptr(index), uintptr(unsafe.Pointer(&bounds)))
			state, _, _ := panelSend.Call(p.controls[panelAccounts], panelListBase+44, uintptr(index), 2)
			background := p.palette.background
			if state&2 != 0 {
				background = p.palette.selection
			}
			for _, x := range []uintptr{80, 220, 350} {
				color, _, _ := pixel.Call(mem, x, uintptr(bounds.top+1))
				if color != background {
					t.Fatalf("theme %d row %d cell at %d background %#x want %#x", theme, index, x, color, background)
				}
			}
		}
		header, _, _ := panelSend.Call(p.controls[panelAccounts], panelListBase+31, 0, 0)
		p.paintHeader(header, mem)
		color, _, _ := pixel.Call(mem, 350, 1)
		if color != p.palette.header {
			t.Fatalf("theme %d header trailing area has native light background", theme)
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

func TestNativePanelWrapsAvailableUpdateAndToggleActions(t *testing.T) {
	tr := nativePanelFixture(t, func(PanelAction) error { return nil })
	p := tr.panel
	value := clonePanel(tr.panelData)
	value.Actions = []Item{{ID: "open", Title: "Open dashboard"}, {ID: "update", Title: "Check for updates"},
		{ID: "install-update", Title: "Update tycswap", Kind: KindUpdate}, {ID: "claude-code", Title: "Update Claude", Kind: KindUpdate},
		{ID: "quit", Title: "Quit"}, {ID: "auto", Title: "Auto-switch", Kind: KindToggle}, {ID: "autostart", Title: "Start at login", Kind: KindToggle}}
	tr.SetPanel(value)
	p.update()
	p.model.settings = true
	p.layout()
	var window, client menuRect
	user32.NewProc("GetWindowRect").Call(p.hwnd, uintptr(unsafe.Pointer(&window)))
	panelGetClient.Call(p.hwnd, uintptr(unsafe.Pointer(&client)))
	tops := make(map[int32]bool)
	for _, action := range value.Actions[1:] {
		var bounds menuRect
		control := p.controls[p.actionControls[action.ID]]
		user32.NewProc("GetWindowRect").Call(control, uintptr(unsafe.Pointer(&bounds)))
		if bounds.left < window.left || bounds.top < window.top || bounds.right > window.right || bounds.bottom > window.bottom {
			t.Fatalf("action %s outside popup bounds: %+v", action.ID, bounds)
		}
		if bounds.right-bounds.left < int32(p.scale(90)) {
			t.Fatalf("action %s crushed below readable width", action.ID)
		}
		tops[bounds.top] = true
	}
	if len(tops) < 2 {
		t.Fatal("actions did not wrap into multiple readable rows")
	}
}

func TestNativePanelRefreshPreservesScrollAndColumnWidth(t *testing.T) {
	tr := nativePanelFixture(t, func(PanelAction) error { return nil })
	p := tr.panel
	value := clonePanel(tr.panelData)
	value.Rows = nil
	for i := 0; i < 50; i++ {
		value.Rows = append(value.Rows, PanelRow{ID: fmt.Sprintf("row:%d", i), Title: fmt.Sprintf("Account %d", i), Active: i == 0, Cells: []string{fmt.Sprintf("Account %d", i), "10%"}})
	}
	tr.SetPanel(value)
	p.update()
	list := p.controls[panelAccounts]
	panelSend.Call(list, panelListBase+30, 0, 222)
	panelSend.Call(list, panelListBase+20, 0, 300)
	top, _, _ := panelSend.Call(list, panelListBase+39, 0, 0)
	if top == 0 {
		t.Fatal("fixture did not scroll")
	}
	for _, statusOnly := range []bool{true, false} {
		value.Status = "refreshed status"
		if !statusOnly {
			value.Rows[15].Cells[1] = "73%"
		}
		tr.SetPanel(value)
		p.update()
		current, _, _ := panelSend.Call(list, panelListBase+39, 0, 0)
		width, _, _ := panelSend.Call(list, panelListBase+29, 0, 0)
		if current != top || width != 222 {
			t.Fatalf("statusOnly=%t refresh reset view: top=%d width=%d", statusOnly, current, width)
		}
	}
}

func TestNativePanelOfflinePreservesLocalDeviceCommands(t *testing.T) {
	actions := make(chan PanelAction, 8)
	tr := nativePanelFixture(t, func(action PanelAction) error { actions <- action; return nil })
	value := clonePanel(tr.panelData)
	value.Offline = true
	value.Actions = []Item{{ID: "open", Title: "Open dashboard"}, {ID: "update", Title: "Check for updates"},
		{ID: "install-update", Title: "Update tycswap", Kind: KindUpdate}, {ID: "claude-code", Title: "Update Claude", Kind: KindUpdate},
		{ID: "autostart", Title: "Start at login", Kind: KindToggle}, {ID: "auto", Title: "Auto-switch", Kind: KindToggle}, {ID: "quit", Title: "Quit"}}
	tr.SetPanel(value)
	tr.panel.update()
	p := tr.panel
	for _, key := range []string{"update", "install-update", "claude-code", "autostart", "quit"} {
		control := p.controls[p.actionControls[key]]
		enabled, _, _ := user32.NewProc("IsWindowEnabled").Call(control)
		if enabled == 0 {
			t.Fatalf("offline local command %s is disabled", key)
		}
		p.command(p.actionControls[key], 0)
		select {
		case action := <-actions:
			if action.Key != key {
				t.Fatalf("local command changed from %s", key)
			}
		case <-time.After(time.Second):
			t.Fatalf("local command %s did not execute", key)
		}
		nativePanelResult(t, tr)
	}
	p.command(p.actionControls["auto"], 0)
	p.command(panelUse, 0)
	p.command(panelSave, 0)
	select {
	case action := <-actions:
		t.Fatalf("offline remote mutation sent %+v", action)
	default:
	}
}
