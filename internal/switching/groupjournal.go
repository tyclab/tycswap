package switching

import (
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/store"
)

// A retiring group's unfinished switch can still hold the default login's or
// a legacy profile's credential; reconciling it needs that profile's locks.
func defaultHandoffRequired(s *store.Store) bool {
	journal, err := groups.LoadJournal(s.SharedRoot(), s.GroupID())
	return err == nil && journal != nil && journal.DefaultHandoff
}

func legacyHandoffProfile(s *store.Store) (string, error) {
	journal, err := groups.LoadJournal(s.SharedRoot(), s.GroupID())
	if err != nil || journal == nil || journal.SourceOwner == nil {
		return "", err
	}
	return journal.SourceOwner.ProfileDir, nil
}
