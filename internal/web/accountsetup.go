package web

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleAccountSetup(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if !decodeBody(w, r, &body) {
		return
	}
	if len(body) != 0 {
		writeError(w, http.StatusBadRequest, "account setup takes no arguments")
		return
	}
	if s.hub.requestAccountSetup() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Setup requested in the Windows tray. Complete sign-in in the terminal window."})
		return
	}
	if unavailable(w, s.d.AccountSetup != nil, "account setup") {
		return
	}
	if err := s.d.AccountSetup(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Account setup opened in a terminal. Choose Claude or Codex, then sign in with your browser."})
}

// One connected desktop receives the request; reconnects never replay it.
func (h *hub) requestAccountSetup() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, subscriber := range h.subs {
		if !subscriber.accountSetup {
			continue
		}
		select {
		case ch <- event{name: "account-setup", data: []byte(`{}`)}:
			return true
		default:
		}
	}
	return false
}
