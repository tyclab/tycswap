package recovery

import (
	"errors"
	"path/filepath"
	"time"
)

type Handover struct {
	SourceSessionID      string      `json:"source_session_id"`
	IncidentID           string      `json:"incident_id"`
	CWD                  string      `json:"cwd"`
	Destination          Destination `json:"destination"`
	DestinationSessionID string      `json:"destination_session_id,omitempty"`
	PacketDigest         string      `json:"packet_digest"`
	Status               string      `json:"status"`
	StartedAt            time.Time   `json:"started_at"`
}

func (s *Store) BeginHandover(plan LaunchPlan, incidentID string) error {
	if plan.SourceSessionID == "" || plan.PacketDigest == "" || !filepath.IsAbs(plan.CWD) {
		return errors.New("validated handover plan required")
	}
	workspace, err := WorkspaceKey(plan.CWD)
	if err != nil {
		return err
	}
	return s.update(func(state *State) error {
		source, ok := state.Sessions[plan.SourceSessionID]
		if !ok || !source.Idle || (!source.Event.Stopped && source.Event.Kind != "ManualSelection") || source.Event.IncidentID != incidentID || source.Event.CWD != plan.CWD || source.PendingTools != 0 {
			return errors.New("source changed or is not idle")
		}
		for id, other := range state.Sessions {
			if id == plan.SourceSessionID || other.Idle || other.Event.CWD == "" {
				continue
			}
			otherWorkspace, err := WorkspaceKey(other.Event.CWD)
			if err != nil {
				return err
			}
			if otherWorkspace == workspace {
				return errors.New("another recorded session became active in this worktree before handover")
			}
		}
		if old, ok := state.Handovers[workspace]; ok && old.Status != "cancelled" && !(old.Status == "reclaimed" && old.SourceSessionID == plan.SourceSessionID) && !(old.Status == "active" && old.DestinationSessionID == plan.SourceSessionID) {
			return errors.New("workspace editing ownership is already transferred or awaiting destination start")
		}
		state.Handovers[workspace] = Handover{SourceSessionID: plan.SourceSessionID, IncidentID: incidentID, CWD: plan.CWD, Destination: plan.Destination, PacketDigest: plan.PacketDigest, Status: "prepared", StartedAt: time.Now()}
		return nil
	})
}

func (s *Store) CompleteHandover(cwd, digest, destinationSessionID string) error {
	workspace, err := WorkspaceKey(cwd)
	if err != nil {
		return err
	}
	if destinationSessionID == "" {
		return errors.New("actual destination session ID required")
	}
	return s.update(func(state *State) error {
		h, ok := state.Handovers[workspace]
		if !ok || h.Status != "prepared" || h.PacketDigest != digest {
			return errors.New("prepared handover changed or is missing")
		}
		if destinationSessionID == h.SourceSessionID {
			return errors.New("destination must have a new native session ID")
		}
		h.Status = "active"
		h.DestinationSessionID = destinationSessionID
		state.Handovers[workspace] = h
		return nil
	})
}

func (s *Store) CancelHandover(cwd, digest string) error {
	workspace, err := WorkspaceKey(cwd)
	if err != nil {
		return err
	}
	return s.update(func(state *State) error {
		h, ok := state.Handovers[workspace]
		if !ok || h.Status != "prepared" || h.PacketDigest != digest {
			return errors.New("only an unstarted matching handover can be cancelled")
		}
		h.Status = "cancelled"
		state.Handovers[workspace] = h
		return nil
	})
}

func (s *Store) ConfirmDestination(event Event, cwd, digest string) error {
	workspace, err := WorkspaceKey(cwd)
	if err != nil {
		return err
	}
	if cwd == "" || digest == "" {
		return nil
	}
	return s.update(func(state *State) error {
		h, ok := state.Handovers[workspace]
		if !ok || h.Status != "prepared" || h.PacketDigest != digest {
			return errors.New("matching prepared handover is missing")
		}
		if event.SessionID == "" || event.SessionID == h.SourceSessionID || event.Provider != h.Destination.Provider || event.CWD != cwd || event.At.Before(h.StartedAt) || event.At.Sub(h.StartedAt) > time.Hour {
			return errors.New("destination session does not match the reviewed launch")
		}
		if event.Provider == "claude" && event.Group != h.Destination.Group {
			return errors.New("destination group does not match")
		}
		h.Status = "active"
		h.DestinationSessionID = event.SessionID
		state.Handovers[workspace] = h
		return nil
	})
}
