// Tests for the mutating routes: each one reaches the right façade method
// with the right arguments, errors map to the A25 status table, and the stop
// route only ever signals a PID procdetect currently lists.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/procdetect"
)

var errFake = errors.New("boom")

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(readBody(t, resp), &out); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return out
}

func TestSwitch_CallsFacadeAndReturnsPayload(t *testing.T) {
	h := newHarness(t)
	resp := h.post("/api/switch/claude:2")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	body := decodeJSON(t, resp)
	if body["ok"] != true {
		t.Errorf("ok = %v", body["ok"])
	}
	if !reflect.DeepEqual(body["result"], map[string]any{"switched": true}) {
		t.Errorf("result = %v", body["result"])
	}
	if got := h.fa.Calls(); !reflect.DeepEqual(got, []string{"SwitchTo(2,true)"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestSwitch_AliasIDPassesThrough(t *testing.T) {
	h := newHarness(t)
	h.post("/api/switch/claude:work")
	if got := h.fa.Calls(); !reflect.DeepEqual(got, []string{"SwitchTo(work,true)"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestSwitch_ErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", cerr.AccountNotFound("Account 9 not found"), http.StatusNotFound},
		{"validation", cerr.Validation("bad id"), http.StatusBadRequest},
		{"lock", cerr.Lock("store locked"), http.StatusConflict},
		{"cc lock timeout", cerr.ClaudeCodeLockTimeout("claude code holds the lock"), http.StatusLocked},
		{"config", cerr.Config("broken config"), http.StatusInternalServerError},
		{"switch", cerr.Switch("cannot switch"), http.StatusInternalServerError},
		{"credential", cerr.CredentialRead("keychain"), http.StatusInternalServerError},
		{"plain", errFake, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.fa.mu.Lock()
			h.fa.switchErr = tc.err
			h.fa.mu.Unlock()
			resp := h.post("/api/switch/claude:9")
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
			if msg := decodeError(t, resp); msg != tc.err.Error() {
				t.Fatalf("error %q, want %q", msg, tc.err.Error())
			}
		})
	}
}

func TestDisableEnable_CallFacade(t *testing.T) {
	h := newHarness(t)
	if resp := h.post("/api/accounts/claude:1/disable"); resp.StatusCode != http.StatusOK {
		t.Fatalf("disable status %d", resp.StatusCode)
	}
	if resp := h.post("/api/accounts/claude:2/enable"); resp.StatusCode != http.StatusOK {
		t.Fatalf("enable status %d", resp.StatusCode)
	}
	want := []string{"SetAccountDisabled(1,true)", "SetAccountDisabled(2,false)"}
	if got := h.fa.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

func TestDisableEnable_ErrorMapping(t *testing.T) {
	h := newHarness(t)
	h.fa.mu.Lock()
	h.fa.disableErr = cerr.AccountNotFound("Account 42 not found")
	h.fa.mu.Unlock()
	resp := h.post("/api/accounts/claude:42/disable")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if msg := decodeError(t, resp); msg != "Account 42 not found" {
		t.Fatalf("error %q", msg)
	}
	h.fa.mu.Lock()
	h.fa.disableErr = cerr.Lock("locked")
	h.fa.mu.Unlock()
	if resp := h.post("/api/accounts/claude:1/enable"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("lock status %d, want 409", resp.StatusCode)
	}
}

func TestAccounts_UnknownActionIs404(t *testing.T) {
	h := newHarness(t)
	if resp := h.post("/api/accounts/claude:1/explode"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
	if len(h.fa.Calls()) != 0 {
		t.Fatal("facade called for unknown action")
	}
}

func TestStop_UnlistedPID404_NeverKills(t *testing.T) {
	h := newHarness(t)
	for _, pid := range []string{"1", "2", "99999", "4241"} {
		resp := h.post("/api/sessions/" + pid + "/stop")
		if pid == "1" {
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("pid %s: status %d, want 400", pid, resp.StatusCode)
			}
		} else if resp.StatusCode != http.StatusNotFound {
			t.Errorf("pid %s: status %d, want 404", pid, resp.StatusCode)
		}
		decodeError(t, resp)
	}
	if got := h.Killed(); len(got) != 0 {
		t.Fatalf("Kill called for unlisted pid: %v", got)
	}
}

func TestStop_IDEPIDIsNotASession(t *testing.T) {
	// 5555 is an IDE instance's PID, not a Claude session — must be refused.
	h := newHarness(t)
	if resp := h.post("/api/sessions/5555/stop"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
	if len(h.Killed()) != 0 {
		t.Fatal("IDE pid was killed")
	}
}

func TestStop_BadPID400(t *testing.T) {
	h := newHarness(t)
	for _, pid := range []string{"abc", "-5", "0", "4242.0", "%20"} {
		resp := h.post("/api/sessions/" + pid + "/stop")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("pid %q: status %d, want 400", pid, resp.StatusCode)
		}
	}
	if len(h.Killed()) != 0 {
		t.Fatal("Kill called for a bad pid")
	}
}

func TestStop_ListedPID_CallsKill(t *testing.T) {
	h := newHarness(t)
	resp := h.post("/api/sessions/4242/stop")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	body := decodeJSON(t, resp)
	res, _ := body["result"].(map[string]any)
	if res["pid"] != float64(4242) || res["signal"] != stopSignalName { // SIGTERM; "terminate" on Windows
		t.Errorf("result %v", body["result"])
	}
	if got := h.Killed(); !reflect.DeepEqual(got, []int{4242}) {
		t.Fatalf("killed %v, want [4242]", got)
	}
	// Kill is handed the listed session's startedAt, so it can verify the
	// process before signalling it.
	h.mu.Lock()
	at := append([]int64(nil), h.killedAt...)
	h.mu.Unlock()
	if !reflect.DeepEqual(at, []int64{1758276000000}) {
		t.Fatalf("Kill got startedAt %v, want the listed session's", at)
	}
}

// A PID whose process is not the one the session file describes (reused
// after a crash, or unverifiable) answers 409 and is reported, not signalled
// further.
func TestStop_NotTheProcess409(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.killErr = fmt.Errorf("%w: it started at 2026-09-19T11:00:00Z, the session file says 2026-09-19T10:00:00Z", ErrNotTheProcess)
	h.mu.Unlock()
	resp := h.post("/api/sessions/4242/stop")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409", resp.StatusCode)
	}
	if msg := decodeError(t, resp); !strings.Contains(msg, "not stopping pid 4242") || !strings.Contains(msg, "another process") {
		t.Fatalf("error %q", msg)
	}
}

func TestStop_ReprobesSessionsPerRequest(t *testing.T) {
	// The listing is consulted at request time, not from a cached state.
	h := newHarness(t)
	h.setSessions(SessionsView{Claude: []procdetect.ClaudeSession{{PID: 777}}})
	if resp := h.post("/api/sessions/4242/stop"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stale pid: status %d, want 404", resp.StatusCode)
	}
	if resp := h.post("/api/sessions/777/stop"); resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh pid: status %d", resp.StatusCode)
	}
	if got := h.Killed(); !reflect.DeepEqual(got, []int{777}) {
		t.Fatalf("killed %v", got)
	}
}

func TestStop_KillError500(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.killErr = errors.New("operation not permitted")
	h.mu.Unlock()
	resp := h.post("/api/sessions/4242/stop")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if msg := decodeError(t, resp); msg != "operation not permitted" {
		t.Fatalf("error %q", msg)
	}
}

func TestMutations_LogErrors(t *testing.T) {
	h := newHarness(t)
	h.fa.mu.Lock()
	h.fa.switchErr = cerr.Switch("no such thing")
	h.fa.mu.Unlock()
	h.post("/api/switch/claude:9")
	h.mu.Lock()
	defer h.mu.Unlock()
	found := false
	for _, l := range h.logs {
		if l == "web: no such thing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("error not logged: %v", h.logs)
	}
}
