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

// ReservedForFable: the Opus / other group never needs Fable, so it ranks
// Fable-capable accounts last and hands them back (DESIGN A71). The default
// login reserves by its live sessions instead (DefaultSessionPolicy, A77).
func (s *Store) ReservedForFable(num string) bool {
	return s.group == groups.Opus && s.FableCapable(num)
}
