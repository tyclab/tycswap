//go:build windows

package tray

import "unsafe"

type panelColors struct{ background, header, text, muted, line, selection, selectedText, accent uintptr }
type panelCustomDraw struct {
	header panelHeader
	stage  uint32
	dc     uintptr
	rect   menuRect
	item   uintptr
	state  uint32
	param  uintptr
}
type panelListDraw struct {
	custom                     panelCustomDraw
	textColor, backgroundColor uint32
	subItem                    int32
}
type panelPaint struct {
	dc              uintptr
	erase           int32
	rect            menuRect
	restore, update int32
	reserved        [32]byte
}
type panelMonitor struct {
	size          uint32
	monitor, work menuRect
	flags         uint32
}

func panelSystemColor(index uintptr) uintptr {
	value, _, _ := user32.NewProc("GetSysColor").Call(index)
	return value
}

func colorsForPanel(theme menuTheme) panelColors {
	if theme == menuHighContrast {
		return panelColors{panelSystemColor(5), panelSystemColor(15), panelSystemColor(8), panelSystemColor(17), panelSystemColor(6), panelSystemColor(13), panelSystemColor(14), panelSystemColor(8)}
	}
	if theme == menuDark {
		return panelColors{0x202020, 0x292929, 0xf0f0f0, 0xb6b6b6, 0x424242, 0x493c33, 0xf0f0f0, themeAccent(theme)}
	}
	return panelColors{0xffffff, 0xf8f6f5, 0x222222, 0x666666, 0xe4dcd7, 0xfcf3ed, 0x222222, themeAccent(theme)}
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
	seen := make(map[uintptr]bool)
	for _, object := range []uintptr{p.font, p.bold, p.titleFont, p.background, p.headerBrush, p.selectionBrush} {
		if object != 0 && object != stock && !seen[object] {
			menuDeleteObject.Call(object)
			seen[object] = true
		}
	}
	if p.imageList != 0 {
		panelControls.NewProc("ImageList_Destroy").Call(p.imageList)
	}
	p.font, p.bold, p.titleFont, p.background, p.headerBrush, p.selectionBrush, p.imageList = 0, 0, 0, 0, 0, 0, 0
}

func (p *windowsPanel) refreshTheme() {
	if p.hwnd == 0 {
		return
	}
	p.captureEdit()
	p.rendering = true
	defer func() { p.rendering = false }()
	stock, _, _ := menuStockObject.Call(17)
	for id, control := range p.controls {
		panelSend.Call(control, panelSetFont, stock, 0)
		if id == panelAccounts || id == panelFields {
			panelSend.Call(control, panelListBase+3, 1, 0)
		}
	}
	p.freeTheme()
	p.theme = readMenuTheme()
	p.palette = colorsForPanel(p.theme)
	var metrics menuNonClientMetrics
	metrics.size = uint32(unsafe.Sizeof(metrics))
	menuSystemParameters.Call(0x29, uintptr(metrics.size), uintptr(unsafe.Pointer(&metrics)), 0)
	font := metrics.menuFont
	if font.faceName[0] == 0 {
		copyUTF16(font.faceName[:], "Segoe UI")
	}
	font.height, font.weight = int32(-p.scale(13)), 400
	p.font, _, _ = menuCreateFont.Call(uintptr(unsafe.Pointer(&font)))
	if p.font == 0 {
		p.font, _, _ = menuStockObject.Call(17)
	}
	font.weight = 700
	p.bold, _, _ = menuCreateFont.Call(uintptr(unsafe.Pointer(&font)))
	if p.bold == 0 {
		p.bold = p.font
	}
	font.height = int32(-p.scale(16))
	p.titleFont, _, _ = menuCreateFont.Call(uintptr(unsafe.Pointer(&font)))
	if p.titleFont == 0 {
		p.titleFont = p.bold
	}
	brush := menuGDI.NewProc("CreateSolidBrush")
	p.background, _, _ = brush.Call(p.palette.background)
	p.headerBrush, _, _ = brush.Call(p.palette.header)
	p.selectionBrush, _, _ = brush.Call(p.palette.selection)
	p.imageList, _, _ = panelControls.NewProc("ImageList_Create").Call(1, uintptr(p.scale(26)), 0x20, 0, 1)
	for id, control := range p.controls {
		font := p.font
		if id == 100 {
			font = p.titleFont
		}
		if id == 220 || id == 225 {
			font = p.bold
		}
		panelSend.Call(control, panelSetFont, font, 1)
		if id == panelAccounts || id == panelFields {
			panelSend.Call(control, panelListBase+1, 0, p.palette.background)
			panelSend.Call(control, panelListBase+36, 0, p.palette.text)
			panelSend.Call(control, panelListBase+38, 0, p.palette.background)
			panelSend.Call(control, panelListBase+3, 1, p.imageList)
		}
	}
	p.columnsFor(panelAccounts, p.model.panel.Columns)
	p.columnsFor(panelFields, panelFieldColumns)
	p.fitColumns(panelAccounts, p.accountWidths())
	p.fitColumns(panelFields, p.fieldWidths())
	panelInvalidate.Call(p.hwnd, 0, 1)
	p.layout()
}

func clampPanelRect(anchor point, work menuRect, width, height int32) menuRect {
	width = min(width, work.right-work.left)
	height = min(height, work.bottom-work.top)
	x := max(work.left, min(anchor.x-width, work.right-width))
	y := max(work.top, min(anchor.y-height, work.bottom-height))
	return menuRect{x, y, x + width, y + height}
}

func (p *windowsPanel) position() {
	if p.anchor == nil {
		p.anchor = new(point)
		pGetCursorPos.Call(uintptr(unsafe.Pointer(p.anchor)))
	}
	anchor := *p.anchor
	monitor, _, _ := user32.NewProc("MonitorFromPoint").Call(uintptr(uint64(uint32(anchor.x))|uint64(uint32(anchor.y))<<32), 2)
	info := panelMonitor{size: uint32(unsafe.Sizeof(panelMonitor{}))}
	if monitor == 0 || func() bool {
		ok, _, _ := user32.NewProc("GetMonitorInfoW").Call(monitor, uintptr(unsafe.Pointer(&info)))
		return ok == 0
	}() {
		info.work = menuRect{0, 0, 1920, 1080}
	}
	width, height := p.contentSize()
	var window, client menuRect
	user32.NewProc("GetWindowRect").Call(p.hwnd, uintptr(unsafe.Pointer(&window)))
	panelGetClient.Call(p.hwnd, uintptr(unsafe.Pointer(&client)))
	width += int(window.right - window.left - client.right)
	height += int(window.bottom - window.top - client.bottom)
	rect := clampPanelRect(anchor, info.work, int32(width), int32(height))
	user32.NewProc("SetWindowPos").Call(p.hwnd, 0, uintptr(rect.left), uintptr(rect.top), uintptr(rect.right-rect.left), uintptr(rect.bottom-rect.top), 0x14)
	panelInvalidate.Call(p.hwnd, 0, 1)
}

func (p *windowsPanel) layout() {
	if p.hwnd == 0 || len(p.controls) == 0 {
		return
	}
	var rect menuRect
	panelGetClient.Call(p.hwnd, uintptr(unsafe.Pointer(&rect)))
	width, height := int(rect.right), int(rect.bottom)
	pad := p.scale(16)
	move := func(id, x, y, w, h int) {
		if control := p.controls[id]; control != 0 {
			panelMove.Call(control, uintptr(x), uintptr(y), uintptr(max(1, w)), uintptr(max(1, h)), 1)
		}
	}
	move(100, pad, p.scale(14), p.scale(130), p.scale(22))
	move(101, p.scale(150), p.scale(16), width-p.scale(314), p.scale(18))
	move(panelDashboard, width-pad-p.scale(140), p.scale(10), p.scale(140), p.scale(30))
	move(panelAccountTab, pad, p.scale(50), p.scale(104), p.scale(34))
	move(panelSettingsTab, pad+p.scale(112), p.scale(50), p.scale(104), p.scale(34))
	buttons, footerHeight := panelFooterLayout(p.model.panel.Actions, (width-2*pad)*96/p.dpi, p.model.settings)
	footer := height - p.scale(footerHeight)
	move(228, pad, footer+p.scale(footerHeight-24), width-2*pad, p.scale(20))
	if p.controls[228] == 0 {
		instance, _, _ := pGetModuleHandleW.Call(0)
		control, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16z("STATIC"))), uintptr(unsafe.Pointer(utf16z(""))), panelChild|panelVisible,
			uintptr(pad), uintptr(footer+p.scale(footerHeight-24)), uintptr(width-2*pad), uintptr(p.scale(20)), p.hwnd, 228, instance, 0)
		p.controls[228] = control
		panelSend.Call(control, panelSetFont, p.font, 1)
	}
	status := p.model.panel.Status
	if p.errorText != "" {
		status = p.errorText
	}
	p.setText(228, status)
	shown := make(map[string]bool)
	for _, button := range buttons {
		shown[button.id] = true
	}
	for key, id := range p.actionControls {
		if !shown[key] {
			panelShow.Call(p.controls[id], 0)
		}
	}
	for _, button := range buttons {
		id := p.actionControls[button.id]
		move(id, pad+p.scale(button.x), footer+p.scale(button.y), p.scale(button.width), p.scale(28))
		panelShow.Call(p.controls[id], 5)
	}
	for _, id := range []int{panelAccounts, 220, 221, 222} {
		panelShow.Call(p.controls[id], uintptr(boolValue(!p.model.settings))*5)
	}
	for _, id := range []int{224, panelScopes, panelFields, 225, 226, panelEdit, panelChoice, panelSave, panelReset, 227} {
		panelShow.Call(p.controls[id], uintptr(boolValue(p.model.settings))*5)
	}
	if p.model.settings {
		move(224, pad, p.scale(104), p.scale(66), p.scale(20))
		move(panelScopes, pad+p.scale(74), p.scale(98), p.scale(220), p.scale(160))
		detail := footer - p.scale(144)
		move(panelFields, pad, p.scale(136), width-2*pad, detail-p.scale(144))
		p.fillLastColumn(panelFields)
		if p.revealPending[panelFields] {
			p.revealSelection(panelFields)
		}
		move(225, pad, detail, width-2*pad, p.scale(20))
		move(226, pad, detail+p.scale(22), width-2*pad, p.scale(52))
		move(panelEdit, pad, detail+p.scale(78), width-2*pad-p.scale(176), p.scale(28))
		move(panelChoice, pad, detail+p.scale(78), width-2*pad-p.scale(176), p.scale(180))
		move(panelSave, width-pad-p.scale(168), detail+p.scale(78), p.scale(78), p.scale(28))
		move(panelReset, width-pad-p.scale(82), detail+p.scale(78), p.scale(82), p.scale(28))
		move(227, pad, detail+p.scale(110), width-2*pad, p.scale(28))
		p.rendering = true
		p.renderEditor()
		p.rendering = false
	} else {
		detail := footer - p.scale(p.detailHeight())
		lines := p.scale(p.detailLines()*18 + 4)
		move(panelAccounts, pad, p.scale(98), width-2*pad, detail-p.scale(106))
		p.fillLastColumn(panelAccounts)
		if p.revealPending[panelAccounts] {
			p.revealSelection(panelAccounts)
		}
		move(220, pad, detail, width-2*pad, p.scale(20))
		move(221, pad, detail+p.scale(22), width-2*pad, lines)
		p.fitScroll(221)
		move(222, pad, detail+p.scale(28)+lines, width-2*pad, p.scale(18))
	}
	panelInvalidate.Call(p.controls[panelAccountTab], 0, 1)
	panelInvalidate.Call(p.controls[panelSettingsTab], 0, 1)
}

// Sized for the longest row so moving the selection never resizes the table.
func (p *windowsPanel) detailLines() int {
	lines := 1
	for _, row := range p.model.panel.Rows {
		lines = max(lines, len(row.Details))
	}
	return min(lines, 7)
}

func (p *windowsPanel) detailHeight() int { return 60 + p.detailLines()*18 }

func (p *windowsPanel) paint() uintptr {
	var paint panelPaint
	dc, _, _ := user32.NewProc("BeginPaint").Call(p.hwnd, uintptr(unsafe.Pointer(&paint)))
	if dc != 0 {
		menuFillRect.Call(dc, uintptr(unsafe.Pointer(&paint.rect)), p.background)
		var rect menuRect
		panelGetClient.Call(p.hwnd, uintptr(unsafe.Pointer(&rect)))
		line := menuRect{0, int32(p.scale(84)), rect.right, int32(p.scale(85))}
		brush, _, _ := menuGDI.NewProc("CreateSolidBrush").Call(p.palette.line)
		menuFillRect.Call(dc, uintptr(unsafe.Pointer(&line)), brush)
		_, footerHeight := panelFooterLayout(p.model.panel.Actions, (int(rect.right)-2*p.scale(16))*96/p.dpi, p.model.settings)
		line.top, line.bottom = rect.bottom-int32(p.scale(footerHeight+4)), rect.bottom-int32(p.scale(footerHeight+3))
		menuFillRect.Call(dc, uintptr(unsafe.Pointer(&line)), brush)
		menuDeleteObject.Call(brush)
	}
	user32.NewProc("EndPaint").Call(p.hwnd, uintptr(unsafe.Pointer(&paint)))
	return 0
}

func (p *windowsPanel) colorControl(dc, control uintptr) uintptr {
	color := p.palette.text
	id, _, _ := user32.NewProc("GetDlgCtrlID").Call(control)
	if id == 101 || id == 221 || id == 222 || id == 224 || id == 226 || id == 227 || id == 228 {
		color = p.palette.muted
	}
	menuTextColor.Call(dc, color)
	menuGDI.NewProc("SetBkColor").Call(dc, p.palette.background)
	return p.background
}

func (p *windowsPanel) drawButton(draw *drawMenuItem) bool {
	if draw.ctlType != 4 {
		return false
	}
	id := int(draw.ctlID)
	selectedTab := id == panelAccountTab && !p.model.settings || id == panelSettingsTab && p.model.settings
	primary := id == panelSave
	brush, color, font := p.background, p.palette.text, p.font
	if selectedTab || id == panelDashboard {
		color, font = p.palette.accent, p.bold
	}
	if primary && draw.itemState&4 == 0 {
		brush, _, _ = menuGDI.NewProc("CreateSolidBrush").Call(p.palette.accent)
		defer menuDeleteObject.Call(brush)
		color, font = p.palette.background, p.bold
	}
	if draw.itemState&1 != 0 {
		brush = p.selectionBrush
	}
	if draw.itemState&4 != 0 {
		color = p.palette.muted
	}
	menuFillRect.Call(draw.hdc, uintptr(unsafe.Pointer(&draw.rect)), brush)
	if id != panelAccountTab && id != panelSettingsTab {
		border, _, _ := menuGDI.NewProc("CreateSolidBrush").Call(p.palette.line)
		user32.NewProc("FrameRect").Call(draw.hdc, uintptr(unsafe.Pointer(&draw.rect)), border)
		menuDeleteObject.Call(border)
	}
	saved, _, _ := menuSaveDC.Call(draw.hdc)
	defer menuRestoreDC.Call(draw.hdc, saved)
	menuSelectObject.Call(draw.hdc, font)
	menuBkMode.Call(draw.hdc, 1)
	menuTextColor.Call(draw.hdc, color)
	text := panelControlText(draw.hwndItem)
	rect := draw.rect
	menuDrawText.Call(draw.hdc, uintptr(unsafe.Pointer(utf16z(text))), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), dtSingleLine|dtVCenter|dtNoPrefix|1|0x8000)
	if selectedTab {
		line := menuRect{rect.left, rect.bottom - int32(p.scale(2)), rect.right, rect.bottom}
		accent, _, _ := menuGDI.NewProc("CreateSolidBrush").Call(p.palette.accent)
		menuFillRect.Call(draw.hdc, uintptr(unsafe.Pointer(&line)), accent)
		menuDeleteObject.Call(accent)
	}
	if draw.itemState&0x10 != 0 {
		user32.NewProc("DrawFocusRect").Call(draw.hdc, uintptr(unsafe.Pointer(&rect)))
	}
	return true
}

func (p *windowsPanel) drawCombo(draw *drawMenuItem) bool {
	if draw.ctlType != 3 {
		return false
	}
	labels := p.comboValues[int(draw.ctlID)]
	text := ""
	if int32(draw.itemID) >= 0 && int(draw.itemID) < len(labels) {
		text = labels[draw.itemID]
	}
	color, brush := p.palette.text, p.background
	if draw.itemState&1 != 0 {
		color, brush = p.palette.selectedText, p.selectionBrush
	}
	if draw.itemState&4 != 0 {
		color = p.palette.muted
	}
	menuFillRect.Call(draw.hdc, uintptr(unsafe.Pointer(&draw.rect)), brush)
	saved, _, _ := menuSaveDC.Call(draw.hdc)
	defer menuRestoreDC.Call(draw.hdc, saved)
	menuSelectObject.Call(draw.hdc, p.font)
	menuBkMode.Call(draw.hdc, 1)
	menuTextColor.Call(draw.hdc, color)
	rect := draw.rect
	rect.left += int32(p.scale(5))
	rect.right -= int32(p.scale(3))
	menuDrawText.Call(draw.hdc, uintptr(unsafe.Pointer(utf16z(text))), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), dtSingleLine|dtVCenter|dtNoPrefix|0x8000)
	if draw.itemState&0x10 != 0 {
		user32.NewProc("DrawFocusRect").Call(draw.hdc, uintptr(unsafe.Pointer(&rect)))
	}
	return true
}

func (p *windowsPanel) drawList(draw *panelListDraw) uintptr {
	if draw.custom.stage == 1 {
		return 0x20
	}
	if draw.custom.stage == 0x30001 {
		p.drawCell(draw.custom.header.hwnd, int(draw.custom.item), int(draw.subItem), draw.custom.dc)
		return 4
	}
	if draw.custom.stage != 0x10001 {
		return 0
	}
	hwnd, index := draw.custom.header.hwnd, int(draw.custom.item)
	count := 3
	if hwnd == p.controls[panelAccounts] {
		count = len(p.model.panel.Columns)
	}
	var bounds, client menuRect
	panelSend.Call(hwnd, panelListBase+14, uintptr(index), uintptr(unsafe.Pointer(&bounds)))
	panelGetClient.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	bounds.left, bounds.right = 0, client.right
	state, _, _ := panelSend.Call(hwnd, panelListBase+44, uintptr(index), 3)
	brush := p.background
	if state&2 != 0 {
		brush = p.selectionBrush
	}
	menuFillRect.Call(draw.custom.dc, uintptr(unsafe.Pointer(&bounds)), brush)
	for column := 0; column < count; column++ {
		p.drawCell(hwnd, index, column, draw.custom.dc)
	}
	focus, _, _ := user32.NewProc("GetFocus").Call()
	if focus == hwnd && state&1 != 0 {
		user32.NewProc("DrawFocusRect").Call(draw.custom.dc, uintptr(unsafe.Pointer(&bounds)))
	}
	return 4
}

func (p *windowsPanel) drawCell(hwnd uintptr, index, column int, dc uintptr) {
	if index < 0 || column < 0 {
		return
	}
	text, active, format := "", false, uintptr(0)
	if hwnd == p.controls[panelAccounts] {
		if index >= len(p.model.panel.Rows) || column >= len(p.model.panel.Columns) {
			return
		}
		row := p.model.panel.Rows[index]
		if column == 0 {
			text, active = row.Title, row.Active
		} else if column < len(row.Cells) {
			text = row.Cells[column]
		}
		if column > 0 && p.model.panel.Columns[column].ID != "owner" {
			format = 2
		}
	} else if hwnd == p.controls[panelFields] {
		scope := p.model.scope()
		if scope == nil || index >= len(scope.Settings) {
			return
		}
		field := scope.Settings[index]
		cells := []string{field.Label, field.Value, field.Source}
		if column >= len(cells) {
			return
		}
		text = cells[column]
	} else {
		return
	}
	rect := menuRect{top: int32(column)}
	panelSend.Call(hwnd, panelListBase+56, uintptr(index), uintptr(unsafe.Pointer(&rect)))
	if column == 0 {
		width, _, _ := panelSend.Call(hwnd, panelListBase+29, 0, 0)
		rect.right = rect.left + int32(width)
	}
	state, _, _ := panelSend.Call(hwnd, panelListBase+44, uintptr(index), 2)
	color, brush, font := p.palette.text, p.background, p.font
	if state&2 != 0 {
		color, brush = p.palette.selectedText, p.selectionBrush
	}
	if active {
		font = p.bold
		if p.theme != menuHighContrast {
			color = p.palette.accent
		}
	}
	menuFillRect.Call(dc, uintptr(unsafe.Pointer(&rect)), brush)
	saved, _, _ := menuSaveDC.Call(dc)
	defer menuRestoreDC.Call(dc, saved)
	menuSelectObject.Call(dc, font)
	menuBkMode.Call(dc, 1)
	menuTextColor.Call(dc, color)
	if column == 0 && hwnd == p.controls[panelAccounts] {
		if active {
			check := rect
			check.left += int32(p.scale(4))
			check.right = check.left + int32(p.scale(16))
			menuDrawText.Call(dc, uintptr(unsafe.Pointer(utf16z("✓"))), ^uintptr(0), uintptr(unsafe.Pointer(&check)), dtSingleLine|dtVCenter|dtNoPrefix)
		}
		rect.left += int32(p.scale(24))
	} else {
		rect.left += int32(p.scale(8))
	}
	rect.right -= int32(p.scale(6))
	menuDrawText.Call(dc, uintptr(unsafe.Pointer(utf16z(text))), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), dtSingleLine|dtVCenter|dtNoPrefix|format|0x8000)
}

func (p *windowsPanel) drawHeader(draw *panelCustomDraw) uintptr {
	if draw.stage == 1 {
		return 0x20
	}
	if draw.stage != 0x10001 {
		return 0
	}
	parent, _, _ := user32.NewProc("GetParent").Call(draw.header.hwnd)
	labels := []string{"Setting", "Value", "Source"}
	index := int(draw.item)
	format := uintptr(0)
	if parent == p.controls[panelAccounts] {
		labels = nil
		for _, column := range p.model.panel.Columns {
			labels = append(labels, column.Label)
		}
		if index > 0 && index < len(p.model.panel.Columns) && p.model.panel.Columns[index].ID != "owner" {
			format = 2
		}
		if index < len(p.model.panel.Columns) && p.model.panel.Columns[index].ID == p.model.panel.SortColumn {
			if p.model.panel.SortDescending {
				labels[index] += " ↓"
			} else {
				labels[index] += " ↑"
			}
		}
	}
	if index < 0 || index >= len(labels) {
		return 0
	}
	menuFillRect.Call(draw.dc, uintptr(unsafe.Pointer(&draw.rect)), p.headerBrush)
	menuSelectObject.Call(draw.dc, p.font)
	menuBkMode.Call(draw.dc, 1)
	menuTextColor.Call(draw.dc, p.palette.muted)
	rect := draw.rect
	rect.left += int32(p.scale(8))
	rect.right -= int32(p.scale(6))
	if parent == p.controls[panelAccounts] && index == 0 {
		rect.left += int32(p.scale(16))
	}
	menuDrawText.Call(draw.dc, uintptr(unsafe.Pointer(utf16z(labels[index]))), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), dtSingleLine|dtVCenter|dtNoPrefix|format|0x8000)
	return 4
}
