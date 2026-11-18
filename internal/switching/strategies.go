// strategies.go — the usage-aware scoring for `best` (spec 02§5). selectBestSwitchable never recommends a move it cannot
// prove lands on strictly more headroom, never onto a worse or merely
// unverifiable account; ties (including current-vs-other) resolve to staying put
// / the earliest slot.
package switching

import "github.com/tyclab/tycswap/internal/store"

// selectBestSwitchable decides the `best` target relative to current (spec
// 02§5). Returns (target, note); target "" with a note means stay. Notes: none,
// current-unavailable, no-comparison, incomplete-comparison, stay, exhausted.
func selectBestSwitchable(s *store.Store, currentNum string, models []string, usage map[string]any) (string, string) {
	data, _ := s.ReadSequence()
	if data == nil {
		return "", "none"
	}

	var others []string
	for _, n := range data.Sequence {
		num := itoa(n)
		if num == currentNum {
			continue
		}
		// store.RotationEligible owns the rule; the candidate set here is exactly
		// SwitchableAccountNumbers minus the current slot.
		if s.RotationEligible(data, num) {
			others = append(others, num)
		}
	}
	if len(others) == 0 {
		return "", "none"
	}

	currentHeadroom := headroomOf(usage[currentNum], models)
	if currentHeadroom == nil {
		// Can't measure where the user is → can't prove any target is better.
		return "", "current-unavailable"
	}

	type scoredEntry struct {
		h   *float64
		num string
	}
	var scored []scoredEntry
	for _, num := range others {
		scored = append(scored, scoredEntry{headroomOf(usage[num], models), num})
	}

	// known preserves rotation (sequence) order; the first maximal wins, so ties
	// resolve to the earliest slot.
	var bestHeadroom *float64
	var bestNum string
	anyKnown := false
	for _, e := range scored {
		if e.h == nil {
			continue
		}
		anyKnown = true
		if bestHeadroom == nil || *e.h > *bestHeadroom {
			bestHeadroom = e.h
			bestNum = e.num
		}
	}
	if !anyKnown {
		return "", "no-comparison"
	}

	// Strictly greater only.
	if *bestHeadroom > *currentHeadroom {
		return bestNum, ""
	}

	// Current is at least as good as everything measurable. Only claim "all
	// exhausted" when every candidate's usage is known.
	for _, e := range scored {
		if e.h == nil {
			return "", "incomplete-comparison"
		}
	}
	if *currentHeadroom <= 0 {
		return "", "exhausted"
	}
	return "", "stay"
}
