package usage

type Class int

const (
	ClassWeek Class = iota
	ClassModel
	ClassSession
)

const (
	FiveHourLabel = "5h"
	SevenDayLabel = "7d"
)

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

func (h Headroom) Weekly() *float64 { return least(h.Week, h.Model) }

func (h Headroom) Binding() *float64 { return least(h.Session, h.Weekly()) }

func (h Headroom) Known() bool { return h.Binding() != nil }

func spent(p *float64) bool { return p != nil && *p <= 0 }

func (h Headroom) Exhausted(c Class) bool { return spent(h.Axis(c)) }

func (h Headroom) WeeklyExhausted() bool { return spent(h.Weekly()) }

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
