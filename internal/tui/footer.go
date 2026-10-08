// footer.go — the keybinding footer bar, the Go analog of Textual's Footer.
//
// Implements the footer-visible-binding tables of spec 09§3.1 (dashboard: s/w/q
// visible; back/g/f/j/k hidden), 09§3.6 (Switch: enter/b/back visible; j/k
// hidden), 09§3.7 (Watch: s always, enter Confirm only while selecting per the
// check_action gate, back visible; f/nav hidden), and 09§4.1 (Auto: l/t/back
// always, left/right/enter only while adjusting per check_action). Keys render
// in the accent color (Textual's footer-key-foreground = ACCENT, 09§8.1),
// labels muted. State-dependence mirrors Textual's check_action, which both
// hides a binding from the footer and makes it inert (09§11.5). The bar
// truncates gracefully at narrow widths via lipgloss display-width handling.
package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// footerBinding is one key+label hint shown in the footer bar.
type footerBinding struct {
	key   string
	label string
}

type footerScreen interface {
	footerBindings(m *Model) []footerBinding
}

const (
	footerSep     = "   " // gap between adjacent key+label entries
	footerEllipse = "…"   // graceful "more, truncated" marker
)

func footerText(bindings []footerBinding, width int) richText {
	var t richText
	if width <= 0 {
		width = 80
	}
	used := 0
	for i, b := range bindings {
		sepW := 0
		if i > 0 {
			sepW = lipgloss.Width(footerSep)
		}
		pieceW := lipgloss.Width(b.key + " " + b.label)
		if i > 0 && used+sepW+pieceW > width {
			t.addPlain(footerSep)
			t.addFg(footerEllipse, colMuted)
			return t
		}
		if i > 0 {
			t.addPlain(footerSep)
		}
		t.add(b.key, segStyle{Fg: colAccent, Bold: true})
		t.addFg(" "+b.label, colMuted)
		used += sepW + pieceW
	}
	return t
}

func (m *Model) renderFooter(bindings []footerBinding) string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	rendered := footerText(bindings, width).render()
	return lipgloss.NewStyle().MaxWidth(width).Render(rendered)
}
