// data.go — display helpers for the TUI (spec 09§6.3/§6.4).
//
// Implements format_duration, format_age, reset_text, reset_clock, clock_stamp,
// sentinel_label, window_pct, binding_pct, and last_seen_note. Reset math is
// recomputed live from resets_at at render time — the API's cached
// countdown/clock strings drift as a measurement ages (09§12). Absolute clock
// strings reuse oauth.FormatReset (its clock component is oauth's
// reset_clock_string, local time, no zero-pad day).
package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

// apiKeySentinel is the one sentinel kind that never shows a "last seen" line
// (API-key accounts have no quota to have seen) (09§5.4).
const apiKeySentinel = jsonout.UsageAPIKey

const serveTTLS = usage.ServeTTLS

const staleOKS = usage.StaleOKS

const allModelsSentinel = "all"

var sentinelNotes = map[string]string{
	jsonout.UsageTokenExpired:        "token expired — Claude Code refreshes the active account",
	jsonout.UsageAPIKey:              "API key (no quota)",
	jsonout.UsageKeychainUnavailable: "keychain unavailable — locked or in use; try again",
	jsonout.UsageReloginRequired:     "re-login needed — refresh token dead; log in with Claude Code, then run: tycswap add",
}

func sentinelLabel(sentinel string) string {
	if note, ok := sentinelNotes[sentinel]; ok {
		return note
	}
	return sentinel
}

func windowPct(lastGood map[string]any, key string) *float64 {
	if lastGood == nil {
		return nil
	}
	w, ok := lastGood[key].(map[string]any)
	if !ok {
		return nil
	}
	return numericPct(w["pct"])
}

func numericPct(v any) *float64 {
	switch n := v.(type) {
	case float64:
		return &n
	case float32:
		f := float64(n)
		return &f
	case int:
		f := float64(n)
		return &f
	case int64:
		f := float64(n)
		return &f
	}
	return nil
}

// bindingPct returns the utilization of the binding (worst) relevant window, or
// nil (poll_policy.binding_pct). Uses the same oauth headroom projection the
// engine decides with, so a displayed ranking never disagrees with the pick.
func bindingPct(lastGood map[string]any, models []string) *float64 {
	h := oauth.AccountHeadroom(oauth.NewUsage(lastGood), models)
	if h == nil {
		return nil
	}
	pct := 100.0 - *h
	return &pct
}

func classPcts(lastGood map[string]any, models []string) classUtilization {
	five, seven, model := oauth.ClassPcts(oauth.NewUsage(lastGood), models)
	return classUtilization{fiveHour: five, sevenDay: seven, model: model}
}

type classUtilization struct {
	fiveHour *float64
	sevenDay *float64
	model    *float64
}

func (c classUtilization) weekly() *float64 {
	switch {
	case c.sevenDay == nil:
		return c.model
	case c.model == nil:
		return c.sevenDay
	case *c.model > *c.sevenDay:
		return c.model
	default:
		return c.sevenDay
	}
}

func (c classUtilization) overAnyBar(s settings.AutoSwitchSettings) bool {
	for _, pair := range []struct {
		pct *float64
		bar float64
	}{
		{c.fiveHour, s.FiveHourThreshold},
		{c.sevenDay, s.SevenDayThreshold},
		{c.model, s.ModelThreshold},
	} {
		if pair.pct != nil && *pair.pct >= pair.bar {
			return true
		}
	}
	return false
}

const exhaustedPct = 100.0

const displayPctCap = 999.0

func pctText(pct float64) string {
	switch {
	case pct > displayPctCap:
		return fmt.Sprintf(">%.0f%%", displayPctCap)
	case pct < -displayPctCap:
		return fmt.Sprintf("<-%.0f%%", displayPctCap)
	}
	return fmt.Sprintf("%.0f%%", pct)
}

type candidateWindow struct {
	Label     string
	Pct       float64
	ResetsAt  string
	Counted   bool
	Binding   bool
	Exhausted bool
}

func candidateWindows(lastGood map[string]any, models []string) []candidateWindow {
	u := oauth.NewUsage(lastGood)
	all := oauth.RelevantWindows(u, []string{allModelsSentinel})
	counted := oauth.RelevantWindows(u, models)
	out := make([]candidateWindow, 0, len(all))
	cursor, binding := 0, -1
	for _, w := range all {
		cell := candidateWindow{Label: w.Label, Pct: w.Pct, ResetsAt: w.ResetsAt,
			Exhausted: w.Pct >= exhaustedPct}
		if cursor < len(counted) && counted[cursor] == w {
			cell.Counted = true
			cursor++
			if binding < 0 || cell.Pct > out[binding].Pct {
				binding = len(out)
			}
		}
		out = append(out, cell)
	}
	if binding >= 0 {
		out[binding].Binding = true
	}
	return out
}

// renewalTS returns the account's weekly-scope renewal epoch (the latest
// parseable weekly reset among the 7d + matched scoped windows), or nil when
// unknown, on the same oauth projection/model axis bindingPct uses so the
// soonest-reset ranking never disagrees with the engine's pick. Go-side
// extension (DESIGN A17).
func renewalTS(lastGood map[string]any, models []string) *float64 {
	return oauth.RenewalTS(oauth.NewUsage(lastGood), models)
}

// resetText renders the live countdown to a window's reset ("resets 2h 13m"),
// "resets now" when elapsed, or "" when unknown (09§6.3 reset_text). now is
// fractional Unix seconds.
//
// Spells the remaining time in FULL, uncapped: this is the account card's
// vocabulary, and the card states the pay-as-you-go spend window, whose reset is
// monthly and so legitimately further out than any 5h/7d window. The cap that
// makes countdownWidest a bound belongs to the shared-column layouts alone, where
// one window's spelling sizes a column every account pays for; a card's bar
// suffix is charged to its own block and bounds nothing else.
func resetText(window map[string]any, now float64) string {
	if window == nil {
		return ""
	}
	ts, ok := parseResetsAt(window)
	if !ok {
		return ""
	}
	if ts-now <= 0 {
		return "resets now"
	}
	return "resets " + formatDuration(ts-now)
}

const displayResetCap = 10 * 24 * 3600

const displayResetOver = ">9d"

func countdownSpelling(remaining float64) string {
	if remaining <= 0 {
		return "now"
	}
	if !(remaining < displayResetCap) {
		return displayResetOver
	}
	return formatDuration(remaining)
}

// candidateCountdown renders the live countdown a candidates-panel window cell
// shows beside its utilization: "resets 2h 13m", "resets now" once the reset has
// elapsed, or "" when the window carries no parseable resets_at (the cell then
// shows no countdown at all). now is fractional Unix seconds.
//
// Shares the account card's wording ("resets …"/"resets now") so the surfaces
// never grow a second reset vocabulary, and spells the remaining time through
// countdownSpelling, which caps it: this cell sizes a shared column, so its width
// must be bounded by countdownWidest for the layout pricing to hold (09§6.3,
// DESIGN A18). Takes the raw resets_at string because a candidateWindow carries
// the timestamp, not the window map.
func candidateCountdown(resetsAt string, now float64) string {
	ts, ok := parseResetsAt(map[string]any{"resets_at": resetsAt})
	if !ok {
		return ""
	}
	return "resets " + countdownSpelling(ts-now)
}

// resetKnown reports whether a window carries a parseable resets_at, i.e.
// whether it has a countdown to show AT ALL. That is a property of the stored
// string and of nothing else: the clock decides how a countdown is SPELLED, never
// whether one exists.
func resetKnown(resetsAt string) bool {
	_, ok := parseResetsAt(map[string]any{"resets_at": resetsAt})
	return ok
}

const countdownWidest = "23h 59m"

// renderClock is what a layout spells its reset countdowns against, and the one
// place the difference between DRAWING and PRICING a layout lives.
//
// A countdown's spelling narrows as it ticks — "2h 13m" is four columns wider
// than "9m" — so a layout measured against the live clock is a different width
// on every frame. That is harmless while it only decides how much a layout
// SHOWS, and it is not harmless when it decides WHICH layout a surface draws:
// the panel would flip between the table and the per-row layout between frames at
// a fixed terminal width, losing figures with no resize.
//
// So the two readings are separated. A PRICED layout spells every countdown at
// countdownWidest, whatever the hour, which makes a score — and therefore the
// choice between two layouts — a pure function of the rows, the width and the
// surface. A DRAWN layout spells them live, so the terminal shows the real
// figure and the columns a short countdown frees are spent on real detail. The
// drawn layout is never narrower per countdown than the priced one, so it
// displays at least what it was priced at and often more, which is the safe
// direction: the bar it cleared is a lower bound.
type renderClock struct {
	now    float64
	widest bool
}

// liveClock is the clock a layout is DRAWN against: countdowns spelled from now,
// in fractional Unix seconds.
func liveClock(now float64) renderClock { return renderClock{now: now} }

// widestClock is the clock a layout is PRICED against: every countdown spelled
// at the widest its grammar can produce, so the price reads no clock.
func widestClock() renderClock { return renderClock{widest: true} }

func (c renderClock) countdown(resetsAt string) string {
	if !c.widest {
		return tableCountdown(resetsAt, c.now)
	}
	if !resetKnown(resetsAt) {
		return ""
	}
	return countdownWidest
}

func (c renderClock) resetText(resetsAt string) string {
	cd := c.countdown(resetsAt)
	if cd == "" {
		return ""
	}
	return "resets " + cd
}

// resetClock returns the absolute local reset time ("20:39" / "Jul 14 09:00"),
// or "" once the reset has elapsed — "resets now" needs no clock (09§6.3
// reset_clock).
func resetClock(window map[string]any, now float64) string {
	if window == nil {
		return ""
	}
	ts, ok := parseResetsAt(window)
	if !ok {
		return ""
	}
	if ts-now <= 0 {
		return ""
	}
	ra, _ := window["resets_at"].(string)
	_, clock, parsed := oauth.FormatReset(ra, unixToTime(now))
	if !parsed {
		return ""
	}
	return clock
}

func parseResetsAt(window map[string]any) (float64, bool) {
	raw, ok := window["resets_at"]
	if !ok || raw == nil {
		return 0, false
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999-07:00", "2006-01-02 15:04:05-07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return float64(t.UnixNano()) / 1e9, true
		}
	}
	return 0, false
}

func unixToTime(now float64) time.Time {
	sec := int64(now)
	nsec := int64((now - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC()
}

func formatDuration(seconds float64) string {
	s := int(seconds)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm", s/60)
	}
	if s < 86400 {
		h := (s / 60) / 60
		m := (s / 60) % 60
		if m != 0 {
			return fmt.Sprintf("%dh %dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	d := (s / 3600) / 24
	h := (s / 3600) % 24
	if h != 0 {
		return fmt.Sprintf("%dd %dh", d, h)
	}
	return fmt.Sprintf("%dd", d)
}

func formatAge(ageS *float64) string {
	if ageS == nil || *ageS < serveTTLS {
		return ""
	}
	return "· " + formatDuration(*ageS) + " ago"
}

func nowLocal() time.Time { return time.Now() }

// clockStamp is the HH:MM:SS local-time stamp for the auto-view event log
// (09§6.3 clock_stamp). now is injectable for deterministic tests.
func clockStamp(now time.Time) string {
	return now.Format("15:04:05")
}

func lastSeenNote(entry usage.UsageEntry) string {
	if entry.LastGood == nil || entry.FetchedAt == nil {
		return ""
	}
	h := oauth.AccountHeadroom(oauth.NewUsage(entry.LastGood), nil)
	if h == nil {
		return ""
	}
	ageMs := int64(*entry.FetchedAt * 1000)
	return fmt.Sprintf("last seen %.0f%% used · %s", 100-*h, ageFromMs(ageMs))
}

func ageFromMs(ms int64) string {
	age := time.Since(time.UnixMilli(ms)).Seconds()
	if age < 0 {
		age = 0
	}
	return formatDuration(math.Floor(age)) + " ago"
}

func staleEntry(entry usage.UsageEntry) bool {
	return entry.AgeS != nil && *entry.AgeS > staleOKS
}

func scopedList(lastGood map[string]any) []map[string]any {
	raw, ok := lastGood["scoped"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// pctLabel renders a percentage the way autoswitch.pct_label does: f"{v:.10g}"
// — ten significant digits so 90.0→"90", 99.9→"99.9" (never a lying "100"),
// 85.555555 stays itself (09§4.5). Any threshold display MUST use this.
func pctLabel(value float64) string {
	return strings.TrimSpace(fmt.Sprintf("%.10g", value))
}
