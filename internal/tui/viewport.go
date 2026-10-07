package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// clampInt clamps v to [lo, hi]. Callers pass lo <= hi.
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// plural returns "s" unless n == 1, for "N account(s)" style indicators.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// mutedLine styles a single muted overflow/breadcrumb line.
func mutedLine(s string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(colMuted)).Render(s)
}

// windowLines returns at most budget lines of `lines`, keeping the cursor block
// ([cursorStart,cursorEnd)) visible when hasCursor, else a top-anchored window
// starting at scrollTop (monitor-scroll, 09§3.7). Hidden content above/below is
// flagged with a muted indicator overlaid onto the window's first/last line —
// but never one that would hide part of the cursor block. Blocks may be multi-
// line (account cards); the window is computed at line granularity so variable
// card heights are handled naturally.
func windowLines(lines []string, budget, cursorStart, cursorEnd, scrollTop int, hasCursor bool) []string {
	n := len(lines)
	if budget <= 0 {
		return nil
	}
	if n <= budget {
		return lines
	}
	var top int
	if hasCursor && cursorStart >= 0 {
		cb := cursorEnd - cursorStart
		top = clampInt(cursorStart-(budget-cb)/2, 0, n-budget)
		// Guarantee the cursor block is inside the window (or its top, if the
		// block is taller than the whole budget).
		if cb >= budget {
			top = clampInt(cursorStart, 0, n-budget)
		} else {
			if cursorStart < top {
				top = cursorStart
			}
			if cursorEnd > top+budget {
				top = cursorEnd - budget
			}
			top = clampInt(top, 0, n-budget)
		}
	} else {
		top = clampInt(scrollTop, 0, n-budget)
	}
	win := make([]string, budget)
	copy(win, lines[top:top+budget])
	if top > 0 && (!hasCursor || cursorStart > top) {
		win[0] = mutedLine(fmt.Sprintf("↑ %d more", top))
	}
	if top+budget < n && (!hasCursor || cursorEnd <= top+budget-1) {
		win[budget-1] = mutedLine(fmt.Sprintf("↓ %d more", n-(top+budget)))
	}
	return win
}

// windowRows windows a list of single-line rows around the cursor (the dashboard
// menu). It is windowLines with a one-line cursor block.
func windowRows(rows []string, budget, cursor int) []string {
	return windowLines(rows, budget, cursor, cursor+1, 0, true)
}
