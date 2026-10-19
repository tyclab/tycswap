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
	section := "add"
	if raw, ok := body["section"]; ok {
		section = ""
		if json.Unmarshal(raw, &section) != nil {
			writeError(w, http.StatusBadRequest, "invalid menu section")
			return
		}
		delete(body, "section")
	}
	if len(body) != 0 || !ValidMenuSection(section) {
		writeError(w, http.StatusBadRequest, "choose add, manage, groups, transfer, or main")
		return
	}
	if s.hub.requestAccountSetup(section) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Menu requested in the tray. Continue in the terminal window."})
		return
	}
	if unavailable(w, s.d.AccountSetup != nil, "account setup") {
		return
	}
	if err := s.d.AccountSetup(section); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Interactive menu opened in a terminal."})
}

func ValidMenuSection(section string) bool {
	switch section {
	case "main", "add", "manage", "groups", "transfer":
		return true
	}
	return false
}

// One connected desktop receives the request; reconnects never replay it.
func (h *hub) requestAccountSetup(section string) bool {
	data, _ := json.Marshal(map[string]string{"section": section})
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, subscriber := range h.subs {
		if !subscriber.accountSetup {
			continue
		}
		select {
		case ch <- event{name: "account-setup", data: data}:
			return true
		default:
		}
	}
	return false
}
