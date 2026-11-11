package tray

import (
	"strconv"
	"strings"
)

// The daily view's geometry, kept free of Win32 so it can be tested with a
// fake text measure (DESIGN A84).

type panelFont uint8

const (
	panelBody panelFont = iota
	panelStrong
	panelSmall
	panelHeading
)

type panelMeasure interface {
	textWidth(font panelFont, text string) int
	lineHeight(font panelFont) int
}

// Every size derives from the body font, as a web page derives from its root
// font: line is the body font's line height and unit half of it.
type panelMetrics struct{ line, unit, pad, control, inset, gap int }

func panelMetricsOf(m panelMeasure) panelMetrics {
	line := max(2, m.lineHeight(panelBody))
	unit := line / 2
	return panelMetrics{line: line, unit: unit, pad: 2 * unit, control: line + unit + 2, inset: unit + unit/2, gap: unit + unit/2}
}

// panelNameCap is the account column's widest, in units: about 34 average
// characters of the body font. A longer name ends in an ellipsis.
const panelNameCap = 36

type panelRect struct{ x, y, width, height int }
type panelSpan struct{ x, width int }
type panelPlace struct{ x, row int }

// panelGeometry places everything in client coordinates. The list is a
// viewport the full width of the panel; row i's top inside it is i*row less
// the scroll offset.
type panelGeometry struct {
	metrics                  panelMetrics
	width, height            int
	title, header, list      panelRect
	note                     panelRect
	footer                   int // the rule above the footer buttons
	columns                  []panelSpan
	row, line1, line2, share int
	first, second            int // the two lines' heights
	content                  int
	scroll                   bool
	buttons                  []panelRect // one per Panel.Actions entry
}

func panelShareWidth(m panelMeasure) int { return m.textWidth(panelBody, "100%") }

// panelLabelWidth is a plan's outlined label: the text, half a unit each side
// and the one-pixel outline.
func panelLabelWidth(m panelMeasure, metrics panelMetrics, plan string) int {
	return m.textWidth(panelSmall, plan) + metrics.unit + 2
}

func panelSecondLineWidth(m panelMeasure, metrics panelMetrics, row PanelRow) int {
	width := 0
	if row.Plan != "" {
		width = panelLabelWidth(m, metrics, row.Plan)
	}
	for _, text := range []string{row.Tag, row.Warning} {
		if text == "" {
			continue
		}
		if width > 0 {
			width += metrics.unit / 2
		}
		width += m.textWidth(panelSmall, text)
	}
	return width
}

// panelColumnWidths are the account column, each limit column and the
// action column, each as wide as its widest content.
func panelColumnWidths(p Panel, m panelMeasure, metrics panelMetrics) []int {
	limits := max(0, len(p.Columns)-1)
	widths := make([]int, limits+2)
	if len(p.Columns) > 0 {
		widths[0] = m.textWidth(panelBody, p.Columns[0].Label)
	}
	header := widths[0]
	for _, row := range p.Rows {
		widths[0] = max(widths[0], m.textWidth(panelStrong, row.Name), panelSecondLineWidth(m, metrics, row))
	}
	widths[0] = max(header, min(widths[0], panelNameCap*metrics.unit))
	least := panelShareWidth(m) + metrics.unit/2 + 3*metrics.unit
	for i := range limits {
		widths[i+1] = max(m.textWidth(panelBody, p.Columns[i+1].Label), least, m.textWidth(panelBody, "—"))
		for _, row := range p.Rows {
			if i < len(row.Cells) {
				widths[i+1] = max(widths[i+1], m.textWidth(panelSmall, row.Cells[i].Reset))
			}
		}
	}
	widths[limits+1] = max(m.textWidth(panelBody, "Switch"), m.textWidth(panelStrong, "In use")) + 2*metrics.inset
	return widths
}

func panelRight(action Item) bool { return action.Kind == KindToggle || action.ID == "open" }

// panelPill is a switch's track: twice as wide as it is tall.
func panelPill(metrics panelMetrics) (width, height int) {
	height = max(6, metrics.line*3/4)
	return 2 * height, height
}

func panelFooterWidth(action Item, m panelMeasure, metrics panelMetrics) int {
	if action.Kind == KindToggle {
		pill, _ := panelPill(metrics)
		state := max(m.textWidth(panelBody, "on"), m.textWidth(panelBody, "off"))
		return m.textWidth(panelBody, action.Title) + metrics.unit/2 + pill + metrics.unit/2 + state + metrics.unit
	}
	return m.textWidth(panelBody, action.Title) + 2*metrics.inset
}

// panelFooterPlace puts the left buttons at the left and the right ones
// flush with the right edge, on one row while width holds both groups with
// spacer between them. Otherwise each group flows onto rows of its own, the
// right group's rows still right-aligned.
func panelFooterPlace(widths []int, right []bool, width, gap, spacer int) []panelPlace {
	out := make([]panelPlace, len(widths))
	total := func(side bool) int {
		sum, n := 0, 0
		for i, w := range widths {
			if right[i] == side {
				sum, n = sum+w, n+1
			}
		}
		return sum + max(0, n-1)*gap
	}
	left, rightWidth := total(false), total(true)
	if left == 0 || rightWidth == 0 {
		spacer = 0
	}
	if left+spacer+rightWidth <= width {
		x, rx := 0, width-rightWidth
		for i, w := range widths {
			if right[i] {
				out[i], rx = panelPlace{rx, 0}, rx+w+gap
			} else {
				out[i], x = panelPlace{x, 0}, x+w+gap
			}
		}
		return out
	}
	row := 0
	flow := func(side bool) {
		x, used := 0, false
		ends := map[int]int{}
		for i, w := range widths {
			if right[i] != side {
				continue
			}
			if x > 0 && x+w > width {
				row, x = row+1, 0
			}
			out[i], x, used = panelPlace{x, row}, x+w+gap, true
			ends[row] = x - gap
		}
		if side {
			for i := range widths {
				if right[i] {
					out[i].x += width - ends[out[i].row]
				}
			}
		}
		if used {
			row++
		}
	}
	flow(false)
	flow(true)
	return out
}

func panelFooterMinWidth(widths []int, right []bool, gap int) int {
	left, group, count := 0, 0, 0
	for i, w := range widths {
		if right[i] {
			group, count = group+w, count+1
		} else {
			left = max(left, w)
		}
	}
	return max(left, group+max(0, count-1)*gap)
}

// layoutPanel sizes the panel to its content within maxWidth by maxHeight
// (the work area less the window frame). Too tall, the list scrolls and
// gains a scroll bar; too wide, the account column gives way.
func layoutPanel(p Panel, m panelMeasure, maxWidth, maxHeight, scrollbar int) panelGeometry {
	metrics := panelMetricsOf(m)
	g := panelGeometry{metrics: metrics, share: panelShareWidth(m)}
	unit, pad := metrics.unit, metrics.pad
	small := m.lineHeight(panelSmall)
	first := max(m.lineHeight(panelStrong), metrics.line)
	// The second line's 4 px hold a plan label's outline and its padding.
	g.first, g.second = first, small+4
	g.line1 = unit * 3 / 4
	g.line2 = g.line1 + first + unit/4
	g.row = max(g.line2+g.second+unit*3/4, metrics.control+unit)
	g.content = len(p.Rows) * g.row
	widths := panelColumnWidths(p, m, metrics)
	least := max(m.textWidth(panelBody, firstLabel(p)), 8*unit)
	var buttonWidths []int
	var right []bool
	for _, action := range p.Actions {
		buttonWidths = append(buttonWidths, panelFooterWidth(action, m, metrics))
		right = append(right, panelRight(action))
	}
	footerWidth := panelFooterMinWidth(buttonWidths, right, metrics.gap)
	for range 2 {
		list := 0
		for _, w := range widths {
			list += w
		}
		list += (len(widths) - 1) * metrics.gap
		g.width = max(list, footerWidth, m.textWidth(panelHeading, p.Title)) + 2*pad
		if g.scroll {
			g.width += scrollbar
		}
		if over := g.width - maxWidth; over > 0 {
			shrink := min(over, max(0, widths[0]-least))
			widths[0] -= shrink
			g.width -= shrink
		}
		g.title = panelRect{pad, unit + unit/2, g.width - 2*pad, m.lineHeight(panelHeading)}
		g.header = panelRect{0, g.title.y + g.title.height + unit, g.width, metrics.line + unit}
		g.list = panelRect{0, g.header.y + g.header.height, g.width, max(1, len(p.Rows)) * g.row}
		inner := g.width - 2*pad
		places := panelFooterPlace(buttonWidths, right, inner, metrics.gap, 2*unit)
		rows := 0
		for _, place := range places {
			rows = max(rows, place.row+1)
		}
		footerHeight := unit + rows*metrics.control + max(0, rows-1)*(unit/2) + unit + unit/2
		fixed := g.list.y + unit/2 + metrics.line + unit/2 + footerHeight
		if over := fixed + g.list.height - maxHeight; over > 0 && !g.scroll && len(p.Rows) > 1 {
			g.scroll = true
			continue
		}
		if g.scroll {
			g.list.height = max(g.row, min(g.list.height, maxHeight-fixed))
		}
		g.note = panelRect{pad, g.list.y + g.list.height + unit/2, inner, metrics.line}
		g.footer = g.note.y + g.note.height + unit/2
		g.height = g.footer + footerHeight
		g.buttons = make([]panelRect, len(places))
		for i, place := range places {
			g.buttons[i] = panelRect{pad + place.x, g.footer + unit + place.row*(metrics.control+unit/2), buttonWidths[i], metrics.control}
		}
		break
	}
	// A wider footer or title widens the account column, so the Switch
	// buttons still end at the right edge.
	used := (len(widths) - 1) * metrics.gap
	for _, w := range widths {
		used += w
	}
	scrollbarWidth := 0
	if g.scroll {
		scrollbarWidth = scrollbar
	}
	widths[0] += max(0, g.width-2*pad-scrollbarWidth-used)
	x := pad
	g.columns = make([]panelSpan, len(widths))
	for i, w := range widths {
		g.columns[i] = panelSpan{x, w}
		x += w + metrics.gap
	}
	return g
}

func firstLabel(p Panel) string {
	if len(p.Columns) == 0 {
		return ""
	}
	return p.Columns[0].Label
}

// switchRect is row i's Switch button in the list viewport.
func (g panelGeometry) switchRect(i, scroll int) panelRect {
	action := g.columns[len(g.columns)-1]
	return panelRect{action.x, i*g.row + (g.row-g.metrics.control)/2 - scroll, action.width, g.metrics.control}
}

func (g panelGeometry) maxScroll() int { return max(0, g.content-g.list.height) }

// scrollToShow is the least scroll that shows row i whole.
func (g panelGeometry) scrollToShow(i, scroll int) int {
	top := i * g.row
	switch {
	case top < scroll:
		return top
	case top+g.row > scroll+g.list.height:
		return top + g.row - g.list.height
	}
	return scroll
}

// panelSummary is a row in words, the Switch button's window text: a screen
// reader cannot see painted text.
func panelSummary(columns []Column, row PanelRow) string {
	parts := []string{"Switch to " + row.Name}
	if row.Active {
		parts = []string{row.Name, "in use"}
	}
	for _, text := range []string{row.Plan, row.Tag, row.Warning} {
		if text != "" {
			parts = append(parts, text)
		}
	}
	for i, cell := range row.Cells {
		if !cell.Absent && i+1 < len(columns) {
			parts = append(parts, columns[i+1].Label+" "+cell.Share)
		}
	}
	return strings.Join(parts, ", ")
}

func panelToggleText(action Item) string {
	if action.Checked {
		return action.Title + ", on"
	}
	return action.Title + ", off"
}

// While the engine is out of reach only what works without it stays usable:
// Start at login and the updates act on this machine.
func panelCommandEnabled(panel Panel, action Item) bool {
	if action.Disabled {
		return false
	}
	return !panel.Offline || action.ID != "auto"
}

func panelSwitchEnabled(panel Panel, row PanelRow) bool {
	return !panel.Offline && !row.Active && !row.Disabled
}

func clonePanel(p Panel) Panel {
	p.Columns = append([]Column(nil), p.Columns...)
	p.Rows = append([]PanelRow(nil), p.Rows...)
	for i := range p.Rows {
		p.Rows[i].Cells = append([]PanelCell(nil), p.Rows[i].Cells...)
	}
	p.Actions = append([]Item(nil), p.Actions...)
	return p
}

type panelColors struct{ background, header, text, muted, line, selection, accent, track, limit, disabledAccent uintptr }

// panelPalette is the panel's colours outside high contrast. Text, muted,
// limit and accent keep 4.5:1 against the background; the limit colour is
// the approved picture's orange.
func panelPalette(dark bool, accent uintptr) panelColors {
	if dark {
		return panelColors{0x202020, 0x292929, 0xf0f0f0, 0xb6b6b6, 0x424242, 0x493c33, accent, 0x404040, colorRef("#ffb454"), panelDimAccent(accent, 0x202020)}
	}
	return panelColors{0xffffff, 0xf8f6f5, 0x222222, 0x666666, 0xe4dcd7, 0xfcf3ed, accent, 0xdedede, colorRef("#a84300"), panelDimAccent(accent, 0xffffff)}
}

func panelDimAccent(accent, background uintptr) uintptr {
	var color uintptr
	for shift := 0; shift < 24; shift += 8 {
		color |= (3*((accent>>shift)&0xff) + ((background >> shift) & 0xff)) / 4 << shift
	}
	return color
}

// colorRef turns "#rrggbb" into a Windows COLORREF (0x00bbggrr).
func colorRef(hex string) uintptr {
	rgb, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	return uintptr((rgb&0xff)<<16 | rgb&0xff00 | (rgb>>16)&0xff)
}
