package core

import (
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/store"
)

// ForGroup scopes the switcher to a retired session group's profile, which
// only the retirement step still reads (DESIGN A78).
func (sw *Switcher) ForGroup(id groups.ID) (*Switcher, error) {
	s, err := sw.Store.ForGroup(id)
	if err != nil {
		return nil, err
	}
	return &Switcher{Store: s}, nil
}

func (sw *Switcher) AccountStore() *store.Store { return sw.Store }

func (sw *Switcher) EnsureSessionAccountAvailable(number, profile string) error {
	return sw.Store.EnsureSessionAccountAvailable(number, profile)
}

func (sw *Switcher) ClaimLegacyProfile(number, email, profile string) error {
	return sw.Store.ClaimLegacyProfile(number, email, profile)
}
