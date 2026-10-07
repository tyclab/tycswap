package tray

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type panelFieldID struct{ scope, key string }

type panelModel struct {
	panel       Panel
	rowID       string
	targetID    string
	targetScope string
	scopeID     string
	fieldKey    string
	settings    bool
	drafts      map[panelFieldID]string
	saves       map[panelFieldID]string
	resets      map[panelFieldID]string
}

func clonePanel(p Panel) Panel {
	p.Columns = append([]Column(nil), p.Columns...)
	p.Rows = append([]PanelRow(nil), p.Rows...)
	for i := range p.Rows {
		p.Rows[i].Cells = append([]string(nil), p.Rows[i].Cells...)
		p.Rows[i].Details = append([]string(nil), p.Rows[i].Details...)
		p.Rows[i].Targets = append([]Target(nil), p.Rows[i].Targets...)
	}
	p.Settings = append([]SettingScope(nil), p.Settings...)
	for i := range p.Settings {
		p.Settings[i].Settings = append([]PanelSetting(nil), p.Settings[i].Settings...)
		for j := range p.Settings[i].Settings {
			field := &p.Settings[i].Settings[j]
			field.Choices = append([]string(nil), field.Choices...)
			if field.Min != nil {
				value := *field.Min
				field.Min = &value
			}
			if field.Max != nil {
				value := *field.Max
				field.Max = &value
			}
		}
	}
	p.Actions = append([]Item(nil), p.Actions...)
	return p
}

func (m *panelModel) update(p Panel) {
	m.panel = p
	if m.row() == nil {
		m.rowID = ""
		for _, row := range p.Rows {
			if m.rowID == "" || row.Active {
				m.rowID = row.ID
			}
			if row.Active {
				break
			}
		}
	}
	m.selectTarget()
	if m.scope() == nil && len(p.Settings) > 0 {
		m.scopeID = p.Settings[0].ID
	}
	m.selectField()
	for _, scope := range p.Settings {
		for _, field := range scope.Settings {
			id := panelFieldID{scope.ID, field.Key}
			value, dirty := m.drafts[id]
			if dirty && field.Value == value {
				delete(m.drafts, id)
			}
			if saved, ok := m.saves[id]; ok && samePanelValue(field, saved, field.Value) {
				if dirty && value == saved {
					delete(m.drafts, id)
				}
				delete(m.saves, id)
			}
			if reset, ok := m.resets[id]; ok && field.IsDefault {
				if dirty && value == reset {
					delete(m.drafts, id)
				}
				delete(m.resets, id)
			}
		}
	}
}

func (m *panelModel) row() *PanelRow {
	for i := range m.panel.Rows {
		if m.panel.Rows[i].ID == m.rowID {
			return &m.panel.Rows[i]
		}
	}
	return nil
}

func (m *panelModel) target() *Target {
	if row := m.row(); row != nil {
		for i := range row.Targets {
			if row.Targets[i].ID == m.targetID {
				return &row.Targets[i]
			}
		}
	}
	return nil
}

func (m *panelModel) selectTarget() {
	if m.target() != nil {
		return
	}
	m.targetID = ""
	if row := m.row(); row != nil {
		for _, target := range row.Targets {
			if m.targetScope == "" || targetScope(target.ID) == m.targetScope {
				m.chooseTarget(target.ID)
				return
			}
		}
	}
}

func targetScope(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[:i]
	}
	return id
}

func (m *panelModel) chooseTarget(id string) { m.targetID, m.targetScope = id, targetScope(id) }

func samePanelValue(field PanelSetting, a, b string) bool {
	if a == b {
		return true
	}
	if field.Kind == "string" {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	if field.Kind == "int" {
		first, err := strconv.ParseInt(a, 10, 64)
		second, secondErr := strconv.ParseInt(b, 10, 64)
		return err == nil && secondErr == nil && first == second
	}
	if field.Kind != "float" {
		return false
	}
	first, err := strconv.ParseFloat(a, 64)
	second, secondErr := strconv.ParseFloat(b, 64)
	return err == nil && secondErr == nil && !math.IsNaN(first) && !math.IsInf(first, 0) && first == second
}

func (m *panelModel) awaitSetting(action PanelAction) {
	id := panelFieldID{action.Scope, action.Key}
	if action.Kind == "setting-set" {
		if m.saves == nil {
			m.saves = make(map[panelFieldID]string)
		}
		m.saves[id] = action.Value
	} else {
		if m.resets == nil {
			m.resets = make(map[panelFieldID]string)
		}
		m.resets[id] = m.value()
	}
}

func (m *panelModel) cancelSetting(action PanelAction) {
	id := panelFieldID{action.Scope, action.Key}
	delete(m.saves, id)
	delete(m.resets, id)
}

func (m *panelModel) scope() *SettingScope {
	for i := range m.panel.Settings {
		if m.panel.Settings[i].ID == m.scopeID {
			return &m.panel.Settings[i]
		}
	}
	return nil
}

func (m *panelModel) field() *PanelSetting {
	if scope := m.scope(); scope != nil {
		for i := range scope.Settings {
			if scope.Settings[i].Key == m.fieldKey {
				return &scope.Settings[i]
			}
		}
	}
	return nil
}

func (m *panelModel) selectField() {
	if m.field() != nil {
		return
	}
	m.fieldKey = ""
	if scope := m.scope(); scope != nil && len(scope.Settings) > 0 {
		m.fieldKey = scope.Settings[0].Key
	}
}

func (m *panelModel) value() string {
	if value, ok := m.drafts[panelFieldID{m.scopeID, m.fieldKey}]; ok {
		return value
	}
	if field := m.field(); field != nil {
		return field.Value
	}
	return ""
}

func (m *panelModel) edit(value string) {
	field := m.field()
	if field == nil {
		return
	}
	id := panelFieldID{m.scopeID, m.fieldKey}
	if value == field.Value {
		delete(m.drafts, id)
		return
	}
	if m.drafts == nil {
		m.drafts = make(map[panelFieldID]string)
	}
	m.drafts[id] = value
}

func (m *panelModel) mutation(kind string) (PanelAction, error) {
	field := m.field()
	if field == nil {
		return PanelAction{}, fmt.Errorf("select a setting")
	}
	if m.panel.Offline {
		return PanelAction{}, fmt.Errorf("reconnect before changing settings")
	}
	if field.ReadOnly != "" {
		return PanelAction{}, fmt.Errorf("%s", field.ReadOnly)
	}
	value := m.value()
	if kind == "setting-set" {
		if err := validatePanelValue(*field, value); err != nil {
			return PanelAction{}, err
		}
	}
	return PanelAction{Kind: kind, Scope: m.scopeID, Key: m.fieldKey, Value: value}, nil
}

func validatePanelValue(field PanelSetting, value string) error {
	var number float64
	var err error
	switch field.Kind {
	case "bool":
		if value != "true" && value != "false" {
			return fmt.Errorf("choose true or false")
		}
	case "choice":
		for _, choice := range field.Choices {
			if value == choice {
				return nil
			}
		}
		return fmt.Errorf("choose %s", strings.Join(field.Choices, " or "))
	case "int":
		var integer int64
		integer, err = strconv.ParseInt(value, 10, 64)
		number = float64(integer)
	case "float":
		number, err = strconv.ParseFloat(value, 64)
	case "string":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("enter a value, or reset this setting")
		}
		return nil
	case "":
		return nil
	default:
		return fmt.Errorf("unsupported setting type %s", field.Kind)
	}
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return fmt.Errorf("enter a valid %s value", field.Kind)
	}
	if field.Kind == "int" || field.Kind == "float" {
		if field.Min != nil && number < *field.Min {
			return fmt.Errorf("minimum is %g", *field.Min)
		}
		if field.Max != nil && number > *field.Max {
			return fmt.Errorf("maximum is %g", *field.Max)
		}
	}
	return nil
}
