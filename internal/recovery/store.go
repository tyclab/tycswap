package recovery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/filelock"
)

type Incident struct {
	Event     Event    `json:"event"`
	Decision  Decision `json:"decision"`
	Offered   bool     `json:"offered"`
	Dismissed bool     `json:"dismissed"`
}

type SessionState struct {
	Event          Event    `json:"event"`
	Sequence       uint64   `json:"sequence"`
	Idle           bool     `json:"idle"`
	PendingTools   int      `json:"pending_tools"`
	ModelUncertain bool     `json:"model_uncertain,omitempty"`
	PossibleModels []string `json:"possible_models,omitempty"`
}

type State struct {
	Version   int                     `json:"version"`
	Sessions  map[string]SessionState `json:"sessions"`
	Incidents map[string]Incident     `json:"incidents"`
	Handovers map[string]Handover     `json:"handovers,omitempty"`
}

type Store struct{ Path string }

func NewStore(root string) *Store { return &Store{Path: filepath.Join(root, "recovery", "state.json")} }

func (s *Store) load() (State, error) {
	state := State{Version: 1, Sessions: map[string]SessionState{}, Incidents: map[string]Incident{}, Handovers: map[string]Handover{}}
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Version != 1 || state.Sessions == nil || state.Incidents == nil {
		return State{}, errors.New("invalid recovery state")
	}
	if state.Handovers == nil {
		state.Handovers = map[string]Handover{}
	}
	return state, nil
}

func (s *Store) update(fn func(*State) error) error {
	return filelock.New(s.Path+".lock", 2*time.Second).With(func() error {
		state, err := s.load()
		if err != nil {
			return err
		}
		if err := fn(&state); err != nil {
			return err
		}
		return atomicfile.WriteJSON(s.Path, state, atomicfile.Opts{})
	})
}

func (s *Store) Snapshot() (State, error) { return s.load() }

func key(session, id string) string { return session + "\x00" + id }

func (s *Store) Record(e Event) (Event, error) {
	if e.SessionID == "" || e.At.IsZero() {
		return e, errors.New("session and event time required")
	}
	err := s.update(func(state *State) error {
		ss := state.Sessions[e.SessionID]
		if e.Kind != "UserPromptSubmit" && e.Kind != "turn/started" && e.Kind != "SessionStart" && e.Kind != "thread/started" && ss.Event.AccountID != "" {
			e.AccountID = ss.Event.AccountID
			e.AccountIdentity = ss.Event.AccountIdentity
		}
		if e.Kind != "turn/started" && e.Kind != "thread/started" && ss.Event.SourcePID > 1 {
			e.SourcePID = ss.Event.SourcePID
		}
		if e.ModelObserved {
			ss.ModelUncertain = false
			ss.PossibleModels = nil
		} else if ss.ModelUncertain {
			e.Model = ""
		} else if ss.Event.Model != "" {
			e.Model = ss.Event.Model
		}
		if e.TranscriptPath == "" {
			e.TranscriptPath = ss.Event.TranscriptPath
		}
		if e.CWD == "" {
			e.CWD = ss.Event.CWD
		}
		switch e.Kind {
		case "ManualSelection":
			ss.Idle = true
		case "thread/started":
			ss.Idle = true
		case "SessionStart":
			ss.Idle = false
		case "UserPromptSubmit", "turn/started":
			workspace, err := WorkspaceKey(e.CWD)
			if err != nil {
				return err
			}
			if h, ok := state.Handovers[workspace]; ok {
				owner := ""
				switch h.Status {
				case "active":
					owner = h.DestinationSessionID
				case "reclaimed":
					owner = h.SourceSessionID
				case "prepared":
					owner = ""
				default:
					owner = e.SessionID
				}
				if owner != e.SessionID {
					return ErrEditOwned
				}
			}
			ss.Sequence++
			ss.Idle = false
		case "Stop", "StopFailure", "SessionEnd", "turn/completed":
			ss.Idle = true
		case "error":
			if e.IncidentID != "" && e.Error != "" {
				state.Incidents[key(e.SessionID, e.IncidentID)] = Incident{Event: e}
			}
			return nil
		default:
			return errors.New("unsupported lifecycle event")
		}
		if e.IncidentID == "" {
			e.IncidentID = incident(e.SessionID, ss.Sequence)
		}
		if e.Kind == "turn/completed" && e.Stopped && e.Error == "" {
			if prior, ok := state.Incidents[key(e.SessionID, e.IncidentID)]; ok && (prior.Event.Error == "usage_limit" || prior.Event.Error == "rate_limit") {
				e.Error = prior.Event.Error
			}
		}
		ss.Event = e
		state.Sessions[e.SessionID] = ss
		if e.Stopped {
			k := key(e.SessionID, e.IncidentID)
			old := state.Incidents[k]
			old.Event = e
			state.Incidents[k] = old
		}
		return nil
	})
	return e, err
}

func (s *Store) Decide(in Input) (Decision, error) {
	return s.WriteDecision(in.Event, Evaluate(in))
}

func (s *Store) WriteDecision(event Event, d Decision) (Decision, error) {
	err := s.update(func(state *State) error {
		if ss, ok := state.Sessions[event.SessionID]; ok && (!ss.Idle || !ss.Event.Stopped || ss.Event.IncidentID != event.IncidentID) {
			d = Decision{Action: "ignore", Reason: "Source is no longer stopped on this incident"}
			return nil
		}
		k := key(event.SessionID, event.IncidentID)
		old := state.Incidents[k]
		old.Event = event
		if old.Dismissed && d.Action == "offer" {
			d.Action = "wait"
			d.Reason = "Handover dismissed for this incident"
		}
		if d.Action == "offer" {
			d.NewlyOffered = !old.Offered
			old.Offered = true
		}
		old.Decision = d
		state.Incidents[k] = old
		return nil
	})
	return d, err
}

func (s *Store) Dismiss(session, id string) error {
	return s.update(func(state *State) error {
		k := key(session, id)
		old, ok := state.Incidents[k]
		if !ok {
			return errors.New("incident not found")
		}
		old.Dismissed = true
		old.Decision.NewlyOffered = false
		old.Decision.Action = "wait"
		old.Decision.Reason = "Handover dismissed for this incident"
		state.Incidents[k] = old
		return nil
	})
}
