package tray

// Panel is the Windows tray's one page, the daily view (DESIGN A84): a row
// per account with its limits, one note line, and a footer of commands.
type Panel struct {
	Title string
	// Note is the last action's result or why the engine is out of reach.
	Note    string
	Alert   bool // draws Note as a warning
	Offline bool
	Empty   string // the list's text while it has no rows
	// Columns[0] is the account column; a row's Cells follow Columns[1:].
	Columns []Column
	Rows    []PanelRow
	// Actions are the footer in Tab order; toggles and "open" sit right.
	Actions []Item
}

type Column struct {
	ID, Label string
}

type PanelRow struct {
	// ID is the row key a Switch dispatches as "switch:<ID>".
	ID, Name string
	Plan     string // drawn in an outlined label; "" draws none
	Tag      string // muted, such as "not rotated"
	Warning  string // in the limit colour, such as a usage status
	Active   bool
	Disabled bool // the account cannot be switched to
	Cells    []PanelCell
}

// PanelCell is one limit: Share, a bar filled to Pct, Reset under them.
type PanelCell struct {
	Absent bool // a dash instead
	Share  string
	Pct    float64
	Limit  bool // at or over the window's auto-switch bar
	Reset  string
}

type PanelAction struct {
	Kind, Key string
}

// PanelTray provides the tray-anchored daily view.
type PanelTray interface {
	SetPanel(Panel)
}
