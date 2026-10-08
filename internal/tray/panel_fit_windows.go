//go:build windows

package tray

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	panelTextExtent = menuGDI.NewProc("GetTextExtentPoint32W")
	panelDpiMetrics = user32.NewProc("GetSystemMetricsForDpi")
)

var panelFieldColumns = []Column{{Label: "Setting"}, {Label: "Value"}, {Label: "Source"}}

// Rows a table shows before it scrolls.
const panelMaxRows = 12

// Every size derives from the body font, as a web page derives from its root
// font: line is the font's line height and unit half of it. Only the two font
// sizes are fixed, scaled for the monitor's DPI.
type panelMetrics struct{ line, unit, pad, control, row, inset int }

func (p *windowsPanel) metrics() panelMetrics {
	line := max(2, p.lineHeight(p.font))
	unit := line / 2
	return panelMetrics{line: line, unit: unit, pad: 2 * unit, control: line + unit + 2, row: line + unit + 1, inset: unit + unit/2}
}

func (p *windowsPanel) buttonWidth(m panelMetrics, font uintptr, text string) int {
	return p.textWidth(font, text) + 2*m.inset
}

type panelHead struct{ row, tabs, rule, content int }

func (p *windowsPanel) head(m panelMetrics) panelHead {
	row := m.unit + m.unit/2
	tabs := row + max(m.control, p.lineHeight(p.titleFont)) + m.unit/2
	rule := tabs + m.control + m.unit/2
	return panelHead{row, tabs, rule, rule + m.unit + m.unit/2}
}

func (p *windowsPanel) footer(m panelMetrics, width int) ([]panelFooterButton, int) {
	buttons, rows := panelFooterLayout(p.model.panel.Actions, width-2*m.pad, m.control, m.unit*3/4, p.model.settings,
		func(action Item) int { return p.buttonWidth(m, p.font, action.FallbackTitle()) })
	if rows > 0 {
		rows += m.unit / 2
	}
	return buttons, m.unit + rows + m.line + m.unit
}

// Below the accounts table: title, details, hint.
func (p *windowsPanel) accountDetail(m panelMetrics) int {
	return m.line + m.unit/4 + p.detailLines()*m.line + 4 + m.unit/2 + m.line + m.unit
}

// Below the settings table: title, description, editor row, error.
func (p *windowsPanel) settingDetail(m panelMetrics) int {
	return m.line + m.unit/4 + 3*m.line + 4 + m.unit/2 + m.control + m.unit/2 + m.line + m.unit
}

func (p *windowsPanel) scopeWidth(m panelMetrics) int {
	var labels []string
	for _, scope := range p.model.panel.Settings {
		labels = append(labels, scope.Label)
	}
	return p.textWidth(p.font, labels...) + 2*m.inset + p.scrollbarWidth()
}

func (p *windowsPanel) dropHeight(m panelMetrics, items int) int {
	return m.control + max(1, min(items, 8))*(m.line+m.unit-1) + 2
}

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

// Widths match drawCell and drawHeader: a unit before the text, three
// quarters after, and three units before an account title for the check
// mark. Every header reserves the sort arrow so sorting never changes a width.
func (p *windowsPanel) accountWidths() []int {
	m := p.metrics()
	columns := p.model.panel.Columns
	widths := make([]int, len(columns))
	for i, column := range columns {
		lead := m.unit
		if i == 0 {
			lead = 3 * m.unit
		}
		widths[i] = p.textWidth(p.font, column.Label+" ↓") + lead + m.unit*3/4
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
		widths[i] = max(widths[i], text+lead+m.unit*3/4)
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
	m := p.metrics()
	widths := make([]int, len(texts))
	for i := range texts {
		widths[i] = p.textWidth(p.font, texts[i]...) + m.unit + m.unit*3/4
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
	row := p.metrics().row
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
	line := max(1, p.lineHeight(p.font))
	show := int(lines)*line > int(client.bottom)
	style, _, _ := user32.NewProc("GetWindowLongW").Call(edit, ^uintptr(15))
	if show == (style&0x200000 != 0) {
		return
	}
	user32.NewProc("ShowScrollBar").Call(edit, 1, uintptr(boolValue(show)))
	user32.NewProc("RedrawWindow").Call(edit, 0, 0, 0x1|0x4|0x400)
}

func (p *windowsPanel) lineHeight(font uintptr) int {
	dc, _, _ := menuGetDC.Call(p.hwnd)
	defer menuReleaseDC.Call(p.hwnd, dc)
	saved, _, _ := menuSelectObject.Call(dc, font)
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

// contentSize is the client size that shows the current page without dead
// space: every row as wide as its content, tables as tall as their rows.
func (p *windowsPanel) contentSize() (int, int) {
	m := p.metrics()
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
	header := p.textWidth(p.titleFont, p.model.panel.Title) + m.unit + p.textWidth(p.font, p.model.panel.Version) + 2*m.unit +
		p.buttonWidth(m, p.bold, panelControlText(p.controls[panelDashboard]))
	tabs := p.buttonWidth(m, p.bold, "Accounts") + m.unit/2 + p.buttonWidth(m, p.bold, "Settings")
	scope := p.textWidth(p.font, panelControlText(p.controls[224])) + m.unit + p.scopeWidth(m)
	editor := 8*m.line + m.unit + p.buttonWidth(m, p.bold, "Save") + m.unit*3/4 + p.buttonWidth(m, p.font, "Reset")
	buttons := 0
	for _, action := range p.model.panel.Actions {
		buttons = max(buttons, p.buttonWidth(m, p.font, action.FallbackTitle()))
	}
	width := max(table(p.accountWidths(), len(p.model.panel.Rows)), table(p.fieldWidths(), settingRows), header, tabs, scope, editor, buttons) + 2*m.pad
	_, footer := p.footer(m, width)
	top := p.head(m).content
	if p.model.settings {
		return width, top + m.control + m.unit + p.listHeight(panelFields, settingRows) + m.unit + p.settingDetail(m) + footer
	}
	return width, top + p.listHeight(panelAccounts, len(p.model.panel.Rows)) + m.unit + p.accountDetail(m) + footer
}
