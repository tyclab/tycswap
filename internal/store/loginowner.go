package store

import "strings"

// LoginOwnerSlot is the account whose credential a login a session link can
// point at holds now, from local evidence only (DESIGN A109, A113). scope ""
// is the default login and "fable" the Fable store: the slot a unique backup
// or the chain record names for the live credential. "mythos/<digest>" is an
// independent Mythos login: the one account its directory is named for. ""
// when that is not known. s may be any scope of the store.
func (s *Store) LoginOwnerSlot(data *SequenceData, scope string) string {
	if digest, ok := strings.CutPrefix(scope, "mythos/"); ok {
		return MythosLoginOwner(data, digest)
	}
	if data == nil || data.Accounts == nil || data.Sequence == nil {
		return ""
	}
	view := s.rootScope()
	switch scope {
	case "":
	case "fable":
		f, err := view.ForFable()
		if err != nil {
			return ""
		}
		view = f
	default:
		return ""
	}
	live, _, err := view.Creds.ReadActive()
	if err != nil {
		return ""
	}
	return view.LocalCredentialSlot(data, live).Slot
}

// rootScope is the default store s was made from, whatever its scope: its
// credential is the default login's, not a group's or the Fable store's.
func (s *Store) rootScope() *Store {
	if s.fable {
		return s.defaultScope()
	}
	if s.group == "" {
		return s
	}
	d := *s
	d.group, d.Creds, d.sharedCreds = "", s.sharedCreds, nil
	return &d
}
