package tray

import (
	"strconv"
	"unsafe"

	"github.com/tyclab/tycswap/internal/brand"
	"golang.org/x/sys/windows"
)

// Only active account rows are owner-drawn. They remain disabled commands,
// but use the dashboard's brand accent instead of Windows' disabled gray.
var (
	menuGDI              = windows.NewLazySystemDLL("gdi32.dll")
	menuGetDC            = user32.NewProc("GetDC")
	menuReleaseDC        = user32.NewProc("ReleaseDC")
	menuSystemParameters = user32.NewProc("SystemParametersInfoW")
	menuMetrics          = user32.NewProc("GetSystemMetrics")
	menuDrawText         = user32.NewProc("DrawTextW")
	menuFillRect         = user32.NewProc("FillRect")
	menuBrush            = user32.NewProc("GetSysColorBrush")
	menuSetInfo          = user32.NewProc("SetMenuItemInfoW")
	menuCreateFont       = menuGDI.NewProc("CreateFontIndirectW")
	menuSelectObject     = menuGDI.NewProc("SelectObject")
	menuDeleteObject     = menuGDI.NewProc("DeleteObject")
	menuStockObject      = menuGDI.NewProc("GetStockObject")
	menuSaveDC           = menuGDI.NewProc("SaveDC")
	menuRestoreDC        = menuGDI.NewProc("RestoreDC")
	menuTextColor        = menuGDI.NewProc("SetTextColor")
	menuBkMode           = menuGDI.NewProc("SetBkMode")
)

const (
	mfOwnerDraw   = 0x0100
	wmDrawItem    = 0x002b
	wmMeasureItem = 0x002c
	odtMenu       = 1
	dtSingleLine  = 0x20
	dtNoPrefix    = 0x800
	dtVCenter     = 0x4
	dtCalcRect    = 0x400
)

type menuRect struct{ left, top, right, bottom int32 }
type measureMenuItem struct {
	ctlType, ctlID, itemID, itemWidth, itemHeight uint32
	itemData                                      uintptr
}
type drawMenuItem struct {
	ctlType, ctlID, itemID, itemAction, itemState uint32
	hwndItem, hdc                                 uintptr
	rect                                          menuRect
	itemData                                      uintptr
}
type menuLogFont struct {
	height, width, escapement, orientation, weight                                              int32
	italic, underline, strikeOut, charSet, outPrecision, clipPrecision, quality, pitchAndFamily byte
	faceName                                                                                    [32]uint16
}
type menuNonClientMetrics struct {
	size                                                                uint32
	borderWidth, scrollWidth, scrollHeight, captionWidth, captionHeight int32
	captionFont                                                         menuLogFont
	smallCaptionWidth, smallCaptionHeight                               int32
	smallCaptionFont                                                    menuLogFont
	menuWidth, menuHeight                                               int32
	menuFont, statusFont, messageFont                                   menuLogFont
	paddedBorderWidth                                                   int32
}
type menuItemInfo struct {
	size, mask, kind, state, id                   uint32
	subMenu, checkedBitmap, uncheckedBitmap, data uintptr
	text                                          *uint16
	textLength                                    uint32
	bitmap                                        uintptr
}

type activeMenuPainter struct {
	titles   map[uintptr]string
	font     uintptr
	ownsFont bool
}

func newActiveMenuPainter(plan []menuStep) *activeMenuPainter {
	p := &activeMenuPainter{titles: make(map[uintptr]string)}
	for _, step := range plan {
		if step.flags&mfOwnerDraw != 0 {
			p.titles[step.cmd] = step.title
		}
	}
	if len(p.titles) == 0 {
		return p
	}
	var metrics menuNonClientMetrics
	metrics.size = uint32(unsafe.Sizeof(metrics))
	if ok, _, _ := menuSystemParameters.Call(0x29, uintptr(metrics.size), uintptr(unsafe.Pointer(&metrics)), 0); ok != 0 {
		metrics.menuFont.weight = 700 // FW_BOLD: keep the system face and size.
		p.font, _, _ = menuCreateFont.Call(uintptr(unsafe.Pointer(&metrics.menuFont)))
		p.ownsFont = p.font != 0
	}
	if p.font == 0 {
		p.font, _, _ = menuStockObject.Call(17)
	} // DEFAULT_GUI_FONT
	return p
}

func (p *activeMenuPainter) close() {
	if p.ownsFont {
		menuDeleteObject.Call(p.font)
	}
}

func menuAccentColor() uintptr {
	accent := brand.Sanitized().AccentColor
	background, _, _ := user32.NewProc("GetSysColor").Call(4) // COLOR_MENU
	r, g, b := background&255, (background>>8)&255, (background>>16)&255
	if 299*r+587*g+114*b >= 128000 {
		accent = brand.LightAccent(accent)
	}
	rgb, _ := strconv.ParseUint(accent[1:], 16, 32)
	// GDI COLORREF is 0x00bbggrr; CSS is #rrggbb.
	return uintptr((rgb&0xff)<<16 | rgb&0xff00 | (rgb>>16)&0xff)
}

func menuGutter() int32 {
	width, _, _ := menuMetrics.Call(71) // SM_CXMENUCHECK
	// Native popup labels include a small gap after the check column.
	return int32(width) + 5
}

func (p *activeMenuPainter) measure(hwnd uintptr, m *measureMenuItem) bool {
	if p == nil || m.ctlType != odtMenu {
		return false
	}
	title, ok := p.titles[m.itemData]
	if !ok {
		return false
	}
	dc, _, _ := menuGetDC.Call(hwnd)
	if dc == 0 {
		return false
	}
	defer menuReleaseDC.Call(hwnd, dc)
	old, _, _ := menuSelectObject.Call(dc, p.font)
	defer menuSelectObject.Call(dc, old)
	var rect menuRect
	menuDrawText.Call(dc, uintptr(unsafe.Pointer(utf16z(title))), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), dtSingleLine|dtNoPrefix|dtCalcRect)
	height, _, _ := menuMetrics.Call(15) // SM_CYMENU
	m.itemWidth = uint32(rect.right - rect.left + menuGutter() + 12)
	m.itemHeight = uint32(max(rect.bottom-rect.top+4, int32(height)))
	return true
}

func (p *activeMenuPainter) draw(d *drawMenuItem) bool {
	if p == nil || d.ctlType != odtMenu {
		return false
	}
	title, ok := p.titles[d.itemData]
	if !ok {
		return false
	}
	saved, _, _ := menuSaveDC.Call(d.hdc)
	defer menuRestoreDC.Call(d.hdc, saved)
	brush, _, _ := menuBrush.Call(4) // COLOR_MENU
	menuFillRect.Call(d.hdc, uintptr(unsafe.Pointer(&d.rect)), brush)
	menuSelectObject.Call(d.hdc, p.font)
	menuBkMode.Call(d.hdc, 1) // TRANSPARENT
	menuTextColor.Call(d.hdc, menuAccentColor())
	text := d.rect
	text.left += menuGutter()
	text.right -= 6
	flags := uintptr(dtSingleLine | dtNoPrefix | dtVCenter)
	menuDrawText.Call(d.hdc, uintptr(unsafe.Pointer(utf16z(title))), ^uintptr(0), uintptr(unsafe.Pointer(&text)), flags)
	check := d.rect
	check.left += 6
	check.right = text.left - 4
	menuDrawText.Call(d.hdc, uintptr(unsafe.Pointer(utf16z("✓"))), ^uintptr(0), uintptr(unsafe.Pointer(&check)), flags|1) // DT_CENTER
	return true
}

// Retain the menu string for accessibility even though we paint the row.
func setOwnerDrawTitle(menu, cmd uintptr, title string) {
	info := menuItemInfo{mask: 0x40, text: utf16z(title)} // MIIM_STRING
	info.size = uint32(unsafe.Sizeof(info))
	menuSetInfo.Call(menu, cmd, 0, uintptr(unsafe.Pointer(&info)))
}
