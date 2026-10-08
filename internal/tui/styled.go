package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// segStyle is the styling of one text run, stored as data so tests can assert
// on it without parsing ANSI. Fg is a hex color ("" = default foreground).
type segStyle struct {
	Fg   string
	Bold bool
	Dim  bool
}

// seg is one styled text run.
type seg struct {
	Text  string
	Style segStyle
}

// richText is an ordered list of styled runs, the analog of rich.Text.
type richText struct {
	segs []seg
}

// add appends a styled run and returns the receiver for chaining.
func (r *richText) add(text string, st segStyle) *richText {
	if text != "" {
		r.segs = append(r.segs, seg{Text: text, Style: st})
	}
	return r
}

// addFg appends a run with just a foreground color.
func (r *richText) addFg(text, fg string) *richText { return r.add(text, segStyle{Fg: fg}) }

// addPlain appends an unstyled run.
func (r *richText) addPlain(text string) *richText { return r.add(text, segStyle{}) }

// addText appends every run of another richText.
func (r *richText) addText(other richText) *richText {
	r.segs = append(r.segs, other.segs...)
	return r
}

// plain returns the concatenated text with all styling dropped.
func (r richText) plain() string {
	var b strings.Builder
	for _, s := range r.segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

// render produces the lipgloss-styled string for View().
func (r richText) render() string {
	var b strings.Builder
	for _, s := range r.segs {
		b.WriteString(s.Style.lip().Render(s.Text))
	}
	return b.String()
}

// lip builds the lipgloss.Style for a segment style.
func (s segStyle) lip() lipgloss.Style {
	st := lipgloss.NewStyle()
	if s.Fg != "" {
		st = st.Foreground(lipgloss.Color(s.Fg))
	}
	if s.Bold {
		st = st.Bold(true)
	}
	if s.Dim {
		st = st.Faint(true)
	}
	return st
}
