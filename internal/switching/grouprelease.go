package switching

import (
	"fmt"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/store"
)

// ReleaseGroup hands a session group's account back to the shared pool, its
// newest credential written to the account's backup first.
func ReleaseGroup(s *store.Store, emit bool) (map[string]any, error) {
	if s.GroupID() == "" {
		return nil, fmt.Errorf("a session group is required")
	}
	if err := groups.SafePath(s.ProfileDir()); err != nil {
		return nil, err
	}
	prov := prefetchLiveIdentity(s)
	var released *groups.Account
	err := withTripleLock(s, func() error {
		if err := s.ReconcileGroup(); err != nil {
			return err
		}
		data, err := s.SequenceForUpdate()
		if err != nil {
			return err
		}
		released, err = s.ReleaseGroupAccount(func(active groups.Account, live string) error {
			kind, _ := classifyOutgoing(s, active.Number, active.Email, live, prov, data)
			if kind != "own-bytes" && kind != "own-family" && kind != "own-rotated" {
				return cerr.Session("the %s profile's credential ownership is unverified (%s); the account claim remains held", s.GroupID(), kind)
			}
			return nil
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	result := map[string]any{"group": s.GroupID(), "released": nil}
	if released != nil {
		result["released"] = numRef(released.Number, released.Email)
	}
	if emit {
		if released == nil {
			printOut("Group " + s.GroupID().Label() + " holds no account.")
		} else {
			printOut("Group " + s.GroupID().Label() + " released Account-" + released.Number + " (" + released.Email + ").")
		}
	}
	return result, nil
}
