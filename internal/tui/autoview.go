package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/termsafe"
)

// event styling (09§4.4).
var eventStyles = map[string]string{
	"switch":              colAccent,
	"error":               colSevWarn,
	"account-quarantined": colSevWarn,
	"all-exhausted":       colSevCrit,
}

var quietKinds = map[string]bool{
	"poll": true, "no-switch": true, "sleep": true, "account-unquarantined": true,
}

// eventColor returns the log color for an event kind (09§4.4).
func eventColor(kind string) string {
	if c, ok := eventStyles[kind]; ok {
		return c
	}
	if quietKinds[kind] {
		return colMuted
	}
	return colForeground
}

// logLine is one event-log entry. A blank stamp is a bare muted system line.
type logLine struct {
	stamp string
	body  string
	color string
}

// autoScreen is the auto-switch view (09§4).
type autoScreen struct {
	settings            settings.AutoSwitchSettings
	configuredThreshold *float64
	entryThreshold      *float64
	adjusting           bool
	engine              AutoEngine
	gen                 int
	events              chan tea.Msg
	dryRun              bool
	log                 []logLine
	quarantined         map[string]string
	loaded              bool
}

func newAutoScreen() *autoScreen { return &autoScreen{} }

func (a *autoScreen) onMount(m *Model) tea.Cmd {
	a.loaded = true
	cmds := []tea.Cmd{m.setStoreOnly(true)}
	a.settings = settings.Load(m.facade.BackupDir())
	a.refreshQuarantine(m)
	ct := a.settings.SevenDayThreshold
	a.configuredThreshold = &ct
	tp := a.settings.SevenDayThreshold
	m.thresholdPct = &tp
	cmds = append(cmds, a.startEngine(m, true))
	return tea.Batch(cmds...)
}

func (a *autoScreen) refreshQuarantine(m *Model) {
	a.quarantined = autoswitch.ReadQuarantine(autoswitch.StatePath(m.facade.BackupDir()))
}

func (a *autoScreen) onExit(m *Model) tea.Cmd {
	if a.engine != nil {
		a.engine.Stop()
	}
	m.facade.ClearPollPolicyInputs()
	if a.configuredThreshold != nil {
		m.thresholdPct = a.configuredThreshold
	}
	return m.setStoreOnly(false)
}

// onSnapshot re-reads the quarantine set on the snapshot cadence (09§4.7); the panel is rebuilt every render, so its
// countdowns are never staler than the frame (DESIGN A18).
func (a *autoScreen) onSnapshot(m *Model) tea.Cmd {
	a.refreshQuarantine(m)
	return nil
}

func panelWidth(m *Model) int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

func (a *autoScreen) update(m *Model, msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "l":
		return a.toggleLive(m)
	case "t":
		return a.adjustThreshold(m)
	case "left":
		return a.thresholdStep(m, -1)
	case "right":
		return a.thresholdStep(m, 1)
	case "enter":
		if a.adjusting {
			a.endAdjust(m)
		}
		return nil
	case "esc", "q":
		return a.back(m)
	}
	return nil
}

// back exits threshold-adjust first, else pops the screen (09§4.2).
func (a *autoScreen) back(m *Model) tea.Cmd {
	if a.adjusting {
		a.endAdjust(m)
		return nil
	}
	return m.popScreen()
}

// -- engine hosting (09§4.3) -------------------------------------------------

func (a *autoScreen) startEngine(m *Model, dryRun bool) tea.Cmd {
	a.dryRun = dryRun
	mode := "DRY-RUN (watching only)"
	if !dryRun {
		mode = "LIVE (will switch accounts)"
	}
	a.appendSystem("— engine started: " + mode + " —")
	if m.newEngine == nil {
		a.appendSystem("— auto-switch engine unavailable in this build —")
		a.engine = nil
		return nil
	}
	a.gen++
	gen := a.gen
	ch := make(chan tea.Msg, 64)
	a.events = ch
	onEvent := func(ev autoswitch.Event) {
		select {
		case ch <- engineEventMsg{gen: gen, ev: ev}:
		default:
		}
	}
	eng := m.newEngine(a.settings, onEvent, dryRun)
	a.engine = eng
	go func() {
		code := eng.RunLoop()
		// Non-blocking, like onEvent: after restartEngine installs a fresh
		// channel nobody drains this one, and if its 64-slot buffer is already
		// full a blocking send would strand this goroutine forever. A stopped
		// message from a superseded engine is dropped by onEngineMsg's
		// generation guard anyway, so losing it here is safe.
		select {
		case ch <- engineStoppedMsg{gen: gen, code: code}:
		default:
		}
	}()
	return drainCmd(ch)
}

// drainCmd blocks on the engine channel and returns the next message; Update
// re-arms it (the standard bubbletea long-running-producer pattern).
func drainCmd(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (a *autoScreen) onEngineMsg(m *Model, msg tea.Msg) tea.Cmd {
	switch e := msg.(type) {
	case engineEventMsg:
		if e.gen != a.gen {
			return nil // stale engine generation; drop, do not re-arm
		}
		a.appendEvent(e.ev)
		cmds := []tea.Cmd{drainCmd(a.events)}
		if e.ev.Kind() == "switch" {
			cmds = append(cmds, m.requestRefresh(false))
		}
		return tea.Batch(cmds...)
	case engineStoppedMsg:
		if e.gen != a.gen {
			return nil
		}
		if e.err != nil {
			return m.notify("Auto-switch engine stopped: "+e.err.Error(), "", "error")
		}
		return nil
	}
	return nil
}

// toggleLive confirms going live, or drops to dry-run unguarded (09§4.3).
func (a *autoScreen) toggleLive(m *Model) tea.Cmd {
	if a.engine == nil {
		return nil
	}
	if a.dryRun {
		return m.pushScreen(&confirmModal{
			title:    "Go live",
			yesLabel: "Go live",
			focusYes: true,
			message: "Go live? tycswap will switch your active account automatically when the threshold is reached.\n\n" +
				"(Same behavior as running `tycswap auto` in a terminal.)",
			onDone: func(m *Model, confirmed bool) tea.Cmd {
				if !confirmed {
					return nil
				}
				return a.restartEngine(m, false)
			},
		})
	}
	return a.restartEngine(m, true)
}

func (a *autoScreen) restartEngine(m *Model, dryRun bool) tea.Cmd {
	if a.engine != nil {
		a.engine.Stop()
	}
	return a.startEngine(m, dryRun)
}

// -- threshold adjust (09§4.5), session-only, never persisted ----------------

func (a *autoScreen) adjustThreshold(m *Model) tea.Cmd {
	if a.adjusting {
		a.endAdjust(m)
		return nil
	}
	a.adjusting = true
	et := a.settings.SevenDayThreshold
	a.entryThreshold = &et
	return nil
}

func (a *autoScreen) thresholdStep(m *Model, delta float64) tea.Cmd {
	if !a.adjusting {
		return nil
	}
	lo, hi := thresholdBounds()
	value := a.settings.SevenDayThreshold + delta
	if value > hi {
		value = hi
	}
	if value < lo {
		value = lo
	}
	a.setThreshold(m, value)
	return nil
}

// Session only: the adjusted threshold is never persisted (09§4.5).
func (a *autoScreen) setThreshold(m *Model, value float64) {
	if value == a.settings.SevenDayThreshold {
		return
	}
	a.settings.SevenDayThreshold = value
	if a.engine != nil {
		a.engine.ApplyThreshold(value)
	}
	v := value
	m.thresholdPct = &v
	// The panel's tiering reads this threshold; the next render rebuilds it.
}

// endAdjust leaves adjust mode; a net change wakes the engine and logs a
// session-set line (09§4.5). No net change → nothing announced.
func (a *autoScreen) endAdjust(m *Model) {
	a.adjusting = false
	if a.entryThreshold != nil && a.settings.SevenDayThreshold == *a.entryThreshold {
		return
	}
	if a.engine != nil {
		a.engine.Wake()
	}
	a.appendSystem(fmt.Sprintf("— 7d threshold set to %s%% for this session —", pctLabel(a.settings.SevenDayThreshold)))
}

// -- candidates (09§4.7) -----------------------------------------------------

// candidateRank carries both ranking keys so the panel can order candidates by
// either strategy from the same pass. "best" compares bestKey (weekly pct, or
// the 997 quarantined / 998 sentinel / 999 usage-unknown sort keys) then account
// number. "soonest-reset" is threshold-tiered so an at/above-threshold account is
// never preferred for its renewal; it sorts after every below-threshold
// candidate, by headroom, as a last resort. It compares tier (0 below-threshold+
// known renewal, 1 below-threshold+unknown renewal, 2 at/over threshold but below
// limit, 3 at/over limit, 4 quarantined, 5 sentinel, 6 usage-unknown), then
// renewal/pct within the tier, then account number (Go-side extension, DESIGN A17).
//
// Panel contract (DESIGN A18): a row ranked as a viable target is always one the
// engine could pick this tick apart from freshness; every engine-unpickable row
// is labeled with why (quarantined / sentinel / usage-unknown), the one exception
// being disabled rows, which are dropped from the panel entirely.
type candidateRank struct {
	number  string
	bestKey float64
	tier    int      // "soonest-reset" tier 0..6
	pct     float64  // the same weekly figure (within-tier tiebreak; 0 when not applicable)
	renewal *float64 // weekly renewal epoch (tiers 0/3; nil = unknown)
}

// candidatesText ranks targets on the engine's own model axis: headroom for "best", tiered soonest weekly renewal for
// "soonest-reset" (DESIGN A17). Quarantined slots stay but are labeled into the non-viable tail, so a row shown as viable is one
// the engine could pick this tick (DESIGN A18). Rows use the shared window table, or all drop to the per-row layout when it
// cannot fit; rows never wrap; width <= 0 means 80; countdowns derive live from now (09§12); a nil snapshot renders nothing.
func (a *autoScreen) candidatesText(snap *reporting.AccountsSnapshot, width int, now float64) richText {
	if snap == nil {
		return richText{}
	}
	if width <= 0 {
		width = 80
	}
	models := settings.ParseModelNames(a.settings.Model)
	// The ± keys move the 7d bar only — the account's whole budget; the 5h and
	// per-model bars are settings (DESIGN A34). All three are needed here, because a
	// row is "at threshold" when ANY window has reached the bar that governs
	// it, and the panel must agree with the engine.
	bars := a.settings
	var ranked []candidateRank
	entries := map[string]candidateEntry{}
	for _, acc := range snap.Accounts {
		// Skip the active account and everything the engine's candidate set
		// excludes — non-switchable (no stored creds/config) and disabled slots,
		// both carried by the snapshot's single RotationEligible field (store
		// RotationEligible). A slot the engine can never pick would let the
		// displayed order disagree with every pick (Go-side deviation, DESIGN A18).
		// A Codex row is never a Claude engine candidate (claude-swap PR #252).
		if acc.Number == snap.ActiveNumber || !acc.RotationEligible || acc.ProviderName() != reporting.ProviderClaude {
			continue
		}
		pct := bindingPct(acc.Usage.LastGood, models)
		entry := candidateEntry{number: acc.Number, email: termsafe.Strip(acc.Email)}
		switch {
		case a.isQuarantined(acc.Number):
			// The engine quarantined this slot (invalid_grant / identity conflict)
			// and will never pick it until the credential is replaced, even though
			// its cached usage may look healthy. Keep the row but label it and rank
			// it into the non-viable tail (DESIGN A18). Quarantine takes precedence
			// over the sentinel and usage cells below.
			entry.label, entry.color = quarantineLabel(a.quarantined[acc.Number]), colSevWarn
			ranked = append(ranked, candidateRank{number: acc.Number, bestKey: 997.0, tier: 4})
		case acc.Usage.Sentinel != "":
			entry.label, entry.color = sentinelLabel(acc.Usage.Sentinel), colMuted
			ranked = append(ranked, candidateRank{number: acc.Number, bestKey: 998.0, tier: 5})
		case pct == nil:
			entry.label, entry.color = "usage unknown", colMuted
			ranked = append(ranked, candidateRank{number: acc.Number, bestKey: 999.0, tier: 6})
		default:
			entry.windows = candidateWindows(acc.Usage.LastGood, models)
			// Rank on the WEEKLY axis like the engine: the binding figure would favour an account resting its 5h window (DESIGN A34).
			pcts := classPcts(acc.Usage.LastGood, models)
			key := *pct
			if w := pcts.weekly(); w != nil {
				key = *w
			}
			r := candidateRank{number: acc.Number, bestKey: key, pct: key,
				renewal: renewalTS(acc.Usage.LastGood, models)}
			switch {
			case *pct >= 100.0:
				r.tier = 3 // at/over a limit — unusable until that window resets
			case pcts.overAnyBar(bars):
				r.tier = 2 // at/over one of its bars but below limit (headroom desc last resort)
			case r.renewal != nil:
				r.tier = 0 // below every bar + known renewal
			default:
				r.tier = 1 // below every bar, unknown renewal
			}
			ranked = append(ranked, r)
		}
		entries[acc.Number] = entry
	}

	var head richText
	head.addFg("Next best", colMuted)
	head.addFg(" · "+countingNote(models), colMuted)
	out := truncRich(head, width)
	if len(ranked) == 0 {
		var empty richText
		empty.addFg("  no other switchable accounts", colMuted)
		out.addText(candidateRowText(empty, width))
		return out
	}
	less := candidateLessBest
	if a.settings.Strategy == "soonest-reset" {
		less = candidateLessSoonest
	}
	sort.SliceStable(ranked, func(i, j int) bool { return less(ranked[i], ranked[j]) })
	ordered := make([]candidateEntry, 0, len(ranked))
	for _, r := range ranked {
		ordered = append(ordered, entries[r.number])
	}

	rows := make([]richText, 0, len(ordered))
	for _, e := range ordered {
		rows = append(rows, e.rowText(width, now))
	}
	// PRICED at the widest spelling every countdown's grammar allows, never at
	// this frame's: the bar the table clears must be the same bar on the next
	// frame, or the panel rearranges itself while the user watches. The lines
	// above are the ones DRAWN, spelled live, and they state at least this much.
	perRow := func(at int) layoutScore {
		var s layoutScore
		for _, e := range ordered {
			_, score := e.rowPriced(at, widestClock())
			s = s.plus(score)
		}
		return s
	}
	if table, ok := pickWindowTable(candidateTableRows(ordered), width, now,
		candidateTableOpts, perRow); ok {
		if len(table.Header.segs) > 0 {
			out.addText(candidateRowText(table.Header, width))
		}
		for _, line := range table.Lines {
			out.addText(candidateRowText(line, width))
		}
		return out
	}
	for _, line := range rows {
		out.addText(line)
	}
	return out
}

type candidateEntry struct {
	number  string
	email   string
	windows []candidateWindow
	label   string // "" → a readable usage row; else why the engine cannot pick it
	color   string
}

var candidateTableOpts = tableOpts{
	indent: 2, slotStyle: segStyle{Fg: colForeground}, headerFloor: 4,
	policy: tablePolicy{PinExhausted: false, KeepBindingCountdown: true},
}

// candidateTableRows projects the ranked entries onto shared-table rows, in
// ranked order. A labeled entry becomes a SPAN row carrying its label in the
// label's own color; a readable one becomes a WINDOW row. The two shapes are
// built through the row constructors, so an entry carrying both a label and
// windows can never render as some blend of them.
func candidateTableRows(entries []candidateEntry) []tableRow {
	rows := make([]tableRow, 0, len(entries))
	for _, e := range entries {
		var label richText
		label.addFg(e.email, colForeground)
		if e.label != "" {
			rows = append(rows, newSpanRow(e.number, label, e.label, e.color, false))
			continue
		}
		rows = append(rows, newWindowRow(e.number, label, e.windows, false))
	}
	return rows
}

// rowText renders the entry through the panel's per-row layout, the one that
// survives any width (DESIGN A18).
func (e candidateEntry) rowText(width int, now float64) richText {
	line, _ := e.rowPriced(width, liveClock(now))
	return line
}

func (e candidateEntry) rowPriced(width int, clk renderClock) (richText, layoutScore) {
	if e.label != "" {
		return candidateLabelRowPriced(e.number, e.email, e.label, e.color, width)
	}
	return candidateRowPriced(e.number, e.email, e.windows, width, clk)
}

func countingNote(models []string) string {
	parts := []string{"5h", "7d"}
	for _, name := range models {
		if strings.EqualFold(name, allModelsSentinel) {
			parts = append(parts[:2], "all models")
			break
		}
		parts = append(parts, name)
	}
	return "counting " + strings.Join(parts, ", ")
}

const (
	candidateGap = "  "  // email → first window cell, or → the label
	candidateSep = " · " // between window cells
)

// candidateNumber is a row's visible left margin + slot number cell. Fixed
// width and free of the row break, so the row's width math can measure it.
func candidateNumber(number string) string { return fmt.Sprintf("  %2s  ", number) }

// No STYLED segment may carry a newline: lipgloss pads styled multi-line segments, pushing the previous row past the width.
func candidateRowText(body richText, width int) richText {
	var t richText
	t.addPlain("\n")
	return *t.addText(truncRich(body, width))
}

func pricedRowText(body pricedText, width int) (richText, layoutScore) {
	fitted, score := body.fit(width)
	var t richText
	t.addPlain("\n")
	return *t.addText(fitted), score
}

// candidateLabelRow renders a candidate the panel cannot rank by usage — a
// quarantined, sentinel or usage-unknown slot: slot number, email, then the
// label saying why the engine will not pick it.
//
// The row NEVER wraps, on the same precedence candidateRow uses: the slot number
// always survives, the email clips first (down to the bare ellipsis), and the
// label — the reason the row is on the panel at all — truncates with an ellipsis
// only as the last resort. The label text itself is never reworded to fit.
func candidateLabelRow(number, email, label, color string, width int) richText {
	line, _ := candidateLabelRowPriced(number, email, label, color, width)
	return line
}

func candidateLabelRowPriced(number, email, label, color string, width int) (richText, layoutScore) {
	head := candidateNumber(number)
	fixed := lipgloss.Width(head) + lipgloss.Width(candidateGap)
	shownEmail, shownLabel := email, label
	if over := fixed + lipgloss.Width(email) + lipgloss.Width(label) - width; over > 0 {
		budget := lipgloss.Width(email) - over
		if budget < 1 {
			budget = 1 // clip to the ellipsis; the label outranks the email
		}
		shownEmail = clipText(email, budget)
	}
	if over := fixed + lipgloss.Width(shownEmail) + lipgloss.Width(label) - width; over > 0 {
		shownLabel = clipText(label, lipgloss.Width(label)-over)
	}
	var body pricedText
	body.chrome(head, segStyle{Fg: colForeground})
	body.identityRun(shownEmail, segStyle{Fg: colForeground}, lipgloss.Width(email))
	body.chrome(candidateGap, segStyle{})
	body.span(shownLabel, segStyle{Fg: color}, lipgloss.Width(label))
	return pricedRowText(body, width)
}

// candidateRow renders a readable candidate's row: slot number, email, then one
// cell per window the account reports, in oauth.RelevantWindows order (5h, 7d,
// then the account's scoped windows). Each cell answers both questions a switch
// target raises — "how used is it" and "when does it free up" — as
// "{label} {pct}% ({countdown})", the countdown derived live from that window's
// resets_at against now (DESIGN A18; a window with no parseable resets_at simply
// shows no parenthetical). The three emphasis levels carry the panel's contract
// (Go-side extension, DESIGN A18), the same one the shared table renders
// (cellPctStyle) so a window reads alike above and below the flip:
//
//   - BINDING — the counted window with the highest pct: the number the row is
//     ranked by and the one the engine decides on. Severity-colored and BOLD;
//     the bold is what says "this is the one being acted on".
//   - COUNTED but not binding — relevant on the configured autoswitch.model axis,
//     so it could bind once it climbs. Severity-colored too, behind its muted
//     label: the color states what the figure MEANS, exactly as the account
//     card's bars and the mini account line state it, so a counted window at 99%
//     never reads as unremarkable for want of binding.
//   - UNCOUNTED — a scoped window autoswitch.model does not match. It affects
//     neither the ranking nor the engine's pick, so it is muted and dim: visible
//     (the user must be able to watch a per-model window fill before configuring
//     it) but plainly informational.
//
// A cell's countdown inherits its cell's level, except that it is never bold: in
// a binding cell the pct is the emphasized figure and the countdown is muted
// supporting detail, exactly as the mini account row renders its own "(resets …)"
// suffix (09§5.5).
//
// The row NEVER wraps; see shedCandidateCell for the order it sheds in. Once
// nothing is left to shed the email clips, and a width too small even for the
// binding cell clips the whole line rather than letting it fold.
func candidateRow(number, email string, windows []candidateWindow, width int, now float64) richText {
	line, _ := candidateRowPriced(number, email, windows, width, liveClock(now))
	return line
}

func candidateRowPriced(number, email string, windows []candidateWindow, width int, clk renderClock) (richText, layoutScore) {
	head := candidateNumber(number)
	cells := candidateCells(windows, clk)
	rowWidth := func(email string) int {
		w := lipgloss.Width(head) + lipgloss.Width(email)
		shown := 0
		for _, cell := range cells {
			if !cell.shown {
				continue
			}
			lead := candidateSep
			if shown == 0 {
				lead = candidateGap // the first cell follows the email gap
			}
			w += lipgloss.Width(lead) + lipgloss.Width(cell.text())
			shown++
		}
		return w
	}
	for rowWidth(email) > width && shedCandidateCell(cells) {
		// Shed detail until the row fits or only the binding pct is left.
	}
	shown := email
	if over := rowWidth(shown) - width; over > 0 {
		budget := lipgloss.Width(email) - over
		if budget < 1 {
			budget = 1 // clip to the ellipsis; the binding cell outranks the email
		}
		shown = clipText(email, budget)
	}

	var body pricedText
	body.chrome(head, segStyle{Fg: colForeground})
	body.identityRun(shown, segStyle{Fg: colForeground}, lipgloss.Width(email))
	first := true
	for _, cell := range cells {
		if !cell.shown {
			continue
		}
		if first {
			body.chrome(candidateGap, segStyle{})
			first = false
		} else {
			body.chrome(candidateSep, segStyle{Fg: colTrack})
		}
		addCandidateCell(&body, cell)
	}
	return pricedRowText(body, width)
}

type candidateCell struct {
	win       candidateWindow
	countdown string // "" when the window carries no parseable resets_at
	shown     bool
	showReset bool
}

// candidateCells resolves each window's countdown once per row (the reset math
// is recomputed from resets_at at render time, 09§12) and starts every cell
// fully shown — the width ladder takes detail away from there. clk is what the
// countdown is spelled against: live when the row is DRAWN, and the widest
// spelling its grammar allows when the row is only being PRICED as the bar the
// shared table must clear (renderClock).
func candidateCells(windows []candidateWindow, clk renderClock) []candidateCell {
	cells := make([]candidateCell, 0, len(windows))
	for _, w := range windows {
		cd := clk.resetText(w.ResetsAt)
		cells = append(cells, candidateCell{win: w, countdown: cd, shown: true, showReset: cd != ""})
	}
	return cells
}

// head is the cell's utilization figure: "7d 88%" (09§5.5's grammar).
func (c candidateCell) head() string {
	return c.win.Label + " " + pctText(c.win.Pct)
}

// resetSuffix is the cell's countdown parenthetical (" (resets 2h 13m)"), or ""
// when the window has no known reset or the width ladder has dropped it.
func (c candidateCell) resetSuffix() string {
	if !c.showReset {
		return ""
	}
	return " (" + c.countdown + ")"
}

// text is the cell's full width-measurable text.
func (c candidateCell) text() string { return c.head() + c.resetSuffix() }

// addCandidateCell appends one window cell at its emphasis level (see
// candidateRow): every COUNTED figure carries its own severity color, the
// BINDING one adding bold, and an uncounted cell is muted and dim. The
// countdown follows as its own segment so it can stay muted (and never bold)
// beside an emphasized binding pct; it is dim only where its whole cell is. A
// cell with no countdown appends nothing extra — richText.add drops empty text
// — so a row whose windows carry no resets_at renders exactly as it did before
// countdowns existed.
func addCandidateCell(t *pricedText, cell candidateCell) {
	switch {
	case cell.win.Binding:
		t.figure(cell.head(), segStyle{Fg: severityColorF(cell.win.Pct), Bold: true})
		t.countdown(cell.resetSuffix(), segStyle{Fg: colMuted})
	case cell.win.Counted:
		t.chrome(cell.win.Label+" ", segStyle{Fg: colMuted})
		t.figure(pctText(cell.win.Pct), segStyle{Fg: severityColorF(cell.win.Pct)})
		t.countdown(cell.resetSuffix(), segStyle{Fg: colMuted})
	default:
		t.figure(cell.head(), segStyle{Fg: colMuted, Dim: true})
		t.countdown(cell.resetSuffix(), segStyle{Fg: colMuted, Dim: true})
	}
}

var candidateShedSteps = []struct {
	binding   bool // the step touches the binding cell (else a non-binding one)
	counted   bool // ... of this class, when not the binding cell
	countdown bool // drop just the countdown (else the whole cell)
}{
	{counted: false, countdown: true},
	{counted: false},
	{counted: true, countdown: true},
	{counted: true},
	{binding: true, countdown: true},
}

// shedCandidateCell performs the single next reduction of the width ladder —
// class-major (candidateShedSteps), rightmost-first within a class — and reports
// whether anything was left to shed. Callers re-measure after each step, so a row
// only ever loses as much as it must.
func shedCandidateCell(cells []candidateCell) bool {
	for _, step := range candidateShedSteps {
		for i := len(cells) - 1; i >= 0; i-- {
			c := &cells[i]
			if !c.shown || c.win.Binding != step.binding {
				continue
			}
			if !step.binding && c.win.Counted != step.counted {
				continue
			}
			if !step.countdown {
				c.shown = false
				return true
			}
			if c.showReset {
				c.showReset = false
				return true
			}
		}
	}
	return false
}

// clipText cuts s to at most width display columns, ending in an ellipsis when it
// had to cut (footer.go's marker, one cell wide).
func clipText(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	limit := width - lipgloss.Width(footerEllipse)
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > limit {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + footerEllipse
}

func truncRich(t richText, width int) richText {
	if width <= 0 {
		return richText{}
	}
	if lipgloss.Width(t.plain()) <= width {
		return t
	}
	limit := width - lipgloss.Width(footerEllipse)
	var out richText
	used := 0
	for _, s := range t.segs {
		if w := lipgloss.Width(s.Text); used+w <= limit {
			out.add(s.Text, s.Style)
			used += w
			continue
		}
		var b strings.Builder
		for _, r := range s.Text {
			rw := lipgloss.Width(string(r))
			if used+rw > limit {
				break
			}
			b.WriteRune(r)
			used += rw
		}
		out.add(b.String(), s.Style)
		break
	}
	out.addFg(footerEllipse, colMuted)
	return out
}

// clipRichLines is truncRich over a MULTI-LINE richText: every line is fitted to
// width on its own, and each row break is re-emitted as its own UNSTYLED
// segment. It is the last-resort width guard for the surfaces that build their
// own line breaks — the account card, and the monitor's stack of blocks — where
// truncRich alone would measure the whole block as one line and cut everything
// after the first break away.
//
// The break must stay outside every styled segment: lipgloss left-aligns a
// styled multi-line segment by padding every line out to the widest, so a break
// inside one renders its blank first line as trailing spaces on the row above,
// pushing that row past the terminal (DESIGN A18).
func clipRichLines(t richText, width int) richText {
	var out, line richText
	flush := func() {
		out.addText(truncRich(line, width))
		line = richText{}
	}
	for _, s := range t.segs {
		for i, part := range strings.Split(s.Text, "\n") {
			if i > 0 {
				flush()
				out.addPlain("\n")
			}
			line.add(part, s.Style)
		}
	}
	flush()
	return out
}

func (a *autoScreen) isQuarantined(number string) bool {
	_, ok := a.quarantined[number]
	return ok
}

func quarantineLabel(reason string) string {
	if reason == "" {
		return "quarantined"
	}
	return "quarantined (" + reason + ")"
}

func candidateLessBest(a, b candidateRank) bool {
	if a.bestKey != b.bestKey {
		return a.bestKey < b.bestKey
	}
	return a.number < b.number
}

// candidateLessSoonest is the threshold-tiered "soonest-reset" panel order, so
// an at/above-threshold account is never preferred for its renewal; it sorts
// after every below-threshold candidate, by headroom, as a last resort. Order by
// tier (0 below-threshold+known renewal, 1 below-threshold+unknown renewal, 2
// at/over threshold below limit, 3 at/over limit, 4 quarantined, 5 sentinel, 6
// usage-unknown), then within the tier by earliest weekly renewal (tier 0, and
// tier 3 with unknown renewal last) or lowest pct (tiers 1/2, and as the tier-3
// tiebreak), then account number. Tiers 4-6 carry no renewal/pct, so they fall
// straight through to the account-number tiebreak (Go-side extension, DESIGN A17;
// quarantine labeling DESIGN A18).
func candidateLessSoonest(a, b candidateRank) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	switch a.tier {
	case 0: // both below threshold with a known renewal
		if *a.renewal != *b.renewal {
			return *a.renewal < *b.renewal
		}
	case 1, 2: // both below threshold w/ unknown renewal, or both at/over threshold
		if a.pct != b.pct {
			return a.pct < b.pct
		}
	case 3: // at/over limit: known renewal first, then renewal asc, then pct asc
		if (a.renewal != nil) != (b.renewal != nil) {
			return a.renewal != nil
		}
		if a.renewal != nil && b.renewal != nil && *a.renewal != *b.renewal {
			return *a.renewal < *b.renewal
		}
		if a.pct != b.pct {
			return a.pct < b.pct
		}
	}
	return a.number < b.number
}

func (a *autoScreen) footerBindings(m *Model) []footerBinding {
	bindings := []footerBinding{
		{"l", "Go live / dry-run"},
		{"t", "Threshold"},
	}
	if a.adjusting {
		bindings = append(bindings,
			footerBinding{"←", "-1%"},
			footerBinding{"→", "+1%"},
			footerBinding{"enter", "Done"},
		)
	}
	return append(bindings, footerBinding{"esc", "Back"})
}

// -- rendering ---------------------------------------------------------------

func (a *autoScreen) view(m *Model) string {
	inner := panelWidth(m)
	var chrome richText
	now := m.nowSeconds()
	chrome.addText(accountsPanelText(m.snapshot, inner, false, m.thresholdPct, now))
	chrome.addPlain("\n\n")
	var status richText
	if a.dryRun || a.engine == nil {
		status.add(" DRY-RUN ", segStyle{Fg: colSevWarn, Bold: true})
	} else {
		status.add(" LIVE ", segStyle{Fg: colBackground, Bold: true})
	}
	status.addPlain("  ")
	indent := lipgloss.Width(status.plain())
	status.addText(a.summaryText())
	chrome.addText(truncRich(status, inner))
	chrome.addPlain("\n")
	if a.adjusting {
		// The adjusting hint has a line of its own under the summary: on the
		// summary line it ran past column 80 and was cut off (DESIGN A28).
		var hint richText
		hint.addPlain(strings.Repeat(" ", indent))
		hint.addFg(adjustHint, colMuted)
		chrome.addText(truncRich(hint, inner))
		chrome.addPlain("\n")
	}
	// The ranked panel is built fresh on every render, at the CURRENT width and
	// the CURRENT clock, rather than cached from the last poll (DESIGN A18): a
	// resize changes which window cells survive on a row, and every cell's reset
	// countdown is live, so a cached panel would both wrap after a narrowing and
	// show countdowns as old as the poll cadence. Building it costs one pass over
	// the snapshot the same render already walks for the account card.
	chrome.addText(a.candidatesText(m.snapshot, inner, now))
	chromeLines := strings.Split(chrome.render(), "\n")

	// Event log (flex): the full history is kept in a.log; only the newest lines
	// that fit are rendered, tail-following like Textual's auto-scrolled RichLog.
	logLines := make([]string, 0, len(a.log))
	for _, ln := range a.log {
		var lt richText
		if ln.stamp != "" {
			lt.addFg(ln.stamp+"  ", colMuted)
		}
		lt.addFg(ln.body, ln.color)
		logLines = append(logLines, lt.render())
	}

	avail := m.contentHeight()
	if avail < 0 {
		// Terminal size unknown → render everything (pre-size fallback).
		out := append([]string{}, chromeLines...)
		out = append(out, "")
		return strings.Join(append(out, logLines...), "\n")
	}
	if avail == 0 {
		return ""
	}
	// Reserve one blank line between the chrome and the log.
	logBudget := avail - len(chromeLines) - 1
	if logBudget < 0 {
		// Tiny terminal: even the chrome does not fully fit. Keep its top (the
		// status block's first line) and drop the log entirely — status truncates
		// last, the log never gets a negative budget.
		if len(chromeLines) > avail {
			chromeLines = chromeLines[:avail]
		}
		return strings.Join(chromeLines, "\n")
	}
	tail := logLines
	if len(tail) > logBudget {
		tail = tail[len(tail)-logBudget:]
	}
	out := append([]string{}, chromeLines...)
	out = append(out, "")
	return strings.Join(append(out, tail...), "\n")
}

func (a *autoScreen) summaryText() richText {
	var t richText
	t.addPlain("auto-switch · switch at ")
	t.addPlain(fmt.Sprintf("5h %s%% · ", pctLabel(a.settings.FiveHourThreshold)))
	thStyle := segStyle{}
	if a.adjusting {
		thStyle = segStyle{Fg: colAccent}
	}
	t.add(fmt.Sprintf("7d %s%%", pctLabel(a.settings.SevenDayThreshold)), thStyle)
	if a.configuredThreshold != nil && a.settings.SevenDayThreshold != *a.configuredThreshold {
		t.addFg(" (session)", colMuted)
	}
	if len(settings.ParseModelNames(a.settings.Model)) > 0 {
		t.addPlain(fmt.Sprintf(" · model %s%%", pctLabel(a.settings.ModelThreshold)))
	}
	t.addPlain(fmt.Sprintf(" · poll every %.0fs", a.settings.IntervalSeconds))
	if a.settings.Strategy != "best" {
		t.addPlain(" · soonest-reset")
	}
	return t
}

const adjustHint = "← → adjust · enter done · session only — Settings persists"

func (a *autoScreen) appendEvent(ev autoswitch.Event) {
	a.log = append(a.log, logLine{stamp: clockStamp(nowLocal()), body: ev.Human(), color: eventColor(ev.Kind())})
}

func (a *autoScreen) appendSystem(text string) {
	a.log = append(a.log, logLine{stamp: "", body: text, color: colMuted})
}

// -- settings/threshold helpers ----------------------------------------------

// loadThreshold reads the configured threshold, or nil on any failure (09§2.1;
// settings.Load is total, so this normally returns the file/default value).
func loadThreshold(backupDir string) *float64 {
	t := settings.Load(backupDir).SevenDayThreshold
	return &t
}

func thresholdBounds() (lo, hi float64) {
	for _, spec := range settings.SettingSpecs {
		if spec.Section == "autoswitch" && spec.JSONKey == "sevenDayThreshold" {
			return spec.Lo, spec.Hi
		}
	}
	return 50.0, 100.0
}
