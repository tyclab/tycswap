package web

import (
	"net/http"
	"strings"
	"testing"
)

// The handover after a limit is removed (DESIGN A95): the page has no card,
// no Continue button and no Guide text for it, and its routes are gone.
func TestHandoverIsGoneFromTheDashboard(t *testing.T) {
	h := newHarness(t)
	resp := h.do(h.withCookie(h.newReq(http.MethodGet, "/", nil)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index status %d", resp.StatusCode)
	}
	page := string(readBody(t, resp))
	script := string(readBody(t, h.get("/static/app.js")))
	for _, gone := range []string{"Continue after a limit", "recovery-card", "Continue in the other tool", "recovery reclaim", "handoverWaitMinutes", "handover"} {
		if strings.Contains(page, gone) {
			t.Errorf("page still has %q", gone)
		}
	}
	for _, gone := range []string{"/api/recovery", "Continue in Codex", "renderRecovery", "handover"} {
		if strings.Contains(script, gone) {
			t.Errorf("app.js still has %q", gone)
		}
	}
	for _, route := range []string{"/api/recovery/prepare", "/api/recovery/dismiss", "/api/recovery/start"} {
		if got := h.postJSON(route, map[string]any{}).StatusCode; got != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", route, got)
		}
	}
	st := decodeJSON(t, h.get("/api/state"))
	if _, ok := st["recovery"]; ok {
		t.Error("state document still has recovery")
	}
}
