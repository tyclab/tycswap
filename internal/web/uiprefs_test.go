// Tests for the page's remembered view choices (DESIGN A27): state.ui and
// POST /api/ui/folded.
package web

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestUIFoldedRoundTrip(t *testing.T) {
	h := newHarness(t)
	st := decodeJSON(t, h.get("/api/state"))
	if !reflect.DeepEqual(st["ui"], map[string]any{"folded": map[string]any{}}) {
		t.Fatalf("ui = %v, want an empty folded map", st["ui"])
	}
	sse := h.openSSE()
	defer sse.close()
	sse.nextState(t, updTimeout)

	resp := h.postJSON("/api/ui/folded", map[string]any{"card": "accounts-card", "folded": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if got := decodeJSON(t, resp); got["ok"] != true {
		t.Errorf("body = %v", got)
	}
	if got := h.prefs.Calls(); !reflect.DeepEqual(got, []string{"SetFolded(accounts-card,true)"}) {
		t.Errorf("calls = %v", got)
	}
	// Other open pages fold too: the route broadcasts.
	var doc struct {
		UI *UIView `json:"ui"`
	}
	if err := json.Unmarshal([]byte(sse.nextState(t, updTimeout).data), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.UI == nil || !doc.UI.Folded["accounts-card"] {
		t.Errorf("state after fold: %+v", doc.UI)
	}
	h.postJSON("/api/ui/folded", map[string]any{"card": "updates-card", "folded": true})
	h.postJSON("/api/ui/folded", map[string]any{"card": "accounts-card", "folded": false})
	st = decodeJSON(t, h.get("/api/state"))
	if !reflect.DeepEqual(st["ui"], map[string]any{"folded": map[string]any{"updates-card": true}}) {
		t.Errorf("ui = %v", st["ui"])
	}
}

// Only the page's two cards are stored; anything else is a 400 and never
// reaches the prefs. A card the file names that the page does not know is
// not carried into the state either.
func TestUIFoldedUnknownCard(t *testing.T) {
	h := newHarness(t)
	for _, body := range []any{map[string]any{"card": "sessions-card", "folded": true}, map[string]any{"folded": true}, "nope"} {
		if resp := h.postJSON("/api/ui/folded", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", body, resp.StatusCode)
		}
	}
	if got := h.prefs.Calls(); len(got) != 0 {
		t.Errorf("prefs called: %v", got)
	}
	h.prefs.mu.Lock()
	h.prefs.folded["plugins-card"] = true
	h.prefs.mu.Unlock()
	st := decodeJSON(t, h.get("/api/state"))
	if !reflect.DeepEqual(st["ui"], map[string]any{"folded": map[string]any{}}) {
		t.Errorf("ui carries an unknown card: %v", st["ui"])
	}
}

func TestUIFoldedErrorsAndNull(t *testing.T) {
	h := newHarness(t)
	h.prefs.mu.Lock()
	h.prefs.setErr = errFake
	h.prefs.mu.Unlock()
	if resp := h.postJSON("/api/ui/folded", map[string]any{"card": "accounts-card", "folded": true}); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("prefs error: %d, want 500", resp.StatusCode)
	}
	h2 := newHarness(t, withNoUIPrefs())
	if v, present := decodeJSON(t, h2.get("/api/state"))["ui"]; !present || v != nil {
		t.Errorf("ui without prefs = %v, want null", v)
	}
	if resp := h2.postJSON("/api/ui/folded", map[string]any{"card": "accounts-card", "folded": true}); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("fold without prefs: %d, want 503", resp.StatusCode)
	}
}
