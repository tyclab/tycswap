//go:build windows

package tray

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	panelTextExtent = menuGDI.NewProc("GetTextExtentPoint32W")
	panelMetrics    = user32.NewProc("GetSystemMetricsForDpi")
)

var panelFieldColumns = []Column{{Label: "Setting"}, {Label: "Value"}, {Label: "Source"}}

// Rows a table shows before it scrolls.
const panelMaxRows = 12

func (p *windowsPanel) textWidth(font uintptr, texts ...string) int {
	dc, _, _ := menuGetDC.Call(p.hwnd)
	defer menuReleaseDC.Call(p.hwnd, dc)
	saved, _, _ := menuSelectObject.Call(dc, font)
	defer menuSelectObject.Call(dc, saved)
	widest := 0
	for _, text := range texts {
		value, err := windows.UTF16FromString(text)
		if err != nil || len(value) < 2 {
			continue
		}
		var size struct{ cx, cy int32 }
		panelTextExtent.Call(dc, uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)-1), uintptr(unsafe.Pointer(&size)))
		widest = max(widest, int(size.cx))
	}
	return widest
}

// Widths match drawCell and drawHeader: 8 px before the text, 6 after, and
// 24 before an account title for the check mark. Every header reserves the
// sort arrow so sorting never changes a width.
func (p *windowsPanel) accountWidths() []int {
	columns := p.model.panel.Columns
	widths := make([]int, len(columns))
	for i, column := range columns {
		lead := p.scale(8)
		if i == 0 {
			lead = p.scale(24)
		}
		widths[i] = p.textWidth(p.font, column.Label+" ↓") + lead + p.scale(6)
		var plain, bold []string
		for _, row := range p.model.panel.Rows {
			text := row.Title
			if i > 0 {
				text = ""
				if i < len(row.Cells) {
					text = row.Cells[i]
				}
			}
			if i == 0 && row.Active {
				bold = append(bold, text)
			} else {
				plain = append(plain, text)
			}
		}
		text := max(p.textWidth(p.font, plain...), p.textWidth(p.bold, bold...))
		widths[i] = max(widths[i], text+lead+p.scale(6))
	}
	return widths
}

// Measured over every scope so changing Apply to keeps the table still.
func (p *windowsPanel) fieldWidths() []int {
	texts := make([][]string, len(panelFieldColumns))
	for i, column := range panelFieldColumns {
		texts[i] = []string{column.Label}
	}
	for _, scope := range p.model.panel.Settings {
		for _, field := range scope.Settings {
			for i, text := range []string{field.Label, field.Value, field.Source} {
				texts[i] = append(texts[i], text)
			}
		}
	}
	widths := make([]int, len(texts))
	for i := range texts {
		widths[i] = p.textWidth(p.font, texts[i]...) + p.scale(14)
	}
	return widths
}

// Only columns whose content width changed are set, so a column the user
// dragged keeps its width until its content changes.
func (p *windowsPanel) fitColumns(id int, widths []int) {
	if p.fittedWidths == nil {
		p.fittedWidths = make(map[int][]int)
	}
	previous := p.fittedWidths[id]
	for i, width := range widths {
		if i < len(previous) && previous[i] == width {
			continue
		}
		panelSend.Call(p.controls[id], panelListBase+30, uintptr(i), uintptr(width))
	}
	p.fittedWidths[id] = widths
	p.fillLastColumn(id)
}

// The last column runs to the list's edge: rows paint their highlight across
// the full width, and the list repaints only what its columns cover.
func (p *windowsPanel) fillLastColumn(id int) {
	widths := p.fittedWidths[id]
	if len(widths) == 0 {
		return
	}
	hwnd := p.controls[id]
	var client menuRect
	panelGetClient.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	used := 0
	for i := range len(widths) - 1 {
		width, _, _ := panelSend.Call(hwnd, panelListBase+29, uintptr(i), 0)
		used += int(width)
	}
	last := len(widths) - 1
	width := max(widths[last], int(client.right)-used)
	if current, _, _ := panelSend.Call(hwnd, panelListBase+29, uintptr(last), 0); int(current) != width {
		panelSend.Call(hwnd, panelListBase+30, uintptr(last), uintptr(width))
	}
}

func (p *windowsPanel) listHeight(id, rows int) int {
	hwnd := p.controls[id]
	header, _, _ := panelSend.Call(hwnd, panelListBase+31, 0, 0)
	bounds := menuRect{0, 0, 10000, 10000}
	var position struct {
		hwnd, after         uintptr
		x, y, width, height int32
		flags               uint32
	}
	layout := struct{ rect, position uintptr }{uintptr(unsafe.Pointer(&bounds)), uintptr(unsafe.Pointer(&position))}
	panelSend.Call(header, 0x1205, 0, uintptr(unsafe.Pointer(&layout)))
	row := p.scale(26)
	if count, _, _ := panelSend.Call(hwnd, panelListBase+4, 0, 0); count > 0 {
		item := menuRect{left: 0}
		panelSend.Call(hwnd, panelListBase+14, 0, uintptr(unsafe.Pointer(&item)))
		row = max(row, int(item.bottom-item.top))
	}
	return int(position.height) + max(1, min(rows, panelMaxRows))*row + 2
}

// A text box shows its scrollbar only when its wrapped text overflows.
func (p *windowsPanel) fitScroll(id int) {
	edit := p.controls[id]
	var client menuRect
	panelGetClient.Call(edit, uintptr(unsafe.Pointer(&client)))
	lines, _, _ := panelSend.Call(edit, 0xba, 0, 0)
	line := max(1, p.lineHeight())
	show := int(lines)*line > int(client.bottom)
	style, _, _ := user32.NewProc("GetWindowLongW").Call(edit, ^uintptr(15))
	if show == (style&0x200000 != 0) {
		return
	}
	user32.NewProc("ShowScrollBar").Call(edit, 1, uintptr(boolValue(show)))
	user32.NewProc("RedrawWindow").Call(edit, 0, 0, 0x1|0x4|0x400)
}

func (p *windowsPanel) lineHeight() int {
	dc, _, _ := menuGetDC.Call(p.hwnd)
	defer menuReleaseDC.Call(p.hwnd, dc)
	saved, _, _ := menuSelectObject.Call(dc, p.font)
	defer menuSelectObject.Call(dc, saved)
	var size struct{ cx, cy int32 }
	panelTextExtent.Call(dc, uintptr(unsafe.Pointer(utf16z("Ag"))), 2, uintptr(unsafe.Pointer(&size)))
	return int(size.cy)
}

func (p *windowsPanel) scrollbarWidth() int {
	if panelMetrics.Find() == nil {
		if width, _, _ := panelMetrics.Call(2, uintptr(p.dpi)); width > 0 {
			return int(width)
		}
	}
	return p.scale(17)
}

// contentSize is the client size that shows the current page without dead
// space: tables as wide as their columns and as tall as their rows.
func (p *windowsPanel) contentSize() (int, int) {
	pad := p.scale(16)
	table := func(widths []int, rows int) int {
		width := 0
		for _, column := range widths {
			width += column
		}
		if rows > panelMaxRows {
			width += p.scrollbarWidth()
		}
		return width
	}
	settingRows := 0
	for _, scope := range p.model.panel.Settings {
		settingRows = max(settingRows, len(scope.Settings))
	}
	width := max(table(p.accountWidths(), len(p.model.panel.Rows)), table(p.fieldWidths(), settingRows)) + 2*pad
	width = max(width, p.scale(314)+p.textWidth(p.font, p.model.panel.Version), p.scale(360))
	_, footer := panelFooterLayout(p.model.panel.Actions, (width-2*pad)*96/p.dpi, p.model.settings)
	if p.model.settings {
		return width, p.scale(136) + p.listHeight(panelFields, settingRows) + p.scale(8+144+footer)
	}
	return width, p.scale(98) + p.listHeight(panelAccounts, len(p.model.panel.Rows)) + p.scale(8+p.detailHeight()+footer)
}
