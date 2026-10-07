//go:build windows

package tray

import (
	"fmt"
	"reflect"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	panelControls      = windows.NewLazySystemDLL("comctl32.dll")
	panelSend          = user32.NewProc("SendMessageW")
	panelShow          = user32.NewProc("ShowWindow")
	panelMove          = user32.NewProc("MoveWindow")
	panelSetText       = user32.NewProc("SetWindowTextW")
	panelGetText       = user32.NewProc("GetWindowTextW")
	panelGetTextLength = user32.NewProc("GetWindowTextLengthW")
	panelEnable        = user32.NewProc("EnableWindow")
	panelGetClient     = user32.NewProc("GetClientRect")
	panelSetFocus      = user32.NewProc("SetFocus")
	panelInvalidate    = user32.NewProc("InvalidateRect")
	panelSubclass      = panelControls.NewProc("SetWindowSubclass")
	panelDefSubclass   = panelControls.NewProc("DefSubclassProc")
)

const (
	panelChild        = 0x40000000
	panelVisible      = 0x10000000
	panelTabStop      = 0x10000
	panelBorder       = 0x800000
	panelNotify       = 0x004e
	panelSetFont      = 0x0030
	panelListBase     = 0x1000
	panelComboReset   = 0x014b
	panelComboAdd     = 0x0143
	panelComboSelect  = 0x014e
	panelComboCurrent = 0x0147
	panelAccountTab   = 201
	panelSettingsTab  = 202
	panelAccounts     = 203
	panelTargets      = 204
	panelUse          = 205
	panelScopes       = 206
	panelFields       = 207
	panelEdit         = 208
	panelChoice       = 209
	panelSave         = 210
	panelReset        = 211
	panelDashboard    = 212
	panelActionBase   = 300
)

type panelHeader struct {
	hwnd, id uintptr
	code     uint32
}
type panelListItem struct {
	mask                         uint32
	item, subItem                int32
	state, stateMask             uint32
	text                         *uint16
	textMax, image               int32
	param                        uintptr
	indent, groupID              int32
	columns                      uint32
	columnIndices, columnFormats uintptr
	group                        int32
}
type panelListColumn struct {
	mask                                                               uint32
	format, width                                                      int32
	text                                                               *uint16
	textMax, subItem, image, order, minWidth, defaultWidth, idealWidth int32
}
type panelListNotification struct {
	header                      panelHeader
	item, subItem               int32
	newState, oldState, changed uint32
	action                      point
	param                       uintptr
}
type panelActionResult struct {
	action PanelAction
	err    error
}

type panelColumnsCache struct {
	columns []Column
	dpi     int
}
type panelRowsCache struct {
	keys []string
	rows [][]string
}

type windowsPanel struct {
	tray     *windowsTray
	hwnd     uintptr
	controls map[int]uintptr
	model    panelModel

	dpi                                                                       int
	theme                                                                     menuTheme
	palette                                                                   panelColors
	font, bold, titleFont, background, headerBrush, selectionBrush, imageList uintptr

	rendering      bool
	switchingPage  bool
	errorText      string
	fieldErrors    map[panelFieldID]string
	pending        map[panelFieldID]bool
	choiceValues   []string
	comboValues    map[int][]string
	actionControls map[string]int
	controlActions map[int]string
	nextAction     int
	columnCache    map[int]panelColumnsCache
	rowCache       map[int]panelRowsCache
}

func nativePanelStruct[T any](value uintptr) *T { return *(**T)(unsafe.Pointer(&value)) }

func panelWindowClass() string { return windowClass() + "Panel" }

func (t *windowsTray) SetPanel(value Panel) {
	value = clonePanel(value)
	t.mu.Lock()
	if t.panelSet && reflect.DeepEqual(t.panelData, value) {
		t.mu.Unlock()
		return
	}
	t.panelData, t.panelSet = value, true
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		pPostMessageW.Call(uintptr(hwnd), wmPanelUpdate, 0, 0)
	}
}

func (t *windowsTray) showPanel() bool {
	t.mu.Lock()
	available := t.panelSet
	t.mu.Unlock()
	if !available {
		return false
	}
	if t.panel == nil {
		if err := t.createPanel(); err != nil {
			_ = t.Notify("Tray overview unavailable", err.Error())
			return false
		}
	}
	t.panel.update()
	t.panel.position()
	panelShow.Call(t.panel.hwnd, 5)
	pSetForegroundWindow.Call(t.panel.hwnd)
	panelSetFocus.Call(t.panel.controls[panelAccounts])
	if t.panel.model.settings {
		panelSetFocus.Call(t.panel.controls[panelScopes])
	}
	return true
}

func (t *windowsTray) createPanel() error {
	init := struct{ size, classes uint32 }{8, 1}
	if ok, _, err := panelControls.NewProc("InitCommonControlsEx").Call(uintptr(unsafe.Pointer(&init))); ok == 0 {
		return fmt.Errorf("native controls: %w", err)
	}
	p := &windowsPanel{tray: t, controls: make(map[int]uintptr), dpi: 96, fieldErrors: make(map[panelFieldID]string), pending: make(map[panelFieldID]bool)}
	t.panel = p
	instance, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512)
	name := utf16z(panelWindowClass())
	class := wndClassExW{cbSize: uint32(unsafe.Sizeof(wndClassExW{})), lpfnWndProc: windows.NewCallback(panelWndProc), hInstance: windows.Handle(instance), hCursor: windows.Handle(cursor), lpszClassName: name}
	if atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 && err != windows.ERROR_CLASS_ALREADY_EXISTS {
		t.panel = nil
		return fmt.Errorf("overview window class: %w", err)
	}
	window, _, err := pCreateWindowExW.Call(0x10000|0x80, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(utf16z("tycswap overview"))),
		0x80000000|panelBorder|0x02000000, 0, 0, 620, 580, uintptr(t.hwnd), 0, instance, 0)
	if window == 0 {
		t.panel = nil
		return fmt.Errorf("overview window: %w", err)
	}
	p.hwnd = window
	p.readDPI()
	controls := []struct {
		id           int
		class, title string
		style        uintptr
	}{
		{100, "STATIC", "", 0}, {101, "STATIC", "", 0},
		{panelDashboard, "BUTTON", "Open dashboard", panelTabStop | 0xb},
		{panelAccountTab, "BUTTON", "Accounts", panelTabStop | 0xb}, {panelSettingsTab, "BUTTON", "Settings", panelTabStop | 0xb},
		{panelAccounts, "SysListView32", "Accounts", panelTabStop | 1 | 4 | 8 | 0x40 | 0x100000 | 0x200000},
		{220, "STATIC", "", 0}, {221, "EDIT", "", 4 | 0x40 | 0x800 | 0x200000}, {222, "STATIC", "", 0},
		{223, "STATIC", "Switch group", 0}, {panelTargets, "COMBOBOX", "", panelTabStop | 3 | 0x10 | 0x200 | 0x200000}, {panelUse, "BUTTON", "Use this account", panelTabStop | 0xb},
		{224, "STATIC", "Apply to", 0}, {panelScopes, "COMBOBOX", "", panelTabStop | 3 | 0x10 | 0x200 | 0x200000},
		{panelFields, "SysListView32", "Settings", panelTabStop | 1 | 4 | 8 | 0x40 | 0x100000 | 0x200000},
		{225, "STATIC", "", 0}, {226, "EDIT", "", 4 | 0x40 | 0x800 | 0x200000},
		{panelEdit, "EDIT", "", panelTabStop | panelBorder | 0x80}, {panelChoice, "COMBOBOX", "", panelTabStop | 3 | 0x10 | 0x200 | 0x200000},
		{panelSave, "BUTTON", "Save", panelTabStop | 0xb}, {panelReset, "BUTTON", "Reset", panelTabStop | 0xb},
		{227, "STATIC", "", 0},
	}
	for _, control := range controls {
		child, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16z(control.class))), uintptr(unsafe.Pointer(utf16z(control.title))),
			panelChild|panelVisible|control.style, 0, 0, 10, 10, window, uintptr(control.id), instance, 0)
		if child == 0 {
			p.close()
			t.panel = nil
			return fmt.Errorf("overview control %s: %w", control.class, err)
		}
		p.controls[control.id] = child
	}
	for _, id := range []int{panelAccounts, panelFields} {
		panelSend.Call(p.controls[id], panelListBase+54, 0, 0x20|0x10000|0x4000)
		panelSubclass.Call(p.controls[id], windows.NewCallback(panelListSubclass), 1, 0)
		header, _, _ := panelSend.Call(p.controls[id], panelListBase+31, 0, 0)
		panelSubclass.Call(header, windows.NewCallback(panelHeaderSubclass), 2, 0)
	}
	for _, id := range []int{panelTargets, panelScopes, panelChoice} {
		panelSubclass.Call(p.controls[id], windows.NewCallback(panelComboSubclass), 3, 0)
	}
	for _, id := range []int{221, 226} {
		panelSubclass.Call(p.controls[id], windows.NewCallback(panelListSubclass), 1, 0)
	}
	p.refreshTheme()
	p.update()
	return nil
}

func (p *windowsPanel) close() {
	if p.hwnd != 0 {
		pDestroyWindow.Call(p.hwnd)
		p.hwnd = 0
	}
	p.freeTheme()
}

func (p *windowsPanel) dialogMessage(message *msg) bool {
	visible, _, _ := user32.NewProc("IsWindowVisible").Call(p.hwnd)
	if visible == 0 {
		return false
	}
	child, _, _ := user32.NewProc("IsChild").Call(p.hwnd, uintptr(message.hwnd))
	if uintptr(message.hwnd) != p.hwnd && child == 0 {
		return false
	}
	if message.message == 0x100 && message.wParam == 0x1b {
		panelShow.Call(p.hwnd, 0)
		return true
	}
	handled, _, _ := user32.NewProc("IsDialogMessageW").Call(p.hwnd, uintptr(unsafe.Pointer(message)))
	return handled != 0
}

func (p *windowsPanel) setText(id int, value string) {
	control := p.controls[id]
	if control != 0 && panelControlText(control) != value {
		panelSetText.Call(control, uintptr(unsafe.Pointer(utf16z(value))))
	}
}

func panelControlText(control uintptr) string {
	length, _, _ := panelGetTextLength.Call(control)
	if length > 65536 {
		length = 65536
	}
	text := make([]uint16, int(length)+1)
	panelGetText.Call(control, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)))
	return windows.UTF16ToString(text)
}

func (p *windowsPanel) enable(id int, enabled bool) {
	panelEnable.Call(p.controls[id], uintptr(boolValue(enabled)))
}
func boolValue(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (p *windowsPanel) update() {
	p.captureEdit()
	p.tray.mu.Lock()
	value := p.tray.panelData
	p.tray.mu.Unlock()
	p.model.update(value)
	p.rendering = true
	defer func() { p.rendering = false }()
	p.setText(100, value.Title)
	p.setText(101, value.Version)
	panelSetText.Call(p.hwnd, uintptr(unsafe.Pointer(utf16z(value.Title+" overview"))))
	p.renderAccounts()
	p.renderScopes()
	p.renderFields()
	p.renderActions()
	p.layout()
}

func (p *windowsPanel) columnsFor(control int, columns []Column) {
	hwnd := p.controls[control]
	if p.columnCache == nil {
		p.columnCache = make(map[int]panelColumnsCache)
	}
	cache := p.columnCache[control]
	if cache.dpi == p.dpi && reflect.DeepEqual(cache.columns, columns) {
		return
	}
	for panelSendColumnCount(hwnd) > 0 {
		panelSend.Call(hwnd, panelListBase+28, 0, 0)
	}
	for i, column := range columns {
		format := int32(0)
		if control == panelAccounts && i > 0 && column.ID != "owner" {
			format = 1
		}
		item := panelListColumn{mask: 1 | 2 | 4, format: format, width: int32(p.scale(column.Width)), text: utf16z(column.Label)}
		panelSend.Call(hwnd, panelListBase+97, uintptr(i), uintptr(unsafe.Pointer(&item)))
	}
	p.columnCache[control] = panelColumnsCache{append([]Column(nil), columns...), p.dpi}
}

func panelSendColumnCount(hwnd uintptr) int {
	header, _, _ := panelSend.Call(hwnd, panelListBase+31, 0, 0)
	count, _, _ := panelSend.Call(header, 0x1200, 0, 0)
	return int(count)
}

func (p *windowsPanel) listRows(id int, keys []string, rows [][]string, selected int) {
	hwnd := p.controls[id]
	if p.rowCache == nil {
		p.rowCache = make(map[int]panelRowsCache)
	}
	cache := p.rowCache[id]
	current, _, _ := panelSend.Call(hwnd, panelListBase+12, ^uintptr(0), 2)
	ordered := reflect.DeepEqual(cache.keys, keys)
	if ordered && reflect.DeepEqual(cache.rows, rows) && int(current) == selected {
		return
	}
	top, _, _ := panelSend.Call(hwnd, panelListBase+39, 0, 0)
	topKey, selectedKey := "", ""
	if int(top) >= 0 && int(top) < len(cache.keys) {
		topKey = cache.keys[top]
	}
	if int(current) >= 0 && int(current) < len(cache.keys) {
		selectedKey = cache.keys[current]
	}
	selectedChanged := selected >= 0 && selected < len(keys) && keys[selected] != selectedKey
	panelSend.Call(hwnd, 0xb, 0, 0)
	if !ordered {
		panelSend.Call(hwnd, panelListBase+9, 0, 0)
	}
	for i, cells := range rows {
		for j, cell := range cells {
			if ordered && i < len(cache.rows) && j < len(cache.rows[i]) && cache.rows[i][j] == cell {
				continue
			}
			item := panelListItem{mask: 1, item: int32(i), subItem: int32(j), text: utf16z(cell)}
			message := uintptr(panelListBase + 116)
			if j == 0 && !ordered {
				message = panelListBase + 77
			}
			parameter := uintptr(i)
			if j == 0 && !ordered {
				parameter = 0
			}
			panelSend.Call(hwnd, message, parameter, uintptr(unsafe.Pointer(&item)))
		}
	}
	if selected >= 0 && (!ordered || int(current) != selected) {
		clear := panelListItem{stateMask: 3}
		panelSend.Call(hwnd, panelListBase+43, ^uintptr(0), uintptr(unsafe.Pointer(&clear)))
		item := panelListItem{state: 3, stateMask: 3}
		panelSend.Call(hwnd, panelListBase+43, uintptr(selected), uintptr(unsafe.Pointer(&item)))
	}
	if !ordered && topKey != "" {
		for i, key := range keys {
			if key != topKey {
				continue
			}
			var first menuRect
			panelSend.Call(hwnd, panelListBase+14, 0, uintptr(unsafe.Pointer(&first)))
			panelSend.Call(hwnd, panelListBase+20, 0, uintptr(i*int(first.bottom-first.top)))
			break
		}
	}
	if selectedChanged {
		panelSend.Call(hwnd, panelListBase+19, uintptr(selected), 0)
	}
	panelSend.Call(hwnd, 0xb, 1, 0)
	panelInvalidate.Call(hwnd, 0, 1)
	p.rowCache[id] = panelRowsCache{append([]string(nil), keys...), rows}
}

func (p *windowsPanel) renderAccounts() {
	columns := p.model.panel.Columns
	p.columnsFor(panelAccounts, columns)
	var rows [][]string
	var keys []string
	selected := -1
	for i, row := range p.model.panel.Rows {
		cells := append([]string(nil), row.Cells...)
		for len(cells) < len(columns) {
			cells = append(cells, "")
		}
		if len(cells) > 0 {
			cells[0] = row.Title
			if row.Active {
				cells[0] = "✓ " + row.Title
			}
		}
		rows = append(rows, cells)
		keys = append(keys, row.ID)
		if row.ID == p.model.rowID {
			selected = i
		}
	}
	p.listRows(panelAccounts, keys, rows, selected)
	header, _, _ := panelSend.Call(p.controls[panelAccounts], panelListBase+31, 0, 0)
	panelInvalidate.Call(header, 0, 1)
	p.renderDetails()
}

func (p *windowsPanel) combo(id int, labels []string, selected int) {
	hwnd := p.controls[id]
	if p.comboValues == nil {
		p.comboValues = make(map[int][]string)
	}
	previous := p.comboValues[id]
	equal := len(previous) == len(labels)
	for i := range labels {
		if !equal || previous[i] != labels[i] {
			equal = false
			break
		}
	}
	if equal {
		current, _, _ := panelSend.Call(hwnd, panelComboCurrent, 0, 0)
		if int(current) != selected {
			panelSend.Call(hwnd, panelComboSelect, uintptr(selected), 0)
		}
		return
	}
	p.comboValues[id] = append([]string(nil), labels...)
	panelSend.Call(hwnd, panelComboReset, 0, 0)
	for _, label := range labels {
		panelSend.Call(hwnd, panelComboAdd, 0, uintptr(unsafe.Pointer(utf16z(label))))
	}
	if selected >= 0 {
		panelSend.Call(hwnd, panelComboSelect, uintptr(selected), 0)
	}
}

func (p *windowsPanel) renderDetails() {
	row := p.model.row()
	var labels []string
	selected := -1
	if row == nil {
		p.setText(220, "No accounts")
		p.setText(221, "Open the dashboard to add an account.")
	} else {
		p.setText(220, row.Title)
		p.setText(221, strings.Join(row.Details, "\r\n"))
		for i, target := range row.Targets {
			labels = append(labels, target.Label)
			if target.ID == p.model.targetID {
				selected = i
			}
		}
	}
	p.combo(panelTargets, labels, selected)
	reason, enabled := "", false
	if target := p.model.target(); target != nil {
		reason, enabled = target.Reason, !target.Disabled
	}
	if p.model.panel.Offline {
		reason, enabled = "Reconnect before switching accounts.", false
	}
	p.setText(222, reason)
	p.enable(panelUse, enabled)
	p.enable(panelTargets, len(labels) > 0)
}

func (p *windowsPanel) renderScopes() {
	var labels []string
	selected := -1
	for i, scope := range p.model.panel.Settings {
		labels = append(labels, scope.Label)
		if scope.ID == p.model.scopeID {
			selected = i
		}
	}
	p.combo(panelScopes, labels, selected)
}

func (p *windowsPanel) renderFields() {
	p.columnsFor(panelFields, []Column{{Label: "Setting", Width: 238}, {Label: "Value", Width: 96}, {Label: "Source", Width: 210}})
	var rows [][]string
	var keys []string
	selected := -1
	if scope := p.model.scope(); scope != nil {
		for i, field := range scope.Settings {
			rows = append(rows, []string{field.Label, field.Value, field.Source})
			keys = append(keys, scope.ID+":"+field.Key)
			if field.Key == p.model.fieldKey {
				selected = i
			}
		}
	}
	p.listRows(panelFields, keys, rows, selected)
	p.renderEditor()
}

func (p *windowsPanel) captureEdit() {
	if p.rendering || p.controls[panelEdit] == 0 || p.model.field() == nil {
		return
	}
	field := p.model.field()
	if field.Kind == "bool" || field.Kind == "choice" {
		return
	}
	p.model.edit(panelControlText(p.controls[panelEdit]))
}

func (p *windowsPanel) renderEditor() {
	field := p.model.field()
	choice := false
	editable := field != nil && field.ReadOnly == "" && !p.model.panel.Offline && p.tray.opts.OnPanelAction != nil
	if field == nil {
		p.setText(225, "No settings available")
		p.setText(226, "")
	} else {
		p.setText(225, field.Label)
		p.setText(226, field.Description+"\r\n"+field.Applies+" · "+field.Source)
		choice = field.Kind == "bool" || field.Kind == "choice"
		p.choiceValues = append([]string(nil), field.Choices...)
		if field.Kind == "bool" {
			p.choiceValues = []string{"false", "true"}
		}
		if choice {
			selected := -1
			for i, value := range p.choiceValues {
				if value == p.model.value() {
					selected = i
				}
			}
			p.combo(panelChoice, p.choiceValues, selected)
		} else {
			p.setText(panelEdit, p.model.value())
		}
	}
	panelShow.Call(p.controls[panelEdit], uintptr(boolValue(!choice))*5)
	panelShow.Call(p.controls[panelChoice], uintptr(boolValue(choice))*5)
	p.enable(panelEdit, editable)
	p.enable(panelChoice, editable)
	id := panelFieldID{p.model.scopeID, p.model.fieldKey}
	dirty := field != nil && !samePanelValue(*field, p.model.value(), field.Value)
	p.enable(panelSave, editable && dirty && !p.pending[id])
	p.enable(panelReset, editable && field != nil && !field.IsDefault && !p.pending[id])
	status := p.fieldErrors[id]
	if p.pending[id] {
		status = "Saving…"
	} else if status == "" && dirty {
		status = "Unsaved changes"
	}
	if field != nil && field.ReadOnly != "" {
		status = field.ReadOnly
	}
	if p.model.panel.Offline {
		status = "Reconnect before changing settings. Your edits are preserved."
	}
	p.setText(227, status)
}

func (p *windowsPanel) renderActions() {
	instance, _, _ := pGetModuleHandleW.Call(0)
	if p.actionControls == nil {
		p.actionControls = make(map[string]int)
		p.controlActions = make(map[int]string)
		p.nextAction = panelActionBase
	}
	valid := make(map[int]bool)
	dashboard := false
	for _, action := range p.model.panel.Actions {
		if action.ID == "open" {
			dashboard = true
			p.enable(panelDashboard, !action.Disabled)
			continue
		}
		id, exists := p.actionControls[action.ID]
		if !exists {
			id = p.nextAction
			p.nextAction++
			p.actionControls[action.ID] = id
			p.controlActions[id] = action.ID
		}
		valid[id] = true
		label := action.FallbackTitle()
		child := p.controls[id]
		if child == 0 {
			child, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16z("BUTTON"))), uintptr(unsafe.Pointer(utf16z(label))), panelChild|panelVisible|panelTabStop|0xb,
				0, 0, 10, 10, p.hwnd, uintptr(id), instance, 0)
		}
		if child != 0 {
			p.controls[id] = child
			p.setText(id, label)
			panelSend.Call(child, panelSetFont, p.font, 1)
			p.enable(id, panelCommandEnabled(p.model.panel, action))
		}
	}
	for id, control := range p.controls {
		if id >= panelActionBase && !valid[id] {
			pDestroyWindow.Call(control)
			delete(p.controls, id)
			delete(p.actionControls, p.controlActions[id])
			delete(p.controlActions, id)
		}
	}
	panelShow.Call(p.controls[panelDashboard], uintptr(boolValue(dashboard))*5)
}

func (p *windowsPanel) command(id int, notification int) {
	if p.rendering {
		return
	}
	if id != panelTargets && id != panelScopes && id != panelEdit && id != panelChoice && notification != 0 {
		return
	}
	switch id {
	case panelAccountTab, panelSettingsTab:
		p.captureEdit()
		p.switchingPage = true
		p.model.settings = id == panelSettingsTab
		p.layout()
		panelShow.Call(p.hwnd, 5)
		pSetForegroundWindow.Call(p.hwnd)
		focus := panelAccounts
		if p.model.settings {
			focus = panelScopes
		}
		panelSetFocus.Call(p.controls[focus])
		p.switchingPage = false
	case panelTargets:
		if notification != 1 {
			return
		}
		selected, _, _ := panelSend.Call(p.controls[id], panelComboCurrent, 0, 0)
		if row := p.model.row(); row != nil && int(selected) >= 0 && int(selected) < len(row.Targets) {
			p.model.chooseTarget(row.Targets[selected].ID)
			p.rendering = true
			p.renderDetails()
			p.rendering = false
		}
	case panelUse:
		if target := p.model.target(); target != nil && !target.Disabled && !p.model.panel.Offline {
			p.dispatch(PanelAction{Kind: "command", Key: target.ID})
		}
	case panelScopes:
		if notification != 1 {
			return
		}
		p.captureEdit()
		selected, _, _ := panelSend.Call(p.controls[id], panelComboCurrent, 0, 0)
		if int(selected) < 0 || int(selected) >= len(p.model.panel.Settings) {
			return
		}
		p.model.scopeID = p.model.panel.Settings[selected].ID
		p.model.selectField()
		p.rendering = true
		p.renderFields()
		p.rendering = false
	case panelEdit:
		if notification != 0x300 {
			return
		}
		p.captureEdit()
		delete(p.fieldErrors, panelFieldID{p.model.scopeID, p.model.fieldKey})
		p.updateEditorButtons()
	case panelChoice:
		if notification != 1 {
			return
		}
		selected, _, _ := panelSend.Call(p.controls[id], panelComboCurrent, 0, 0)
		if int(selected) >= 0 && int(selected) < len(p.choiceValues) {
			p.model.edit(p.choiceValues[selected])
		}
		delete(p.fieldErrors, panelFieldID{p.model.scopeID, p.model.fieldKey})
		p.updateEditorButtons()
	case panelSave, panelReset:
		p.captureEdit()
		kind := "setting-set"
		if id == panelReset {
			kind = "setting-reset"
		}
		action, err := p.model.mutation(kind)
		if err != nil {
			p.fieldErrors[panelFieldID{p.model.scopeID, p.model.fieldKey}] = err.Error()
			p.updateEditorButtons()
			return
		}
		p.dispatch(action)
	case panelDashboard:
		p.dispatch(PanelAction{Kind: "command", Key: "open"})
	case 2:
		panelShow.Call(p.hwnd, 0)
	default:
		for _, action := range p.model.panel.Actions {
			if action.ID != p.controlActions[id] {
				continue
			}
			if panelCommandEnabled(p.model.panel, action) {
				p.dispatch(PanelAction{Kind: "command", Key: action.ID})
			}
			break
		}
	}
}

func (p *windowsPanel) updateEditorButtons() {
	p.rendering = true
	p.renderEditor()
	p.rendering = false
}

func (p *windowsPanel) dispatch(action PanelAction) {
	if strings.HasPrefix(action.Kind, "setting-") {
		id := panelFieldID{action.Scope, action.Key}
		if p.pending[id] {
			return
		}
		p.pending[id] = true
		p.model.awaitSetting(action)
		delete(p.fieldErrors, id)
		p.updateEditorButtons()
	}
	if action.Kind == "command" && action.Key == "open" {
		panelShow.Call(p.hwnd, 0)
	}
	go func() {
		var err error
		if p.tray.opts.OnPanelAction != nil {
			err = p.tray.opts.OnPanelAction(action)
		} else if action.Kind == "command" && p.tray.opts.OnClick != nil {
			p.tray.opts.OnClick(action.Key)
		} else {
			err = fmt.Errorf("this action is unavailable")
		}
		p.tray.mu.Lock()
		p.tray.panelResults = append(p.tray.panelResults, panelActionResult{action, err})
		hwnd := p.tray.hwnd
		p.tray.mu.Unlock()
		if hwnd != 0 {
			pPostMessageW.Call(uintptr(hwnd), wmPanelResult, 0, 0)
		}
	}()
}

func (t *windowsTray) applyPanelResults() {
	t.mu.Lock()
	results := t.panelResults
	t.panelResults = nil
	t.mu.Unlock()
	if t.panel == nil {
		return
	}
	p := t.panel
	for _, result := range results {
		if strings.HasPrefix(result.action.Kind, "setting-") {
			id := panelFieldID{result.action.Scope, result.action.Key}
			delete(p.pending, id)
			if result.err != nil {
				p.fieldErrors[id] = result.err.Error()
				p.model.cancelSetting(result.action)
			}
		} else if result.err != nil {
			p.errorText = result.err.Error()
		} else {
			p.errorText = ""
		}
	}
	p.updateEditorButtons()
	p.layout()
}

func (p *windowsPanel) notification(pointer uintptr) uintptr {
	header := nativePanelStruct[panelHeader](pointer)
	if header.code == 0xfffffff4 {
		return p.drawList(nativePanelStruct[panelListDraw](pointer))
	}
	if p.rendering {
		return 0
	}
	if header.code == uint32(0xffffff9b) {
		note := nativePanelStruct[panelListNotification](pointer)
		if note.item < 0 || note.newState&2 == 0 {
			return 0
		}
		p.captureEdit()
		if header.hwnd == p.controls[panelAccounts] && int(note.item) < len(p.model.panel.Rows) {
			p.model.rowID = p.model.panel.Rows[note.item].ID
			p.model.selectTarget()
			p.rendering = true
			p.renderDetails()
			p.rendering = false
		} else if header.hwnd == p.controls[panelFields] {
			if scope := p.model.scope(); scope != nil && int(note.item) < len(scope.Settings) {
				p.model.fieldKey = scope.Settings[note.item].Key
				p.updateEditorButtons()
			}
		}
	} else if header.code == 0xffffff94 && header.hwnd == p.controls[panelAccounts] {
		note := nativePanelStruct[panelListNotification](pointer)
		if note.subItem >= 0 && int(note.subItem) < len(p.model.panel.Columns) {
			p.dispatch(PanelAction{Kind: "sort", Key: p.model.panel.Columns[note.subItem].ID})
		}
	}
	return 0
}

func panelWndProc(hwnd windows.HWND, message uint32, wParam, lParam uintptr) uintptr {
	winMu.Lock()
	t := winTray
	winMu.Unlock()
	if t == nil || t.panel == nil {
		result, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
		return result
	}
	p := t.panel
	switch message {
	case wmClose:
		panelShow.Call(uintptr(hwnd), 0)
		return 0
	case 0x0006:
		if wParam&0xffff == 0 && !p.switchingPage {
			panelShow.Call(uintptr(hwnd), 0)
		}
		return 0
	case wmCommand:
		p.command(int(wParam&0xffff), int(wParam>>16))
		return 0
	case panelNotify:
		if lParam != 0 {
			return p.notification(lParam)
		}
		return 0
	case 0x0005:
		if p.hwnd != 0 {
			p.layout()
		}
		return 0
	case 0x02e0:
		p.dpi = int(wParam & 0xffff)
		p.refreshTheme()
		p.position()
		return 0
	case 0x001a, 0x0015, 0x031a:
		p.refreshTheme()
		return 0
	case 0x000f:
		return p.paint()
	case 0x0014:
		return 1
	case wmDrawItem:
		if lParam != 0 && (p.drawButton(nativePanelStruct[drawMenuItem](lParam)) || p.drawCombo(nativePanelStruct[drawMenuItem](lParam))) {
			return 1
		}
	case wmMeasureItem:
		if lParam != 0 {
			measure := nativePanelStruct[measureMenuItem](lParam)
			if measure.ctlType == 3 {
				measure.itemHeight = uint32(p.scale(24))
				return 1
			}
		}
	case 0x0133, 0x0134, 0x0138:
		return p.colorControl(wParam, lParam)
	}
	result, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return result
}

func panelListSubclass(hwnd uintptr, message uint32, wParam, lParam, id, ref uintptr) uintptr {
	result, _, _ := panelDefSubclass.Call(hwnd, uintptr(message), wParam, lParam)
	winMu.Lock()
	t := winTray
	winMu.Unlock()
	if t != nil && t.panel != nil && t.panel.theme == menuDark {
		switch message {
		case 0xf, 0x85, 0x115, 0x114, 0x20a, 0xa0, 0xa1, 0xa2, 0x5:
			t.panel.paintScrollbars(hwnd)
		}
		if message == 0x317 && lParam&2 != 0 {
			t.panel.paintScrollbarsOnDC(hwnd, wParam)
		}
	}
	return result
}
