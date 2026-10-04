// Tests for the settings and directory-mapping routes: listing, Set with
// string / number / bool values, Unset via DELETE and POST, path-required
// mapping rules, error mapping and nil-façade 503s.
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
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
	want := sampleSettings()
	for i := range want {
		want[i].Applies = settingApplies(want[i].Key)
	}
	if !reflect.DeepEqual(canon(t, body.Settings), canon(t, want)) {
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
		resp := h.postJSON("/api/settings/autoswitch.sevenDayThreshold", map[string]any{"value": tc.value})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("value %v: status %d: %s", tc.value, resp.StatusCode, readBody(t, resp))
		}
		res, _ := decodeJSON(t, resp)["result"].(map[string]any)
		if res["key"] != "autoswitch.sevenDayThreshold" || res["value"] != tc.want {
			t.Errorf("value %v: result %v", tc.value, res)
		}
		want = append(want, "Set(autoswitch.sevenDayThreshold,"+tc.want+")")
	}
	if got := h.set.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

// Saving autoswitch.model, or unsetting it, also retargets a running engine,
// so the settings grid and the Count model limits toggle behave the same
// while the engine runs; another key never reaches the engine, and a stopped
// engine or a build without one is left alone.
func TestSettingModel_AppliesToRunningEngine(t *testing.T) {
	h := newHarness(t) // sampleAuto reports a running engine
	resp := h.postJSON("/api/settings/autoswitch.model", map[string]any{"value": " Fable, Opus "})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set: status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if res, _ := decodeJSON(t, resp)["result"].(map[string]any); res["applied"] != true || res["key"] != "autoswitch.model" {
		t.Fatalf("set result %v, want applied: true", res)
	}
	if resp := h.send(http.MethodDelete, "/api/settings/autoswitch.model", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unset: status %d", resp.StatusCode)
	}
	if resp := h.post("/api/settings/autoswitch.model/unset"); resp.StatusCode != http.StatusOK {
		t.Fatalf("unset via POST: status %d", resp.StatusCode)
	}
	if resp := h.postJSON("/api/settings/autoswitch.sevenDayThreshold", map[string]any{"value": 80}); resp.StatusCode != http.StatusOK {
		t.Fatalf("threshold: status %d", resp.StatusCode)
	}
	if res, _ := decodeJSON(t, h.postJSON("/api/settings/autoswitch.strategy", map[string]any{"value": "best"}))["result"].(map[string]any); res["applied"] != nil {
		t.Fatalf("another key reports applied: %v", res)
	}
	wantSet := []string{"Set(autoswitch.model, Fable, Opus )", "Unset(autoswitch.model)", "Unset(autoswitch.model)", "Set(autoswitch.sevenDayThreshold,80)", "Set(autoswitch.strategy,best)"}
	if got := h.set.Calls(); !reflect.DeepEqual(got, wantSet) {
		t.Fatalf("settings calls %v, want %v", got, wantSet)
	}
	wantAuto := []string{`ApplyModels("Fable, Opus")`, `ApplyModels("")`, `ApplyModels("")`}
	if got := h.auto.Calls(); !reflect.DeepEqual(got, wantAuto) {
		t.Fatalf("engine calls %v, want %v (the model saves, trimmed, and nothing else)", got, wantAuto)
	}

	// Stopped engine: the setting is saved, the engine is not touched.
	h.auto.mu.Lock()
	h.auto.view.Running = false
	h.auto.mu.Unlock()
	resp = h.postJSON("/api/settings/autoswitch.model", map[string]any{"value": "all"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set while stopped: status %d", resp.StatusCode)
	}
	if res, _ := decodeJSON(t, resp)["result"].(map[string]any); res["applied"] != nil {
		t.Fatalf("stopped engine reported applied: %v", res)
	}
	if got := h.auto.Calls(); len(got) != len(wantAuto) {
		t.Fatalf("a stopped engine was retargeted: %v", got)
	}

	// The engine refusing the retarget is the response's error; the save
	// itself happened and the next state shows it.
	h.auto.mu.Lock()
	h.auto.view.Running = true
	h.auto.errs[`ApplyModels("all")`] = cerr.Lock("engine busy")
	h.auto.mu.Unlock()
	if resp := h.postJSON("/api/settings/autoswitch.model", map[string]any{"value": "all"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("engine error: status %d, want 409", resp.StatusCode)
	}
}

func TestSettingModel_NoEngineWired(t *testing.T) {
	h := newHarness(t, withNoAuto())
	resp := h.postJSON("/api/settings/autoswitch.model", map[string]any{"value": "all"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if res, _ := decodeJSON(t, resp)["result"].(map[string]any); res["applied"] != nil {
		t.Fatalf("no engine, yet applied: %v", res)
	}
	if got := h.set.Calls(); !reflect.DeepEqual(got, []string{"Set(autoswitch.model,all)"}) {
		t.Fatalf("settings calls %v", got)
	}
}

func TestSettingSet_ValueRequired400(t *testing.T) {
	h := newHarness(t)
	for _, body := range []any{nil, map[string]any{}, map[string]any{"value": nil}, map[string]any{"other": 1}} {
		if resp := h.postJSON("/api/settings/autoswitch.sevenDayThreshold", body); resp.StatusCode != http.StatusBadRequest {
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
		resp := h.postJSON("/api/settings/autoswitch.sevenDayThreshold", map[string]any{"value": "x"})
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
	h.postJSON("/api/settings/autoswitch.sevenDayThreshold", map[string]any{"value": 50})
	ev := st.nextState(t, timeout)
	var doc map[string]any
	_ = json.Unmarshal([]byte(ev.data), &doc)
	if doc["serverTime"] != "2026-09-19T10:00:01Z" {
		t.Fatalf("no fresh state after Set: %v", doc["serverTime"])
	}
}

// -- mappings ----------------------------------------------------------------

// Every key says when a saved value takes effect, worded from what the code
// does (A27): autoswitch.model at once (the routes retarget a running
// engine), the threshold at the next engine start with the slider for the
// running one, the Codex keys at the next start of a Codex engine (the Auto
// tab's with Codex accounts, `tycswap auto`; never the TUI's, A47), and every
// other key, a new one included, at the next engine start.
func TestSettingApplies(t *testing.T) {
	cases := map[string]string{
		"autoswitch.model":             "At once",
		"autoswitch.sevenDayThreshold": "slider",
		"autoswitch.fiveHourThreshold": "When an engine next starts",
		"autoswitch.modelThreshold":    "When an engine next starts",
		"autoswitch.codexEnabled":      "When the Codex engine next starts",
		"autoswitch.codexThreshold":    "When the Codex engine next starts",
		"autoswitch.intervalSeconds":   "When an engine next starts",
		"autoswitch.cooldownSeconds":   "When an engine next starts",
		"autoswitch.someFutureKey":     "When an engine next starts",
	}
	for key, want := range cases {
		if got := settingApplies(key); !strings.Contains(got, want) {
			t.Errorf("settingApplies(%s) = %q, want it to say %q", key, got, want)
		}
	}
	// The routes retarget the engine this page hosts only: an engine in
	// another process (the TUI, tycswap auto) keeps the value it started with.
	if got := settingApplies("autoswitch.model"); !strings.Contains(got, "tycswap auto keeps its value until it next starts") {
		t.Errorf("settingApplies(autoswitch.model) = %q, want it to name the engines it does not reach", got)
	}
	// The TUI's engine never runs the Codex engine, so the Codex keys do
	// not claim it.
	for _, key := range []string{"autoswitch.codexEnabled", "autoswitch.codexThreshold"} {
		if got := settingApplies(key); !strings.Contains(got, "when the dashboard started with Codex accounts") || !strings.Contains(got, "tycswap auto") || !strings.Contains(got, "The terminal dashboard's engine rotates Claude accounts only.") {
			t.Errorf("settingApplies(%s) = %q", key, got)
		}
	}
}

// The note reaches both the list route and the state, and annotating never
// writes into the slice the facade handed over.
func TestSettingsCarryApplies(t *testing.T) {
	h := newHarness(t)
	var body struct {
		Settings []SettingView `json:"settings"`
	}
	if err := json.Unmarshal(readBody(t, h.get("/api/settings")), &body); err != nil {
		t.Fatal(err)
	}
	var st struct {
		Settings []SettingView `json:"settings"`
	}
	if err := json.Unmarshal(readBody(t, h.get("/api/state")), &st); err != nil {
		t.Fatal(err)
	}
	for _, list := range [][]SettingView{body.Settings, st.Settings} {
		if len(list) == 0 {
			t.Fatal("no settings")
		}
		for _, sv := range list {
			if sv.Applies != settingApplies(sv.Key) {
				t.Errorf("%s applies = %q", sv.Key, sv.Applies)
			}
		}
	}
	h.set.mu.Lock()
	defer h.set.mu.Unlock()
	for _, sv := range h.set.views {
		if sv.Applies != "" {
			t.Errorf("the facade's own slice was annotated: %s", sv.Key)
		}
	}
}
