package web

import (
	"net/http"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/reporting"
)

type RecoveryView struct {
	Incidents     []recovery.Incident          `json:"incidents"`
	Coverage      string                       `json:"coverage"`
	Error         string                       `json:"error,omitempty"`
	WaitMinutes   int                          `json:"waitMinutes"`
	SessionModels map[string]string            `json:"sessionModels"`
	Handovers     map[string]recovery.Handover `json:"handovers"`
}

type RecoveryStartRequest struct {
	Packet           recovery.Packet      `json:"packet"`
	Destination      recovery.Destination `json:"destination"`
	ReviewedDigest   string               `json:"reviewedDigest"`
	Explicit         bool                 `json:"explicit"`
	PendingConfirmed bool                 `json:"pendingConfirmed"`
}

type RecoveryStartResult struct {
	Started bool                `json:"started"`
	Plan    recovery.LaunchPlan `json:"plan"`
	Message string              `json:"message"`
}

type RecoveryFacade interface {
	View(reporting.AccountsSnapshot) RecoveryView
	Prepare(session string, supplied recovery.Source) (recovery.Packet, error)
	Dismiss(session, incident string) error
	Start(RecoveryStartRequest) (RecoveryStartResult, error)
}

func (s *Server) handleRecoveryPrepare(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Recovery != nil, "limit recovery") {
		return
	}
	var body struct {
		SessionID string          `json:"sessionId"`
		Context   recovery.Source `json:"context"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mutate(w, func() (map[string]any, error) {
		packet, err := s.d.Recovery.Prepare(body.SessionID, body.Context)
		if err != nil {
			return nil, cerr.Validation("%s", err.Error())
		}
		return map[string]any{"packet": packet}, nil
	})
}

func (s *Server) handleRecoveryDismiss(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Recovery != nil, "limit recovery") {
		return
	}
	var body struct {
		SessionID  string `json:"sessionId"`
		IncidentID string `json:"incidentId"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mutate(w, func() (map[string]any, error) {
		err := s.d.Recovery.Dismiss(body.SessionID, body.IncidentID)
		return map[string]any{"dismissed": err == nil}, err
	})
}

func (s *Server) handleRecoveryStart(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Recovery != nil, "limit recovery") {
		return
	}
	var body RecoveryStartRequest
	if !decodeBody(w, r, &body) {
		return
	}
	s.mutate(w, func() (map[string]any, error) {
		result, err := s.d.Recovery.Start(body)
		if err != nil {
			return nil, cerr.Validation("%s", err.Error())
		}
		return map[string]any{"started": result.Started, "plan": result.Plan, "message": result.Message}, nil
	})
}
