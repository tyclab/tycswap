// table.go: one window table for the auto panel and the dashboard monitor. Columns are the union of WINDOW rows' labels in a
// canonical order (5h, 7d, scoped by name), keyed by (label, occurrence) so a repeated label never overwrites a cell. Counted
// figures carry their severity colour, the binding one bold; only uncounted, unexhausted cells are muted and dim (DESIGN A18).
// The table never wraps: it sheds headers, then countdowns, then label width, then whole label groups (renderWindowTable).
// Pinned columns (counted, protected, exhausted per policy) never drop; below minTableWidth no table exists and the surface
// draws its per-row layout for every row. The draw choice is priced with countdowns at their widest, so it reads no clock
// and stays monotone in the width (releaseBar).

package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	tableGutter  = "  " // label cell → first column, and between columns
	tableCdGap   = "  " // a cell's percentage → its countdown
	tableSlotGap = "  " // the slot number → the label cell
	tableMissing = "—"  // a row that does not report a column's window
)

type tableRowKind int

const (
	windowRowKind tableRowKind = iota // window cells, laid into the columns
	spanRowKind                       // one message written across the columns
)

// tableRow is one account line of the shared table: a WINDOW row carrying its
// own cells, or a SPAN row whose Span (in SpanFg) is written across the window
// columns instead. Stale dims the row's window cells the way the mini account
// line dims a stale measurement (09§5.4); it never dims the label cell.
//
// Span and every Label segment are NEWLINE-FREE by construction. The table emits
// no line break of its own and every caller joins its lines itself, so a break
// smuggled in through a cell would land inside a STYLED segment — which lipgloss
// pads out to the widest line, appending that padding to the row above and
// pushing it past the terminal (DESIGN A18).
type tableRow struct {
	Slot    string
	Label   richText
	Windows []candidateWindow
	Span    string
	SpanFg  string
	Stale   bool
	kind    tableRowKind
}

func newWindowRow(slot string, label richText, windows []candidateWindow, stale bool) tableRow {
	return tableRow{Slot: slot, Label: oneLineRich(label), Windows: windows,
		Stale: stale, kind: windowRowKind}
}

func newSpanRow(slot string, label richText, span, spanFg string, stale bool) tableRow {
	return tableRow{Slot: slot, Label: oneLineRich(label), Span: oneLine(span),
		SpanFg: spanFg, Stale: stale, kind: spanRowKind}
}

func (r tableRow) span() bool { return r.kind == spanRowKind }

func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

func oneLineRich(t richText) richText {
	folded := false
	for _, s := range t.segs {
		if strings.ContainsAny(s.Text, "\r\n") {
			folded = true
			break
		}
	}
	if !folded {
		return t
	}
	segs := make([]seg, len(t.segs))
	for i, s := range t.segs {
		segs[i] = seg{Text: oneLine(s.Text), Style: s.Style}
	}
	return richText{segs: segs}
}

// tablePolicy is the half of the width ladder that cannot read the same way on
// both surfaces, because the table's release bar is each surface's OWN per-row
// fallback and the two fallbacks say different things:
//
//   - PinExhausted — a column some row has RUN OUT in is never dropped, and its
//     countdown is the last one the row gives up. The monitor's per-row layout
//     states an exhausted window unconditionally ("Fable (!)", "5h 100%
//     (resets 12m)"), so the table has to as well. The panel's per-row layout
//     DISCARDS an exhausted uncounted figure at every width, so pinning one
//     there would trade the panel's whole table away to protect a figure its own
//     fallback then throws away.
//   - KeepBindingCountdown — the BINDING cell's countdown is the last countdown
//     shed, as candidateShedSteps' final rung holds it back on the panel. The
//     monitor's per-row layout has no such rung, so there it sheds with the rest.
type tablePolicy struct {
	PinExhausted         bool
	KeepBindingCountdown bool
}

// tableOpts is the per-surface slot-cell chrome. headerFloor: the panel keeps a whole syllable (its fallback prints full
// model names); the monitor takes the ladder's own floor.
type tableOpts struct {
	indent      int
	slotStyle   segStyle
	headerFloor int
	policy      tablePolicy
}

type windowTable struct {
	Header richText
	Lines  []richText
}

// windowColumn is one column of the table: the window it names, which
// occurrence of that name it carries, whether the window counts on the current
// model axis, what the rows lay into it, and the width ladder's state for it.
//
// A column is sized from its DATA, never its name: pctW covers its percentages, and the header fits over that (bodyW).
type windowColumn struct {
	label     string
	occ       int      // which occurrence of label within a row this column is
	hdr       string   // the header as currently spelled (ladder[level])
	ladder    []string // the spellings, finest first (headerLadders)
	counted   bool
	binding   bool // some row's BINDING cell sits here
	exhausted bool // some row has run out here (candidateWindow.Exhausted)
	reports   int  // how many rows lay a figure into it
	pinned    bool // no width may drop it (pinTableColumns)
	pctW      int  // percentage sub-width, measured over the CELLS alone
	cdW       int  // countdown sub-width (0 when no row shows one here)
	showCd    bool // a countdown is shown here (implies cdW > 0)
	dropped   bool // the whole column has been shed (non-pinned columns only)
}

func (c windowColumn) bodyW() int {
	if h := lipgloss.Width(c.hdr); h > c.pctW {
		return h
	}
	return c.pctW
}

func (c windowColumn) hdrMin() int {
	if len(c.ladder) == 0 {
		return 0
	}
	return lipgloss.Width(c.ladder[len(c.ladder)-1])
}

// floorW is the narrowest the column's body can ever be drawn: its figures, or
// its coarsest spelling, whichever needs more. It is the term minTableWidth
// charges for the column and it reads no clock — no countdown survives at the
// floor.
func (c windowColumn) floorW() int {
	if h := c.hdrMin(); h > c.pctW {
		return h
	}
	return c.pctW
}

func (c windowColumn) width() int {
	if c.showCd {
		return c.bodyW() + lipgloss.Width(tableCdGap) + c.cdW
	}
	return c.bodyW()
}

type tableCell struct {
	present   bool
	pct       string  // the rendered percentage ("88%")
	value     float64 // ... and the number behind it, for the severity ramp
	countdown string
	counted   bool
	binding   bool // the row's binding window, always one of its COUNTED ones
	exhausted bool // the window has run out (candidateWindow.Exhausted)
}

func tableCountdown(resetsAt string, now float64) string {
	return strings.TrimPrefix(candidateCountdown(resetsAt, now), "resets ")
}

type layoutScore struct {
	figures    int // window utilization figures rendered whole
	countdowns int // reset countdowns rendered whole
	spanChars  int // columns of span-row MESSAGE rendered (never the cut marker)
	identChars int // columns of account identity rendered — MEASURED, never compared
}

func (s layoutScore) plus(o layoutScore) layoutScore {
	return layoutScore{
		figures:    s.figures + o.figures,
		countdowns: s.countdowns + o.countdowns,
		spanChars:  s.spanChars + o.spanChars,
		identChars: s.identChars + o.identChars,
	}
}

// atLeast reports whether s displays no less than other on EVERY data axis.
//
// DOMINANCE, never a sum: the axes do not convert into one another, and no
// exchange rate between a countdown and a column of a message is defensible. A
// TIE is a win for the table — where both layouts state the same figures, the
// same resets and the same reasons, the aligned columns are the value the table
// adds and the reader pays nothing for them.
//
// IDENTITY IS NOT AN AXIS. A shared-column table buys its alignment out of the
// identity cell, and the slot number `tycswap use N` takes names the account
// either way; were identity compared here, the table would be refused at exactly
// the widths where it is doing what it is for. It is measured (identChars) and
// reported, and a surface may therefore show a SHORTER email at a width where it
// switched to the table — the one quantity this choice does not keep monotone.
func (s layoutScore) atLeast(other layoutScore) bool {
	return s.figures >= other.figures &&
		s.countdowns >= other.countdowns &&
		s.spanChars >= other.spanChars
}

func releaseBar(here, ref layoutScore) layoutScore {
	return layoutScore{figures: ref.figures, countdowns: ref.countdowns, spanChars: here.spanChars}
}

type pricedKind int

const (
	pricedIdent     pricedKind = iota // the account's identity cell
	pricedFigure                      // one window's utilization figure
	pricedCountdown                   // one window's reset countdown
	pricedSpan                        // a span row's message
)

type pricedMark struct {
	kind       pricedKind
	start, end int
	content    int
}

type pricedText struct {
	t     richText
	at    int
	marks []pricedMark
}

func (p *pricedText) chrome(text string, st segStyle) {
	p.t.add(text, st)
	p.at += lipgloss.Width(text)
}

func (p *pricedText) datum(text string, st segStyle, kind pricedKind, content int) {
	start := p.at
	p.chrome(text, st)
	if p.at > start {
		p.marks = append(p.marks, pricedMark{kind: kind, start: start, end: p.at, content: content})
	}
}

func (p *pricedText) figure(text string, st segStyle) { p.datum(text, st, pricedFigure, 0) }

func (p *pricedText) countdown(text string, st segStyle) { p.datum(text, st, pricedCountdown, 0) }

func (p *pricedText) span(text string, st segStyle, full int) {
	p.datum(text, st, pricedSpan, shownContent(full, lipgloss.Width(text)))
}

func (p *pricedText) spanWhole(text string, st segStyle) {
	p.datum(text, st, pricedSpan, lipgloss.Width(text))
}

func (p *pricedText) identityWhole(t richText) { p.identity(t, rtWidth(t)) }

func (p *pricedText) identityRun(text string, st segStyle, full int) {
	p.datum(text, st, pricedIdent, shownContent(full, lipgloss.Width(text)))
}

func (p *pricedText) identity(t richText, full int) {
	start := p.at
	p.t.addText(t)
	p.at += rtWidth(t)
	if p.at > start {
		p.marks = append(p.marks, pricedMark{kind: pricedIdent, start: start, end: p.at,
			content: shownContent(full, p.at-start)})
	}
}

func (p pricedText) fit(width int) (richText, layoutScore) {
	out := truncRich(p.t, width)
	limit := rtWidth(out)
	if limit < p.at {
		limit -= lipgloss.Width(footerEllipse)
	}
	var s layoutScore
	for _, m := range p.marks {
		shown := m.content
		if m.end > limit {
			shown = limit - m.start
		}
		if shown > m.content {
			shown = m.content
		}
		if shown < 0 {
			shown = 0
		}
		switch {
		case m.kind == pricedIdent:
			s.identChars += shown
		case m.kind == pricedSpan:
			s.spanChars += shown
		case m.end > limit: // a half-drawn figure or countdown is neither
		case m.kind == pricedFigure:
			s.figures++
		case m.kind == pricedCountdown:
			s.countdowns++
		}
	}
	return out, s
}

func shownContent(full, shown int) int {
	if shown >= full {
		return shown
	}
	if c := shown - lipgloss.Width(footerEllipse); c > 0 {
		return c
	}
	return 0
}

// A TEST SEAM: surfaces call pickWindowTable, since a table drawn on existence alone may lose to the per-row layout.
func renderWindowTable(rows []tableRow, width int, now float64, opts tableOpts) (windowTable, bool) {
	tbl, _, ok := priceWindowTable(rows, width, now, opts)
	return tbl, ok
}

func priceWindowTable(rows []tableRow, width int, now float64, opts tableOpts) (windowTable, layoutScore, bool) {
	lay, ok := layoutWindowTable(rows, width, liveClock(now), opts)
	if !ok {
		return windowTable{}, layoutScore{}, false
	}
	tbl, score := lay.render(opts)
	return tbl, score, true
}

// perRowPricer is what a surface's OWN per-row layout displays at a width — the
// bar the shared table is held to. It is asked for a PRICED layout, spelling
// every countdown at countdownWidest (widestClock), so that the bar reads no
// clock; the lines the surface actually draws when the table loses are spelled
// live and state at least as much.
type perRowPricer func(width int) layoutScore

// pickWindowTable is the choice both surfaces make on every render: price the
// shared table AND the surface's own per-row layout, and report the table only
// when it is the one that displays more. ok false means DRAW THE PER-ROW LAYOUT
// — either because no table exists at this width (minTableWidth, the cheap
// pre-check that avoids building one that cannot be) or because the table it
// would draw says less than the lines it would replace.
//
// A union-column table is not universally better than a per-row layout: its
// columns are the union ACROSS rows, so every row pays for every other row's
// windows, em dashes included, and the countdowns are what it sheds to pay. The
// choice is therefore priced at render time rather than declared once — and it
// is priced against releaseBar, which is what keeps it monotone in the width.
//
// SCORING AND RENDERING ARE SEPARATE LAYOUTS, and that is what keeps the choice
// off the clock. The layout that is PRICED spells every countdown at
// countdownWidest, so both sides of the comparison — the table's score, the
// reference width the bar is taken at (fullWidth), and the per-row layout at
// that width — are functions of (rows, width, opts) alone. The layout that is
// DRAWN spells them live and therefore sheds no more than the priced one did, so
// the terminal shows at least what cleared the bar and usually more. Paying the
// widest spelling in the DRAWN layout instead would have bought the same
// stability with real columns, on every frame, forever. What it costs instead is
// a second ladder walk per render, and no width at all.
func pickWindowTable(rows []tableRow, width int, now float64, opts tableOpts, perRow perRowPricer) (windowTable, bool) {
	priced, ok := layoutWindowTable(rows, width, widestClock(), opts)
	if !ok {
		return windowTable{}, false
	}
	_, score := priced.render(opts)
	here := perRow(width)
	ref := here
	if priced.full > width {
		ref = perRow(priced.full)
	}
	if !score.atLeast(releaseBar(here, ref)) {
		return windowTable{}, false
	}
	drawn, ok := layoutWindowTable(rows, width, liveClock(now), opts)
	if !ok {
		// Unreachable: whether a table EXISTS is minTableWidth's one comparison and
		// no term of it reads the clock, so the two layouts agree on it always.
		return windowTable{}, false
	}
	tbl, _ := drawn.render(opts)
	return tbl, true
}

type tableLayout struct {
	rows                    []tableRow
	cols                    []*windowColumn
	grid                    [][]tableCell
	spans                   tableSpans
	slotW, slotNumW, labelW int
	width                   int
	full                    int // the width at which nothing more is shed (fullWidth)
	level                   int // the header abbreviation level in force
	elided                  int // columns rung (g) dropped, for the header's "+N"
}

func measureTable(rows []tableRow, clk renderClock, opts tableOpts) tableLayout {
	cols, at := tableColumns(rows)
	grid := tableGrid(rows, at, len(cols), clk)
	measureColumns(cols, grid)
	headerLadders(cols, opts.headerFloor)
	pinTableColumns(rows, grid, cols, opts.policy)

	slotNumW, labelW := 2, 0
	for _, r := range rows {
		if w := lipgloss.Width(r.Slot); w > slotNumW {
			slotNumW = w
		}
		if w := rtWidth(r.Label); w > labelW {
			labelW = w
		}
	}
	return tableLayout{rows: rows, cols: cols, grid: grid, spans: measureSpans(rows),
		slotW:    opts.indent + slotNumW + lipgloss.Width(tableSlotGap),
		slotNumW: slotNumW, labelW: labelW}
}

// labelFloor is the narrowest the SHARED identity cell may be narrowed to: one
// column for the bare ellipsis, and none at all when no row carries a label —
// a table of unlabelled rows must not reserve a column for nothing.
func (l tableLayout) labelFloor() int {
	if l.labelW == 0 {
		return 0
	}
	return 1
}

// minTableWidth is the narrowest terminal this table can be laid out in: the
// width of the fully-shed table, in closed form.
//
//	slot cell + label floor
//	  + for each PINNED column: the gutter + max(its figures, its coarsest name)
//	  ... or, when a SPAN row asks for more,
//	slot cell + label floor + the gutter + the widest span floor
//
// Below it the caller renders its own per-row layout; at or above it the ladder
// is guaranteed to reach a fitting state, which is why EXISTENCE needs no trial
// layout (whether the table that exists is the one to draw is priced separately,
// pickWindowTable). No term reads the clock: every countdown is already shed at
// the floor, so `now` cannot appear in it.
func (l tableLayout) minTableWidth() int {
	if len(l.rows) == 0 {
		return 0
	}
	floor := l.labelFloor()
	window := l.slotW + floor
	for _, c := range l.cols {
		if c.pinned {
			window += lipgloss.Width(tableGutter) + c.floorW()
		}
	}
	span := 0
	if l.spans.any {
		span = l.slotW + floor + lipgloss.Width(tableGutter) + l.spans.floor
	}
	if span > window {
		return span
	}
	return window
}

func minTableWidth(rows []tableRow, opts tableOpts) int {
	if len(rows) == 0 {
		return 0
	}
	// The floor reads no countdown, so the clock it is measured at cannot move it.
	return measureTable(rows, liveClock(0), opts).minTableWidth()
}

func layoutWindowTable(rows []tableRow, width int, clk renderClock, opts tableOpts) (tableLayout, bool) {
	if len(rows) == 0 {
		return tableLayout{width: width}, true
	}
	l := measureTable(rows, clk, opts)
	if width < l.minTableWidth() {
		return tableLayout{}, false
	}
	l.width, l.full = width, l.fullWidth()
	l.shed(width, opts.policy)
	if !l.sound(width) {
		// Unreachable: at or above minTableWidth the fully-shed table fits, every
		// span row clears its floor and every window row keeps its protected cell.
		// Kept as a hard post-condition (never a gate) so that a change to the
		// pinned sets which made one of them reachable again shows up as a surface
		// that drew its per-row layout rather than as a table that lies (I2/I4).
		return tableLayout{}, false
	}
	return l, true
}

func (l *tableLayout) shed(width int, policy tablePolicy) {
	for l.windowRowsWidth() > width {
		next, moved := shrinkTableHeaders(l.cols, l.level)
		if !moved {
			break
		}
		l.level = next
	}
	for l.windowRowsWidth() > width && l.shedCountdown(policy, false) {
	}
	if over := l.tableWidth() - width; over > 0 {
		if floor := l.labelFloor(); l.labelW-over < floor {
			l.labelW = floor
		} else {
			l.labelW -= over
		}
	}
	for l.windowRowsWidth() > width && l.shedCountdown(policy, true) {
	}
	for l.windowRowsWidth() > width && l.dropTableGroup() {
	}
}

func (l tableLayout) sound(width int) bool {
	return l.windowRowsWidth() <= width && l.spansFit(width) && l.rowsKeepAFigure()
}

func (l tableLayout) render(opts tableOpts) (windowTable, layoutScore) {
	if len(l.rows) == 0 {
		return windowTable{}, layoutScore{}
	}
	out := windowTable{Header: l.header()}
	var score layoutScore
	for i, r := range l.rows {
		line, s := tableLine(r, l.grid[i], l.cols, l.slotW, l.slotNumW, l.labelW, l.width, opts)
		out.Lines = append(out.Lines, line)
		score = score.plus(s)
	}
	return out, score
}

func tableColumns(rows []tableRow) ([]*windowColumn, [][]int) {
	type colKey struct {
		label string
		occ   int
	}
	var cols []*windowColumn
	index := map[colKey]int{}
	at := make([][]int, len(rows))
	for i, r := range rows {
		at[i] = make([]int, len(r.Windows))
		seen := map[string]int{}
		for k, w := range r.Windows {
			key := colKey{label: w.Label, occ: seen[w.Label]}
			seen[w.Label]++
			j, ok := index[key]
			if !ok {
				j = len(cols)
				index[key] = j
				cols = append(cols, &windowColumn{label: key.label, occ: key.occ})
			}
			if w.Counted {
				cols[j].counted = true
			}
			at[i][k] = j
		}
	}
	return canonicalTableColumns(cols, at)
}

const (
	windowLabel5h = "5h"
	windowLabel7d = "7d"
)

func tableColumnRank(label string) int {
	switch label {
	case windowLabel5h:
		return 0
	case windowLabel7d:
		return 1
	}
	return 2
}

func canonicalTableColumns(cols []*windowColumn, at [][]int) ([]*windowColumn, [][]int) {
	order := make([]int, len(cols))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := cols[order[a]], cols[order[b]]
		if rx, ry := tableColumnRank(x.label), tableColumnRank(y.label); rx != ry {
			return rx < ry
		}
		if x.label != y.label {
			return x.label < y.label
		}
		return x.occ < y.occ
	})
	moved := make([]int, len(cols))
	sorted := make([]*windowColumn, len(cols))
	for to, from := range order {
		moved[from] = to
		sorted[to] = cols[from]
	}
	for i := range at {
		for k := range at[i] {
			at[i][k] = moved[at[i][k]]
		}
	}
	return sorted, at
}

func tableGrid(rows []tableRow, at [][]int, ncols int, clk renderClock) [][]tableCell {
	grid := make([][]tableCell, len(rows))
	for i, r := range rows {
		grid[i] = make([]tableCell, ncols)
		for k, w := range r.Windows {
			grid[i][at[i][k]] = tableCell{
				present:   true,
				pct:       pctText(w.Pct),
				value:     w.Pct,
				countdown: clk.countdown(w.ResetsAt),
				counted:   w.Counted,
				binding:   w.Binding,
				exhausted: w.Exhausted,
			}
		}
	}
	return grid
}

func measureColumns(cols []*windowColumn, grid [][]tableCell) {
	for j, c := range cols {
		c.pctW, c.reports = 0, 0
		c.cdW, c.showCd = 0, false
		for i := range grid {
			cell := grid[i][j]
			if !cell.present {
				continue
			}
			c.reports++
			if w := lipgloss.Width(cell.pct); w > c.pctW {
				c.pctW = w
			}
			if cell.exhausted {
				c.exhausted = true
			}
			if cell.binding {
				c.binding = true
			}
			if cell.countdown != "" {
				c.showCd = true
				if cw := lipgloss.Width(cell.countdown); cw > c.cdW {
					c.cdW = cw
				}
			}
		}
	}
}

func pinTableColumns(rows []tableRow, grid [][]tableCell, cols []*windowColumn, policy tablePolicy) {
	for _, c := range cols {
		c.pinned = c.counted
	}
	pin := func(j int) {
		if j >= 0 {
			cols[j].pinned = true
		}
	}
	for i, r := range rows {
		if r.span() {
			continue
		}
		pin(protectedColumn(grid[i]))
		if !policy.PinExhausted {
			continue
		}
		for j, cell := range grid[i] {
			if cell.present && cell.exhausted {
				pin(j)
			}
		}
	}
	group := map[string]bool{}
	for _, c := range cols {
		if c.pinned {
			group[c.label] = true
		}
	}
	for _, c := range cols {
		if group[c.label] {
			c.pinned = true
		}
	}
}

// protectedColumn is the one cell a WINDOW row may never be laid out without:
// its BINDING window — the figure the ranking orders by and the engine decides
// on — or, for a row with no counted cell at all, its HIGHEST figure, which is
// what such a row is read by. -1 for a row that reports nothing.
//
// The monitor's scoped-only shape is the second case and it is not a corner: an
// account whose only windows are per-model ones enumerates them on the empty
// axis, so not one of its cells counts, and the figure that says whether the
// account can serve anything is the largest of them.
func protectedColumn(cells []tableCell) int {
	best := -1
	for j, cell := range cells {
		if !cell.present {
			continue
		}
		if cell.binding {
			return j
		}
		if best < 0 || cell.value > cells[best].value {
			best = j
		}
	}
	return best
}

const headerHardFloor = 2

// headerLadders spells every column's header at each level of the abbreviation
// ladder, finest first, and installs level 0 (the full labels).
//
// THE LADDER IS INJECTIVE OR IT IS NOT A LADDER. A level is admitted only when
// its map from DISTINCT label to spelling is one-to-one over this table's whole
// label set — abbreviating two different models to one string is not terse, it
// is FALSE, and a header that names the wrong window is worse than a table that
// does not fit. Two columns carrying the SAME label are spelled the same on
// purpose: the account really does report two windows of that model.
//
// The levels, coarsening downward:
//
//	level 0   the labels themselves
//	level 1   minus the token prefix every scoped label shares ("claude-")
//	level 2   minus a trailing eight-digit date token ("-20251101")
//	level 3+  middle-elided at k columns, k descending to the floor — the head
//	          and the tail both survive, because a model name distinguishes
//	          itself at both ends
//
// Levels 1 and 2 are preferred to any elision because they are STABLE: they
// depend on the label set only through its shared prefix, so adding an account
// leaves the other headers spelled exactly as they were. "5h" and "7d" never
// abbreviate at any level — they are two columns already, and they are the two
// windows every other surface names the same way.
//
// The result is a pure function of the distinct-label set, so a header's
// spelling is a property of what the table contains and never of how wide the
// terminal is at this moment: only WHICH level is in force is a width question.
func headerLadders(cols []*windowColumn, floor int) {
	if floor < headerHardFloor {
		floor = headerHardFloor
	}
	labels, scoped := distinctColumnLabels(cols)
	levels := headerLevels(labels, scoped, floor)
	for _, c := range cols {
		c.ladder = make([]string, len(levels))
		for i, lv := range levels {
			c.ladder[i] = lv[c.label]
		}
		c.hdr = c.ladder[0]
	}
}

func distinctColumnLabels(cols []*windowColumn) (labels []string, scoped map[string]bool) {
	seen := map[string]bool{}
	scoped = map[string]bool{}
	for _, c := range cols {
		if seen[c.label] {
			continue
		}
		seen[c.label] = true
		labels = append(labels, c.label)
		if c.label != windowLabel5h && c.label != windowLabel7d {
			scoped[c.label] = true
		}
	}
	return labels, scoped
}

func headerLevels(labels []string, scoped map[string]bool, floor int) []map[string]string {
	identity := map[string]string{}
	for _, l := range labels {
		identity[l] = l
	}
	levels := []map[string]string{identity}
	base := identity
	for _, step := range []func(map[string]string, []string, map[string]bool) map[string]string{
		dropSharedPrefix, dropDateToken,
	} {
		next := step(base, labels, scoped)
		if next == nil || !injectiveLevel(next, labels) || !clearsFloor(next, labels, floor) {
			continue
		}
		levels = append(levels, next)
		base = next
	}
	for k := widestLevel(base, labels) - 1; k >= floor; k-- {
		next := elideLevel(base, labels, scoped, k)
		if !injectiveLevel(next, labels) {
			break
		}
		levels = append(levels, next)
	}
	return levels
}

func dropSharedPrefix(base map[string]string, labels []string, scoped map[string]bool) map[string]string {
	var subject []string
	for _, l := range labels {
		if scoped[l] {
			subject = append(subject, base[l])
		}
	}
	if len(subject) < 2 {
		return nil
	}
	prefix := subject[0]
	for _, s := range subject[1:] {
		prefix = commonPrefix(prefix, s)
	}
	if i := strings.LastIndexAny(prefix, "-_"); i >= 0 {
		prefix = prefix[:i+1]
	} else {
		return nil
	}
	out := map[string]string{}
	for _, l := range labels {
		s := base[l]
		if scoped[l] && strings.HasPrefix(s, prefix) && len(s) > len(prefix) {
			s = s[len(prefix):]
		}
		out[l] = s
	}
	return out
}

func commonPrefix(a, b string) string {
	i := 0
	for _, r := range a {
		n := len(string(r))
		if i+n > len(b) || a[i:i+n] != b[i:i+n] {
			break
		}
		i += n
	}
	return a[:i]
}

func dropDateToken(base map[string]string, labels []string, scoped map[string]bool) map[string]string {
	out, cut := map[string]string{}, false
	for _, l := range labels {
		s := base[l]
		if scoped[l] {
			if trimmed, ok := trimDateToken(s); ok {
				s, cut = trimmed, true
			}
		}
		out[l] = s
	}
	if !cut {
		return nil
	}
	return out
}

func trimDateToken(s string) (string, bool) {
	i := strings.LastIndexAny(s, "-_")
	if i < 0 || len(s)-i-1 != 8 {
		return s, false
	}
	for _, r := range s[i+1:] {
		if r < '0' || r > '9' {
			return s, false
		}
	}
	return s[:i], true
}

func elideLevel(base map[string]string, labels []string, scoped map[string]bool, k int) map[string]string {
	out := map[string]string{}
	for _, l := range labels {
		s := base[l]
		if scoped[l] {
			s = middleElide(s, k)
		}
		out[l] = s
	}
	return out
}

func middleElide(s string, k int) string {
	if lipgloss.Width(s) <= k {
		return s
	}
	mark := lipgloss.Width(footerEllipse)
	if k <= mark {
		return footerEllipse
	}
	head := (k - mark + 1) / 2
	rs := []rune(s)
	front, used := 0, 0
	for front < len(rs) && used+lipgloss.Width(string(rs[front])) <= head {
		used += lipgloss.Width(string(rs[front]))
		front++
	}
	want := k - mark - used
	back, used := len(rs), 0
	for back > front && used+lipgloss.Width(string(rs[back-1])) <= want {
		used += lipgloss.Width(string(rs[back-1]))
		back--
	}
	return string(rs[:front]) + footerEllipse + string(rs[back:])
}

func injectiveLevel(level map[string]string, labels []string) bool {
	seen := map[string]string{}
	for _, l := range labels {
		s := level[l]
		if s == "" {
			return false
		}
		if other, clash := seen[s]; clash && other != l {
			return false
		}
		seen[s] = l
	}
	return true
}

func clearsFloor(level map[string]string, labels []string, floor int) bool {
	for _, l := range labels {
		want := floor
		if w := lipgloss.Width(l); w < want {
			want = w
		}
		if lipgloss.Width(level[l]) < want {
			return false
		}
	}
	return true
}

func widestLevel(level map[string]string, labels []string) int {
	w := 0
	for _, l := range labels {
		if n := lipgloss.Width(level[l]); n > w {
			w = n
		}
	}
	return w
}

func setHeaderLevel(cols []*windowColumn, level int) {
	for _, c := range cols {
		i := level
		if i >= len(c.ladder) {
			i = len(c.ladder) - 1
		}
		c.hdr = c.ladder[i]
	}
}

func shrinkTableHeaders(cols []*windowColumn, level int) (int, bool) {
	if len(cols) == 0 || level+1 >= len(cols[0].ladder) {
		return level, false
	}
	setHeaderLevel(cols, level+1)
	return level + 1, true
}

type tableSpans struct {
	any   bool
	floor int
}

func measureSpans(rows []tableRow) tableSpans {
	var sp tableSpans
	for _, r := range rows {
		if !r.span() {
			continue
		}
		sp.any = true
		if f := spanMin(r.Span); f > sp.floor {
			sp.floor = f
		}
	}
	return sp
}

// Backstop only: a standing test proves every real message floor is below it.
const spanHardCap = 24

func spanMin(msg string) int {
	if f := spanFloor(msg); f < spanHardCap {
		return f
	}
	return spanHardCap
}

// spanTokenFloor is the narrowest a SINGLE-TOKEN message may be drawn: enough of
// the token to tell one state from another, plus the marker saying it was cut.
//
// It is this package's number rather than the store's, and that is the whole
// point of it. A phrased message keeps its classification WORD whole, which is a
// width this package writes and can therefore bound; a message with no space at
// all is an unmapped sentinel state exactly as the store wrote it
// ("usage_probe_failed_no_such_store_state"), so "keep the first word whole"
// would let a store-supplied string set the width every account's identity cell
// pays for. Twelve columns is what the widest PHRASED message in this codebase
// already demands ("quarantined …"), so a diagnostic identifier never costs the
// shared cell more than a real sentence does.
const spanTokenFloor = 12

func spanFloor(msg string) int {
	full := lipgloss.Width(msg)
	word := lipgloss.Width(firstWord(msg))
	if word == full {
		if full > spanTokenFloor {
			return spanTokenFloor
		}
		return full
	}
	if w := word + lipgloss.Width(footerEllipse); w < full {
		return w
	}
	return full
}

func firstWord(msg string) string {
	if i := strings.IndexByte(msg, ' '); i >= 0 {
		return msg[:i]
	}
	return msg
}

// spanBudget is the columns a SPAN row's message has left once the slot cell, an
// identity cell of identW columns and the gutter behind it are laid out.
// Negative when the identity cell alone already fills the row.
//
// The identity width is the row's OWN when the message is drawn (tableLine) and
// the SHARED cell when the ladder is deciding what to narrow (spansFit,
// spanFloorWidth): a row's own label is never wider than the shared cell, so the
// ladder's measurement is a floor on every row's real budget, and any row whose
// label is narrower than the widest simply gets more than the ladder promised.
func spanBudget(width, slotW, identW int) int {
	return width - slotW - identW - lipgloss.Width(tableGutter)
}

// spanIdentW is the width a SPAN row draws its OWN identity cell in: the shared
// cell, less whatever this row's message still needs beyond what that cell
// leaves it, and never below the bare ellipsis — nor ever ABOVE the shared cell,
// so a row can only give identity back to its own message and never take a
// column from another account.
//
// This is what candidateLabelRow and miniAccountText each do on their own line
// ("clip to the ellipsis; the label outranks the email"), and it is what makes
// the table's message never shorter than the per-row layout's at the same width:
// an account's identity is worth less than the reason the engine cannot use it,
// and the slot number names the account either way.
func spanIdentW(msg string, width, slotW, labelW int) int {
	if labelW <= 0 {
		return 0
	}
	switch room := spanBudget(width, slotW, 0) - lipgloss.Width(msg); {
	case room >= labelW:
		return labelW
	case room < 1:
		return 1
	default:
		return room
	}
}

func (l tableLayout) spansFit(width int) bool {
	if !l.spans.any {
		return true
	}
	return spanBudget(width, l.slotW, l.labelW) >= l.spans.floor
}

func (l tableLayout) windowRowsWidth() int {
	w := l.slotW + l.labelW
	for _, c := range l.cols {
		if c.dropped {
			continue
		}
		w += lipgloss.Width(tableGutter) + c.width()
	}
	return w
}

func (l tableLayout) spanFloorWidth() int {
	if !l.spans.any {
		return 0
	}
	w := l.slotW + l.labelW
	if l.spans.floor > 0 {
		w += lipgloss.Width(tableGutter) + l.spans.floor
	}
	return w
}

// fullWidth is the narrowest width at which the WINDOW rows shed no COUNTED
// DATA: every column present, every countdown shown, and the shared identity
// cell still at its full desire. Only NAMING is already spent there — the
// headers at their coarsest admissible spelling — because naming is not an axis
// anything is priced on and rung (a) is walked to exhaustion before any figure
// or countdown gives ground.
//
// NO SPAN ROW'S MESSAGE LENGTH APPEARS IN IT, and that is the point. This width
// is the reference the release bar is taken at, so anything charged to it is
// charged to EVERY account: adding the widest whole message here made the LENGTH
// of one account's reason text — not even rendered at the widths in question —
// decide whether every other account got the table. The message axis is priced
// at the render width instead, where both layouts are equally bound by the
// terminal (releaseBar), so a message costs nothing but its own row.
//
// Measured on a FRESH layout, before the ladder walks, and on a PRICED one, so
// the countdown terms are the clock-free countdownWidest. At or above it the
// table states every figure and every countdown it has, so it states no less
// than any per-row layout of the same rows at any width whatever. That is what
// makes it the fixed reference the choice between the two layouts is priced
// against (pickWindowTable), and what makes that choice monotone in the width.
func (l tableLayout) fullWidth() int {
	if len(l.rows) == 0 {
		return 0
	}
	full := l.slotW + l.labelW
	for _, c := range l.cols {
		full += lipgloss.Width(tableGutter) + c.floorW()
		if c.showCd {
			full += lipgloss.Width(tableCdGap) + c.cdW
		}
	}
	return full
}

func (l tableLayout) tableWidth() int {
	win, span := l.windowRowsWidth(), l.spanFloorWidth()
	if span > win {
		return span
	}
	return win
}

func countdownRung(c *windowColumn, policy tablePolicy) int {
	switch {
	case policy.PinExhausted && c.exhausted:
		return 3
	case !c.counted:
		return 0
	case c.binding && policy.KeepBindingCountdown:
		return 2
	}
	return 1
}

func (l *tableLayout) shedCountdown(policy tablePolicy, last bool) bool {
	rungs := []int{0, 1, 2}
	if last {
		rungs = []int{3}
	}
	for _, rung := range rungs {
		for j := len(l.cols) - 1; j >= 0; j-- {
			c := l.cols[j]
			if c.dropped || !c.showCd || countdownRung(c, policy) != rung {
				continue
			}
			c.showCd = false
			return true
		}
	}
	return false
}

func (l *tableLayout) dropTableGroup() bool {
	victim, fewest, at := "", 0, -1
	for j := len(l.cols) - 1; j >= 0; j-- {
		c := l.cols[j]
		if c.dropped || c.pinned {
			continue
		}
		if n := l.groupReports(c.label); at < 0 || n < fewest {
			victim, fewest, at = c.label, n, j
		}
	}
	if at < 0 {
		return false
	}
	for _, c := range l.cols {
		if c.label == victim && !c.dropped {
			c.dropped = true
			l.elided++
		}
	}
	return true
}

func (l tableLayout) groupReports(label string) int {
	n := 0
	for i, r := range l.rows {
		if r.span() {
			continue
		}
		for j, c := range l.cols {
			if c.label == label && l.grid[i][j].present {
				n++
				break
			}
		}
	}
	return n
}

func (l tableLayout) rowsKeepAFigure() bool {
	for i, r := range l.rows {
		if r.span() {
			continue
		}
		kept, reports := false, false
		for j, c := range l.cols {
			if !l.grid[i][j].present {
				continue
			}
			reports = true
			if !c.dropped {
				kept = true
				break
			}
		}
		if reports && !kept {
			return false
		}
	}
	return true
}

// header renders the column header line: each column's name as the abbreviation
// ladder currently spells it, muted, and additionally dim when the column is
// uncounted, right-aligned over its own percentage sub-cell. An empty richText
// when there are no columns at all — a table of nothing but SPAN rows carries no
// header.
//
// The name is fitted to the sub-cell by the LADDER and never by a clip here: the
// sub-cell is at least as wide as the name it carries (bodyW), so the pad is
// never negative and a header always sits exactly over its own figures.
//
// A table that dropped columns says SO, once, at the end of the header
// (tableElision): a row's em dashes say "this account reports no such window",
// and without the marker there would be nothing anywhere to say that some window
// it does report is not on the grid at all.
func (l tableLayout) header() richText {
	var t richText
	t.addPlain(spaces(l.slotW + l.labelW))
	for _, c := range l.cols {
		if c.dropped {
			continue
		}
		t.addPlain(tableGutter + spaces(c.bodyW()-lipgloss.Width(c.hdr)))
		st := segStyle{Fg: colMuted}
		if !c.counted {
			st.Dim = true
		}
		t.add(c.hdr, st)
		if c.showCd {
			t.addPlain(spaces(lipgloss.Width(tableCdGap) + c.cdW))
		}
	}
	t = trimTrailingSpace(t)
	if mark := tableElision(l.elided); mark != "" {
		if lipgloss.Width(t.plain())+1+lipgloss.Width(mark) <= l.width {
			t.addPlain(" ")
			t.add(mark, segStyle{Fg: colMuted, Dim: true})
		}
	}
	return t
}

// tableElision is the marker the header carries when the width ladder dropped
// columns: "+3", never wider than two columns and never on the rows themselves.
// A count past what two columns can spell reads as "+9 or more", which is all a
// marker this size can honestly say and all a reader needs from it — the exact
// number is the one thing a wider terminal will tell them.
func tableElision(n int) string {
	switch {
	case n <= 0:
		return ""
	case n > 9:
		return "+9"
	}
	return fmt.Sprintf("+%d", n)
}

func tableLine(r tableRow, cells []tableCell, cols []*windowColumn, slotW, slotNumW, labelW, width int, opts tableOpts) (richText, layoutScore) {
	var t pricedText
	t.chrome(spaces(opts.indent)+padLeft(r.Slot, slotNumW)+tableSlotGap, opts.slotStyle)
	identW := labelW
	if r.span() {
		identW = spanIdentW(r.Span, width, slotW, labelW)
	}
	label := truncRich(r.Label, identW)
	t.identity(label, rtWidth(r.Label))
	if r.span() {
		t.chrome(tableGutter, segStyle{})
		t.span(clipText(r.Span, spanBudget(width, slotW, rtWidth(label))),
			segStyle{Fg: r.SpanFg}, lipgloss.Width(r.Span))
		return finishTableLine(t, width)
	}
	t.chrome(spaces(labelW-rtWidth(label)), segStyle{})
	for j, c := range cols {
		if c.dropped {
			continue
		}
		cell := cells[j]
		// A column this row does not report: the em dash, plain colMuted and
		// never dim. Dim is what an UNCOUNTED figure wears, and "this account
		// reports no such window" is a different statement from "this figure
		// does not count here" — a missing figure may never read as an ignored
		// one.
		t.chrome(tableGutter, segStyle{})
		if !cell.present {
			t.chrome(spaces(c.bodyW()-lipgloss.Width(tableMissing)), segStyle{})
			t.chrome(tableMissing, segStyle{Fg: colMuted})
		} else {
			t.chrome(spaces(c.bodyW()-lipgloss.Width(cell.pct)), segStyle{})
			t.figure(cell.pct, cellPctStyle(cell, r.Stale))
		}
		if c.showCd {
			cd := ""
			if cell.present {
				cd = cell.countdown
			}
			t.chrome(tableCdGap, segStyle{})
			t.countdown(cd, cellCountdownStyle(cell, r.Stale))
			t.chrome(spaces(c.cdW-lipgloss.Width(cd)), segStyle{})
		}
	}
	return finishTableLine(t, width)
}

// finishTableLine fits a row to the width it was laid out for and drops the
// padding its last cell would otherwise carry. The fit never cuts — every row is
// laid out inside the width by construction (tableLayout.sound) — and is applied
// so that a row which somehow did overrun is priced at what a terminal would
// really show rather than at what the layout meant.
func finishTableLine(t pricedText, width int) (richText, layoutScore) {
	rt, score := t.fit(width)
	return trimTrailingSpace(rt), score
}

// cellPctStyle is a cell's percentage emphasis (DESIGN A18). Color and weight
// carry different information and are never conflated:
//
//   - COLOR is the window's own SEVERITY, on every counted figure, binding or
//     not. Severity is what a percentage MEANS — a window at 99% is nearly
//     exhausted wherever it is rendered — and every other surface already says
//     it that way (accountCardText's bars, miniAccountText, tycswap list). A
//     counted figure in the plain foreground would read as unremarkable at the
//     very moment it matters most.
//   - BOLD, and bold alone, marks the row's BINDING window: the figure the
//     ranking orders by and the engine decides on. That is the cell's ROLE, not
//     its state, and the row needs both said at once. An uncounted window is
//     never the binding one, so bold stays where it is regardless.
//
// So a cell reads three ways, not two, and the third is the one the accounts
// monitor lives on. That surface enumerates its windows on the EMPTY model axis
// — every per-model window there is uncounted by construction — and a window at
// or over 100% is the single highest-value figure a row can carry: it says this
// account cannot serve that model at all, which is true on any axis and is
// exactly what miniAccountText has always said outright ("Fable (!)", in the
// critical color). An EXHAUSTED cell therefore carries its severity color
// whether or not it counts; only an uncounted window still short of its limit
// drives nothing and stays muted and dim. A stale measurement dims whichever
// level applies, exactly as the mini account line already dims its own figures.
func cellPctStyle(cell tableCell, stale bool) segStyle {
	switch {
	case cell.counted:
		return segStyle{Fg: severityColorF(cell.value), Bold: cell.binding, Dim: stale}
	case cell.exhausted:
		return segStyle{Fg: severityColorF(cell.value), Dim: stale}
	}
	return segStyle{Fg: colMuted, Dim: true}
}

// cellCountdownStyle is a countdown's emphasis: its own cell's level, except
// that it is NEVER bold — inside a binding cell (which is a counted one) the
// percentage is the emphasized figure and the countdown is muted supporting
// detail (09§5.5).
func cellCountdownStyle(cell tableCell, stale bool) segStyle {
	if cell.counted {
		return segStyle{Fg: colMuted, Dim: stale}
	}
	return segStyle{Fg: colMuted, Dim: true}
}

func rtWidth(t richText) int { return lipgloss.Width(t.plain()) }

// spaces is n blank columns (never negative).
func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

func padLeft(s string, width int) string {
	if pad := width - lipgloss.Width(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}

func trimTrailingSpace(t richText) richText {
	segs := append([]seg(nil), t.segs...)
	for len(segs) > 0 {
		last := &segs[len(segs)-1]
		trimmed := strings.TrimRight(last.Text, " ")
		if trimmed == last.Text {
			break
		}
		if trimmed == "" {
			segs = segs[:len(segs)-1]
			continue
		}
		last.Text = trimmed
		break
	}
	return richText{segs: segs}
}
