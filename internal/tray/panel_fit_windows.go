//go:build windows

package tray

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	panelTextExtent = menuGDI.NewProc("GetTextExtentPoint32W")
	panelDpiMetrics = user32.NewProc("GetSystemMetricsForDpi")
	panelScrollInfo = user32.NewProc("SetScrollInfo")
	panelReadScroll = user32.NewProc("GetScrollInfo")
	panelScrollBar  = user32.NewProc("ShowScrollBar")
)

type panelMonitor struct {
	size          uint32
	monitor, work menuRect
	flags         uint32
}

type panelScroll struct {
	size, mask     uint32
	low, high      int32
	page           uint32
	position, drag int32
}

func (p *windowsPanel) fontFor(font panelFont) uintptr {
	switch font {
	case panelStrong:
		return p.strong
	case panelSmall:
		return p.small
	case panelHeading:
		return p.titleFont
	}
	return p.font
}

// textWidth answers panelMeasure with GDI. One pixel more than the extent:
// a cell exactly as wide as the extent can still end in an ellipsis.
func (p *windowsPanel) textWidth(font panelFont, text string) int {
	value, err := windows.UTF16FromString(text)
	if err != nil || len(value) < 2 {
		return 0
	}
	dc, _, _ := menuGetDC.Call(p.hwnd)
	defer menuReleaseDC.Call(p.hwnd, dc)
	saved, _, _ := menuSelectObject.Call(dc, p.fontFor(font))
	defer menuSelectObject.Call(dc, saved)
	var size struct{ cx, cy int32 }
	panelTextExtent.Call(dc, uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)-1), uintptr(unsafe.Pointer(&size)))
	return int(size.cx) + 1
}

func (p *windowsPanel) lineHeight(font panelFont) int {
	dc, _, _ := menuGetDC.Call(p.hwnd)
	defer menuReleaseDC.Call(p.hwnd, dc)
	saved, _, _ := menuSelectObject.Call(dc, p.fontFor(font))
	defer menuSelectObject.Call(dc, saved)
	var size struct{ cx, cy int32 }
	panelTextExtent.Call(dc, uintptr(unsafe.Pointer(utf16z("Ag"))), 2, uintptr(unsafe.Pointer(&size)))
	return int(size.cy)
}

func (p *windowsPanel) scrollbarWidth() int {
	if panelDpiMetrics.Find() == nil {
		if width, _, _ := panelDpiMetrics.Call(2, uintptr(p.dpi)); width > 0 {
			return int(width)
		}
	}
	width, _, _ := menuMetrics.Call(2)
	return int(width)
}

// panelOffscreen is a left edge that keeps a window this wide outside every
// monitor: left of the virtual screen.
func panelOffscreen(width int) int {
	left, _, _ := menuMetrics.Call(76)
	return int(int32(left)) - width - 64
}

func (p *windowsPanel) workArea() menuRect {
	if p.anchor == nil {
		p.anchor = new(point)
		if !p.picture {
			pGetCursorPos.Call(uintptr(unsafe.Pointer(p.anchor)))
		}
	}
	anchor := *p.anchor
	monitor, _, _ := user32.NewProc("MonitorFromPoint").Call(uintptr(uint64(uint32(anchor.x))|uint64(uint32(anchor.y))<<32), 2)
	info := panelMonitor{size: uint32(unsafe.Sizeof(panelMonitor{}))}
	if monitor == 0 {
		return menuRect{0, 0, 1920, 1080}
	}
	if ok, _, _ := user32.NewProc("GetMonitorInfoW").Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return menuRect{0, 0, 1920, 1080}
	}
	if p.picture {
		*p.anchor = point{info.work.right, info.work.bottom}
	}
	return info.work
}

// position sizes the popup to its content within the work area by the
// pointer, its corner at the pointer, and lays out the children.
func (p *windowsPanel) position() {
	if p.hwnd == 0 {
		return
	}
	work := p.workArea()
	var window, client menuRect
	user32.NewProc("GetWindowRect").Call(p.hwnd, uintptr(unsafe.Pointer(&window)))
	panelGetClient.Call(p.hwnd, uintptr(unsafe.Pointer(&client)))
	frameWidth := int(window.right-window.left) - int(client.right)
	frameHeight := int(window.bottom-window.top) - int(client.bottom)
	g := layoutPanel(p.panel, p, int(work.right-work.left)-frameWidth, int(work.bottom-work.top)-frameHeight, p.scrollbarWidth())
	p.geometry = g
	rect := clampPanelRect(*p.anchor, work, int32(g.width+frameWidth), int32(g.height+frameHeight))
	if p.picture {
		width := rect.right - rect.left
		rect.left = int32(panelOffscreen(int(width)))
		rect.right = rect.left + width
	}
	panelSetPos.Call(p.hwnd, 0, uintptr(rect.left), uintptr(rect.top), uintptr(rect.right-rect.left), uintptr(rect.bottom-rect.top), 0x14)
	p.layoutChildren()
	panelInvalidate.Call(p.hwnd, 0, 1)
	panelInvalidate.Call(p.list, 0, 1)
}

func (p *windowsPanel) layoutChildren() {
	g := p.geometry
	panelScrollBar.Call(p.list, 1, uintptr(boolValue(g.scroll)))
	panelMove.Call(p.list, 0, uintptr(g.list.y), uintptr(max(1, g.width)), uintptr(max(1, g.list.height)), 1)
	p.scroll = min(max(0, p.scroll), g.maxScroll())
	if g.scroll {
		info := panelScroll{mask: 0x1 | 0x2 | 0x4, high: int32(max(0, g.content-1)), page: uint32(g.list.height), position: int32(p.scroll)}
		info.size = uint32(unsafe.Sizeof(info))
		panelScrollInfo.Call(p.list, 1, uintptr(unsafe.Pointer(&info)), 1)
	}
	p.placeRows()
	for i, action := range p.panel.Actions {
		if control := p.controls[p.actionIDs[action.ID]]; control != 0 && i < len(g.buttons) {
			b := g.buttons[i]
			panelMove.Call(control, uintptr(b.x), uintptr(b.y), uintptr(b.width), uintptr(b.height), 1)
		}
	}
}

func (p *windowsPanel) placeRows() {
	for i, row := range p.panel.Rows {
		if control := p.controls[p.rowIDs[row.ID]]; control != 0 {
			r := p.geometry.switchRect(i, p.scroll)
			panelMove.Call(control, uintptr(r.x), uintptr(r.y), uintptr(r.width), uintptr(r.height), 1)
		}
	}
}

func (p *windowsPanel) scrollTo(y int) {
	y = min(max(0, y), p.geometry.maxScroll())
	if y == p.scroll {
		return
	}
	p.scroll = y
	info := panelScroll{mask: 0x4, position: int32(y)}
	info.size = uint32(unsafe.Sizeof(info))
	panelScrollInfo.Call(p.list, 1, uintptr(unsafe.Pointer(&info)), 1)
	p.placeRows()
	panelInvalidate.Call(p.list, 0, 1)
}

func (p *windowsPanel) vscroll(wParam uintptr) {
	g := p.geometry
	switch wParam & 0xffff {
	case 0:
		p.scrollTo(p.scroll - g.row/2)
	case 1:
		p.scrollTo(p.scroll + g.row/2)
	case 2:
		p.scrollTo(p.scroll - g.list.height)
	case 3:
		p.scrollTo(p.scroll + g.list.height)
	case 4, 5:
		info := panelScroll{mask: 0x10}
		info.size = uint32(unsafe.Sizeof(info))
		panelReadScroll.Call(p.list, 1, uintptr(unsafe.Pointer(&info)))
		p.scrollTo(int(info.drag))
	case 6:
		p.scrollTo(0)
	case 7:
		p.scrollTo(g.maxScroll())
	}
}

// wheel scrolls a row per notch, wherever over the popup it turns.
func (p *windowsPanel) wheel(wParam uintptr) {
	delta := int(int16(wParam >> 16))
	p.scrollTo(p.scroll - delta*p.geometry.row/120)
}
