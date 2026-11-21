package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"syscall"

	"github.com/tyclab/tycswap/internal/cerr"

	"github.com/tyclab/tycswap/internal/reporting"
)

// The local dashboard and its remote tray both use the host's CLI operations.
type mythosSwitcher interface {
	SwitchMythos(id string) (map[string]any, error)
}

type sessionMover interface {
	SessionMoves(id string) ([]string, error)
	UseSession(id, store string) error
}

type allocationEditor interface {
	SetModels(id string, models []string) error
}

type nextStartSelection interface {
	NextStartModel() (string, error)
	SetNextStartModel(model string) error
}

func (s *Server) handleMythosSwitch(w http.ResponseWriter, r *http.Request, provider, id string) {
	if provider != reporting.ProviderClaude {
		writeError(w, http.StatusBadRequest, "the Mythos login belongs to a Claude account")
		return
	}
	q := r.URL.Query()
	if isTruthy(q.Get("empty")) || isTruthy(q.Get("force")) || isTruthy(q.Get("confirmAuthChange")) {
		writeError(w, http.StatusBadRequest, "the Mythos login takes an account with a ready sign-in")
		return
	}
	ops, ok := s.d.Accounts.(mythosSwitcher)
	if unavailable(w, ok, "Mythos login selection") {
		return
	}
	s.mutateLogin(w, func() (map[string]any, error) {
		res, err := ops.SwitchMythos(id)
		if err == nil {
			s.logAction(r, "Selected Mythos login "+id)
		}
		return res, err
	})
}

// A live client holding its refresh lock and another tycswap process holding
// the store lock are both retryable conflicts for these login operations.
func (s *Server) mutateLogin(w http.ResponseWriter, fn func() (map[string]any, error)) {
	s.mutate(w, func() (map[string]any, error) {
		result, err := fn()
		var ce *cerr.Error
		if errors.Is(err, syscall.EBUSY) || errors.As(err, &ce) && ce.Kind == cerr.KindClaudeCodeLockTimeout {
			return result, httpErr(http.StatusConflict, "%s", err)
		}
		return result, err
	})
}

// A missing or null field cannot clear an allocation or a queued start.
func requiredField[T any](w http.ResponseWriter, r *http.Request, name string) (T, bool) {
	var value *T
	var body map[string]json.RawMessage
	if decodeBody(w, r, &body) {
		if raw, ok := body[name]; ok && len(body) == 1 && json.Unmarshal(raw, &value) == nil && value != nil {
			return *value, true
		}
		writeError(w, http.StatusBadRequest, "Supply a valid "+name+" field.")
	}
	var zero T
	return zero, false
}

func (s *Server) handleSessionUse(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.d.Accounts.(sessionMover)
	if unavailable(w, ok, "session moves") {
		return
	}
	dest, ok := requiredField[string](w, r, "store")
	if !ok {
		return
	}
	switch dest {
	case "default", "fable", "mythos":
	default:
		writeError(w, http.StatusBadRequest, "Choose the default login, Fable store, or Mythos login.")
		return
	}
	id := r.PathValue("id")
	s.mutateLogin(w, func() (map[string]any, error) {
		err := ops.UseSession(id, dest)
		if err == nil {
			s.logAction(r, "Moved session "+id+" to "+dest)
		}
		return nil, err
	})
}

func (s *Server) handleAccountModels(w http.ResponseWriter, r *http.Request) {
	provider, id, ok := pathKey(w, r)
	if !ok {
		return
	}
	if provider != reporting.ProviderClaude {
		writeError(w, http.StatusBadRequest, "model allocations apply to Claude accounts only")
		return
	}
	ops, ok := s.d.Accounts.(allocationEditor)
	if unavailable(w, ok, "account models") {
		return
	}
	models, ok := requiredField[[]string](w, r, "models")
	if !ok {
		return
	}
	for _, m := range models {
		switch m {
		case "mythos", "fable", "opus":
		default:
			writeError(w, http.StatusBadRequest, "Choose Mythos, Fable, or Opus.")
			return
		}
	}
	s.mutateLogin(w, func() (map[string]any, error) {
		err := ops.SetModels(id, models)
		if err == nil {
			s.logAction(r, "Changed the models for account "+id)
		}
		return nil, err
	})
}

func (s *Server) handleNextStartModel(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.d.Accounts.(nextStartSelection)
	if unavailable(w, ok, "next Claude start") {
		return
	}
	model, ok := requiredField[string](w, r, "model")
	if !ok {
		return
	}
	if model != "" && model != "mythos" {
		writeError(w, http.StatusBadRequest, "Choose Mythos or clear the next Claude start.")
		return
	}
	s.mutateLogin(w, func() (map[string]any, error) { return nil, ops.SetNextStartModel(model) })
}
