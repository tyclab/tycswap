package transfer

import (
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/codex/store"
)

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
