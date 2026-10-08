package tray

type Panel struct {
	Title, Version, Status string
	Offline                bool
	SortColumn             string
	SortDescending         bool
	Columns                []Column
	Rows                   []PanelRow
	Settings               []SettingScope
	Actions                []Item
}

type Column struct {
	ID, Label string
}

type PanelRow struct {
	ID, Title string
	Active    bool
	Cells     []string
	Details   []string
	Targets   []Target
}

type Target struct {
	ID, Label, Reason string
	Disabled          bool
}

type SettingScope struct {
	ID, Label string
	Settings  []PanelSetting
}

type PanelSetting struct {
	Key, Label, Value, Kind string
	Description, Applies    string
	Source, ReadOnly        string
	Choices                 []string
	Min, Max                *float64
	IsDefault               bool
}

type PanelAction struct {
	Kind, Scope, Key, Value string
}

// PanelTray provides a tray-anchored overview with native table and form controls.
type PanelTray interface {
	SetPanel(Panel)
}
