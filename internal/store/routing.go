package store

import "github.com/tyclab/tycswap/internal/groups"

func (s *Store) WithRoutingModel(model groups.ID) *Store {
	clone := *s
	clone.routingModel = model
	if clone.group != "" {
		clone.Creds = &profileCredentials{Store: clone.sharedCreds, owner: &clone}
	}
	return &clone
}

func (s *Store) WithRoutingLockHeld() *Store {
	clone := *s
	clone.routingLockHeld = true
	if clone.group != "" {
		clone.Creds = &profileCredentials{Store: clone.sharedCreds, owner: &clone}
	}
	return &clone
}

func (s *Store) RoutingLockHeld() bool { return s.routingLockHeld }
