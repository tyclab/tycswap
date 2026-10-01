// resolve.go — resolving a NUM|EMAIL|ALIAS identifier to a Codex slot.
// Implements claude-swap PR #252 codex/switcher.py CodexSwitcher.resolve_account.
//
// Lives here rather than in the switcher so export's --account can use it
// without an import cycle; the switcher reuses this one rule instead of
// carrying a second copy that could drift.

package transfer

import (
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/codex/store"
)

// ResolveSlot returns the first slot (in slot order) whose number, email or
// alias equals identifier, trimmed and compared case-insensitively. An empty
// identifier never matches — it would otherwise match the empty alias every
// slot starts with. The error is an AccountNotFoundError with the Python's
// text: "No Codex account matches '<id>'" (the untrimmed identifier).
func ResolveSlot(st *store.Store, identifier string) (store.Slot, error) {
	needle := strings.ToLower(strings.TrimSpace(identifier))
	if needle != "" {
		for _, sl := range st.Slots() {
			if needle == sl.Number || needle == strings.ToLower(sl.Email) ||
				(sl.Alias != "" && needle == strings.ToLower(sl.Alias)) {
				return sl, nil
			}
		}
	}
	return store.Slot{}, cerr.AccountNotFound("No Codex account matches '%s'", identifier)
}
