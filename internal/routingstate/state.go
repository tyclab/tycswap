package routingstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/procdetect"
)

type Session struct {
	Group  string `json:"group"`
	ID     string `json:"id"`
	PID    int    `json:"pid"`
	Model  string `json:"model"`
	Launch string `json:"launch,omitempty"`
}

type Lease struct {
	ID      string `json:"id"`
	Group   string `json:"group"`
	Session string `json:"session"`
	Account string `json:"account"`
	Model   string `json:"model"`
	Agent   string `json:"agent,omitempty"`
	PID     int    `json:"pid"`
}

type State struct {
	Version  int                `json:"version"`
	Sessions map[string]Session `json:"sessions"`
	Leases   map[string]Lease   `json:"leases"`
	History  map[string]Session `json:"history,omitempty"`
}

func Key(group, id string) string { return group + "/" + id }

func Family(model string) (string, error) {
	m := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), "[1m]")
	for _, ch := range m {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
			return "", fmt.Errorf("unknown model %q", model)
		}
	}
	for _, family := range []string{"fable", "opus", "sonnet", "haiku"} {
		if m == family || strings.HasPrefix(m, "claude-"+family+"-") && len(m) > len("claude-"+family+"-") {
			return family, nil
		}
	}
	return "", fmt.Errorf("unknown model %q", model)
}

func Read(root string) (*State, error) {
	path := filepath.Join(root, "routing.json")
	if err := groups.SafePath(path); err != nil {
		return nil, err
	}
	state := &State{Version: 1, Sessions: map[string]Session{}, Leases: map[string]Lease{}, History: map[string]Session{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, state); err != nil {
		return nil, fmt.Errorf("routing state is unreadable: %w", err)
	}
	if state.Version != 1 || state.Sessions == nil || state.Leases == nil {
		return nil, fmt.Errorf("routing state has an unsupported schema")
	}
	if state.History == nil {
		state.History = map[string]Session{}
	}
	for key, session := range state.Sessions {
		if _, err := groups.Parse(session.Group); err != nil {
			return nil, err
		}
		if session.ID == "" || session.PID <= 1 || key != Key(session.Group, session.ID) {
			return nil, fmt.Errorf("routing session identity is invalid")
		}
		if _, err := Family(session.Model); err != nil {
			return nil, err
		}
	}
	for key, lease := range state.Leases {
		if _, err := groups.Parse(lease.Group); err != nil {
			return nil, err
		}
		if lease.ID == "" || lease.Account == "" || lease.PID <= 1 || key != Key(lease.Group, lease.ID) {
			return nil, fmt.Errorf("routing request identity is invalid")
		}
		if session, ok := state.Sessions[Key(lease.Group, lease.Session)]; !ok || session.PID != lease.PID {
			return nil, fmt.Errorf("routing request has no matching process")
		}
		if _, err := Family(lease.Model); err != nil {
			return nil, err
		}
	}
	for key, session := range state.History {
		if _, err := groups.Parse(session.Group); err != nil {
			return nil, err
		}
		if session.ID == "" || session.Launch == "" || key != Key(session.Group, session.ID) {
			return nil, fmt.Errorf("routing history identity is invalid")
		}
		if _, err := Family(session.Model); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func (s *State) Prune() {
	for key, session := range s.Sessions {
		if !procdetect.IsPIDAlive(session.PID) {
			delete(s.Sessions, key)
		}
	}
	for key, lease := range s.Leases {
		if _, ok := s.Sessions[Key(lease.Group, lease.Session)]; !ok {
			delete(s.Leases, key)
		}
	}
}

func With(root string, fn func(*State) error) error {
	path := filepath.Join(root, "routing.lock")
	if err := groups.SafePath(path); err != nil {
		return err
	}
	return filelock.New(path, 0).With(func() error {
		state, err := Read(root)
		if err != nil {
			return err
		}
		before, _ := json.Marshal(state)
		state.Prune()
		if err := fn(state); err != nil {
			return err
		}
		after, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(before, after) {
			return nil
		}
		return atomicfile.Write(filepath.Join(root, "routing.json"), after, atomicfile.Opts{})
	})
}

func (s *State) CheckSwitch(group, target string) error {
	for _, lease := range s.Leases {
		if lease.Group == group && (target == "" || target != lease.Account) {
			return fmt.Errorf("profile %s has an unfinished request on Account-%s", group, lease.Account)
		}
	}
	return nil
}

func (s *State) CheckIdle(group string) error {
	for _, session := range s.Sessions {
		if group == "" || session.Group == group {
			return fmt.Errorf("profile %s has a live managed session", session.Group)
		}
	}
	return nil
}
