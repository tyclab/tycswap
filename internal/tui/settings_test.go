// settings_test.go — the Settings screen (DESIGN A28): the dashboard reaches
// it, it lists every key the settings package defines, each kind is edited
// with its own control and persisted through settings.json, a refused value is
// reported and not written, reset removes the key, writes ride the single-
// flight gate, and the footer legend names the bindings.
package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tyclab/tycswap/internal/settings"
)

// -- fixtures ----------------------------------------------------------------

// settingsModel is a dashboard sized for a full render with the Settings
// screen open over an empty backup root (no settings.json yet).
func settingsModel(t *testing.T) (*Model, *settingsScreen, string) {
	t.Helper()
	dir := t.TempDir()
	m := newTestModel(&fakeFacade{backupDir: dir})
	m.width, m.height = 100, 40
	execAll(m.openSettings())
	s, ok := m.top().(*settingsScreen)
	if !ok {
		t.Fatalf("openSettings pushed %T, want *settingsScreen", m.top())
	}
	return m, s, dir
}

// selectKey puts the cursor on the row for a dotted key.
func selectKey(t *testing.T, s *settingsScreen, dotted string) {
	t.Helper()
	for i, r := range s.rows {
		if r.Spec.Dotted() == dotted {
			s.index = i
			return
		}
	}
	t.Fatalf("no row for %q", dotted)
}

func keyPress(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// typeText types s one rune at a time.
func typeText(m *Model, s *settingsScreen, text string) {
	for _, r := range text {
		s.update(m, keyPress(string(r)))
	}
}

// land executes the command a settings mutation returned and feeds its result
// through Update, the way the program does: the action runs, busy clears, and
// the stacked screens learn that a write landed.
func landSettingsAction(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command to run the action, got nil")
	}
	done, ok := runCmd(cmd).(actionDoneMsg)
	if !ok {
		t.Fatalf("expected the command to yield an actionDoneMsg")
	}
	m.Update(done)
}

// readSettings returns settings.json's autoswitch section, or nil when the
// file or the section is absent.
func readSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	section, _ := raw["autoswitch"].(map[string]any)
	return section
}

func rowFor(t *testing.T, s *settingsScreen, dotted string) settings.Effective {
	t.Helper()
	for _, r := range s.rows {
		if r.Spec.Dotted() == dotted {
			return r
		}
	}
	t.Fatalf("no row for %q", dotted)
	return settings.Effective{}
}

// -- reaching the screen -----------------------------------------------------

func TestSettingsEntryInRootMenu(t *testing.T) {
	d := newDashboardScreen()
	entries := d.rootEntries()
	at := -1
	for i, e := range entries {
		if e.actionID == "settings" {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("root menu has no settings row: %#v", entries)
	}
	if entries[at].label != "Settings…" {
		t.Fatalf("settings row label = %q", entries[at].label)
	}
	if entries[at+1].actionID != "quit" {
		t.Fatalf("Settings… must sit right above Quit, got %q after it", entries[at+1].actionID)
	}
}

func TestSettingsOpensFromMenuAndKey(t *testing.T) {
	m := newTestModel(&fakeFacade{backupDir: t.TempDir()})
	d := m.top().(*dashboardScreen)
	execAll(d.dispatch(m, "settings"))
	if _, ok := m.top().(*settingsScreen); !ok {
		t.Fatalf("dispatch(settings) left %T on top", m.top())
	}
	execAll(m.popScreen())
	execAll(d.update(m, keyPress("c")))
	if _, ok := m.top().(*settingsScreen); !ok {
		t.Fatalf("key c left %T on top", m.top())
	}
	// Idempotent, like openAuto.
	execAll(m.openSettings())
	if len(m.stack) != 2 {
		t.Fatalf("openSettings on the settings screen must not stack another; depth = %d", len(m.stack))
	}
	execAll(m.top().(*settingsScreen).update(m, keyPress("esc")))
	if len(m.stack) != 1 {
		t.Fatalf("esc must pop the settings screen; depth = %d", len(m.stack))
	}
}

// -- the rows ----------------------------------------------------------------

func TestSettingsListsEveryKeyFromTheSettingsPackage(t *testing.T) {
	m, s, _ := settingsModel(t)
	if len(s.rows) != len(settings.SettingSpecs) {
		t.Fatalf("rows = %d, want one per spec (%d)", len(s.rows), len(settings.SettingSpecs))
	}
	view := stripANSI(s.view(m))
	for i, spec := range settings.SettingSpecs {
		if s.rows[i].Spec.Dotted() != spec.Dotted() {
			t.Fatalf("row %d is %q, want registry order %q", i, s.rows[i].Spec.Dotted(), spec.Dotted())
		}
		if !strings.Contains(view, spec.JSONKey) {
			t.Fatalf("view does not list %q:\n%s", spec.JSONKey, view)
		}
		if !strings.Contains(view, spec.Section) {
			t.Fatalf("view has no %q section header:\n%s", spec.Section, view)
		}
	}
	// Nothing is set yet: every row carries the default marker.
	if n := strings.Count(view, "(default)"); n != len(settings.SettingSpecs) {
		t.Fatalf("default markers = %d, want %d:\n%s", n, len(settings.SettingSpecs), view)
	}
	// The detail names the highlighted key, its help, its range and when the
	// engine applies it.
	first := settings.SettingSpecs[0]
	for _, want := range []string{first.Dotted(), first.Help, kindLabel(first), engineNote} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail lacks %q:\n%s", want, view)
		}
	}
}

func TestSettingsRowsFollowAnExternalChange(t *testing.T) {
	m, s, dir := settingsModel(t)
	if _, err := settings.SetSetting(dir, "autoswitch.threshold", "75"); err != nil {
		t.Fatal(err)
	}
	// A poll lands: the row and the dashboard's bar tick follow the file.
	execAll(m.applySnapshot(snapshotOf("1", acct("1", "a@x.com", true, nil))))
	row := rowFor(t, s, "autoswitch.threshold")
	if !row.IsSet || row.Value != 75.0 {
		t.Fatalf("threshold row after an external set = %+v", row)
	}
	if m.thresholdPct == nil || *m.thresholdPct != 75 {
		t.Fatalf("bar tick = %v, want 75", m.thresholdPct)
	}
}

// -- editing by kind ---------------------------------------------------------

func TestSettingsIntWithinRangePersists(t *testing.T) {
	m, s, dir := settingsModel(t)
	selectKey(t, s, "autoswitch.unhealthyTicks")
	s.update(m, keyPress("enter"))
	if !s.editing || s.input != "3" {
		t.Fatalf("enter should open the input over the current value; editing=%v input=%q", s.editing, s.input)
	}
	s.update(m, keyPress("backspace"))
	typeText(m, s, "5")
	cmd := s.update(m, keyPress("enter"))
	if s.editing {
		t.Fatal("an accepted value closes the input")
	}
	if !m.busy {
		t.Fatal("the write must ride the single-flight gate (busy)")
	}
	landSettingsAction(t, m, cmd)
	if m.busy {
		t.Fatal("busy must clear when the write lands")
	}
	if got := readSettings(t, dir)["unhealthyTicks"]; got != 5.0 {
		t.Fatalf("settings.json unhealthyTicks = %v, want 5", got)
	}
	row := rowFor(t, s, "autoswitch.unhealthyTicks")
	if !row.IsSet || row.Value != 5 {
		t.Fatalf("row after the write = %+v, want set to 5", row)
	}
	if !hasToast(m, "autoswitch.unhealthyTicks = 5", "", "") {
		t.Fatalf("expected the config-set line as a toast, got %v", toastMessages(m))
	}
}

func TestSettingsIntOutOfRangeIsRefusedWithTheMessage(t *testing.T) {
	m, s, dir := settingsModel(t)
	selectKey(t, s, "autoswitch.unhealthyTicks")
	s.update(m, keyPress("enter"))
	s.update(m, keyPress("backspace"))
	typeText(m, s, "500")
	if cmd := s.update(m, keyPress("enter")); cmd != nil {
		t.Fatal("a refused value must start no action")
	}
	if !s.editing {
		t.Fatal("a refused value keeps the input open for correction")
	}
	if want := "autoswitch.unhealthyTicks must be between 1 and 100"; s.editError != want {
		t.Fatalf("editError = %q, want %q", s.editError, want)
	}
	if m.busy {
		t.Fatal("nothing was started")
	}
	if readSettings(t, dir) != nil {
		t.Fatal("a refused value must not be written")
	}
	view := stripANSI(s.view(m))
	if !strings.Contains(view, s.editError) {
		t.Fatalf("the message must be on screen:\n%s", view)
	}
	// Not a number is refused the same way.
	s.update(m, keyPress("backspace"))
	s.update(m, keyPress("backspace"))
	s.update(m, keyPress("backspace"))
	typeText(m, s, "x")
	s.update(m, keyPress("enter"))
	if want := "autoswitch.unhealthyTicks expects an integer, got 'x'"; s.editError != want {
		t.Fatalf("editError = %q, want %q", s.editError, want)
	}
}

func TestSettingsFloatPersistsAndMovesTheBarTick(t *testing.T) {
	m, s, dir := settingsModel(t)
	selectKey(t, s, "autoswitch.threshold")
	s.update(m, keyPress("enter"))
	if s.input != "90" {
		t.Fatalf("input prefilled with %q, want the effective value 90", s.input)
	}
	s.update(m, keyPress("backspace"))
	s.update(m, keyPress("backspace"))
	typeText(m, s, "80")
	landSettingsAction(t, m, s.update(m, keyPress("enter")))
	if got := readSettings(t, dir)["threshold"]; got != 80.0 {
		t.Fatalf("settings.json threshold = %v, want 80", got)
	}
	if m.thresholdPct == nil || *m.thresholdPct != 80 {
		t.Fatalf("dashboard bar tick = %v, want 80 after the write", m.thresholdPct)
	}
}

// NaN parses as a float; it is refused inline with the range message like any
// other out-of-range value, not let through to a failing write.
func TestSettingsFloatNaNIsRefusedInline(t *testing.T) {
	m, s, dir := settingsModel(t)
	selectKey(t, s, "autoswitch.threshold")
	s.update(m, keyPress("enter"))
	s.update(m, keyPress("backspace"))
	s.update(m, keyPress("backspace"))
	typeText(m, s, "NaN")
	if cmd := s.update(m, keyPress("enter")); cmd != nil {
		t.Fatal("a refused value must start no action")
	}
	if want := "autoswitch.threshold must be between 50 and 99.9"; !s.editing || s.editError != want {
		t.Fatalf("editing=%v editError=%q, want the input open with %q", s.editing, s.editError, want)
	}
	if readSettings(t, dir) != nil {
		t.Fatal("a refused value must not be written")
	}
}

func TestSettingsBoolToggles(t *testing.T) {
	m, s, dir := settingsModel(t)
	selectKey(t, s, "autoswitch.codexEnabled")
	cmd := s.update(m, keyPress("enter"))
	if s.editing {
		t.Fatal("enter on a bool saves at once, with no input")
	}
	landSettingsAction(t, m, cmd)
	if got := readSettings(t, dir)["codexEnabled"]; got != false {
		t.Fatalf("codexEnabled after toggle = %v, want false", got)
	}
	landSettingsAction(t, m, s.update(m, keyPress("enter")))
	if got := readSettings(t, dir)["codexEnabled"]; got != true {
		t.Fatalf("codexEnabled after second toggle = %v, want true", got)
	}
	if row := rowFor(t, s, "autoswitch.codexEnabled"); !row.IsSet || row.Value != true {
		t.Fatalf("row after toggles = %+v", row)
	}
}

func TestSettingsEnumCycles(t *testing.T) {
	m, s, dir := settingsModel(t)
	selectKey(t, s, "autoswitch.strategy")
	landSettingsAction(t, m, s.update(m, keyPress("enter")))
	if got := readSettings(t, dir)["strategy"]; got != "soonest-reset" {
		t.Fatalf("strategy after one cycle = %v, want soonest-reset", got)
	}
	landSettingsAction(t, m, s.update(m, keyPress("enter")))
	if got := readSettings(t, dir)["strategy"]; got != "best" {
		t.Fatalf("strategy after two cycles = %v, want best (wrapped)", got)
	}
	if !hasToast(m, "autoswitch.strategy = best", "", "") {
		t.Fatalf("toasts = %v", toastMessages(m))
	}
}

func TestSettingsStringEditsAndEmptyIsRefused(t *testing.T) {
	m, s, dir := settingsModel(t)
	selectKey(t, s, "autoswitch.model")
	s.update(m, keyPress("enter"))
	if s.input != "" {
		t.Fatalf("an unset string starts empty, not %q", s.input)
	}
	s.update(m, keyPress("enter"))
	if !strings.Contains(s.editError, "expects a non-empty value") {
		t.Fatalf("empty string editError = %q", s.editError)
	}
	typeText(m, s, "Fable, Opus")
	landSettingsAction(t, m, s.update(m, keyPress("enter")))
	if got := readSettings(t, dir)["model"]; got != "Fable, Opus" {
		t.Fatalf("model = %v", got)
	}
}

// -- reset -------------------------------------------------------------------

func TestSettingsResetRemovesTheKey(t *testing.T) {
	m, s, dir := settingsModel(t)
	if _, err := settings.SetSetting(dir, "autoswitch.threshold", "80"); err != nil {
		t.Fatal(err)
	}
	s.reload(m)
	selectKey(t, s, "autoswitch.threshold")
	landSettingsAction(t, m, s.update(m, keyPress("u")))
	if section := readSettings(t, dir); section != nil {
		if _, present := section["threshold"]; present {
			t.Fatalf("threshold still in settings.json after reset: %v", section)
		}
	}
	row := rowFor(t, s, "autoswitch.threshold")
	if row.IsSet || row.Value != 90.0 {
		t.Fatalf("row after reset = %+v, want unset default 90", row)
	}
	if !hasToast(m, "autoswitch.threshold unset (default: 90)", "", "") {
		t.Fatalf("toasts = %v", toastMessages(m))
	}
	if m.thresholdPct == nil || *m.thresholdPct != 90 {
		t.Fatalf("bar tick after reset = %v, want 90", m.thresholdPct)
	}

	// Reset on a key that is not set: the CLI's notice, no action.
	cmd := s.update(m, keyPress("u"))
	if m.busy {
		t.Fatal("reset of an unset key must start no action")
	}
	dropCmd(cmd)
	if !hasToast(m, "autoswitch.threshold is not set; nothing to do", "", "") {
		t.Fatalf("toasts = %v", toastMessages(m))
	}
}

// -- gates and failures ------------------------------------------------------

func TestSettingsWritesRideTheSingleFlightGate(t *testing.T) {
	m, s, dir := settingsModel(t)
	m.busy = true
	selectKey(t, s, "autoswitch.codexEnabled")
	dropCmd(s.update(m, keyPress("enter")))
	if !hasToast(m, "Another action is still running", "", "warning") {
		t.Fatalf("toasts = %v", toastMessages(m))
	}
	if readSettings(t, dir) != nil {
		t.Fatal("a refused action must write nothing")
	}
}

// A typed value the gate refuses stays in the open input, to be saved once
// the running action has landed; a poll while typing leaves it alone too.
func TestSettingsTypedValueSurvivesTheGateAndAPoll(t *testing.T) {
	m, s, dir := settingsModel(t)
	selectKey(t, s, "autoswitch.threshold")
	s.update(m, keyPress("enter"))
	s.update(m, keyPress("backspace"))
	s.update(m, keyPress("backspace"))
	typeText(m, s, "8")
	execAll(m.applySnapshot(snapshotOf("1", acct("1", "a@x.com", true, nil))))
	if !s.editing || s.input != "8" {
		t.Fatalf("a poll must not touch the open input; editing=%v input=%q", s.editing, s.input)
	}
	typeText(m, s, "5")
	m.busy = true
	dropCmd(s.update(m, keyPress("enter")))
	if !hasToast(m, "Another action is still running", "", "warning") {
		t.Fatalf("toasts = %v", toastMessages(m))
	}
	if !s.editing || s.input != "85" {
		t.Fatalf("a refused save must keep the input; editing=%v input=%q", s.editing, s.input)
	}
	if readSettings(t, dir) != nil {
		t.Fatal("a refused save must write nothing")
	}
	m.busy = false
	landSettingsAction(t, m, s.update(m, keyPress("enter")))
	if s.editing {
		t.Fatal("the save, once let through, closes the input")
	}
	if got := readSettings(t, dir)["threshold"]; got != 85.0 {
		t.Fatalf("settings.json threshold = %v, want 85", got)
	}
}

// A file written by a newer version keeps its unknown keys and sections and
// its schemaVersion through a write from this screen.
func TestSettingsWriteKeepsUnknownKeys(t *testing.T) {
	m, s, dir := settingsModel(t)
	path := filepath.Join(dir, "settings.json")
	newer := `{"schemaVersion": 2, "future": {"x": 1}, "autoswitch": {"futureKnob": true, "threshold": 80}}`
	if err := os.WriteFile(path, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	s.reload(m)
	selectKey(t, s, "autoswitch.codexEnabled")
	landSettingsAction(t, m, s.update(m, keyPress("enter")))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	section, _ := raw["autoswitch"].(map[string]any)
	future, _ := raw["future"].(map[string]any)
	if raw["schemaVersion"] != 2.0 || future["x"] != 1.0 || section["futureKnob"] != true ||
		section["threshold"] != 80.0 || section["codexEnabled"] != false {
		t.Fatalf("settings.json after the write = %s", data)
	}
}

func TestSettingsWriteFailureOpensTheOutputModal(t *testing.T) {
	m, s, dir := settingsModel(t)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	selectKey(t, s, "autoswitch.codexEnabled")
	landSettingsAction(t, m, s.update(m, keyPress("enter")))
	om, ok := m.top().(*outputModal)
	if !ok {
		t.Fatalf("a failed write must open the output modal, top is %T", m.top())
	}
	if om.title != "Set autoswitch.codexEnabled — failed" || !strings.Contains(om.output, "not valid JSON") {
		t.Fatalf("modal = %q / %q", om.title, om.output)
	}
}

func TestSettingsEscCancelsTheInputThenLeaves(t *testing.T) {
	m, s, _ := settingsModel(t)
	selectKey(t, s, "autoswitch.threshold")
	s.update(m, keyPress("enter"))
	typeText(m, s, "1")
	execAll(s.update(m, keyPress("esc")))
	if s.editing || s.input != "" || len(m.stack) != 2 {
		t.Fatalf("first esc cancels the input only; editing=%v input=%q depth=%d", s.editing, s.input, len(m.stack))
	}
	execAll(s.update(m, keyPress("esc")))
	if len(m.stack) != 1 {
		t.Fatalf("second esc leaves the screen; depth = %d", len(m.stack))
	}
}

// -- footer legend -----------------------------------------------------------

func TestSettingsFooterLegend(t *testing.T) {
	m, s, _ := settingsModel(t)
	d := m.stack[0].(*dashboardScreen)
	mustContain(t, footerText(d.footerBindings(m), wideFooter).plain(), "c Settings")

	selectKey(t, s, "autoswitch.threshold")
	got := footerText(s.footerBindings(m), wideFooter).plain()
	mustContain(t, got, "enter Edit", "u Reset to default", "esc Back")

	selectKey(t, s, "autoswitch.codexEnabled")
	mustContain(t, footerText(s.footerBindings(m), wideFooter).plain(), "enter Toggle")

	selectKey(t, s, "autoswitch.strategy")
	mustContain(t, footerText(s.footerBindings(m), wideFooter).plain(), "enter Next choice")

	selectKey(t, s, "autoswitch.threshold")
	s.update(m, keyPress("enter"))
	editing := footerText(s.footerBindings(m), wideFooter).plain()
	mustContain(t, editing, "enter Save", "esc Cancel")
	mustOmit(t, editing, "Reset", "Back")
}

// -- layout ------------------------------------------------------------------

func TestSettingsViewNeverWraps(t *testing.T) {
	m, s, _ := settingsModel(t)
	selectKey(t, s, "autoswitch.strategy")
	for _, width := range []int{20, 40, 60, 80, 120} {
		m.width = width
		for _, editing := range []bool{false, true} {
			s.editing = editing
			s.input = "soonest-reset"
			for i, line := range strings.Split(stripANSI(s.view(m)), "\n") {
				if w := lipgloss.Width(line); w > width {
					t.Fatalf("width %d editing=%v: line %d is %d columns: %q", width, editing, i, w, line)
				}
			}
		}
	}
	s.editing = false
	// Height-bounded: the rows window around the cursor; nothing exceeds the
	// content height.
	m.width, m.height = 80, 12
	lines := strings.Split(s.view(m), "\n")
	if len(lines) > m.contentHeight() {
		t.Fatalf("view is %d lines for a content height of %d", len(lines), m.contentHeight())
	}
}

// At 80 columns every key's detail is read whole: the help, the kind with its
// range or choices, the default and the engine note wrap between words rather
// than being clipped.
func TestSettingsDetailIsReadWholeAt80(t *testing.T) {
	_, s, _ := settingsModel(t)
	for i, r := range s.rows {
		s.index = i
		lines := s.detailLines(80)
		var plain []string
		for _, l := range lines {
			l = stripANSI(l)
			if w := lipgloss.Width(l); w > 80 {
				t.Fatalf("%s: detail line is %d columns: %q", r.Spec.Dotted(), w, l)
			}
			plain = append(plain, l)
		}
		joined := strings.Join(plain, " ")
		for _, want := range []string{r.Spec.Dotted() + "  " + r.Spec.Help, kindLabel(r.Spec), engineNote} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%s: detail lacks %q:\n%s", r.Spec.Dotted(), want, strings.Join(plain, "\n"))
			}
		}
		if strings.Contains(joined, "…") {
			t.Fatalf("%s: detail is clipped at 80 columns:\n%s", r.Spec.Dotted(), strings.Join(plain, "\n"))
		}
	}
}

// -- the Auto view's adjust mode stays session-only and says where to persist --

func TestAutoAdjustHintNamesSettings(t *testing.T) {
	host := &engineHost{}
	m := newModel(&fakeFacade{backupDir: t.TempDir()}, "dashboard", WithEngineFactory(host.factory()))
	m.pushScreen(newAutoScreen())
	a := m.top().(*autoScreen)
	m.width, m.height = 80, 40
	if strings.Contains(stripANSI(a.view(m)), "Settings") {
		t.Fatal("the hint shows only while adjusting")
	}
	a.adjustThreshold(m)
	a.thresholdStep(m, -1) // the " (session)" marker lengthens the summary
	// Readable whole at 80 columns; no line wider than the terminal at 80 or
	// 60 (the event log's lines are not fitted, so not narrower than that).
	const hint = "← → adjust · enter done · session only — Settings persists"
	if view := stripANSI(a.view(m)); !strings.Contains(view, hint) {
		t.Fatalf("the adjusting hint %q is not shown whole at 80 columns:\n%s", hint, view)
	}
	for _, width := range []int{80, 60} {
		m.width = width
		for i, line := range strings.Split(stripANSI(a.view(m)), "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Fatalf("auto view line %d is %d columns at width %d: %q", i, w, width, line)
			}
		}
	}
	a.endAdjust(m)
	m.width = 80
	if strings.Contains(stripANSI(a.view(m)), "Settings") {
		t.Fatal("the hint goes when adjusting ends")
	}
}
