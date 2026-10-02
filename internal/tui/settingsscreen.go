// settingsscreen.go — the Settings screen: every settings.json key, grouped by
// section the way `tycswap config list` prints them, edited in place with a
// control matching its kind and persisted at once through the settings
// package (DESIGN A28).
//
// The screen is a reader and a writer of settings.json and nothing else: its
// rows come from settings.EffectiveSettings, every write goes through
// settings.SetSetting / settings.UnsetSetting (the strict, validated path
// `tycswap config set|unset` uses), and the rows are re-read after every write
// and on every poll, so a change made by another tycswap instance shows up on
// the next tick rather than at the next visit. Writes ride the app's single-
// flight action gate (09§2.6): a write failure opens the output modal, a
// refused value is reported inline before anything is written. The frozen
// Facade (A13) is not extended — the file is reached through Facade.BackupDir,
// as the Auto view's loadThreshold reaches it.
package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tyclab/tycswap/internal/settings"
)

// engineNote states, for every key, when a running auto-switch engine applies
// a saved value. The engine reads settings.json once, when it starts (NewEngine
// takes a frozen value; nothing re-reads the file per tick), and `tycswap
// config set` does not notify a running `tycswap auto` either. In the TUI an
// engine lives only while the Auto view is open and is started from a fresh
// settings.Load at mount, so a change made here is in force the next time
// that view opens — the same contract as the CLI path. One note for every
// key, because no key is applied earlier than that.
const engineNote = "applies when the auto-switch engine next starts"

// settingsScreen lists and edits settings.json (DESIGN A28).
type settingsScreen struct {
	rows  []settings.Effective
	index int

	// editing is the inline text input for the int/float/string kinds. input is
	// the text so far; editError is the last refused value's message, cleared
	// on the next keystroke that changes the input.
	editing   bool
	input     string
	editError string
}

func newSettingsScreen() *settingsScreen { return &settingsScreen{} }

// onMount reads the rows once before the first render.
func (s *settingsScreen) onMount(m *Model) tea.Cmd {
	s.reload(m)
	return nil
}

// onSnapshot re-reads the rows on every poll, so a value changed by another
// tycswap instance (or `tycswap config set` in a terminal) is shown on the next
// tick — the same discipline the dashboard's account submenus follow.
func (s *settingsScreen) onSnapshot(m *Model) tea.Cmd {
	s.reload(m)
	return nil
}

// onMessage re-reads the rows the moment a write lands (the app fans every
// completed action out to the stacked screens), so the row shows the saved
// value without waiting for the post-action refresh pass.
func (s *settingsScreen) onMessage(m *Model, msg tea.Msg) tea.Cmd {
	if _, ok := msg.(actionDoneMsg); ok {
		s.reload(m)
	}
	return nil
}

// reload re-reads every key's effective value and set-marker, and syncs the
// dashboard's bar tick to the file's threshold, as the Auto view's mount does:
// a threshold saved here moves the tick on the accounts monitor at once. The
// Auto view is never stacked under this screen (both open from the dashboard),
// so no session override is in effect to be overwritten.
func (s *settingsScreen) reload(m *Model) {
	root := m.facade.BackupDir()
	s.rows = settings.EffectiveSettings(root)
	if s.index >= len(s.rows) {
		s.index = max(len(s.rows)-1, 0)
	}
	if t := loadThreshold(root); t != nil {
		m.thresholdPct = t
	}
}

// current is the highlighted row, if any.
func (s *settingsScreen) current() (settings.Effective, bool) {
	if s.index < 0 || s.index >= len(s.rows) {
		return settings.Effective{}, false
	}
	return s.rows[s.index], true
}

func (s *settingsScreen) update(m *Model, msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if s.editing {
		return s.updateEditing(m, key)
	}
	switch key.String() {
	case "esc", "q":
		return m.popScreen()
	case "j", "down":
		if s.index < len(s.rows)-1 {
			s.index++
		}
	case "k", "up":
		if s.index > 0 {
			s.index--
		}
	case "enter":
		return s.edit(m)
	case "u":
		return s.reset(m)
	}
	return nil
}

// updateEditing drives the inline input: enter submits, esc cancels (the
// first stage of the two-stage escape; a second esc leaves the screen),
// backspace deletes, every other printable key is typed.
func (s *settingsScreen) updateEditing(m *Model, key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "esc":
		s.editing, s.input, s.editError = false, "", ""
	case "enter":
		return s.submit(m)
	case "backspace":
		if s.input != "" {
			r := []rune(s.input)
			s.input = string(r[:len(r)-1])
			s.editError = ""
		}
	case " ":
		s.input += " "
		s.editError = ""
	default:
		if key.Type == tea.KeyRunes {
			s.input += string(key.Runes)
			s.editError = ""
		}
	}
	return nil
}

// edit acts on the highlighted key with the control its kind calls for: a
// bool toggles and a choice cycles to the next value, both saved at once; an
// int, float or string opens the inline input over the current value (empty
// for an unset string, whose "(none)" is a marker, not a value).
func (s *settingsScreen) edit(m *Model) tea.Cmd {
	row, ok := s.current()
	if !ok {
		return nil
	}
	switch row.Spec.Kind {
	case settings.KindBool:
		cur, _ := row.Value.(bool)
		return s.save(m, row.Spec, strconv.FormatBool(!cur))
	case settings.KindChoice:
		cur, _ := row.Value.(string)
		return s.save(m, row.Spec, nextChoice(row.Spec.Choices, cur))
	}
	s.editing, s.editError = true, ""
	s.input = ""
	if row.Value != nil {
		s.input = settings.FormatSettingValue(row.Value)
	}
	return nil
}

// nextChoice is the choice after cur in choices, wrapping; an unknown cur
// starts the cycle at the first choice.
func nextChoice(choices []string, cur string) string {
	for i, c := range choices {
		if c == cur {
			return choices[(i+1)%len(choices)]
		}
	}
	return choices[0]
}

// submit validates the typed value with the parser `tycswap config set` uses
// and, when it is refused, keeps the input open with the message under it so
// the value can be corrected; nothing is written. An accepted value is saved
// through the single-flight gate; when the gate refuses it (another action is
// still running) the input stays open too, so the typed value is not lost.
func (s *settingsScreen) submit(m *Model) tea.Cmd {
	row, ok := s.current()
	if !ok {
		return nil
	}
	raw := strings.TrimSpace(s.input)
	if _, err := settings.ParseSettingValue(row.Spec, raw); err != nil {
		s.editError = err.Error()
		return nil
	}
	busy := m.busy
	cmd := s.save(m, row.Spec, raw)
	if !busy {
		s.editing, s.input, s.editError = false, "", ""
	}
	return cmd
}

// save persists one key through settings.SetSetting (strict: the value is
// validated again under the settings lock) on the app's single-flight action
// gate. The success toast is the line `tycswap config set` prints; a failure
// opens the output modal with the error, as every other action's does.
func (s *settingsScreen) save(m *Model, spec settings.Spec, raw string) tea.Cmd {
	root := m.facade.BackupDir()
	key := spec.Dotted()
	return m.startMessageAction("Set "+key, func() (string, string, error) {
		value, err := settings.SetSetting(root, key, raw)
		if err != nil {
			return "", "", err
		}
		return key + " = " + settings.FormatSettingValue(value), "", nil
	})
}

// reset removes the highlighted key from settings.json (settings.UnsetSetting)
// so its default applies again. A key that is not set is reported and nothing
// is written — the notice `tycswap config unset` prints in that case.
func (s *settingsScreen) reset(m *Model) tea.Cmd {
	row, ok := s.current()
	if !ok {
		return nil
	}
	key := row.Spec.Dotted()
	notSet := key + " is not set; nothing to do"
	if !row.IsSet {
		return m.notify(notSet, "", "")
	}
	def := settings.FormatSettingValue(row.Spec.Default)
	root := m.facade.BackupDir()
	return m.startMessageAction("Reset "+key, func() (string, string, error) {
		removed, err := settings.UnsetSetting(root, key)
		if err != nil {
			return "", "", err
		}
		if !removed {
			return notSet, "", nil
		}
		return key + " unset (default: " + def + ")", "", nil
	})
}

// footerBindings are the screen's footer-visible bindings: enter's label names
// what it does to the highlighted key's kind (toggle, next choice, edit); u
// resets; esc goes back. While typing, enter saves and esc cancels, and the
// rest are inert, as a check_action gate would make them.
func (s *settingsScreen) footerBindings(m *Model) []footerBinding {
	if s.editing {
		return []footerBinding{{"enter", "Save"}, {"esc", "Cancel"}}
	}
	label := "Edit"
	if row, ok := s.current(); ok {
		switch row.Spec.Kind {
		case settings.KindBool:
			label = "Toggle"
		case settings.KindChoice:
			label = "Next choice"
		}
	}
	return []footerBinding{{"enter", label}, {"u", "Reset to default"}, {"esc", "Back"}}
}

// -- rendering ---------------------------------------------------------------

// view pins the title above and the highlighted key's detail below; the rows
// between them flex, windowed around the cursor (viewport.go). Every line is
// fitted to the width before it is returned, so no row ever wraps.
func (s *settingsScreen) view(m *Model) string {
	width := panelWidth(m)
	title := mutedClipped("settings · "+settings.SettingsPath(m.facade.BackupDir()), width)
	rows, cursor := s.rowLines(width)
	detail := s.detailLines(width)

	avail := m.contentHeight()
	if avail < 0 {
		// Terminal size unknown → render everything (pre-size fallback).
		out := append([]string{title, ""}, rows...)
		out = append(out, "")
		return strings.Join(append(out, detail...), "\n")
	}
	if avail == 0 {
		return ""
	}
	pinned := 3 + len(detail) // title, blank, blank, detail
	budget := avail - pinned
	if budget < 1 {
		// Tiny terminal: the title and the rows that fit; the detail drops first.
		out := append([]string{title, ""}, windowRows(rows, max(avail-2, 0), cursor)...)
		if len(out) > avail {
			out = out[:avail]
		}
		return strings.Join(out, "\n")
	}
	out := append([]string{title, ""}, windowRows(rows, budget, cursor)...)
	out = append(out, "")
	return strings.Join(append(out, detail...), "\n")
}

// rowLines renders every key under its section header, in registry order,
// columns aligned as `tycswap config list` aligns them, and reports the line
// the cursor sits on.
func (s *settingsScreen) rowLines(width int) ([]string, int) {
	keyW, valW := 0, 0
	for _, r := range s.rows {
		keyW = max(keyW, lipgloss.Width(r.Spec.JSONKey))
		valW = max(valW, lipgloss.Width(settings.FormatSettingValue(r.Value)))
	}
	var lines []string
	cursor := 0
	section := ""
	for i, r := range s.rows {
		if r.Spec.Section != section {
			if section != "" {
				lines = append(lines, "")
			}
			section = r.Spec.Section
			var h richText
			h.add("  "+section, segStyle{Fg: colMuted, Bold: true})
			lines = append(lines, clipRichLines(h, width).render())
		}
		if i == s.index {
			cursor = len(lines)
		}
		lines = append(lines, s.rowText(r, i == s.index, keyW, valW, width))
	}
	return lines, cursor
}

// rowText is one key's row: the accent left-border cursor (09§8.2), the key,
// then its value — the inline input while this row is being edited, else the
// effective value with the muted "(default)" marker when the key is not set.
func (s *settingsScreen) rowText(r settings.Effective, selected bool, keyW, valW, width int) string {
	var t richText
	if selected {
		t.add("▌ ", segStyle{Fg: colAccent})
	} else {
		t.addPlain("  ")
	}
	t.addFg(fmt.Sprintf("%-*s", keyW, r.Spec.JSONKey), colForeground)
	t.addPlain("  ")
	if selected && s.editing {
		t.add(s.input, segStyle{Fg: colAccent, Bold: true})
		t.add("▏", segStyle{Fg: colAccent})
		return clipRichLines(t, width).render()
	}
	t.addFg(fmt.Sprintf("%-*s", valW, settings.FormatSettingValue(r.Value)), colForeground)
	if !r.IsSet {
		t.addFg("  (default)", colMuted)
	}
	return clipRichLines(t, width).render()
}

// detailLines describe the highlighted key: its dotted name and help text, its
// kind with range or choices, its default and when the engine applies it, and
// a third line for the refused value's message or the typing hint. Always
// three lines, so the layout does not jump when editing starts.
func (s *settingsScreen) detailLines(width int) []string {
	r, ok := s.current()
	if !ok {
		return nil
	}
	fit := func(t richText) string { return clipRichLines(t, width).render() }
	var l1, l2, l3 richText
	l1.addFg(r.Spec.Dotted(), colForeground)
	l1.addFg("  "+r.Spec.Help, colMuted)
	l2.addFg(kindLabel(r.Spec)+" · default "+settings.FormatSettingValue(r.Spec.Default)+" · "+engineNote, colMuted)
	switch {
	case s.editError != "":
		l3.addFg(s.editError, colSevCrit)
	case s.editing:
		l3.addFg("type a value · enter save · esc cancel", colMuted)
	}
	return []string{fit(l1), fit(l2), fit(l3)}
}

// kindLabel names a key's kind with its range (int/float) or its choices.
func kindLabel(spec settings.Spec) string {
	switch spec.Kind {
	case settings.KindFloat, settings.KindInt:
		return fmt.Sprintf("%s %s–%s", spec.Kind, settings.FormatSettingValue(spec.Lo), settings.FormatSettingValue(spec.Hi))
	case settings.KindChoice:
		return "one of " + strings.Join(spec.Choices, ", ")
	}
	return string(spec.Kind)
}
