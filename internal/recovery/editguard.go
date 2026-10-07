package recovery

import (
	"errors"
	"fmt"
)

var ErrEditOwned = errors.New("workspace edit ownership belongs to another session")

func (s *Store) EditBlock(session, cwd string) (string, error) {
	workspace, err := WorkspaceKey(cwd)
	if err != nil {
		return "", err
	}
	state, err := s.load()
	if err != nil {
		return "", err
	}
	h, ok := state.Handovers[workspace]
	if !ok {
		return "", nil
	}
	owner := ""
	switch h.Status {
	case "prepared":
	case "active":
		owner = h.DestinationSessionID
	case "reclaimed":
		owner = h.SourceSessionID
	default:
		return "", nil
	}
	if session != "" && session == owner {
		return "", nil
	}
	return fmt.Sprintf("Editing ownership for this workspace is held by the handover destination. Stop that destination and run tycswap recovery reclaim --cwd %q --confirm-no-background-tools before continuing the source.", cwd), nil
}

func (s *Store) Reclaim(cwd, digest string, destinationStopped, backgroundConfirmed bool) error {
	workspace, err := WorkspaceKey(cwd)
	if err != nil {
		return err
	}
	if !destinationStopped || !backgroundConfirmed {
		return errors.New("destination must be stopped and background tools reviewed before reclaiming")
	}
	return s.update(func(state *State) error {
		h, ok := state.Handovers[workspace]
		if !ok || h.PacketDigest != digest || h.Status != "active" || h.DestinationSessionID == "" {
			return errors.New("confirmed active destination is required; unresolved launches must be reconciled first")
		}
		h.Status = "reclaimed"
		state.Handovers[workspace] = h
		return nil
	})
}
