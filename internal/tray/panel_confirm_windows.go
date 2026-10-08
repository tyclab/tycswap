//go:build windows

package tray

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	panelConfirmTitle   = 400
	panelConfirmCancel  = 401
	panelConfirmTarget  = 410
	panelConfirmDismiss = wmApp + 20
)

type panelConfirm struct {
	hwnd    uintptr
	targets []Target
}

var panelConfirmProcedure uintptr

func panelConfirmClass() string { return windowClass() + "Confirm" }

// confirmSwitch opens a small modal window listing every destination for the
// activated account. Unusable destinations stay listed, disabled, with why.
func (p *windowsPanel) confirmSwitch(index int) {
	if index < 0 {
		selected, _, _ := panelSend.Call(p.controls[panelAccounts], panelListBase+12, ^uintptr(0), 2)
		index = int(int32(selected))
	}
	if p.confirm != nil || index < 0 || index >= len(p.model.panel.Rows) || len(p.model.panel.Rows[index].Targets) == 0 {
		return
	}
	row := p.model.panel.Rows[index]
	instance, _, _ := pGetModuleHandleW.Call(0)
	if panelConfirmProcedure == 0 {
		panelConfirmProcedure = windows.NewCallback(panelConfirmProc)
	}
	cursor, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512)
	name := utf16z(panelConfirmClass())
	class := wndClassExW{cbSize: uint32(unsafe.Sizeof(wndClassExW{})), lpfnWndProc: panelConfirmProcedure, hInstance: windows.Handle(instance), hCursor: windows.Handle(cursor), lpszClassName: name}
	if atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 && err != windows.ERROR_CLASS_ALREADY_EXISTS {
		return
	}
	title := "Switch to " + row.Title + "?"
	pad, buttonHeight := p.scale(16), p.scale(30)
	width := min(max(p.scale(340), p.textWidth(p.bold, title)+2*pad), p.scale(560))
	height := p.scale(48) + len(row.Targets)*(buttonHeight+p.scale(6)) + p.scale(10+28) + pad
	var owner menuRect
	user32.NewProc("GetWindowRect").Call(p.hwnd, uintptr(unsafe.Pointer(&owner)))
	x := int(owner.left+owner.right)/2 - width/2
	y := int(owner.top+owner.bottom)/2 - height/2
	window, _, _ := pCreateWindowExW.Call(0x10000|0x80, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(utf16z("Switch account"))),
		0x80000000|panelBorder, uintptr(x), uintptr(y), uintptr(width), uintptr(height), p.hwnd, 0, instance, 0)
	if window == 0 {
		return
	}
	p.confirm = &panelConfirm{hwnd: window, targets: row.Targets}
	child := func(id int, class, text string, style uintptr, x, y, w, h int, font uintptr) uintptr {
		control, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16z(class))), uintptr(unsafe.Pointer(utf16z(text))),
			panelChild|panelVisible|style, uintptr(x), uintptr(y), uintptr(w), uintptr(h), window, uintptr(id), instance, 0)
		panelSend.Call(control, panelSetFont, font, 1)
		return control
	}
	child(panelConfirmTitle, "STATIC", title, 0x80, pad, pad, width-2*pad, p.scale(20), p.bold)
	focus := uintptr(0)
	top := p.scale(48)
	for i, target := range row.Targets {
		label := target.Label
		reason := target.Reason
		if p.model.panel.Offline {
			reason = "Reconnect before switching accounts."
		}
		disabled := target.Disabled || p.model.panel.Offline
		if disabled && reason != "" {
			label += " (" + reason + ")"
		}
		button := child(panelConfirmTarget+i, "BUTTON", label, panelTabStop|0xb, pad, top, width-2*pad, buttonHeight, p.font)
		panelEnable.Call(button, uintptr(boolValue(!disabled)))
		if !disabled && focus == 0 {
			focus = button
		}
		top += buttonHeight + p.scale(6)
	}
	cancel := child(panelConfirmCancel, "BUTTON", "Cancel", panelTabStop|0xb, width-pad-p.scale(96), top+p.scale(10), p.scale(96), p.scale(28), p.font)
	if focus == 0 {
		focus = cancel
	}
	panelShow.Call(window, 5)
	pSetForegroundWindow.Call(window)
	panelEnable.Call(p.hwnd, 0)
	panelSetFocus.Call(focus)
}

func (p *windowsPanel) closeConfirm() {
	if p.confirm == nil {
		return
	}
	window := p.confirm.hwnd
	p.confirm = nil
	panelEnable.Call(p.hwnd, 1)
	pDestroyWindow.Call(window)
}

func (p *windowsPanel) confirmCommand(id int) {
	if id == panelConfirmCancel || id == 2 {
		p.closeConfirm()
		return
	}
	index := id - panelConfirmTarget
	if p.confirm == nil || index < 0 || index >= len(p.confirm.targets) {
		return
	}
	target := p.confirm.targets[index]
	if target.Disabled || p.model.panel.Offline {
		return
	}
	p.closeConfirm()
	p.dispatch(PanelAction{Kind: "command", Key: target.ID})
}

func (p *windowsPanel) confirmMessage(message *msg) bool {
	window := p.confirm.hwnd
	child, _, _ := user32.NewProc("IsChild").Call(window, uintptr(message.hwnd))
	if uintptr(message.hwnd) != window && child == 0 {
		return false
	}
	if message.message == 0x100 && message.wParam == 0x1b {
		p.closeConfirm()
		return true
	}
	if message.message == 0x100 && message.wParam == 0x0d {
		focus, _, _ := user32.NewProc("GetFocus").Call()
		if inside, _, _ := user32.NewProc("IsChild").Call(window, focus); inside != 0 {
			panelSend.Call(focus, 0xf5, 0, 0)
		}
		return true
	}
	handled, _, _ := user32.NewProc("IsDialogMessageW").Call(window, uintptr(unsafe.Pointer(message)))
	return handled != 0
}

func panelConfirmProc(hwnd windows.HWND, message uint32, wParam, lParam uintptr) uintptr {
	winMu.Lock()
	t := winTray
	winMu.Unlock()
	if t == nil || t.panel == nil || t.panel.confirm == nil || t.panel.confirm.hwnd != uintptr(hwnd) {
		result, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
		return result
	}
	p := t.panel
	switch message {
	case wmCommand:
		p.confirmCommand(int(wParam & 0xffff))
		return 0
	case wmDrawItem:
		if lParam != 0 && p.drawButton(nativePanelStruct[drawMenuItem](lParam)) {
			return 1
		}
	case 0x0133, 0x0134, 0x0138:
		return p.colorControl(wParam, lParam)
	case 0x0014:
		var rect menuRect
		panelGetClient.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
		menuFillRect.Call(wParam, uintptr(unsafe.Pointer(&rect)), p.background)
		return 1
	case 0x0006:
		// Clicking outside dismisses the confirmation and the panel, like the
		// panel alone; closing hands activation back to the panel first.
		if wParam&0xffff == 0 && lParam != p.hwnd {
			pPostMessageW.Call(uintptr(hwnd), panelConfirmDismiss, 0, 0)
		}
	case panelConfirmDismiss:
		p.closeConfirm()
		panelShow.Call(p.hwnd, 0)
		return 0
	case wmClose:
		p.closeConfirm()
		return 0
	}
	result, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return result
}
