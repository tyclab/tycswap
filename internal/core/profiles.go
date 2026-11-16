package core

import "github.com/tyclab/tycswap/internal/groups"

// ForGroup scopes the switcher to a retired session group's profile, which
// only the retirement step still reads (DESIGN A78).
func (sw *Switcher) ForGroup(id groups.ID) (*Switcher, error) {
	s, err := sw.Store.ForGroup(id)
	if err != nil {
		return nil, err
	}
	return &Switcher{Store: s}, nil
}
