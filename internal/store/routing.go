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

// ReservedForFable reports whether this profile's rotation leaves the account
// to Fable work: the Opus / other group never needs Fable, so an account that
// can continue Fable sessions ranks after every account that cannot, and the
// group hands such an account back as soon as one that cannot is below every
// bar (DESIGN A71). The default login and the Fable group reserve nothing.
func (s *Store) ReservedForFable(num string) bool {
	if s.group != groups.Opus {
		return false
	}
	return s.WithRoutingModel(groups.Fable).GroupCompatibility(num, groups.Continue).Allowed
}
