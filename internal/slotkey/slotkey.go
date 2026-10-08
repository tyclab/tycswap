// Package slotkey is the total order over slot keys: numerics by value (ties lexicographic), then non-numerics lexicographically.
package slotkey

import (
	"sort"
	"strconv"
)

// A naive numeric-or-lexicographic comparator is intransitive on "15"/"3"/"2abc", which sort.Slice may resolve nondeterministically.
func Less(a, b string) bool {
	na, aerr := strconv.Atoi(a)
	nb, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		if na != nb {
			return na < nb
		}
		return a < b
	case aerr == nil: // a numeric, b not: numerics first
		return true
	case berr == nil: // b numeric, a not: numerics first
		return false
	default:
		return a < b
	}
}

func Sorted(keys []string) []string {
	out := append([]string(nil), keys...)
	sort.SliceStable(out, func(i, j int) bool { return Less(out[i], out[j]) })
	return out
}
