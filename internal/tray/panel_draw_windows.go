//go:build windows

package tray

import (
	"unsafe"
)

var (
	panelCreateBrush = menuGDI.NewProc("CreateSolidBrush")
	panelCreatePen   = menuGDI.NewProc("CreatePen")
	panelRoundRect   = menuGDI.NewProc("RoundRect")
	panelEllipse     = menuGDI.NewProc("Ellipse")
	panelFocusRect   = user32.NewProc("DrawFocusRect")
)

const (
	dtCenter      = 0x1
	dtRight       = 0x2
	dtEndEllipsis = 0x8000
	panelLine     = dtSingleLine | dtVCenter | dtNoPrefix | dtEndEllipsis
)

type panelPaint struct {
	dc              uintptr
	erase           int32
	rect            menuRect
	restore, update int32
	reserved        [32]byte
}

func panelSystemColor(index uintptr) uintptr {
	value, _, _ := user32.NewProc("GetSysColor").Call(index)
	return value
}

// High contrast takes the system's colours; the limit state uses the
// highlight colour, which every high-contrast scheme sets apart. The bar's
// track is the grey text colour: window text and window frame are one
// colour there, and a track in either would hide the share.
func colorsForPanel(theme menuTheme) panelColors {
	if theme == menuHighContrast {
		window, face, text, gray, frame, highlight := panelSystemColor(5), panelSystemColor(15), panelSystemColor(8), panelSystemColor(17), panelSystemColor(6), panelSystemColor(13)
		return panelColors{window, face, text, gray, frame, highlight, text, gray, highlight, gray}
	}
	return panelPalette(theme == menuDark, themeAccent(theme))
}

func setPanelDPIAware() {
	proc := user32.NewProc("SetProcessDpiAwarenessContext")
	if proc.Find() == nil {
		proc.Call(^uintptr(3))
	}
}

func (p *windowsPanel) readDPI() {
	proc := user32.NewProc("GetDpiForWindow")
	if proc.Find() == nil {
		if dpi, _, _ := proc.Call(p.hwnd); dpi > 0 {
			p.dpi = int(dpi)
		}
	}
}

func (p *windowsPanel) scale(value int) int { return (value*p.dpi + 48) / 96 }

func (p *windowsPanel) freeTheme() {
	stock, _, _ := menuStockObject.Call(17)
	seen := map[uintptr]bool{}
	for _, object := range []uintptr{p.font, p.strong, p.small, p.titleFont} {
		if object != 0 && object != stock && !seen[object] {
			menuDeleteObject.Call(object)
			seen[object] = true
		}
	}
	p.font, p.strong, p.small, p.titleFont = 0, 0, 0, 0
}

// refreshTheme rebuilds the fonts and colours for the current app mode and
// DPI. Only the four font sizes are fixed; every other size derives from
// them (panelMetrics).
func (p *windowsPanel) refreshTheme() {
	if p.hwnd == 0 {
		return
	}
	p.freeTheme()
	p.theme = readMenuTheme()
	p.palette = colorsForPanel(p.theme)
	var metrics menuNonClientMetrics
	metrics.size = uint32(unsafe.Sizeof(metrics))
	menuSystemParameters.Call(0x29, uintptr(metrics.size), uintptr(unsafe.Pointer(&metrics)), 0)
	face := metrics.menuFont
	if face.faceName[0] == 0 {
		copyUTF16(face.faceName[:], "Segoe UI")
	}
	stock, _, _ := menuStockObject.Call(17)
	create := func(height, weight int32) uintptr {
		font := face
		font.height, font.weight = -height, weight
		if handle, _, _ := menuCreateFont.Call(uintptr(unsafe.Pointer(&font))); handle != 0 {
			return handle
		}
		return stock
	}
	p.font = create(int32(p.scale(13)), 400)
	p.strong = create(int32(p.scale(13)), 600)
	p.small = create(int32(p.scale(11)), 400)
	p.titleFont = create(int32(p.scale(16)), 700)
	p.position()
	user32.NewProc("RedrawWindow").Call(p.hwnd, 0, 0, 0x1|0x4|0x80|0x400)
}

func clampPanelRect(anchor point, work menuRect, width, height int32) menuRect {
	width = min(width, work.right-work.left)
	height = min(height, work.bottom-work.top)
	x := max(work.left, min(anchor.x-width, work.right-width))
	y := max(work.top, min(anchor.y-height, work.bottom-height))
	return menuRect{x, y, x + width, y + height}
}

func (p *windowsPanel) fill(dc uintptr, r menuRect, color uintptr) {
	brush, _, _ := panelCreateBrush.Call(color)
	menuFillRect.Call(dc, uintptr(unsafe.Pointer(&r)), brush)
	menuDeleteObject.Call(brush)
}

func (p *windowsPanel) text(dc, font, color uintptr, r menuRect, text string, flags uintptr) {
	if text == "" {
		return
	}
	menuSelectObject.Call(dc, font)
	menuBkMode.Call(dc, 1)
	menuTextColor.Call(dc, color)
	menuDrawText.Call(dc, uintptr(unsafe.Pointer(utf16z(text))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), flags)
}

// shape draws a rounded rectangle (radius r) or, with ellipse, the circle
// in it, outlined in edge and filled with inside; noFill leaves it hollow.
func (p *windowsPanel) shape(dc uintptr, b menuRect, radius int, edge, inside uintptr, noFill, ellipse bool) {
	pen, _, _ := panelCreatePen.Call(0, 1, edge)
	brush, _, _ := menuStockObject.Call(5)
	if !noFill {
		brush, _, _ = panelCreateBrush.Call(inside)
		defer menuDeleteObject.Call(brush)
	}
	oldPen, _, _ := menuSelectObject.Call(dc, pen)
	oldBrush, _, _ := menuSelectObject.Call(dc, brush)
	if ellipse {
		panelEllipse.Call(dc, uintptr(b.left), uintptr(b.top), uintptr(b.right), uintptr(b.bottom))
	} else {
		panelRoundRect.Call(dc, uintptr(b.left), uintptr(b.top), uintptr(b.right), uintptr(b.bottom), uintptr(radius), uintptr(radius))
	}
	menuSelectObject.Call(dc, oldPen)
	menuSelectObject.Call(dc, oldBrush)
	menuDeleteObject.Call(pen)
}

func (p *windowsPanel) rule(dc uintptr, left, right, y int) {
	p.fill(dc, menuRect{int32(left), int32(y), int32(right), int32(y + 1)}, p.palette.line)
}

// paint draws the popup's own parts: title, column labels, the rules, the
// note line.
func (p *windowsPanel) paint(dc uintptr) {
	if dc == 0 {
		return
	}
	saved, _, _ := menuSaveDC.Call(dc)
	defer menuRestoreDC.Call(dc, saved)
	var client menuRect
	panelGetClient.Call(p.hwnd, uintptr(unsafe.Pointer(&client)))
	p.fill(dc, client, p.palette.background)
	g := p.geometry
	p.text(dc, p.titleFont, p.palette.text, panelBox(g.title), p.panel.Title, panelLine)
	header := panelBox(g.header)
	header.right = client.right
	p.fill(dc, header, p.palette.header)
	p.rule(dc, 0, int(client.right), g.header.y+g.header.height-1)
	for i, column := range p.panel.Columns {
		if i < len(g.columns) {
			span := g.columns[i]
			p.text(dc, p.font, p.palette.muted, menuRect{int32(span.x), header.top, int32(span.x + span.width), header.bottom}, column.Label, panelLine)
		}
	}
	p.rule(dc, 0, int(client.right), g.list.y+g.list.height)
	note, color := p.panel.Note, p.palette.text
	if p.panel.Alert {
		color = p.palette.limit
	}
	p.text(dc, p.font, color, panelBox(g.note), note, panelLine)
	p.rule(dc, 0, int(client.right), g.footer)
}

func panelBox(r panelRect) menuRect {
	return menuRect{int32(r.x), int32(r.y), int32(r.x + r.width), int32(r.y + r.height)}
}

// paintList draws the rows visible at the scroll offset; the Switch buttons
// on them draw themselves.
func (p *windowsPanel) paintList(dc uintptr) {
	if dc == 0 {
		return
	}
	saved, _, _ := menuSaveDC.Call(dc)
	defer menuRestoreDC.Call(dc, saved)
	var client menuRect
	panelGetClient.Call(p.list, uintptr(unsafe.Pointer(&client)))
	p.fill(dc, client, p.palette.background)
	g := p.geometry
	if len(p.panel.Rows) == 0 {
		p.text(dc, p.font, p.palette.muted, client, p.panel.Empty, panelLine|dtCenter)
		return
	}
	if g.row <= 0 {
		return
	}
	for i := p.scroll / g.row; i < len(p.panel.Rows) && i*g.row-p.scroll < int(client.bottom); i++ {
		top := i*g.row - p.scroll
		p.paintRow(dc, p.panel.Rows[i], top)
		if i < len(p.panel.Rows)-1 {
			p.rule(dc, g.metrics.pad, int(client.right)-g.metrics.pad, top+g.row-1)
		}
	}
}

func (p *windowsPanel) paintRow(dc uintptr, row PanelRow, top int) {
	g := p.geometry
	unit := g.metrics.unit
	first := menuRect{0, int32(top + g.line1), 0, int32(top + g.line1 + g.first)}
	second := menuRect{0, int32(top + g.line2), 0, int32(top + g.line2 + g.second)}
	account := g.columns[0]
	end := int32(account.x + account.width)
	first.left, first.right = int32(account.x), end
	p.text(dc, p.strong, p.palette.text, first, row.Name, panelLine)
	line := second
	line.left, line.right = int32(account.x), end
	if row.Plan != "" {
		label := line
		label.right = min(end, label.left+int32(panelLabelWidth(p, g.metrics, row.Plan)))
		p.shape(dc, label, unit/2, p.palette.muted, 0, true, false)
		p.text(dc, p.small, p.palette.text, label, row.Plan, panelLine|dtCenter)
		line.left = label.right + int32(unit/2)
	}
	for _, part := range []struct {
		text  string
		color uintptr
	}{{row.Tag, p.palette.muted}, {row.Warning, p.palette.limit}} {
		if part.text != "" && line.left < end {
			p.text(dc, p.small, part.color, line, part.text, panelLine)
			line.left += int32(p.textWidth(panelSmall, part.text) + unit/2)
		}
	}
	for i, cell := range row.Cells {
		if i+1 >= len(g.columns)-1 {
			break
		}
		span := g.columns[i+1]
		left, right := int32(span.x), int32(span.x+span.width)
		if cell.Absent {
			p.text(dc, p.font, p.palette.muted, menuRect{left, int32(top), right, int32(top + g.row)}, "—", panelLine|dtCenter)
			continue
		}
		share, fill := p.palette.text, p.palette.accent
		if cell.Limit {
			share, fill = p.palette.limit, p.palette.limit
		}
		p.text(dc, p.font, share, menuRect{left, first.top, left + int32(g.share), first.bottom}, cell.Share, panelLine|dtRight)
		height := max(2, unit/2)
		bar := menuRect{left + int32(g.share+unit/2), (first.top+first.bottom)/2 - int32(height/2), right, 0}
		bar.bottom = bar.top + int32(height)
		p.fill(dc, bar, p.palette.track)
		filled := bar
		filled.right = bar.left + int32(float64(bar.right-bar.left)*min(100, max(0, cell.Pct))/100+0.5)
		if filled.right > filled.left {
			p.fill(dc, filled, fill)
		}
		p.text(dc, p.small, p.palette.muted, menuRect{left, second.top, right, second.bottom}, cell.Reset, panelLine)
	}
}

// drawButton draws every control: a row's Switch or "In use", a switch as
// label, pill and state, and the footer's commands as plain outlined
// buttons.
func (p *windowsPanel) drawButton(draw *drawMenuItem) bool {
	if draw.ctlType != 4 {
		return false
	}
	id := int(draw.ctlID)
	disabled, pressed := draw.itemState&4 != 0, draw.itemState&1 != 0
	focused := draw.itemState&0x10 != 0 && draw.itemState&0x200 == 0
	dc, rect := draw.hdc, draw.rect
	saved, _, _ := menuSaveDC.Call(dc)
	defer menuRestoreDC.Call(dc, saved)
	p.fill(dc, rect, p.palette.background)
	label, color := "", p.palette.text
	if i, ok := p.rowFor(id); ok {
		if p.panel.Rows[i].Active {
			p.text(dc, p.strong, p.palette.accent, rect, "In use", panelLine|dtCenter)
			return true
		}
		label = "Switch"
	} else if action, ok := p.actionFor(id); ok {
		if action.Kind == KindToggle {
			p.drawToggle(dc, rect, action, disabled)
			if focused {
				panelFocusRect.Call(dc, uintptr(unsafe.Pointer(&rect)))
			}
			return true
		}
		label = action.Title
	} else {
		return false
	}
	if pressed {
		p.fill(dc, rect, p.palette.selection)
	}
	if disabled {
		color = p.palette.muted
	}
	p.shape(dc, rect, p.geometry.metrics.unit/2, p.palette.line, 0, true, false)
	p.text(dc, p.font, color, rect, label, panelLine|dtCenter)
	if focused {
		inner := menuRect{rect.left + 2, rect.top + 2, rect.right - 2, rect.bottom - 2}
		panelFocusRect.Call(dc, uintptr(unsafe.Pointer(&inner)))
	}
	return true
}

// drawToggle is a switch: its label, a pill filled with the accent and the
// knob right when on, outlined with the knob left when off, and the word.
func (p *windowsPanel) drawToggle(dc uintptr, rect menuRect, action Item, disabled bool) {
	m := p.geometry.metrics
	text, edge := p.palette.text, p.palette.accent
	if action.Checked {
		text = p.palette.accent
	}
	if disabled {
		text, edge = p.palette.muted, p.palette.disabledAccent
	}
	x := rect.left + int32(m.unit/2)
	p.text(dc, p.font, text, menuRect{x, rect.top, rect.right, rect.bottom}, action.Title, panelLine)
	x += int32(p.textWidth(panelBody, action.Title) + m.unit/2)
	width, height := panelPill(m)
	top := (rect.top+rect.bottom)/2 - int32(height/2)
	pill := menuRect{x, top, x + int32(width), top + int32(height)}
	knob := int32(height - 4)
	if action.Checked {
		p.shape(dc, pill, height, edge, edge, false, false)
		p.shape(dc, menuRect{pill.right - 2 - knob, top + 2, pill.right - 2, top + 2 + knob}, 0, p.palette.background, p.palette.background, false, true)
	} else {
		p.shape(dc, pill, height, p.palette.muted, 0, true, false)
		p.shape(dc, menuRect{pill.left + 2, top + 2, pill.left + 2 + knob, top + 2 + knob}, 0, p.palette.muted, p.palette.muted, false, true)
	}
	state := "off"
	if action.Checked {
		state = "on"
	}
	p.text(dc, p.font, text, menuRect{pill.right + int32(m.unit/2), rect.top, rect.right, rect.bottom}, state, panelLine)
}
