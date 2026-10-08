// uiprefs.go: the dashboard port changes each start and browser storage is per origin, so the server keeps folded cards.

package web

import "net/http"

// UIPrefs stores the view choices.
type UIPrefs interface {
	Folded() map[string]bool
	SetFolded(card string, folded bool) error
}

// UIView is the state's ui section: the folded cards, never null.
type UIView struct {
	Folded map[string]bool `json:"folded"`
}

// foldable are the cards that fold: the page's ids, and nothing else is
// stored.
var foldable = map[string]bool{"accounts-card": true, "updates-card": true}

func (s *Server) handleFolded(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.UIPrefs != nil, "view preferences") {
		return
	}
	var b struct {
		Card   string `json:"card"`
		Folded bool   `json:"folded"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	if !foldable[b.Card] {
		writeError(w, http.StatusBadRequest, "unknown card")
		return
	}
	if err := s.d.UIPrefs.SetFolded(b.Card, b.Folded); err != nil {
		s.d.Logger("web: fold " + b.Card + ": " + err.Error())
		writeError(w, statusFor(err), err.Error())
		return
	}
	s.broadcast() // other open pages fold too
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// uiView is the prefs as the state carries them: only the folded cards the
// page knows, each true.
func (s *Server) uiView() *UIView {
	v := &UIView{Folded: map[string]bool{}}
	for card, on := range s.d.UIPrefs.Folded() {
		if on && foldable[card] {
			v.Folded[card] = true
		}
	}
	return v
}
