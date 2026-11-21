package reporting

import (
	"strings"

	"github.com/tyclab/tycswap/internal/store"
)

// SessionLoginOwner names the actual link target using local evidence only.
func SessionLoginOwner(s *store.Store, target string) (label, number string) {
	scope, err := targetScope(s.BackupDir(), target)
	if err != nil {
		return "unknown", "unknown"
	}
	view := s.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	data, _ := view.ReadSequence()
	if digest, ok := strings.CutPrefix(scope, "mythos/"); ok {
		if num := store.MythosLoginOwner(data, digest); num != "" {
			return "Mythos login", num
		}
		return "Mythos login", "unknown"
	}
	label = "default login"
	if scope == "fable" {
		label = "Fable store"
		view, err = view.ForFable()
	}
	if err == nil && data != nil && data.Accounts != nil && data.Sequence != nil {
		if live, _, err := view.Creds.ReadActive(); err == nil {
			if owner := view.LocalCredentialSlot(data, live); owner.Slot != "" {
				return label, owner.Slot
			}
		}
	}
	return label, "unknown"
}
