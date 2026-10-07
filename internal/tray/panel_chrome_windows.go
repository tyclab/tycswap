//go:build windows

package tray

import (
	"unsafe"
)

type panelScrollInfo struct {
	size                        uint32
	rect                        menuRect
	line, top, bottom, reserved int32
	states                      [6]uint32
}
type panelComboInfo struct {
	size              uint32
	item, button      menuRect
	state             uint32
	combo, edit, list uintptr
}

func currentPanel() *windowsPanel {
	winMu.Lock()
	defer winMu.Unlock()
	if winTray != nil {
		return winTray.panel
	}
	return nil
}

func panelHeaderSubclass(hwnd uintptr, message uint32, wParam, lParam, id, ref uintptr) uintptr {
	p := currentPanel()
	if p != nil {
		if message == 0x14 {
			return 1
		}
		if message == 0xf {
			var paint panelPaint
			dc, _, _ := user32.NewProc("BeginPaint").Call(hwnd, uintptr(unsafe.Pointer(&paint)))
			p.paintHeader(hwnd, dc)
			user32.NewProc("EndPaint").Call(hwnd, uintptr(unsafe.Pointer(&paint)))
			return 0
		}
		if message == 0x318 || message == 0x317 {
			p.paintHeader(hwnd, wParam)
			return 0
		}
	}
	result, _, _ := panelDefSubclass.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func (p *windowsPanel) paintHeader(hwnd, dc uintptr) {
	if dc == 0 {
		return
	}
	var rect menuRect
	panelGetClient.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	menuFillRect.Call(dc, uintptr(unsafe.Pointer(&rect)), p.headerBrush)
	count, _, _ := panelSend.Call(hwnd, 0x1200, 0, 0)
	for i := uintptr(0); i < count; i++ {
		var bounds menuRect
		panelSend.Call(hwnd, 0x1207, i, uintptr(unsafe.Pointer(&bounds)))
		p.drawHeader(&panelCustomDraw{header: panelHeader{hwnd: hwnd}, stage: 0x10001, dc: dc, rect: bounds, item: i})
	}
}

func panelComboSubclass(hwnd uintptr, message uint32, wParam, lParam, id, ref uintptr) uintptr {
	result, _, _ := panelDefSubclass.Call(hwnd, uintptr(message), wParam, lParam)
	if p := currentPanel(); p != nil && p.theme != menuHighContrast {
		if message == 0xf {
			dc, _, _ := menuGetDC.Call(hwnd)
			p.paintComboArrow(hwnd, dc)
			menuReleaseDC.Call(hwnd, dc)
		} else if message == 0x318 || message == 0x317 {
			p.paintComboArrow(hwnd, wParam)
		}
	}
	return result
}

func (p *windowsPanel) paintComboArrow(hwnd, dc uintptr) {
	info := panelComboInfo{size: uint32(unsafe.Sizeof(panelComboInfo{}))}
	if ok, _, _ := user32.NewProc("GetComboBoxInfo").Call(hwnd, uintptr(unsafe.Pointer(&info))); ok == 0 || dc == 0 {
		return
	}
	menuFillRect.Call(dc, uintptr(unsafe.Pointer(&info.button)), p.headerBrush)
	menuSelectObject.Call(dc, p.font)
	menuBkMode.Call(dc, 1)
	menuTextColor.Call(dc, p.palette.muted)
	menuDrawText.Call(dc, uintptr(unsafe.Pointer(utf16z("▾"))), ^uintptr(0), uintptr(unsafe.Pointer(&info.button)), dtSingleLine|dtVCenter|dtNoPrefix|1)
	var rect menuRect
	panelGetClient.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	border, _, _ := menuGDI.NewProc("CreateSolidBrush").Call(p.palette.line)
	user32.NewProc("FrameRect").Call(dc, uintptr(unsafe.Pointer(&rect)), border)
	menuDeleteObject.Call(border)
}

func (p *windowsPanel) paintScrollbars(hwnd uintptr) {
	dc, _, _ := user32.NewProc("GetWindowDC").Call(hwnd)
	defer menuReleaseDC.Call(hwnd, dc)
	p.paintScrollbarsOnDC(hwnd, dc)
}

func (p *windowsPanel) paintScrollbarsOnDC(hwnd, dc uintptr) {
	if dc == 0 {
		return
	}
	var window menuRect
	user32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&window)))
	for _, object := range []uintptr{^uintptr(4), ^uintptr(5)} {
		info := panelScrollInfo{size: uint32(unsafe.Sizeof(panelScrollInfo{}))}
		ok, _, _ := user32.NewProc("GetScrollBarInfo").Call(hwnd, object, uintptr(unsafe.Pointer(&info)))
		if ok == 0 || info.states[0]&(0x8000|0x10000) != 0 {
			continue
		}
		rect := info.rect
		rect.left -= window.left
		rect.right -= window.left
		rect.top -= window.top
		rect.bottom -= window.top
		menuFillRect.Call(dc, uintptr(unsafe.Pointer(&rect)), p.headerBrush)
		thumb := rect
		vertical := object == ^uintptr(4)
		if vertical {
			thumb.top += info.top
			thumb.bottom = rect.top + info.bottom
			thumb.left += 2
			thumb.right -= 2
		} else {
			thumb.left += info.top
			thumb.right = rect.left + info.bottom
			thumb.top += 2
			thumb.bottom -= 2
		}
		brush, _, _ := menuGDI.NewProc("CreateSolidBrush").Call(0x606060)
		menuFillRect.Call(dc, uintptr(unsafe.Pointer(&thumb)), brush)
		menuDeleteObject.Call(brush)
		menuSelectObject.Call(dc, p.font)
		menuBkMode.Call(dc, 1)
		menuTextColor.Call(dc, p.palette.muted)
		first, last := rect, rect
		up, down := "‹", "›"
		if vertical {
			first.bottom = first.top + info.line
			last.top = last.bottom - info.line
			up, down = "▴", "▾"
		} else {
			first.right = first.left + info.line
			last.left = last.right - info.line
		}
		menuDrawText.Call(dc, uintptr(unsafe.Pointer(utf16z(up))), ^uintptr(0), uintptr(unsafe.Pointer(&first)), dtSingleLine|dtVCenter|dtNoPrefix|1)
		menuDrawText.Call(dc, uintptr(unsafe.Pointer(utf16z(down))), ^uintptr(0), uintptr(unsafe.Pointer(&last)), dtSingleLine|dtVCenter|dtNoPrefix|1)
	}
}
