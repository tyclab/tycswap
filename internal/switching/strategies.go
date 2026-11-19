// strategies.go — the usage-aware choice for `best` (spec 02§5, DESIGN A99):
// the account the account order ranks first, as the engine ranks its targets,
// over the windows the default-session policy counts; never an account at a
// counted limit, and never a move away from a live account that ranks first.
package switching

import (
	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/store"
)

// selectBestSwitchable decides the `best` target relative to current (spec
// 02§5). Returns (target, note); target "" with a note means stay. Notes: none,
// current-unavailable, no-comparison, incomplete-comparison, stay, exhausted.
func selectBestSwitchable(s *store.Store, currentNum string, c Counting, usage map[string]any) (string, string) {
	data, _ := s.ReadSequence()
	if data == nil {
		return "", "none"
	}
	known := func(num string) bool { _, ok := usage[num].(map[string]any); return ok }

	// The candidate set is SwitchableAccountNumbers (store.RotationEligible)
	// without the quarantined slots the engine never targets, in sequence
	// order with the current slot in its place, so ties resolve by slot.
	var candidates []string
	others, measured := 0, 0
	for _, n := range data.Sequence {
		num := itoa(n)
		if num == currentNum {
			candidates = append(candidates, num)
			continue
		}
		if s.RotationEligible(data, num) && !c.Quarantined[num] && (c.Serves == nil || c.Serves(num)) {
			candidates = append(candidates, num)
			others++
			if known(num) {
				measured++
			}
		}
	}
	if others == 0 {
		return "", "none"
	}
	if !known(currentNum) {
		// Can't measure where the user is → can't prove any target is better.
		return "", "current-unavailable"
	}
	if measured == 0 {
		return "", "no-comparison"
	}
	unknown := measured < others
	ranked := autoswitch.RankTargets(candidates, usage, c.Settings, c.Models, c.Reserved, s.Clk.Now())
	switch {
	case len(ranked) > 0 && ranked[0] != currentNum:
		return ranked[0], ""
	case unknown:
		return "", "incomplete-comparison"
	case len(ranked) > 0:
		return "", "stay"
	}
	return "", "exhausted"
}
