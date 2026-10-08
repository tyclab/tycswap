package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/termsafe"
)

const (
	barFilled = "━"
	barHalf   = "╸"
	barEmpty  = "─"
	barTick   = "┃"
)

func barCells(pct *float64, width int, stale bool, threshold *float64) richText {
	var t richText
	if pct == nil {
		return *t.addFg(strings.Repeat(barEmpty, width), colTrack)
	}
	frac := math.Min(math.Max(*pct, 0.0), 100.0) / 100.0
	cells := frac * float64(width)
	full := int(cells)
	half := (cells-float64(full)) >= 0.5 && full < width
	tickAt := -1
	if threshold != nil {
		tickAt = int(math.Round(*threshold / 100.0 * float64(width)))
		if tickAt > width-1 {
			tickAt = width - 1
		}
		if tickAt < 0 {
			tickAt = 0
		}
	}
	color := severityColorF(*pct)
	fillStyle := segStyle{Fg: color, Dim: stale}
	for i := 0; i < width; i++ {
		switch {
		case tickAt >= 0 && i == tickAt:
			t.add(barTick, segStyle{Fg: colSevWarn})
		case i < full:
			t.add(barFilled, fillStyle)
		case i == full && half:
			t.add(barHalf, fillStyle)
		default:
			t.add(barEmpty, segStyle{Fg: colTrack})
		}
	}
	return t
}

func usageBar(label string, pct *float64, suffix string, width int, stale bool, threshold *float64) richText {
	var t richText
	t.addFg(label+" ", colMuted)
	t.addText(barCells(pct, width, stale, threshold))
	if pct == nil {
		t.addFg("  usage unknown", colMuted)
	} else {
		t.add(fmt.Sprintf(" %3.0f%%", *pct), segStyle{Fg: severityColorF(*pct), Dim: stale})
	}
	if suffix != "" {
		t.addFg("  "+suffix, colMuted)
	}
	return t
}

// resetParts returns the countdown suffix and its clock-extended variant for
// one window (09§5.2). Equal when no clock is known; both "" when no reset.
func resetParts(window map[string]any, now float64) (reset, resetFull string) {
	reset = resetText(window, now)
	if reset == "" {
		return "", ""
	}
	clock := resetClock(window, now)
	if clock != "" {
		return reset, reset + " · " + clock
	}
	return reset, reset
}

type usageRow struct {
	Label      string
	Pct        float64
	Suffix     string
	SuffixFull string
}

func usageRows(lastGood map[string]any, now float64) []usageRow {
	if lastGood == nil {
		return nil
	}
	var rows []usageRow
	if spend, ok := lastGood["spend"].(map[string]any); ok && spend != nil {
		used := asFloatOr(spend["used"], 0)
		limit := asFloatOr(spend["limit"], 0)
		amounts := fmt.Sprintf("$%s / $%s", formatMoney(used), formatMoney(limit))
		reset, resetFull := resetParts(spend, now)
		suffix := amounts
		suffixFull := amounts
		if reset != "" {
			suffix = reset + "  " + amounts
		}
		if resetFull != "" {
			suffixFull = resetFull + "  " + amounts
		}
		rows = append(rows, usageRow{"$$", asFloatOr(spend["pct"], 0), suffix, suffixFull})
	}
	for _, kl := range []struct{ key, label string }{{"five_hour", "5h"}, {"seven_day", "7d"}} {
		if window, ok := lastGood[kl.key].(map[string]any); ok && window != nil {
			reset, resetFull := resetParts(window, now)
			rows = append(rows, usageRow{kl.label, asFloatOr(window["pct"], 0), reset, resetFull})
		}
	}
	for _, window := range scopedList(lastGood) {
		pct := asFloatOr(window["pct"], 0)
		suffix, suffixFull := resetParts(window, now)
		if pct >= 100 {
			if suffix != "" {
				suffix = suffix + "  (!)"
			} else {
				suffix = "(!)"
			}
			if suffixFull != "" {
				suffixFull = suffixFull + "  (!)"
			} else {
				suffixFull = "(!)"
			}
		}
		name, _ := window["name"].(string)
		rows = append(rows, usageRow{name, pct, suffix, suffixFull})
	}
	return rows
}

func sentinelText(acc reporting.AccountSnapshot) string {
	label := sentinelLabel(acc.Usage.Sentinel)
	if acc.Usage.Sentinel == apiKeySentinel && acc.BaseURL != "" {
		if host := termsafe.Strip(ccsettings.Host(acc.BaseURL)); host != "" {
			label += " → " + host
		}
	}
	return label
}

// Stored strings come from exports, APIs and other tools' files: control sequences are stripped here, where drawn.
func identityText(acc reporting.AccountSnapshot) richText {
	var t richText
	email := termsafe.Strip(acc.Email)
	if acc.Alias != "" {
		t.add(termsafe.Strip(acc.Alias), segStyle{Fg: colAccent, Bold: true})
		t.addFg(" ("+email+")", colForeground)
	} else {
		t.addFg(email, colForeground)
	}
	return t
}

func accountCardText(acc reporting.AccountSnapshot, width int, threshold *float64, now float64) richText {
	var t richText
	t.add(fmt.Sprintf("%2s  ", acc.Number), segStyle{Fg: colForeground, Bold: true})
	t.addText(identityText(acc))
	t.addFg("  ["+termsafe.Strip(acc.DisplayTag())+"]", colMuted)
	if b := providerBadge(acc); b != "" {
		t.addFg(b, colMuted)
	}
	if acc.IsActive {
		t.add("   ● active", segStyle{Fg: colAccent, Bold: true})
	}
	if acc.Disabled {
		t.addFg("   (disabled)", colSevWarn)
	}
	if acc.AtLimit {
		t.add("   at limit: "+strings.Join(acc.LimitingWindows, ", "), segStyle{Fg: colSevCrit})
	}
	if age := formatAge(acc.Usage.AgeS); age != "" {
		t.addFg("   "+age, colMuted)
	}

	if acc.Usage.Sentinel != "" {
		t.addPlain("\n    ")
		st := segStyle{Fg: colSevWarn}
		marker := "⚠"
		if acc.Usage.Sentinel == apiKeySentinel {
			st = segStyle{Fg: colMuted}
			marker = "·"
		}
		t.add(marker+" "+sentinelText(acc), st)
		if acc.Usage.Sentinel != apiKeySentinel {
			if ls := lastSeenNote(acc.Usage); ls != "" {
				t.addPlain("\n    ")
				t.addFg("└ "+ls, colMuted)
			}
		}
		return t
	}

	rows := usageRows(acc.Usage.LastGood, now)
	if len(rows) == 0 {
		t.addPlain("\n    ")
		t.addFg("usage unavailable", colMuted)
		if acc.Usage.LastError != "" {
			t.addFg(" · "+acc.Usage.LastError, colMuted)
		}
		return t
	}

	stale := staleEntry(acc.Usage)
	labelWidth := 0
	for _, r := range rows {
		if len(r.Label) > labelWidth {
			labelWidth = len(r.Label)
		}
	}
	barWidth := width - 42 - labelWidth
	if barWidth > 30 {
		barWidth = 30
	}
	if barWidth < 12 {
		barWidth = 12
	}
	rowOverhead := 4 + labelWidth + 1 + barWidth + 5 + 2
	for _, r := range rows {
		suffix := r.Suffix
		if r.SuffixFull != r.Suffix && rowOverhead+len(r.SuffixFull) <= width {
			suffix = r.SuffixFull
		}
		t.addPlain("\n    ")
		pct := r.Pct
		t.addText(usageBar(padRight(r.Label, labelWidth), &pct, suffix, barWidth, stale, threshold))
	}
	return t
}

// miniLabelCell is a non-active account's identity, the one piece the monitor
// row keeps whichever layout renders it: the alias form ("alias (email)") when
// an alias is set, the org tag, and the "(disabled)" marker in the warning
// color (amber, not muted — Go-side deviation, DESIGN A18). It is the label
// cell of the shared table's monitor rows, and the head of miniAccountText.
func miniLabelCell(acc reporting.AccountSnapshot) richText {
	var t richText
	t.addText(identityText(acc))
	t.addFg("  ["+termsafe.Strip(acc.DisplayTag())+"]", colMuted)
	if b := providerBadge(acc); b != "" {
		t.addFg(b, colMuted)
	}
	if acc.Disabled {
		t.addFg("  (disabled)", colSevWarn)
	}
	return t
}

// monitorRow projects one non-active account onto a shared-table row (table.go)
// — the accounts monitor's layout (09§5.5) — carrying its slot, its label cell,
// and every window it reports rather than 5h/7d alone: a scoped window at 96%
// used to be invisible here until it hit 100%, and nothing said when anything
// reset. A sentinel or a slot with no usable measurement has no windows to lay
// out and becomes a SPAN row carrying the reason, exactly as the per-row layout
// writes it. Windows are enumerated on the empty model axis, so 5h and 7d count
// (the worse of them binds and carries the severity color) and a scoped window
// is informational — the same axis miniAccountText has always displayed. Every
// per-model window on this surface is therefore UNCOUNTED, which is why the
// cell's emphasis rule (cellPctStyle) has to keep exhaustion visible on an
// uncounted cell: a monitor row is where an exhausted per-model window is
// otherwise reported, and it is reported nowhere else at all.
//
// It takes no clock, and neither does the panel's projection: a row carries the
// raw resets_at, and the hour enters only where a layout SPELLS a countdown
// (renderClock). That is what lets the same rows be priced off the clock and
// drawn against it.
func monitorRow(acc reporting.AccountSnapshot) tableRow {
	label, stale := miniLabelCell(acc), staleEntry(acc.Usage)
	if acc.Usage.Sentinel != "" {
		fg := colSevWarn
		if acc.Usage.Sentinel == apiKeySentinel {
			fg = colMuted
		}
		return newSpanRow(acc.Number, label, sentinelText(acc), fg, stale)
	}
	windows := candidateWindows(acc.Usage.LastGood, nil)
	if len(windows) == 0 {
		return newSpanRow(acc.Number, label, "usage unknown", colMuted, stale)
	}
	return newWindowRow(acc.Number, label, windows, stale)
}

// monitorTableOpts is the monitor's slot-cell chrome: no indent (the mini rows
// start where the active card's header does) and the slot BOLD MUTED — the
// exact segment miniAccountText writes for its own slot cell, so a table row
// and the per-row line that replaces it at a narrower width present an account's
// number identically. Its headers take the ladder's own floor: this surface's
// per-row fallback names a scoped window only once it is exhausted, so columns
// bought with header width buy figures the fallback never states.
//
// Its policy is that same per-row layout: miniAccountText states EVERY exhausted
// window unconditionally, and states its reset too, so the table pins an
// exhausted column and sheds its countdown last. It has no rung holding a
// binding countdown back, so neither does the table.
var monitorTableOpts = tableOpts{
	slotStyle: segStyle{Fg: colMuted, Bold: true}, headerFloor: headerHardFloor,
	policy: tablePolicy{PinExhausted: true, KeepBindingCountdown: false},
}

// miniAccountText renders one minimized line for an inactive account (09§5.5),
// fitted to width. This is the monitor's PER-ROW fallback layout: the shared
// table is drawn whenever it displays more (see monitorLayout), and this shape
// renders every mini row when it does not — so it is also the RELEASE BAR the
// table is measured against, and it has to answer the same width the table was
// refused.
//
// Its windows come from candidateWindows, the one projection every surface reads
// (data.go), rather than from the stored map: a window the projection drops as
// unusable is a window this line may not state either, or the fallback would
// claim ("5h NaN%") what the table correctly refuses to show.
func miniAccountText(acc reporting.AccountSnapshot, width int, now float64) richText {
	line, _ := miniAccountPriced(acc, width, liveClock(now))
	return line
}

// miniAccountPriced is miniAccountText with what the line DISPLAYS: the figures
// and resets that survived the clip, the columns of any reason it states instead,
// and the columns of the identity cell. It is the bar the shared table is held to
// at the same width (layoutScore.atLeast).
//
// An exhausted per-model window is priced as a FIGURE: "Fable (!)" states that
// window's utilization — that the account cannot serve it at all — which is the
// same statement the table's cell makes with "100%", and the only statement this
// line has ever made about an uncounted window.
//
// clk spells the countdowns: live for the line that is drawn, widest for the bar
// (renderClock), so the bar this surface holds the table to reads no clock.
func miniAccountPriced(acc reporting.AccountSnapshot, width int, clk renderClock) (richText, layoutScore) {
	var t pricedText
	t.chrome(fmt.Sprintf("%2s  ", acc.Number), segStyle{Fg: colMuted, Bold: true})
	t.identityWhole(miniLabelCell(acc))
	t.chrome("   ", segStyle{})

	if acc.Usage.Sentinel != "" {
		st := segStyle{Fg: colSevWarn}
		if acc.Usage.Sentinel == apiKeySentinel {
			st = segStyle{Fg: colMuted}
		}
		t.spanWhole(sentinelText(acc), st)
		return t.fit(width)
	}

	stale := staleEntry(acc.Usage)
	parts := 0
	for _, w := range candidateWindows(acc.Usage.LastGood, nil) {
		if !w.Counted {
			if !w.Exhausted {
				continue
			}
			if parts > 0 {
				t.chrome(" · ", segStyle{Fg: colTrack})
			}
			t.figure(w.Label+" (!)", segStyle{Fg: colSevCrit})
			parts++
			continue
		}
		if parts > 0 {
			t.chrome(" · ", segStyle{Fg: colTrack})
		}
		t.chrome(w.Label+" ", segStyle{Fg: colMuted})
		t.figure(pctText(w.Pct), segStyle{Fg: severityColorF(w.Pct), Dim: stale})
		if w.Exhausted {
			if reset := clk.resetText(w.ResetsAt); reset != "" {
				t.countdown(" ("+reset+")", segStyle{Fg: colMuted})
			}
		}
		parts++
	}
	if parts == 0 {
		t.chrome("usage unknown", segStyle{Fg: colMuted})
	}
	return t.fit(width)
}

func monitorAccounts(snap *reporting.AccountsSnapshot) int {
	if snap == nil {
		return 0
	}
	return len(snap.Accounts)
}

// The table is laid out once across all non-active accounts; otherwise all fall back to mini lines, never mixed.
// width is the whole content budget: no caller draws a frame around these lines.
func monitorLayout(snap *reporting.AccountsSnapshot, width int, showMinis bool, threshold *float64, now float64, allowTable bool) (groups [][]richText, header string) {
	var minis []richText
	if showMinis {
		for _, acc := range snap.Accounts {
			if acc.IsActive {
				continue
			}
			minis = append(minis, miniAccountText(acc, width, now))
		}
	}
	var table windowTable
	tabled := false
	if allowTable && showMinis {
		var rows []tableRow
		for _, acc := range snap.Accounts {
			if !acc.IsActive {
				rows = append(rows, monitorRow(acc))
			}
		}
		// PRICED at the widest spelling every countdown's grammar allows, never at
		// this frame's: the bar the table clears must be the same bar on the next
		// frame, or the monitor rearranges itself while the user watches. The lines
		// above are the ones DRAWN, spelled live, and they state at least this much.
		perRow := func(at int) layoutScore {
			var s layoutScore
			for _, acc := range snap.Accounts {
				if acc.IsActive {
					continue
				}
				_, score := miniAccountPriced(acc, at, widestClock())
				s = s.plus(score)
			}
			return s
		}
		// The table's columns are the union across accounts, so it can cost more
		// than it states — em dashes where an account reports no such window, and
		// countdowns shed to pay for them. It is drawn only where it displays no
		// less than the per-row layout it replaces, which is what makes the monitor
		// never say less than the lines it replaces (I13).
		table, tabled = pickWindowTable(rows, width, now, monitorTableOpts, perRow)
	}
	// Every block is fitted to the monitor's own width before it leaves here, the
	// way the candidates panel fits each of its rows (candidateRowText): the table
	// never overruns, but the account card and the per-row mini line are laid out
	// from their content and this is the one place that can hold them to the
	// terminal.
	fit := func(t richText) richText { return clipRichLines(t, width) }
	next := 0
	for _, acc := range snap.Accounts {
		switch {
		case acc.IsActive:
			groups = append(groups, []richText{fit(accountCardText(acc, width, threshold, now))})
		case !showMinis:
		case !tabled:
			groups = append(groups, []richText{minis[next]})
			next++
		default:
			var group []richText
			if next == 0 && len(table.Header.segs) > 0 {
				group = append(group, fit(table.Header))
				header = group[0].render()
			}
			groups = append(groups, append(group, fit(table.Lines[next])))
			next++
		}
	}
	return groups, header
}

// joinBlocks stacks monitor blocks with the breathe rule (09§5.6): a blank line
// around any multi-line (expanded) block, a single newline between two minis.
func joinBlocks(blocks []richText) richText {
	var t richText
	previousMultiline := false
	for i, block := range blocks {
		multiline := strings.Contains(block.plain(), "\n")
		if i > 0 {
			if multiline || previousMultiline {
				t.addPlain("\n\n")
			} else {
				t.addPlain("\n")
			}
		}
		t.addText(block)
		previousMultiline = multiline
	}
	return t
}

// accountsPanelText renders the always-visible monitor (09§5.6): the active
// account full-size, others as one-line minis when showMinis. snap nil →
// "loading…"; no accounts → the two-line empty hint. Blocks breathe (blank
// line) around any multi-line (expanded) block.
func accountsPanelText(snap *reporting.AccountsSnapshot, width int, showMinis bool, threshold *float64, now float64) richText {
	return monitorPanelText(snap, width, showMinis, threshold, now, true)
}

func monitorPanelText(snap *reporting.AccountsSnapshot, width int, showMinis bool, threshold *float64, now float64, allowTable bool) richText {
	var t richText
	if snap == nil {
		// Fitted like every other block this panel emits: a terminal narrower than
		// the word is where it matters, and this branch is live on every frame before
		// the first poll returns.
		t.addFg("loading…", colMuted)
		return clipRichLines(t, width)
	}
	if len(snap.Accounts) == 0 {
		t.addFg("No managed accounts yet.\nUse the menu below: Add account — from your current Claude Code login, or from a setup-token / API key.", colMuted)
		return clipRichLines(t, width)
	}
	groups, _ := monitorLayout(snap, width, showMinis, threshold, now, allowTable)
	var blocks []richText
	for _, g := range groups {
		blocks = append(blocks, g...)
	}
	if len(blocks) == 0 {
		// Fitted like every other block this panel emits: the line is 23 columns of
		// literal and a terminal narrower than that is exactly where it matters.
		t.addFg("no active managed login", colMuted)
		return clipRichLines(t, width)
	}
	return joinBlocks(blocks)
}

// accountsMonitorCapped renders the dashboard monitor (showMinis) into at most
// budget lines, dropping trailing accounts and appending a muted "· N more
// accounts" indicator for the ones it elided (the sweep fix for the dashboard:
// a panel truncates rather than pushing the interactive menu off the screen).
// budget<=0 yields no lines. Short/empty monitors (loading, the empty hint, or
// a monitor that already fits) render whole.
//
// The cap works in whole accounts (monitorLayout): the shared table's column
// header is charged to the first mini row's group and admitted with it, so a
// budget that cannot fit that row drops its header too — the monitor never
// shows a header row with nothing under it, and never drops the header while
// rows below it remain.
//
// That header costs a line the per-row layout never spends, and a line is an
// account. So the budget is priced in BOTH layouts and the better bargain wins
// (monitorFit.beats): the table is preferred, but never at the price of an
// account the per-row layout would have shown, and never while it leaves budget
// lines unused with accounts still elided. The overflow indicator outranks the
// header on the same rule — a budget too small to hold the header, a mini row
// and the indicator at once takes the per-row layout, so the count of elided
// accounts is never the line that gets clipped. When neither layout can keep
// the indicator — a one-line budget — the table stands and the lone-header
// rescue in cappedMonitor applies.
func accountsMonitorCapped(snap *reporting.AccountsSnapshot, width int, threshold *float64, now float64, budget int) []string {
	if budget <= 0 {
		return nil
	}
	tabled := fitMonitor(snap, width, threshold, now, budget, true)
	if perRow := fitMonitor(snap, width, threshold, now, budget, false); perRow.beats(tabled) {
		return perRow.lines
	}
	return tabled.lines
}

type monitorFit struct {
	lines     []string
	shown     int
	indicated bool
}

// Ties go to other, priced first and always the table: equal content keeps the aligned columns.
func (f monitorFit) beats(other monitorFit) bool {
	if f.shown != other.shown {
		return f.shown > other.shown
	}
	return f.indicated && !other.indicated
}

// fitMonitor renders the monitor into at most budget lines through ONE layout
// and reports what that bought. A monitor that already fits is never capped at
// all — which is where the table's column header can cost an account, since the
// per-row layout of the same monitor is one line shorter.
func fitMonitor(snap *reporting.AccountsSnapshot, width int, threshold *float64, now float64, budget int, allowTable bool) monitorFit {
	lines := strings.Split(monitorPanelText(snap, width, true, threshold, now, allowTable).render(), "\n")
	if len(lines) <= budget {
		return monitorFit{lines: lines, shown: monitorAccounts(snap), indicated: true}
	}
	return cappedMonitor(snap, width, threshold, now, budget, allowTable)
}

func cappedMonitor(snap *reporting.AccountsSnapshot, width int, threshold *float64, now float64, budget int, allowTable bool) monitorFit {
	groups, header := monitorLayout(snap, width, true, threshold, now, allowTable)
	var acc richText
	prevMultiline := false
	shown := 0
	for i, group := range groups {
		var trial richText
		trial.addText(acc)
		last := prevMultiline
		for j, block := range group {
			multiline := strings.Contains(block.plain(), "\n")
			if i > 0 || j > 0 {
				if multiline || last {
					trial.addPlain("\n\n")
				} else {
					trial.addPlain("\n")
				}
			}
			trial.addText(block)
			last = multiline
		}
		if shown > 0 && strings.Count(trial.plain(), "\n")+1 > budget-1 {
			break
		}
		acc = trial
		prevMultiline = last
		shown++
	}
	lines := strings.Split(acc.render(), "\n")
	indicated := true
	if hidden := len(groups) - shown; hidden > 0 {
		lines = append(lines, mutedLine(clipText(
			fmt.Sprintf("· %d more account%s", hidden, plural(hidden)), width)))
		indicated = len(lines) <= budget // else the slice below clips it away
	}
	if len(lines) > budget {
		lines = lines[:budget]
	}
	if header != "" && len(lines) == 1 && lines[0] == header && len(groups[0]) > 1 {
		lines[0] = groups[0][1].render()
	}
	return monitorFit{lines: lines, shown: shown, indicated: indicated}
}

func formatMoney(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%.2f", v)
	dot := strings.IndexByte(s, '.')
	intPart, frac := s[:dot], s[dot:]
	n := len(intPart)
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 && (n-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(intPart[i])
	}
	out := b.String() + frac
	if neg {
		out = "-" + out
	}
	return out
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func asFloatOr(v any, def float64) float64 {
	if p := numericPct(v); p != nil {
		return *p
	}
	return def
}
