package recovery

import (
	"errors"
	"time"
)

func (s *Store) ModelChange(session, from, to string, at time.Time) error {
	if session == "" || ModelClass(from) == "" || ModelClass(to) == "" {
		return errors.New("known source and destination models required")
	}
	return s.update(func(state *State) error {
		ss, ok := state.Sessions[session]
		if !ok {
			return errors.New("session model is not recorded")
		}
		ss.ModelUncertain = true
		ss.PossibleModels = unique([]string{from, to})
		ss.Event.Model = ""
		ss.Event.ModelObserved = false
		ss.Event.At = at
		state.Sessions[session] = ss
		return nil
	})
}
