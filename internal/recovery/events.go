package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const MaxEventBytes = 1024 * 1024

type Event struct {
	Provider        string    `json:"provider"`
	AccountID       string    `json:"account_id,omitempty"`
	AccountIdentity string    `json:"account_identity,omitempty"`
	SourcePID       int       `json:"source_pid,omitempty"`
	SessionID       string    `json:"session_id"`
	IncidentID      string    `json:"incident_id,omitempty"`
	Group           string    `json:"group,omitempty"`
	Model           string    `json:"model,omitempty"`
	ModelObserved   bool      `json:"model_observed,omitempty"`
	Kind            string    `json:"kind"`
	Error           string    `json:"error,omitempty"`
	Stopped         bool      `json:"stopped"`
	At              time.Time `json:"at"`
	TranscriptPath  string    `json:"transcript_path,omitempty"`
	CWD             string    `json:"cwd,omitempty"`
	ProfileDir      string    `json:"profile_dir,omitempty"`
}

func ParseClaude(data []byte, group, model string, now time.Time) (Event, error) {
	if len(data) > MaxEventBytes {
		return Event{}, errors.New("hook payload exceeds limit")
	}
	var p struct {
		Kind       string `json:"hook_event_name"`
		SessionID  string `json:"session_id"`
		Model      string `json:"model"`
		Error      string `json:"error"`
		Transcript string `json:"transcript_path"`
		CWD        string `json:"cwd"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return Event{}, err
	}
	if p.SessionID == "" {
		return Event{}, errors.New("hook session_id is required")
	}
	switch p.Kind {
	case "SessionStart", "UserPromptSubmit", "Stop", "StopFailure", "SessionEnd":
	default:
		return Event{}, fmt.Errorf("unsupported Claude hook %q", p.Kind)
	}
	if p.Model != "" {
		model = p.Model
	}
	e := Event{Provider: "claude", SessionID: p.SessionID, Group: group, Model: model, Kind: p.Kind, ModelObserved: p.Model != "", At: now, TranscriptPath: p.Transcript, CWD: p.CWD}
	if p.Kind == "StopFailure" {
		switch p.Error {
		case "rate_limit", "overloaded", "authentication_failed", "oauth_org_not_allowed", "account_on_hold", "billing_error", "invalid_request", "model_not_found", "server_error", "max_output_tokens", "cloud_credential_error":
			e.Error = p.Error
		default:
			e.Error = "unknown"
		}
		e.Stopped = true
	}
	return e, nil
}

func ParseCodex(data []byte, model string, now time.Time) (Event, error) {
	if len(data) > MaxEventBytes {
		return Event{}, errors.New("event exceeds limit")
	}
	var n struct {
		Method string `json:"method"`
		Params struct {
			ThreadID string `json:"threadId"`
			Thread   struct {
				ID    string `json:"id"`
				CWD   string `json:"cwd"`
				Model string `json:"model"`
				Path  string `json:"path"`
			} `json:"thread"`
			TurnID    string `json:"turnId"`
			WillRetry bool   `json:"willRetry"`
			Turn      struct {
				ID     string          `json:"id"`
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
			} `json:"turn"`
			Error json.RawMessage `json:"error"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &n); err != nil {
		return Event{}, err
	}
	if n.Method == "thread/started" {
		n.Params.ThreadID = n.Params.Thread.ID
		if n.Params.Thread.Model != "" {
			model = n.Params.Thread.Model
		}
	}
	if n.Params.ThreadID == "" {
		return Event{}, errors.New("app-server threadId is required")
	}
	e := Event{Provider: "codex", SessionID: n.Params.ThreadID, Group: "codex", Model: model, ModelObserved: n.Method == "thread/started" && n.Params.Thread.Model != "", Kind: n.Method, At: now}
	switch n.Method {
	case "thread/started":
		e.CWD = n.Params.Thread.CWD
		e.TranscriptPath = n.Params.Thread.Path
	case "turn/started":
		e.IncidentID = n.Params.Turn.ID
	case "turn/completed":
		e.IncidentID = n.Params.Turn.ID
		e.Stopped = n.Params.Turn.Status == "failed"
		if e.Stopped && len(n.Params.Turn.Error) > 0 && string(n.Params.Turn.Error) != "null" {
			e.Error = codexError(n.Params.Turn.Error)
		}
	case "error":
		e.IncidentID = n.Params.TurnID
		e.Stopped = false
		if !n.Params.WillRetry {
			e.Error = codexError(n.Params.Error)
		}
	default:
		return Event{}, fmt.Errorf("unsupported app-server notification %q", n.Method)
	}
	if e.IncidentID == "" && n.Method != "thread/started" {
		return Event{}, errors.New("app-server turn ID is required")
	}
	return e, nil
}

func codexError(raw json.RawMessage) string {
	var p struct {
		Info json.RawMessage `json:"codexErrorInfo"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return "unknown"
	}
	var code string
	if json.Unmarshal(p.Info, &code) != nil {
		var variants map[string]json.RawMessage
		if json.Unmarshal(p.Info, &variants) == nil && len(variants) == 1 {
			for key := range variants {
				code = key
			}
		}
	}
	if code == "usageLimitExceeded" || code == "UsageLimitExceeded" {
		return "usage_limit"
	}
	if code == "rateLimitExceeded" || code == "RateLimitExceeded" {
		return "rate_limit"
	}
	return "unknown"
}

func incident(session string, seq uint64) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", session, seq)))
	return hex.EncodeToString(h[:12])
}
