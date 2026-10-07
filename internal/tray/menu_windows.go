package tray

import (
	"strconv"
	"strings"
	"unsafe"

	"github.com/tyclab/tycswap/internal/brand"
	"golang.org/x/sys/windows"
)

// Dark menus are owner-drawn while Windows retains command and keyboard handling.
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
	titles                              map[uintptr]string
	steps                               map[uintptr]menuStep
	theme                               menuTheme
	regular, background, hover, divider uintptr
	font                                uintptr
	ownsFont                            bool
}

func newActiveMenuPainter(plan []menuStep) *activeMenuPainter {
	return newMenuPainter(plan, menuLight)
}

func newMenuPainter(plan []menuStep, theme menuTheme) *activeMenuPainter {
	p := &activeMenuPainter{titles: make(map[uintptr]string), steps: make(map[uintptr]menuStep), theme: theme}
	for _, step := range plan {
		if step.flags&mfOwnerDraw != 0 {
			p.titles[step.cmd] = step.title
			p.steps[step.cmd] = step
		}
	}
	if len(p.titles) == 0 {
		return p
	}
	var metrics menuNonClientMetrics
	metrics.size = uint32(unsafe.Sizeof(metrics))
	if ok, _, _ := menuSystemParameters.Call(0x29, uintptr(metrics.size), uintptr(unsafe.Pointer(&metrics)), 0); ok != 0 {
		p.regular, _, _ = menuCreateFont.Call(uintptr(unsafe.Pointer(&metrics.menuFont)))
		metrics.menuFont.weight = 700 // FW_BOLD: keep the system face and size.
		p.font, _, _ = menuCreateFont.Call(uintptr(unsafe.Pointer(&metrics.menuFont)))
		p.ownsFont = p.font != 0
	}
	if p.font == 0 {
		p.font, _, _ = menuStockObject.Call(17)
	} // DEFAULT_GUI_FONT
	if theme == menuDark {
		create := menuGDI.NewProc("CreateSolidBrush")
		p.background, _, _ = create.Call(0x202020)
		p.hover, _, _ = create.Call(0x383838)
		p.divider, _, _ = create.Call(0x606060)
	}
	return p
}

func (p *activeMenuPainter) close() {
	for _, object := range []uintptr{p.regular, p.background, p.hover, p.divider} {
		if object != 0 {
			menuDeleteObject.Call(object)
		}
	}
	if p.ownsFont {
		menuDeleteObject.Call(p.font)
	}
}

func menuAccentColor() uintptr { return themeAccent(menuLight) }

func themeAccent(theme menuTheme) uintptr {
	accent := brand.Sanitized().AccentColor
	if theme != menuDark {
		accent = brand.LightAccent(accent)
	}
	rgb, _ := strconv.ParseUint(accent[1:], 16, 32)
	return uintptr((rgb&0xff)<<16 | rgb&0xff00 | (rgb>>16)&0xff)
}

func (p *activeMenuPainter) rowFont(step menuStep) uintptr {
	if step.active || p.regular == 0 {
		return p.font
	}
	return p.regular
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
	step := p.steps[m.itemData]
	if step.op == opSeparator {
		m.itemWidth = 1
		m.itemHeight = 9
		return true
	}
	old, _, _ := menuSelectObject.Call(dc, p.rowFont(step))
	defer menuSelectObject.Call(dc, old)
	var rect menuRect
	menuDrawText.Call(dc, uintptr(unsafe.Pointer(utf16z(title))), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), dtSingleLine|dtNoPrefix|dtCalcRect)
	height, _, _ := menuMetrics.Call(15) // SM_CYMENU
	m.itemWidth = uint32(rect.right - rect.left + menuGutter() + 24)
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
	step := p.steps[d.itemData]
	color := themeAccent(p.theme)
	if p.theme == menuDark {
		brush = p.background
		color = 0xf0f0f0
		if step.flags&mfGrayed != 0 {
			color = 0xaaaaaa
		} else if d.itemState&1 != 0 {
			brush = p.hover
		}
		if step.active {
			color = themeAccent(p.theme)
		}
	}
	menuFillRect.Call(d.hdc, uintptr(unsafe.Pointer(&d.rect)), brush)
	if step.op == opSeparator {
		line := d.rect
		line.left += 8
		line.right -= 8
		line.top = (line.top + line.bottom) / 2
		line.bottom = line.top + 1
		menuFillRect.Call(d.hdc, uintptr(unsafe.Pointer(&line)), p.divider)
		return true
	}
	menuSelectObject.Call(d.hdc, p.rowFont(step))
	menuBkMode.Call(d.hdc, 1) // TRANSPARENT
	menuTextColor.Call(d.hdc, color)
	text := d.rect
	text.left += menuGutter()
	text.right -= 6
	flags := uintptr(dtSingleLine | dtNoPrefix | dtVCenter)
	menuDrawText.Call(d.hdc, uintptr(unsafe.Pointer(utf16z(title))), ^uintptr(0), uintptr(unsafe.Pointer(&text)), flags)
	check := d.rect
	check.left += 6
	check.right = text.left - 4
	if step.flags&mfChecked != 0 {
		menuDrawText.Call(d.hdc, uintptr(unsafe.Pointer(utf16z("✓"))), ^uintptr(0), uintptr(unsafe.Pointer(&check)), flags|1)
	} // DT_CENTER
	if step.op == opClose {
		arrow := d.rect
		arrow.left = arrow.right - 18
		menuDrawText.Call(d.hdc, uintptr(unsafe.Pointer(utf16z("›"))), ^uintptr(0), uintptr(unsafe.Pointer(&arrow)), flags|1)
	}
	return true
}

// Retain the menu string for accessibility even though we paint the row.
func setOwnerDrawTitle(menu, cmd uintptr, title string) {
	info := menuItemInfo{mask: 0x40, text: utf16z(title)} // MIIM_STRING
	info.size = uint32(unsafe.Sizeof(info))
	menuSetInfo.Call(menu, cmd, 0, uintptr(unsafe.Pointer(&info)))
}

func setOwnerDrawTitleAtEnd(menu uintptr, title string) {
	count, _, _ := user32.NewProc("GetMenuItemCount").Call(menu)
	info := menuItemInfo{mask: 0x40, text: utf16z(title)}
	info.size = uint32(unsafe.Sizeof(info))
	menuSetInfo.Call(menu, count-1, 1, uintptr(unsafe.Pointer(&info)))
}

func (p *activeMenuPainter) menuChar(menu uintptr, char rune) uintptr {
	count, _, _ := user32.NewProc("GetMenuItemCount").Call(menu)
	if int32(count) < 0 {
		return 0
	}
	var matches []uintptr
	selected := -1
	for i := uintptr(0); i < count; i++ {
		info := menuItemInfo{mask: 0x100 | 0x20 | 1}
		info.size = uint32(unsafe.Sizeof(info))
		ok, _, _ := user32.NewProc("GetMenuItemInfoW").Call(menu, i, 1, uintptr(unsafe.Pointer(&info)))
		if ok == 0 || info.state&3 != 0 || info.kind&mfSeparator != 0 {
			continue
		}
		if info.state&0x80 != 0 {
			selected = int(i)
		}
		title := strings.TrimSpace(p.titles[info.data])
		if strings.HasPrefix(strings.ToLower(title), strings.ToLower(string(char))) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		return 0
	}
	if len(matches) == 1 {
		return matches[0] | 2<<16
	}
	for _, i := range matches {
		if int(i) > selected {
			return i | 3<<16
		}
	}
	return matches[0] | 3<<16
}
