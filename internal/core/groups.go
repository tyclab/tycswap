package core

import (
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
)

func (sw *Switcher) ForGroup(id groups.ID) (*Switcher, error) {
	s, err := sw.Store.ForGroup(id)
	if err != nil {
		return nil, err
	}
	return &Switcher{Store: s}, nil
}

func (sw *Switcher) GroupStore() *store.Store { return sw.Store }

func (sw *Switcher) EnsureSessionAccountAvailable(number, profile string) error {
	return sw.Store.EnsureSessionAccountAvailable(number, profile)
}

func (sw *Switcher) ClaimLegacyProfile(number, email, profile string) error {
	return sw.Store.ClaimLegacyProfile(number, email, profile)
}

func (sw *Switcher) WithGroupIntent(intent groups.Intent) *Switcher {
	return &Switcher{Store: sw.Store.WithGroupIntent(intent)}
}

func (sw *Switcher) WithGroupHandoff() *Switcher {
	return &Switcher{Store: sw.Store.WithGroupHandoff()}
}

func (sw *Switcher) ReconcileGroup() error { return switching.ReconcileGroup(sw.Store) }
