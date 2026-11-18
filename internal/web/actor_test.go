package web

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// actionLines collects what the server hands Deps.ActionLog.
type actionLines struct {
	mu    sync.Mutex
	lines []string
}

func (a *actionLines) option() option {
	return func(h *harness, d *Deps) {
		d.ActionLog = func(change, actor string) {
			a.mu.Lock()
			a.lines = append(a.lines, change+" by "+actor)
			a.mu.Unlock()
		}
	}
}

func (a *actionLines) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.lines
	a.lines = nil
	return out
}

// Every change made through the API names the dashboard, how the request
// authenticated and its remote address, once and only after it succeeded;
// a refused or idle request names nobody.
func TestDashboardActionsNameTheirActor(t *testing.T) {
	var got actionLines
	h := newHarness(t, got.option())
	const actor = " by dashboard(cookie,127.0.0.1:"
	for _, step := range []struct {
		name string
		do   func() *http.Response
		want string // "" for no line
	}{
		{"disable", func() *http.Response { return h.post("/api/accounts/claude:1/disable") }, "Disabled account 1" + actor},
		{"enable", func() *http.Response { return h.post("/api/accounts/claude:2/enable") }, "Enabled account 2" + actor},
		{"switch", func() *http.Response { return h.post("/api/switch/claude:2") }, "Switched to account 2" + actor},
		{"set", func() *http.Response {
			return h.postJSON("/api/settings/autoswitch.strategy", map[string]any{"value": "best"})
		}, "Set autoswitch.strategy=best" + actor},
		{"unset", func() *http.Response { return h.post("/api/settings/autoswitch.strategy/unset") }, "Unset autoswitch.strategy" + actor},
		{"start", func() *http.Response { return h.postJSON("/api/auto/start", map[string]any{}) }, "Set autoswitch.enabled=true" + actor},
		{"stop", func() *http.Response { return h.post("/api/auto/stop") }, "Set autoswitch.enabled=false" + actor},
		{"set alias", func() *http.Response {
			return h.postJSON("/api/accounts/claude:2/alias", map[string]any{"alias": "Team"})
		}, "Set alias 'team' for account 2" + actor},
		{"remove alias", func() *http.Response { return h.post("/api/accounts/claude:1/alias") }, "Removed alias for account 1" + actor},
	} {
		resp := step.do()
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", step.name, resp.StatusCode)
		}
		lines := got.take()
		if step.want == "" {
			if len(lines) != 0 {
				t.Errorf("%s: logged %q, want nothing", step.name, lines)
			}
			continue
		}
		if len(lines) != 1 || !strings.HasPrefix(lines[0], step.want) || !strings.HasSuffix(lines[0], ")") {
			t.Errorf("%s: logged %q, want one line starting %q", step.name, lines, step.want)
		}
	}

	h.fa.mu.Lock()
	h.fa.disableErr = errors.New("roster locked")
	h.fa.switchPayload = map[string]any{"switched": false, "reason": "already-active"}
	h.fa.mu.Unlock()
	h.set.mu.Lock()
	h.set.unsetOK = false
	h.set.mu.Unlock()
	h.post("/api/accounts/claude:1/disable").Body.Close()
	h.post("/api/switch/claude:1").Body.Close()
	h.post("/api/settings/autoswitch.strategy/unset").Body.Close()
	if lines := got.take(); len(lines) != 0 {
		t.Errorf("a refused disable, an idle switch and a no-op unset logged %q", lines)
	}

	// Account 1's alias is "work" and account 2 has none in the snapshot.
	h.postJSON("/api/accounts/claude:1/alias", map[string]any{"alias": "Work"}).Body.Close()
	h.postJSON("/api/accounts/claude:2/alias", map[string]any{"alias": " "}).Body.Close()
	h.ops.mu.Lock()
	h.ops.errs["SetAlias"] = errors.New("roster locked")
	h.ops.mu.Unlock()
	h.postJSON("/api/accounts/claude:2/alias", map[string]any{"alias": "x"}).Body.Close()
	if lines := got.take(); len(lines) != 0 {
		t.Errorf("an unchanged alias, removing none and a refused alias logged %q", lines)
	}
}
