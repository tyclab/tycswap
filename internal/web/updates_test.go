// Tests for the Updates surface (DESIGN A27): the state section, POST
// /api/updates/check and /api/updates/apply (auth and CSRF are covered for
// both by the allRoutes tables in auth_test.go), the one-apply-at-a-time
// rule, the serve loop's periodic check, Server.Refresh, and the markup the
// dashboard needs for the card and the header indicator.
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
)

const updTimeout = 5 * time.Second

func TestStateUpdatesNullWithoutFacade(t *testing.T) {
	h := newHarness(t, withNoUpdates())
	st := decodeJSON(t, h.get("/api/state"))
	if v, present := st["updates"]; !present || v != nil {
		t.Errorf("updates = %v (present %v), want null", v, present)
	}
	for _, p := range []string{"/api/updates/check", "/api/updates/apply"} {
		if resp := h.postJSON(p, map[string]string{"target": "app"}); resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s without a facade: %d, want 503", p, resp.StatusCode)
		}
	}
}

// What the last check could not read travels as optional fields; the lists
// a page iterates are never null.
func TestStateUpdatesView(t *testing.T) {
	h := newHarness(t)
	at := testNow.Add(-3 * time.Minute)
	h.upd.mu.Lock()
	h.upd.view = UpdatesView{
		Available: true, CheckedAt: &at,
		App:        &AppUpdateView{Current: "v0.4.0", Latest: "v0.5.0", Available: true, Hint: "git pull && make install"},
		ClaudeCode: &ClaudeCodeUpdateView{Installed: "2.1.0", Latest: "2.2.0", State: "update", Method: "the native installer", Command: "claude update", Available: true, Error: "HTTP 502"},
	}
	h.upd.mu.Unlock()
	st := decodeJSON(t, h.get("/api/state"))
	want := map[string]any{
		"available": true, "checking": false, "checkedAt": at.Format(time.RFC3339Nano),
		"app":        map[string]any{"current": "v0.4.0", "latest": "v0.5.0", "available": true, "hint": "git pull && make install"},
		"claudeCode": map[string]any{"installed": "2.1.0", "latest": "2.2.0", "state": "update", "method": "the native installer", "command": "claude update", "available": true, "error": "HTTP 502"},
	}
	if !reflect.DeepEqual(st["updates"], want) {
		t.Errorf("updates = %#v\nwant %#v", st["updates"], want)
	}
}

func TestUpdatesCheckStartsACheckAndBroadcasts(t *testing.T) {
	h := newHarness(t)
	sse := h.openSSE()
	defer sse.close()
	sse.nextState(t, updTimeout)                                           // initial
	waitFor(t, updTimeout, func() bool { return len(h.upd.Calls()) == 1 }) // Serve's own check at start
	before := 1
	h.upd.mu.Lock()
	h.upd.markChecking = true
	h.upd.mu.Unlock()

	resp := h.post("/api/updates/check")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", resp.StatusCode, readBody(t, resp))
	}
	if body := decodeJSON(t, resp); len(body) != 0 {
		t.Errorf("body = %v, want {}", body)
	}
	if got := h.upd.Calls(); len(got) != before+1 || got[len(got)-1] != "Check" {
		t.Errorf("calls = %v", got)
	}
	// The page shows "Checking…" from the state the route pushes.
	var doc struct {
		Updates *UpdatesView `json:"updates"`
	}
	if err := json.Unmarshal([]byte(sse.nextState(t, updTimeout).data), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Updates == nil || !doc.Updates.Checking {
		t.Errorf("state after check: updates = %+v, want checking", doc.Updates)
	}
}

func TestUpdatesCheckError(t *testing.T) {
	h := newHarness(t)
	h.upd.mu.Lock()
	h.upd.checkErr = cerr.Validation("nothing checks here")
	h.upd.mu.Unlock()
	resp := h.post("/api/updates/check")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	if msg := decodeJSON(t, resp)["error"]; msg != "nothing checks here" {
		t.Errorf("error = %q", msg)
	}
}

// The serve loop asks the facade to check once at start and on every update
// tick (A27: six hours by default), never on a poll tick.
func TestUpdatesPeriodicCheck(t *testing.T) {
	h := newHarness(t)
	waitFor(t, updTimeout, func() bool { return len(h.upd.Calls()) == 1 })
	h.fireTick()
	h.fireTick()
	if got := h.upd.Calls(); !reflect.DeepEqual(got, []string{"Check"}) {
		t.Fatalf("poll ticks checked: %v", got)
	}
	h.fireUpdateTick()
	waitFor(t, updTimeout, func() bool { return len(h.upd.Calls()) == 2 })
	h.fireUpdateTick()
	waitFor(t, updTimeout, func() bool { return len(h.upd.Calls()) == 3 })
	// A facade that refuses is logged, and the loop goes on.
	h.upd.mu.Lock()
	h.upd.checkErr = errFake
	h.upd.mu.Unlock()
	h.fireUpdateTick()
	waitFor(t, updTimeout, func() bool {
		for _, l := range h.Logs() {
			if strings.Contains(l, "updates check: boom") {
				return true
			}
		}
		return false
	})
	h.fireTick() // the loop is alive
}

// Without a facade the loop builds no update ticker and never checks.
func TestUpdatesNoFacadeNoTicker(t *testing.T) {
	built := false
	h := newHarness(t, withNoUpdates(), func(h *harness, d *Deps) {
		d.UpdateTicker = func(time.Duration) (<-chan time.Time, func()) { built = true; return h.updTick, func() {} }
	})
	h.fireTick()
	if built {
		t.Error("an update ticker was built without a facade")
	}
}

// Each target reaches the facade as it is, and the answer is the facade's
// UpdateResult itself (not wrapped), followed by a fresh state.
func TestUpdatesApplyEachTarget(t *testing.T) {
	for _, target := range []string{"app", "claude-code"} {
		t.Run(target, func(t *testing.T) {
			h := newHarness(t)
			h.upd.mu.Lock()
			h.upd.result = UpdateResult{Message: "Done.", Output: "line 1\nline 2"}
			h.upd.mu.Unlock()
			sse := h.openSSE()
			defer sse.close()
			sse.nextState(t, updTimeout)

			resp := h.postJSON("/api/updates/apply", map[string]string{"target": target})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
			}
			got := decodeJSON(t, resp)
			if !reflect.DeepEqual(got, map[string]any{"message": "Done.", "output": "line 1\nline 2"}) {
				t.Errorf("body = %v", got)
			}
			calls := h.upd.Calls()
			if calls[len(calls)-1] != "Apply("+target+")" {
				t.Errorf("calls = %v", calls)
			}
			sse.nextState(t, updTimeout) // the broadcast after the apply
		})
	}
}

func TestUpdatesApplyBadTarget(t *testing.T) {
	h := newHarness(t)
	for _, body := range []any{map[string]string{"target": "plugins"}, map[string]string{"target": ""}, map[string]string{}, "not json"} {
		resp := h.postJSON("/api/updates/apply", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", body, resp.StatusCode)
		}
	}
	for _, c := range h.upd.Calls() {
		if strings.HasPrefix(c, "Apply") {
			t.Errorf("a bad target reached the facade: %v", h.upd.Calls())
		}
	}
}

// A failed apply maps its error by kind and carries the output along.
func TestUpdatesApplyErrorKeepsOutput(t *testing.T) {
	h := newHarness(t)
	h.upd.mu.Lock()
	h.upd.applyErr = cerr.Validation("go install failed")
	h.upd.result = UpdateResult{Output: "go: module not found"}
	h.upd.mu.Unlock()
	resp := h.postJSON("/api/updates/apply", map[string]string{"target": "app"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	got := decodeJSON(t, resp)
	if got["error"] != "go install failed" || got["output"] != "go: module not found" {
		t.Errorf("body = %v", got)
	}
	// The log names the target and the status, never what the installer
	// printed (the error is its last line).
	for _, l := range h.Logs() {
		if strings.Contains(l, "go install failed") || strings.Contains(l, "module not found") {
			t.Errorf("the log carries the installer's output: %q", l)
		}
	}
	if logs := strings.Join(h.Logs(), "\n"); !strings.Contains(logs, "web: update app failed (HTTP 400)") {
		t.Errorf("logs = %q", logs)
	}
	// A plain error is a 500 and an empty output is omitted.
	h.upd.mu.Lock()
	h.upd.applyErr, h.upd.result = errFake, UpdateResult{}
	h.upd.mu.Unlock()
	resp = h.postJSON("/api/updates/apply", map[string]string{"target": "app"})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", resp.StatusCode)
	}
	if got := decodeJSON(t, resp); got["output"] != nil {
		t.Errorf("empty output present: %v", got)
	}
}

// One apply at a time: a second while one runs is a 409, and the slot is
// free again once the first returns.
func TestUpdatesApplyConcurrentIsConflict(t *testing.T) {
	h := newHarness(t)
	gate := make(chan struct{})
	h.upd.mu.Lock()
	h.upd.gate = gate
	h.upd.result = UpdateResult{Message: "ok"}
	h.upd.mu.Unlock()
	first := make(chan *http.Response, 1)
	go func() { first <- h.postJSON("/api/updates/apply", map[string]string{"target": "app"}) }()
	waitFor(t, updTimeout, func() bool {
		for _, c := range h.upd.Calls() {
			if c == "Apply(app)" {
				return true
			}
		}
		return false
	})
	if resp := h.postJSON("/api/updates/apply", map[string]string{"target": "claude-code"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second apply: %d, want 409", resp.StatusCode)
	}
	close(gate)
	select {
	case resp := <-first:
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("first apply: %d", resp.StatusCode)
		}
	case <-time.After(updTimeout):
		t.Fatal("first apply did not return")
	}
	h.upd.mu.Lock()
	h.upd.gate = nil
	h.upd.mu.Unlock()
	if resp := h.postJSON("/api/updates/apply", map[string]string{"target": "claude-code"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply after the first finished: %d, want 200", resp.StatusCode)
	}
}

func TestUpdatesRoutesArePostOnly(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/api/updates/check", "/api/updates/apply"} {
		if resp := h.get(p); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: %d, want 405", p, resp.StatusCode)
		}
	}
}

// Refresh pushes a state at once, coalesces a burst and never blocks.
func TestRefreshBroadcasts(t *testing.T) {
	h := newHarness(t)
	sse := h.openSSE()
	defer sse.close()
	sse.nextState(t, updTimeout) // initial

	h.upd.mu.Lock()
	h.upd.view = UpdatesView{Available: true, App: &AppUpdateView{Current: "v0.4.0", Latest: "v0.5.0", Available: true}}
	h.upd.mu.Unlock()
	h.s.Refresh()
	var doc struct {
		Updates *UpdatesView `json:"updates"`
	}
	if err := json.Unmarshal([]byte(sse.nextState(t, updTimeout).data), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Updates == nil || !doc.Updates.Available {
		t.Errorf("refreshed state: updates = %+v, want the new view", doc.Updates)
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.s.Refresh()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(updTimeout):
		t.Fatal("Refresh blocked its caller")
	}
}

// Before Serve the request waits for the loop; after Serve it is a no-op.
// Neither blocks.
func TestRefreshBeforeAndAfterServe(t *testing.T) {
	tick := make(chan time.Time)
	s, err := New(Deps{
		Facade:        &fakeFacade{snap: sampleSnapshot()},
		Sessions:      func() SessionsView { return SessionsView{} },
		SessionTitle:  func(string, string, string) string { return "" },
		Clock:         clock.NewFake(testNow),
		Rand:          fixedRand(),
		Ticker:        func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} },
		AuthOverrides: func() AuthOverridesView { return AuthOverridesView{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		s.Refresh()
	}
	if _, err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()
	// The pending refresh is consumed by the loop's first turn.
	waitFor(t, updTimeout, func() bool { return len(s.refresh) == 0 })
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(updTimeout):
		t.Fatal("Serve did not return")
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 3; i++ {
			s.Refresh()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(updTimeout):
		t.Fatal("Refresh after Serve blocked")
	}
}

// The card, the quiet line, the header indicator and the client code that
// drives them.
func TestIndexHTML_UpdatesMarkup(t *testing.T) {
	index := staticFile(t, "index.html")
	for _, id := range []string{"hdr-updates", "hdr-updates-count", "hdr-updates-text", "updates", "updates-card", "updates-title", "updates-checked", "updates-list", "updates-ok", "updates-ok-text", "updates-ok-when", "updates-errors", "updates-result", "updates-result-text", "updates-result-more", "updates-result-output"} {
		if !strings.Contains(index, `id="`+id+`"`) {
			t.Errorf("element #%s missing", id)
		}
	}
	for _, action := range []string{"updates-show", "updates-check", "updates-dismiss"} {
		if !strings.Contains(index, `data-action="`+action+`"`) {
			t.Errorf("action %q missing", action)
		}
	}
	js := staticFile(t, "app.js")
	for _, needle := range []string{"function renderUpdates(", "function renderUpdatesBadge(", "function updateCount(", "'updates-apply'", "/api/updates/check", "/api/updates/apply", "updApplying", "'claude-code'", "Could not check for every update", "Not checked for updates yet", "Everything is up to date"} {
		if !strings.Contains(js, needle) {
			t.Errorf("app.js lacks %q", needle)
		}
	}
	css := staticFile(t, "style.css")
	for _, needle := range []string{".hdr-upd", ".upd-card", ".upd-item", "@keyframes upd-pulse", "prefers-reduced-motion: reduce) { .hdr-upd"} {
		if !strings.Contains(css, needle) {
			t.Errorf("style.css lacks %q", needle)
		}
	}
}
