// Tests for the settings and directory-mapping routes: listing, Set with
// string / number / bool values, Unset via DELETE and POST, path-required
// mapping rules, error mapping and nil-façade 503s.
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
)

func TestSettingsList_GET(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/api/settings")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body struct {
		Settings []SettingView `json:"settings"`
	}
	if err := json.Unmarshal(readBody(t, resp), &body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(canon(t, body.Settings), canon(t, sampleSettings())) {
		t.Fatalf("settings %+v", body.Settings)
	}
	// nil slice from the facade → [] not null
	h.set.mu.Lock()
	h.set.views = nil
	h.set.mu.Unlock()
	var raw map[string]any
	_ = json.Unmarshal(readBody(t, h.get("/api/settings")), &raw)
	if l, ok := raw["settings"].([]any); !ok || len(l) != 0 {
		t.Fatalf("settings = %v, want []", raw["settings"])
	}
}

func TestSettingSet_ValueTypes(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		value any
		want  string
	}{
		{"soonest-reset", "soonest-reset"},
		{80, "80"},
		{72.5, "72.5"},
		{true, "true"},
		{false, "false"},
		{"", ""},
	}
	var want []string
	for _, tc := range cases {
		resp := h.postJSON("/api/settings/autoswitch.threshold", map[string]any{"value": tc.value})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("value %v: status %d: %s", tc.value, resp.StatusCode, readBody(t, resp))
		}
		res, _ := decodeJSON(t, resp)["result"].(map[string]any)
		if res["key"] != "autoswitch.threshold" || res["value"] != tc.want {
			t.Errorf("value %v: result %v", tc.value, res)
		}
		want = append(want, "Set(autoswitch.threshold,"+tc.want+")")
	}
	if got := h.set.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

func TestSettingSet_ValueRequired400(t *testing.T) {
	h := newHarness(t)
	for _, body := range []any{nil, map[string]any{}, map[string]any{"value": nil}, map[string]any{"other": 1}} {
		if resp := h.postJSON("/api/settings/autoswitch.threshold", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %v: status %d", body, resp.StatusCode)
		}
	}
	if len(h.set.Calls()) != 0 {
		t.Fatal("Set reached without value")
	}
}

func TestSettingSet_ErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{cerr.Validation("threshold must be 1-100"), http.StatusBadRequest},
		{cerr.Config("settings.json unreadable"), http.StatusInternalServerError},
		{errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		h := newHarness(t)
		h.set.mu.Lock()
		h.set.setErr = tc.err
		h.set.mu.Unlock()
		resp := h.postJSON("/api/settings/autoswitch.threshold", map[string]any{"value": "x"})
		if resp.StatusCode != tc.want {
			t.Errorf("%v: status %d, want %d", tc.err, resp.StatusCode, tc.want)
		}
		if msg := decodeError(t, resp); msg != tc.err.Error() {
			t.Errorf("error %q", msg)
		}
	}
}

func TestSettingUnset_DeleteAndPost(t *testing.T) {
	h := newHarness(t)
	resp := h.send(http.MethodDelete, "/api/settings/autoswitch.strategy", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	res, _ := decodeJSON(t, resp)["result"].(map[string]any)
	if res["key"] != "autoswitch.strategy" || res["removed"] != true {
		t.Errorf("result %v", res)
	}
	h.set.mu.Lock()
	h.set.unsetOK = false
	h.set.mu.Unlock()
	resp = h.post("/api/settings/autoswitch.strategy/unset")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST unset status %d", resp.StatusCode)
	}
	res, _ = decodeJSON(t, resp)["result"].(map[string]any)
	if res["removed"] != false {
		t.Errorf("result %v", res)
	}
	if got := h.set.Calls(); !reflect.DeepEqual(got, []string{"Unset(autoswitch.strategy)", "Unset(autoswitch.strategy)"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestSettingUnset_Error(t *testing.T) {
	h := newHarness(t)
	h.set.mu.Lock()
	h.set.unsetErr = cerr.Validation("unknown key")
	h.set.mu.Unlock()
	if resp := h.send(http.MethodDelete, "/api/settings/nope.key", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSettings_Nil503(t *testing.T) {
	h := newHarness(t, withNoSettings())
	routes := []struct{ method, path string }{
		{"GET", "/api/settings"}, {"POST", "/api/settings/k"}, {"DELETE", "/api/settings/k"}, {"POST", "/api/settings/k/unset"},
	}
	for _, r := range routes {
		resp := h.send(r.method, r.path, map[string]any{"value": "1"})
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status %d, want 503", r.method, r.path, resp.StatusCode)
		}
		decodeError(t, resp)
	}
	var st map[string]any
	_ = json.Unmarshal(readBody(t, h.get("/api/state")), &st)
	if st["settings"] != nil {
		t.Fatalf("state.settings = %v, want null", st["settings"])
	}
}

func TestSettingSet_BroadcastsState(t *testing.T) {
	h := newHarness(t)
	st := h.openSSE()
	defer st.close()
	st.nextState(t, timeout)
	h.clk.Advance(time.Second)
	h.postJSON("/api/settings/autoswitch.threshold", map[string]any{"value": 50})
	ev := st.nextState(t, timeout)
	var doc map[string]any
	_ = json.Unmarshal([]byte(ev.data), &doc)
	if doc["serverTime"] != "2026-09-19T10:00:01Z" {
		t.Fatalf("no fresh state after Set: %v", doc["serverTime"])
	}
}

// -- mappings ----------------------------------------------------------------
