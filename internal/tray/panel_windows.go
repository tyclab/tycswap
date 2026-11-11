//go:build windows

package tray

import (
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The daily view (DESIGN A84): the popup paints the title, the column
// labels, the note and the rules; a child viewport paints the rows and
// scrolls them. Every Switch, switch and footer command is a real owner-drawn
// BUTTON, so focus, Tab order and screen readers keep working.

var (
	panelSend          = user32.NewProc("SendMessageW")
	panelShow          = user32.NewProc("ShowWindow")
	panelMove          = user32.NewProc("MoveWindow")
	panelSetText       = user32.NewProc("SetWindowTextW")
	panelGetText       = user32.NewProc("GetWindowTextW")
	panelGetTextLength = user32.NewProc("GetWindowTextLengthW")
	panelEnable        = user32.NewProc("EnableWindow")
	panelIsVisible     = user32.NewProc("IsWindowVisible")
	panelIsChild       = user32.NewProc("IsChild")
	panelGetClient     = user32.NewProc("GetClientRect")
	panelSetFocus      = user32.NewProc("SetFocus")
	panelGetFocus      = user32.NewProc("GetFocus")
	panelInvalidate    = user32.NewProc("InvalidateRect")
	panelSetPos        = user32.NewProc("SetWindowPos")
	panelControlID     = user32.NewProc("GetDlgCtrlID")
	panelNextTab       = user32.NewProc("GetNextDlgTabItem")
)

const (
	panelChild         = 0x40000000
	panelVisible       = 0x10000000
	panelTabStop       = 0x10000
	panelBorder        = 0x800000
	panelClipChildren  = 0x02000000
	panelControlParent = 0x10000
	panelToolWindow    = 0x80
	panelNoActivate    = 0x08000000
	panelOwnerDraw     = 0xb
	panelActionBase    = 300
	panelRowBase       = 1000
)

type windowsPanel struct {
	tray     *windowsTray
	hwnd     uintptr // the popup
	list     uintptr // the rows' viewport
	panel    Panel
	geometry panelGeometry
	scroll   int
	anchor   *point
	picture  bool // drawn for a picture: outside every monitor, never activated

	dpi                            int
	theme                          menuTheme
	palette                        panelColors
	font, strong, small, titleFont uintptr

	controls  map[int]uintptr // control ID → window
	keys      map[int]string  // control ID → row ID or action ID
	rowIDs    map[string]int
	actionIDs map[string]int
	nextRow   int
	nextNamed int
}

func nativePanelStruct[T any](value uintptr) *T { return *(**T)(unsafe.Pointer(&value)) }

func panelWindowClass() string { return windowClass() + "Panel" }
func panelListClass() string   { return windowClass() + "PanelRows" }

// One callback each for the process: Windows has a fixed number of them.
var panelProcedures = sync.OnceValues(func() (uintptr, uintptr) {
	return windows.NewCallback(panelWndProc), windows.NewCallback(panelListProc)
})

func (t *windowsTray) SetPanel(value Panel) {
	value = clonePanel(value)
	t.mu.Lock()
	if t.panelSet && reflect.DeepEqual(t.panelData, value) {
		t.mu.Unlock()
		return
	}
	t.panelData, t.panelSet = value, true
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		pPostMessageW.Call(uintptr(hwnd), wmPanelUpdate, 0, 0)
	}
}

func (t *windowsTray) showPanel() bool {
	t.mu.Lock()
	available := t.panelSet
	t.mu.Unlock()
	if !available {
		return false
	}
	if t.panel == nil {
		if err := t.createPanel(); err != nil {
			_ = t.Notify("Tray overview unavailable", err.Error())
			return false
		}
	}
	p := t.panel
	p.anchor = nil
	p.update()
	panelShow.Call(p.hwnd, 5)
	pSetForegroundWindow.Call(p.hwnd)
	// The popup itself takes focus, so a stray Enter or Space switches
	// nothing; the first Tab reaches the first Switch.
	panelSetFocus.Call(p.hwnd)
	panelSend.Call(p.hwnd, 0x127, 3, 0) // WM_CHANGEUISTATE, UIS_INITIALIZE: focus cues follow the last input
	return true
}

func (t *windowsTray) createPanel() error { return t.newPanel(false) }

func (t *windowsTray) newPanel(picture bool) error {
	p := &windowsPanel{tray: t, dpi: 96, picture: picture, controls: map[int]uintptr{}, keys: map[int]string{},
		rowIDs: map[string]int{}, actionIDs: map[string]int{}, nextRow: panelRowBase, nextNamed: panelActionBase}
	t.panel = p
	instance, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512)
	main, list := panelProcedures()
	for _, class := range []struct {
		name string
		proc uintptr
	}{{panelWindowClass(), main}, {panelListClass(), list}} {
		wc := wndClassExW{cbSize: uint32(unsafe.Sizeof(wndClassExW{})), lpfnWndProc: class.proc, hInstance: windows.Handle(instance), hCursor: windows.Handle(cursor), lpszClassName: utf16z(class.name)}
		if atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 && err != windows.ERROR_CLASS_ALREADY_EXISTS {
			t.panel = nil
			return fmt.Errorf("overview window class: %w", err)
		}
	}
	ex, x := uintptr(panelControlParent|panelToolWindow), uintptr(0)
	if picture {
		ex |= panelNoActivate
		x = uintptr(panelOffscreen(620))
	}
	window, _, err := pCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(utf16z(panelWindowClass()))), 0,
		0x80000000|panelBorder|panelClipChildren, x, 0, 620, 480, uintptr(t.hwnd), 0, instance, 0)
	if window == 0 {
		t.panel = nil
		return fmt.Errorf("overview window: %w", err)
	}
	p.hwnd = window
	p.readDPI()
	p.list, _, err = pCreateWindowExW.Call(panelControlParent, uintptr(unsafe.Pointer(utf16z(panelListClass()))), 0,
		panelChild|panelVisible|panelClipChildren, 0, 0, 10, 10, window, 100, instance, 0)
	if p.list == 0 {
		p.close()
		t.panel = nil
		return fmt.Errorf("overview list: %w", err)
	}
	p.refreshTheme()
	p.update()
	return nil
}

func (p *windowsPanel) close() {
	if p.hwnd != 0 {
		pDestroyWindow.Call(p.hwnd)
		p.hwnd, p.list = 0, 0
	}
	p.freeTheme()
}

func (p *windowsPanel) hide() { panelShow.Call(p.hwnd, 0) }

// dialogMessage gives the popup a dialog's keyboard: Tab and Shift+Tab walk
// the controls, Space or Enter press the focused one, Esc closes.
func (p *windowsPanel) dialogMessage(message *msg) bool {
	if visible, _, _ := panelIsVisible.Call(p.hwnd); visible == 0 {
		return false
	}
	if child, _, _ := panelIsChild.Call(p.hwnd, uintptr(message.hwnd)); uintptr(message.hwnd) != p.hwnd && child == 0 {
		return false
	}
	if message.message == 0x100 {
		focus, _, _ := panelGetFocus.Call()
		switch message.wParam {
		case 0x1b:
			p.hide()
			return true
		case 0x0d:
			if inside, _, _ := panelIsChild.Call(p.hwnd, focus); inside != 0 {
				id, _, _ := panelControlID.Call(focus)
				p.command(int(id), 0)
			}
			return true
		case 0x09:
			if focus == 0 || focus == p.hwnd {
				shift, _, _ := user32.NewProc("GetKeyState").Call(0x10)
				if next, _, _ := panelNextTab.Call(p.hwnd, 0, uintptr(boolValue(int16(shift) < 0))); next != 0 {
					panelSetFocus.Call(next)
					p.reveal(next)
				}
				return true
			}
		}
	}
	handled, _, _ := user32.NewProc("IsDialogMessageW").Call(p.hwnd, uintptr(unsafe.Pointer(message)))
	if handled != 0 && message.message == 0x100 {
		focus, _, _ := panelGetFocus.Call()
		p.reveal(focus)
	}
	return handled != 0
}

func (p *windowsPanel) setText(control uintptr, value string) {
	if control != 0 && panelControlText(control) != value {
		panelSetText.Call(control, uintptr(unsafe.Pointer(utf16z(value))))
	}
}

func panelControlText(control uintptr) string {
	length, _, _ := panelGetTextLength.Call(control)
	text := make([]uint16, min(int(length), 65536)+1)
	panelGetText.Call(control, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)))
	return windows.UTF16ToString(text)
}

func boolValue(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (p *windowsPanel) update() {
	p.tray.mu.Lock()
	value := p.tray.panelData
	p.tray.mu.Unlock()
	p.panel = value
	panelSetText.Call(p.hwnd, uintptr(unsafe.Pointer(utf16z(value.Title))))
	p.syncRows()
	p.syncActions()
	p.position()
}

// release hands the focus to the popup before control is disabled or
// destroyed: Windows would leave it nowhere, and then the popup gets no
// Tab or Esc (a Switch that succeeds becomes "In use" and is disabled).
func (p *windowsPanel) release(control uintptr) {
	if focus, _, _ := panelGetFocus.Call(); focus == control {
		panelSetFocus.Call(p.hwnd)
	}
}

func (p *windowsPanel) button(parent uintptr, id int) uintptr {
	instance, _, _ := pGetModuleHandleW.Call(0)
	control, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16z("BUTTON"))), 0, panelChild|panelVisible|panelTabStop|panelOwnerDraw,
		0, 0, 10, 10, parent, uintptr(id), instance, 0)
	return control
}

// syncControls keeps one control per key across updates, so a live update never
// moves focus or changes a control's identity, and orders them for Tab.
func (p *windowsPanel) syncControls(parent, after uintptr, keys []string, ids map[string]int, next *int, render func(i int) (string, bool)) {
	keep := map[int]bool{}
	for i, key := range keys {
		id, ok := ids[key]
		if !ok {
			id, *next = *next, *next+1
			ids[key], p.keys[id] = id, key
		}
		control := p.controls[id]
		if control == 0 {
			if control = p.button(parent, id); control == 0 {
				continue
			}
			p.controls[id] = control
		}
		keep[id] = true
		text, enabled := render(i)
		p.setText(control, text)
		if !enabled {
			p.release(control)
		}
		panelEnable.Call(control, uintptr(boolValue(enabled)))
		panelSetPos.Call(control, after, 0, 0, 0, 0, 0x1|0x2|0x10)
		after = control
		panelInvalidate.Call(control, 0, 1)
	}
	for key, id := range ids {
		if !keep[id] {
			p.release(p.controls[id])
			pDestroyWindow.Call(p.controls[id])
			delete(p.controls, id)
			delete(p.keys, id)
			delete(ids, key)
		}
	}
}

func (p *windowsPanel) syncRows() {
	keys := make([]string, len(p.panel.Rows))
	for i, row := range p.panel.Rows {
		keys[i] = row.ID
	}
	p.syncControls(p.list, 0, keys, p.rowIDs, &p.nextRow, func(i int) (string, bool) {
		row := p.panel.Rows[i]
		return panelSummary(p.panel.Columns, row), panelSwitchEnabled(p.panel, row)
	})
}

func (p *windowsPanel) syncActions() {
	keys := make([]string, len(p.panel.Actions))
	for i, action := range p.panel.Actions {
		keys[i] = action.ID
	}
	p.syncControls(p.hwnd, p.list, keys, p.actionIDs, &p.nextNamed, func(i int) (string, bool) {
		action := p.panel.Actions[i]
		text := action.Title
		if action.Kind == KindToggle {
			text = panelToggleText(action)
		}
		return text, panelCommandEnabled(p.panel, action)
	})
}

func (p *windowsPanel) rowFor(id int) (int, bool) {
	if id < panelRowBase {
		return 0, false
	}
	for i, row := range p.panel.Rows {
		if row.ID == p.keys[id] {
			return i, true
		}
	}
	return 0, false
}

func (p *windowsPanel) actionFor(id int) (Item, bool) {
	if id >= panelRowBase {
		return Item{}, false
	}
	for _, action := range p.panel.Actions {
		if key, ok := p.keys[id]; ok && action.ID == key {
			return action, true
		}
	}
	return Item{}, false
}

func (p *windowsPanel) command(id int, notification int) {
	if notification != 0 {
		return
	}
	if id == 2 {
		p.hide()
		return
	}
	if i, ok := p.rowFor(id); ok {
		if row := p.panel.Rows[i]; panelSwitchEnabled(p.panel, row) {
			p.dispatch(PanelAction{Kind: "command", Key: "switch:" + row.ID})
		}
		return
	}
	if action, ok := p.actionFor(id); ok && panelCommandEnabled(p.panel, action) {
		p.dispatch(PanelAction{Kind: "command", Key: action.ID})
	}
}

func (p *windowsPanel) dispatch(action PanelAction) {
	if action.Key == "open" {
		p.hide()
	}
	// Off the UI thread: a switch waits on locks and the engine. Its
	// outcome comes back as the next panel's note.
	go func() {
		if p.tray.opts.OnPanelAction != nil {
			p.tray.opts.OnPanelAction(action)
		} else if p.tray.opts.OnClick != nil {
			p.tray.opts.OnClick(action.Key)
		}
	}()
}

// reveal scrolls a focused Switch into view.
func (p *windowsPanel) reveal(focus uintptr) {
	if inside, _, _ := panelIsChild.Call(p.list, focus); focus == 0 || inside == 0 {
		return
	}
	id, _, _ := panelControlID.Call(focus)
	if i, ok := p.rowFor(int(id)); ok {
		p.scrollTo(p.geometry.scrollToShow(i, p.scroll))
	}
}

func panelWndProc(hwnd windows.HWND, message uint32, wParam, lParam uintptr) uintptr {
	p := currentPanel()
	if p == nil || p.hwnd != uintptr(hwnd) {
		result, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
		return result
	}
	switch message {
	case wmClose:
		p.hide()
		return 0
	case 0x0006:
		if wParam&0xffff == 0 && !p.picture {
			p.hide()
		}
		return 0
	case wmCommand:
		p.command(int(wParam&0xffff), int(wParam>>16))
		return 0
	case 0x02e0:
		p.dpi = int(wParam & 0xffff)
		p.refreshTheme()
		return 0
	case 0x001a, 0x0015, 0x031a:
		p.refreshTheme()
		return 0
	case 0x000f:
		var paint panelPaint
		dc, _, _ := user32.NewProc("BeginPaint").Call(p.hwnd, uintptr(unsafe.Pointer(&paint)))
		p.paint(dc)
		user32.NewProc("EndPaint").Call(p.hwnd, uintptr(unsafe.Pointer(&paint)))
		return 0
	case 0x0318:
		p.paint(wParam)
		return 0
	case 0x0014:
		return 1
	case wmDrawItem:
		if lParam != 0 && p.drawButton(nativePanelStruct[drawMenuItem](lParam)) {
			return 1
		}
	case 0x020a:
		p.wheel(wParam)
		return 0
	}
	result, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return result
}

func panelListProc(hwnd windows.HWND, message uint32, wParam, lParam uintptr) uintptr {
	p := currentPanel()
	if p == nil || p.list != uintptr(hwnd) {
		result, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
		return result
	}
	switch message {
	case 0x000f:
		var paint panelPaint
		dc, _, _ := user32.NewProc("BeginPaint").Call(p.list, uintptr(unsafe.Pointer(&paint)))
		p.paintList(dc)
		user32.NewProc("EndPaint").Call(p.list, uintptr(unsafe.Pointer(&paint)))
		return 0
	case 0x0318:
		p.paintList(wParam)
		return 0
	case 0x0014:
		return 1
	case wmDrawItem:
		if lParam != 0 && p.drawButton(nativePanelStruct[drawMenuItem](lParam)) {
			return 1
		}
	case wmCommand:
		p.command(int(wParam&0xffff), int(wParam>>16))
		return 0
	case 0x0115:
		p.vscroll(wParam)
		return 0
	case 0x020a:
		p.wheel(wParam)
		return 0
	}
	result, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	if p.theme == menuDark {
		switch message {
		case 0x85, 0xa0, 0xa1, 0xa2, 0x5:
			p.paintScrollbars(p.list)
		case 0x317:
			if lParam&2 != 0 {
				p.paintScrollbarsOnDC(p.list, wParam)
			}
		}
	}
	return result
}
