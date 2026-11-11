//go:build windows

package tray

import (
	"unsafe"
)

type panelScrollBarInfo struct {
	size                        uint32
	rect                        menuRect
	line, top, bottom, reserved int32
	states                      [6]uint32
}

func currentPanel() *windowsPanel {
	winMu.Lock()
	defer winMu.Unlock()
	if winTray != nil {
		return winTray.panel
	}
	return nil
}

func (p *windowsPanel) paintScrollbars(hwnd uintptr) {
	dc, _, _ := user32.NewProc("GetWindowDC").Call(hwnd)
	defer menuReleaseDC.Call(hwnd, dc)
	p.paintScrollbarsOnDC(hwnd, dc)
}

// paintScrollbarsOnDC draws the list's vertical scroll bar in the dark
// palette; Windows draws it light whatever the app mode.
func (p *windowsPanel) paintScrollbarsOnDC(hwnd, dc uintptr) {
	if dc == 0 {
		return
	}
	var window menuRect
	user32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&window)))
	info := panelScrollBarInfo{size: uint32(unsafe.Sizeof(panelScrollBarInfo{}))}
	ok, _, _ := user32.NewProc("GetScrollBarInfo").Call(hwnd, ^uintptr(4), uintptr(unsafe.Pointer(&info)))
	if ok == 0 || info.states[0]&(0x8000|0x10000) != 0 {
		return
	}
	rect := info.rect
	rect.left -= window.left
	rect.right -= window.left
	rect.top -= window.top
	rect.bottom -= window.top
	p.fill(dc, rect, p.palette.header)
	thumb := menuRect{rect.left + 2, rect.top + info.top, rect.right - 2, rect.top + info.bottom}
	p.fill(dc, thumb, 0x606060)
	first, last := rect, rect
	first.bottom = first.top + info.line
	last.top = last.bottom - info.line
	p.text(dc, p.font, p.palette.muted, first, "▴", dtSingleLine|dtVCenter|dtNoPrefix|dtCenter)
	p.text(dc, p.font, p.palette.muted, last, "▾", dtSingleLine|dtVCenter|dtNoPrefix|dtCenter)
}
