package reporting

import (
	"strings"

	"github.com/tyclab/tycswap/internal/store"
)

// SessionLoginOwner names the actual link target using local evidence only
// (store.LoginOwnerSlot, which statusline.json's logins share).
func SessionLoginOwner(s *store.Store, target string) (label, number string) {
	scope, err := targetScope(s.BackupDir(), target)
	if err != nil {
		return "unknown", "unknown"
	}
	label = "default login"
	if strings.HasPrefix(scope, "mythos/") {
		label = "Mythos login"
	} else if scope == "fable" {
		label = "Fable store"
	}
	view := s.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	data, _ := view.ReadSequence()
	if num := view.LoginOwnerSlot(data, scope); num != "" {
		return label, num
	}
	return label, "unknown"
}
