// classes.go — an account reports three kinds of limit, and the engine must
// not treat them as one number (DESIGN A34).
//
// The rolling 5-hour window is a RATE limit, and it bursts: a fan-out of
// parallel subagents can take it from 85 % to full inside one poll interval.
// Reaching 100 % costs a wait, but one that lands in the middle of whatever
// was running.
//
// The 7-day window is the account's BUDGET, and it creeps: it moves at the
// pace of a week's work, and an account that reaches 100 % is out of service
// for days.
//
// A per-model weekly window is a budget too, but a narrower one: at 100 %
// that one model is gone for the week while the account still serves
// everything else. It counts only when autoswitch.model says so.
//
// Collapsing all three into max(pct), which is what a single headroom figure
// does, judges a burst-prone rate window and a slow budget against one bar,
// which cannot suit both. Each class therefore has a bar of its own.
package usage

// Class separates the kinds of limit a window imposes. Each has a threshold of
// its own (autoswitch.fiveHourThreshold / sevenDayThreshold / modelThreshold).
type Class int

const (
	// ClassWeek is the 7d window: the account's whole budget. It is the zero
	// value because it is the limit that costs the most when spent.
	ClassWeek Class = iota
	// ClassModel is a per-model weekly window: one model's budget for the
	// week, counted only when autoswitch.model names it.
	ClassModel
	// ClassSession is the rolling 5h window: a rate limit that refills within
	// hours.
	ClassSession
)

// Window labels relevantWindows gives the two account-wide windows; every
// other label is a per-model window's display name.
const (
	FiveHourLabel = "5h"
	SevenDayLabel = "7d"
)

// ClassOf reports which kind of limit a relevant-window label denotes.
func ClassOf(label string) Class {
	switch label {
	case FiveHourLabel:
		return ClassSession
	case SevenDayLabel:
		return ClassWeek
	default:
		return ClassModel
	}
}

// Name is the label a log line or a UI uses for the class.
func (c Class) Name() string {
	switch c {
	case ClassSession:
		return FiveHourLabel
	case ClassModel:
		return "model"
	default:
		return SevenDayLabel
	}
}

// Headroom is an account's remaining room on each axis, as percentages. A nil
// axis means "no window of that class reported a value": unknown, never
// "full" and never "empty". Model is nil whenever per-model windows are not
// counted.
type Headroom struct {
	Session *float64
	Week    *float64
	Model   *float64
}

// Axis picks one class out of the three.
func (h Headroom) Axis(c Class) *float64 {
	switch c {
	case ClassSession:
		return h.Session
	case ClassModel:
		return h.Model
	default:
		return h.Week
	}
}

// least is the smaller of two optional figures; nil means unknown.
func least(a, b *float64) *float64 {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case *b < *a:
		return b
	default:
		return a
	}
}

// Weekly is the binding BUDGET figure: the week, or a counted model window
// when that is the tighter of the two. It is what ranking compares, since 5h
// room refills within hours.
func (h Headroom) Weekly() *float64 { return least(h.Week, h.Model) }

// Binding is the axis that limits the account first: the smallest of the
// three, i.e. the single figure used before this split. nil when no axis is
// known.
func (h Headroom) Binding() *float64 { return least(h.Session, h.Weekly()) }

// Known reports whether any window at all was readable.
func (h Headroom) Known() bool { return h.Binding() != nil }

// spent reports whether an optional figure is at or over its limit.
func spent(p *float64) bool { return p != nil && *p <= 0 }

// Exhausted reports whether one axis is at or over its limit.
func (h Headroom) Exhausted(c Class) bool { return spent(h.Axis(c)) }

// WeeklyExhausted separates "resting for a while" from "spent for days": true
// when the week, or a counted model window, has run out.
func (h Headroom) WeeklyExhausted() bool { return spent(h.Weekly()) }

// AccountHeadroomByClass splits the remaining room into the three axes (the
// 5h rate limit, the 7d budget and the counted per-model weekly windows),
// because a switch decision weighs them differently and each has a threshold
// of its own (DESIGN A34). Within an axis the worst window binds.
func AccountHeadroomByClass(usage map[string]any, models []string) Headroom {
	var h Headroom
	for _, w := range relevantWindows(usage, models) {
		room := 100.0 - w.pct
		axis := &h.Week
		switch ClassOf(w.label) {
		case ClassSession:
			axis = &h.Session
		case ClassModel:
			axis = &h.Model
		}
		if *axis == nil || room < **axis {
			v := room
			*axis = &v
		}
	}
	return h
}
