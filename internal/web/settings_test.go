// Tests for the settings and directory-mapping routes: listing, Set with
// string / number / bool values, Unset via DELETE and POST, path-required
// mapping rules, error mapping and nil-façade 503s, and the keys the list
// leaves out (DESIGN A93).
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

// autoswitch.model is set through tycswap config only (DESIGN A93): the
// list and the state leave it out, and every settings route refuses to set
// or unset it, naming the command, without touching the store or the engine.
func TestSettingsLeaveOutTheModelSetting(t *testing.T) {
	h := newHarness(t)
	h.set.mu.Lock()
	h.set.views = append(h.set.views, SettingView{Key: "autoswitch.model", Kind: "string", Value: "Fable"})
	h.set.mu.Unlock()
	var body, st struct {
		Settings []SettingView `json:"settings"`
	}
	if err := json.Unmarshal(readBody(t, h.get("/api/settings")), &body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readBody(t, h.get("/api/state")), &st); err != nil {
		t.Fatal(err)
	}
	for _, list := range [][]SettingView{body.Settings, st.Settings} {
		if len(list) == 0 {
			t.Fatal("no settings")
		}
		for _, sv := range list {
			if sv.Key == "autoswitch.model" {
				t.Fatalf("the settings list carries autoswitch.model: %+v", list)
			}
		}
	}
	for _, resp := range []*http.Response{
		h.postJSON("/api/settings/autoswitch.model", map[string]any{"value": "all"}),
		h.send(http.MethodDelete, "/api/settings/autoswitch.model", nil),
		h.post("/api/settings/autoswitch.model/unset"),
	} {
		if body := string(readBody(t, resp)); resp.StatusCode != http.StatusBadRequest ||
			!strings.Contains(body, "tycswap config set autoswitch.model") || !strings.Contains(body, "tycswap config unset autoswitch.model") {
			t.Errorf("status %d: %s", resp.StatusCode, body)
		}
	}
	if calls := h.set.Calls(); len(calls) != 0 {
		t.Errorf("a refused save reached the store: %v", calls)
	}
	if calls := h.auto.Calls(); len(calls) != 0 {
		t.Errorf("a refused save reached the engine host: %v", calls)
	}
}

// Poll interval is listed only while no scheduler owns the ticks: auto --once
// never reads it (DESIGN A93).
func TestSettingsListPollIntervalOnlyWithoutAnOwner(t *testing.T) {
	h := newHarness(t)
	h.set.mu.Lock()
	h.set.views = []SettingView{{Key: "autoswitch.fiveHourThreshold", Kind: "float"}, {Key: "autoswitch.intervalSeconds", Kind: "float"}}
	h.set.mu.Unlock()
	listed := func() []string {
		var body struct {
			Settings []SettingView `json:"settings"`
		}
		if err := json.Unmarshal(readBody(t, h.get("/api/settings")), &body); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, sv := range body.Settings {
			keys = append(keys, sv.Key)
		}
		return keys
	}
	if got := listed(); !reflect.DeepEqual(got, []string{"autoswitch.fiveHourThreshold", "autoswitch.intervalSeconds"}) {
		t.Fatalf("no owner: listed %v", got)
	}
	h.auto.mu.Lock()
	h.auto.view = AutoView{Available: true, Running: true, Owner: "tycswap-auto.timer"}
	h.auto.mu.Unlock()
	if got := listed(); !reflect.DeepEqual(got, []string{"autoswitch.fiveHourThreshold"}) {
		t.Fatalf("with an owner: listed %v", got)
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
// does (A27): the Codex keys at the next start of a Codex engine (the Auto
// tab's with Codex accounts, `tycswap auto`; never the TUI's, A47), and every
// other key, a new one included, at the next engine start.
func TestSettingApplies(t *testing.T) {
	cases := map[string]string{
		"autoswitch.sevenDayThreshold": "Applies when auto-switch restarts",
		"autoswitch.fiveHourThreshold": "Applies when auto-switch restarts",
		"autoswitch.modelThreshold":    "Applies when auto-switch restarts",
		"autoswitch.codexEnabled":      "Applies when Codex auto-switch restarts",
		"autoswitch.codexThreshold":    "Applies when Codex auto-switch restarts",
		"autoswitch.intervalSeconds":   "Applies when auto-switch restarts",
		"autoswitch.cooldownSeconds":   "Applies when auto-switch restarts",
		"autoswitch.someFutureKey":     "Applies when auto-switch restarts",
	}
	for key, want := range cases {
		if got := settingApplies(key); !strings.Contains(got, want) {
			t.Errorf("settingApplies(%s) = %q, want it to say %q", key, got, want)
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

// autoswitch.enabled is read by every tick, so its row on the Settings tab
// says a save applies at the next tick, not at a restart (DESIGN A83).
func TestEnabledSettingAppliesAtTheNextTick(t *testing.T) {
	h := newHarness(t)
	h.set.mu.Lock()
	h.set.views = append([]SettingView{{Key: "autoswitch.enabled", Kind: "bool", Value: false, Default: false}}, h.set.views...)
	h.set.mu.Unlock()
	var body struct {
		Settings []SettingView `json:"settings"`
	}
	if err := json.Unmarshal(readBody(t, h.get("/api/settings")), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Settings) == 0 || body.Settings[0].Key != "autoswitch.enabled" || body.Settings[0].Applies != "Applies at the next tick." {
		t.Fatalf("enabled row = %+v", body.Settings)
	}
}

// With an external scheduler named, the Settings tab says a saved value
// applies at its next check (DESIGN A51, A83); Poll interval is not listed.
func TestSettingsNotesForAnExternalScheduler(t *testing.T) {
	h := newHarness(t)
	h.auto.mu.Lock()
	h.auto.view = AutoView{Available: true, Running: true, Owner: "tycswap-auto.timer"}
	h.auto.mu.Unlock()
	h.set.mu.Lock()
	h.set.views = []SettingView{{Key: "autoswitch.fiveHourThreshold", Kind: "float"}, {Key: "autoswitch.intervalSeconds", Kind: "float"}}
	h.set.mu.Unlock()
	var body struct {
		Settings []SettingView `json:"settings"`
	}
	if err := json.Unmarshal(readBody(t, h.get("/api/settings")), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Settings) != 1 || body.Settings[0].Applies != "Applies at the next scheduled check." {
		t.Errorf("settings = %+v, want the 5-hour row applying at the next scheduled check", body.Settings)
	}
}
