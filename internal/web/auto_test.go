// Tests for the auto-switch routes and the `auto` SSE stream: start (dry-run
// flag), stop, wake, threshold bounds, error mapping, nil-façade 503s, and
// the fan-out of engine events to every subscriber followed by a state
// broadcast.
package web

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
)

func TestAutoStart_DryRunFlag(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		body any
		want string
	}{
		{map[string]any{"dryRun": true}, "Start(true)"},
		{map[string]any{"dryRun": false}, "Start(false)"},
		{map[string]any{}, "Start(false)"},
		{nil, "Start(false)"},
	}
	var want []string
	for _, tc := range cases {
		resp := h.postJSON("/api/auto/start", tc.body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("body %v: status %d: %s", tc.body, resp.StatusCode, readBody(t, resp))
		}
		res, _ := decodeJSON(t, resp)["result"].(map[string]any)
		if res["running"] != true || res["dryRun"] != (tc.want == "Start(true)") {
			t.Errorf("body %v: result %v", tc.body, res)
		}
		want = append(want, tc.want)
	}
	if got := h.auto.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

func TestAutoStopWake(t *testing.T) {
	h := newHarness(t)
	for _, a := range []string{"stop", "wake"} {
		resp := h.post("/api/auto/" + a)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", a, resp.StatusCode)
		}
		res, _ := decodeJSON(t, resp)["result"].(map[string]any)
		if res["action"] != a {
			t.Errorf("%s: result %v", a, res)
		}
	}
	if got := h.auto.Calls(); !reflect.DeepEqual(got, []string{"Stop", "Wake"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestAutoThreshold_Bounds(t *testing.T) {
	h := newHarness(t)
	ok := []struct {
		v    any
		want string
	}{
		{50, "ApplyThreshold(50)"}, {99.9, "ApplyThreshold(99.9)"}, {55.5, "ApplyThreshold(55.5)"}, {80, "ApplyThreshold(80)"}, {100, "ApplyThreshold(100)"},
	}
	var want []string
	for _, tc := range ok {
		resp := h.postJSON("/api/auto/threshold", map[string]any{"threshold": tc.v})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("threshold %v: status %d: %s", tc.v, resp.StatusCode, readBody(t, resp))
		}
		want = append(want, tc.want)
	}
	// The slider moves the 7d bar, so the bounds are
	// autoswitch.sevenDayThreshold's (50-100), as in the CLI and the TUI; 0
	// would make every account count as over the limit (DESIGN A34).
	bad := []any{
		map[string]any{"threshold": -1},
		map[string]any{"threshold": 0},
		map[string]any{"threshold": 49.9},
		map[string]any{"threshold": 100.01},
		map[string]any{"threshold": 1000},
		map[string]any{"threshold": "80"},
		map[string]any{"threshold": nil},
		map[string]any{},
		nil,
		"{\"threshold\": NaN}",
	}
	for _, body := range bad {
		resp := h.postJSON("/api/auto/threshold", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %v: status %d, want 400", body, resp.StatusCode)
		}
	}
	if got := h.auto.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v (out-of-range values must not reach the engine)", got, want)
	}
}

func TestAuto_ErrorMapping(t *testing.T) {
	h := newHarness(t)
	h.auto.mu.Lock()
	h.auto.errs["Start(false)"] = cerr.Validation("engine already running")
	h.auto.errs["Stop"] = cerr.Config("no engine")
	h.auto.errs["ApplyThreshold(50)"] = cerr.Lock("busy")
	h.auto.mu.Unlock()
	if resp := h.postJSON("/api/auto/start", map[string]any{"dryRun": false}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("start status %d", resp.StatusCode)
	}
	if resp := h.post("/api/auto/stop"); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("stop status %d", resp.StatusCode)
	}
	if resp := h.postJSON("/api/auto/threshold", map[string]any{"threshold": 50}); resp.StatusCode != http.StatusConflict {
		t.Errorf("threshold status %d", resp.StatusCode)
	}
}

func TestAuto_Nil503(t *testing.T) {
	h := newHarness(t, withNoAuto())
	for _, p := range []string{"/api/auto/start", "/api/auto/stop", "/api/auto/wake", "/api/auto/threshold"} {
		resp := h.postJSON(p, map[string]any{"threshold": 50, "dryRun": true})
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503", p, resp.StatusCode)
		}
	}
	var st map[string]any
	_ = json.Unmarshal(readBody(t, h.get("/api/state")), &st)
	if st["auto"] != nil {
		t.Fatalf("state.auto = %v, want null", st["auto"])
	}
}

func TestAuto_StateEventsNeverNull(t *testing.T) {
	h := newHarness(t)
	h.auto.mu.Lock()
	h.auto.view = AutoView{Available: true}
	h.auto.mu.Unlock()
	var st map[string]any
	_ = json.Unmarshal(readBody(t, h.get("/api/state")), &st)
	auto := st["auto"].(map[string]any)
	if ev, ok := auto["events"].([]any); !ok || len(ev) != 0 {
		t.Fatalf("auto.events = %v, want []", auto["events"])
	}
	if auto["quarantine"] != nil || auto["settings"] != nil || auto["startedAt"] != nil {
		t.Fatalf("nil maps/pointers must serialise as null: %v", auto)
	}
}

func TestSSE_AutoEventFanOutAndStateBroadcast(t *testing.T) {
	h := newHarness(t)
	const n = 5
	streams := make([]*sseStream, n)
	for i := range streams {
		streams[i] = h.openSSE()
		streams[i].nextState(t, timeout)
	}
	defer func() {
		for _, st := range streams {
			st.close()
		}
	}()
	waitFor(t, timeout, func() bool { return h.s.hub.count() == n })

	h.clk.Advance(3 * time.Second)
	ev := AutoEventView{At: 1758276100, Kind: "switch", Message: "switched to #2", Account: "2", Fields: map[string]any{"from": "1", "headroom": 42.5}}
	h.fireAuto(ev)

	for i, st := range streams {
		got := st.next(t, timeout)
		if got.name != "auto" {
			t.Fatalf("sub %d: first event %q, want auto", i, got.name)
		}
		var back AutoEventView
		if err := json.Unmarshal([]byte(got.data), &back); err != nil {
			t.Fatalf("sub %d: bad auto JSON: %v", i, err)
		}
		if !reflect.DeepEqual(canon(t, back), canon(t, ev)) {
			t.Fatalf("sub %d: auto event %+v, want %+v", i, back, ev)
		}
		state := st.next(t, timeout)
		if state.name != "state" {
			t.Fatalf("sub %d: second event %q, want state", i, state.name)
		}
		var doc map[string]any
		_ = json.Unmarshal([]byte(state.data), &doc)
		if doc["serverTime"] != "2026-09-19T10:00:03Z" {
			t.Fatalf("sub %d: state after auto event has serverTime %v", i, doc["serverTime"])
		}
		st.expectNone(t, 30*time.Millisecond)
	}

	// A second event, with the optional fields omitted from the wire.
	h.fireAuto(AutoEventView{At: 1758276101, Kind: "poll", Message: "polled"})
	got := streams[0].nextNamed(t, "auto", timeout)
	var raw map[string]any
	_ = json.Unmarshal([]byte(got.data), &raw)
	if _, has := raw["account"]; has {
		t.Errorf("empty account not omitted: %v", raw)
	}
	if _, has := raw["fields"]; has {
		t.Errorf("nil fields not omitted: %v", raw)
	}
	if raw["kind"] != "poll" || raw["at"] != float64(1758276101) {
		t.Errorf("auto event %v", raw)
	}
}

// A Codex tick reaches the stream as an ordinary `auto` frame carrying
// "provider":"codex"; a Claude engine event carries no provider key, so its
// JSON is what it was before Codex (DESIGN A47).
func TestSSE_AutoEvent_CarriesProvider(t *testing.T) {
	h := newHarness(t, withCodex())
	st := h.openSSE()
	defer st.close()
	st.nextState(t, timeout)

	codex := AutoEventView{At: 1758276100, Kind: "switch", Message: "codex: switched 1 (98%) -> 2 (12%)", Account: "2", Provider: "codex",
		Fields: map[string]any{"outcome": "switched", "detail": "switched 1 (98%) -> 2 (12%)", "switchedTo": "2", "runningPids": []any{}}}
	h.fireAuto(codex)
	var raw map[string]any
	if err := json.Unmarshal([]byte(st.nextNamed(t, "auto", timeout).data), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["provider"] != "codex" || raw["kind"] != "switch" || raw["account"] != "2" {
		t.Errorf("codex auto frame %v", raw)
	}
	if !reflect.DeepEqual(canon(t, raw), canon(t, codex)) {
		t.Errorf("codex auto frame %v, want %+v", raw, codex)
	}

	h.fireAuto(AutoEventView{At: 1758276101, Kind: "switch", Message: "switched to #2", Account: "2"})
	raw = nil
	if err := json.Unmarshal([]byte(st.nextNamed(t, "auto", timeout).data), &raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw["provider"]; has {
		t.Errorf("a Claude event carries a provider key: %v", raw)
	}
}

func TestSSE_AutoEventsClosedChannelKeepsServing(t *testing.T) {
	h := newHarness(t)
	st := h.openSSE()
	defer st.close()
	st.nextState(t, timeout)
	close(h.autoEv)
	// The loop must keep ticking and serving after the auto stream ends.
	h.clk.Advance(time.Second)
	h.fireTick()
	ev := st.nextState(t, timeout)
	var doc map[string]any
	_ = json.Unmarshal([]byte(ev.data), &doc)
	if doc["serverTime"] != "2026-09-19T10:00:01Z" {
		t.Fatalf("tick after closed auto channel: %v", doc["serverTime"])
	}
	if resp := h.get("/api/state"); resp.StatusCode != http.StatusOK {
		t.Fatalf("state status %d", resp.StatusCode)
	}
}

func TestSSE_NoAutoEventsChannel(t *testing.T) {
	h := newHarness(t, withNoAutoEvents())
	st := h.openSSE()
	defer st.close()
	st.nextState(t, timeout)
	h.fireTick()
	st.nextState(t, timeout)
	st.expectNone(t, 30*time.Millisecond)
}

func TestSSE_AutoEventsDoNotDisturbLateSubscriber(t *testing.T) {
	// An event published before a subscriber joined is not replayed; the
	// subscriber gets the initial state only (the state carries the ring).
	h := newHarness(t)
	early := h.openSSE()
	defer early.close()
	early.nextState(t, timeout)
	h.fireAuto(AutoEventView{At: 1, Kind: "poll", Message: "x"})
	early.nextNamed(t, "auto", timeout)
	early.nextState(t, timeout)
	late := h.openSSE()
	defer late.close()
	first := late.next(t, timeout)
	if first.name != "state" {
		t.Fatalf("late subscriber first event %q", first.name)
	}
	late.expectNone(t, 30*time.Millisecond)
}

// TestAutoModelAppliesLive: the "Count model limits" switch must reach the
// RUNNING engine, not only settings.json — otherwise headroom, the at-limit
// verdict and the ranking keep counting a window the user just excluded.
func TestAutoModelAppliesLive(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"model":"all"}`, `ApplyModels("all")`},
		{`{"model":""}`, `ApplyModels("")`},
		{`{"model":"  Fable , Opus "}`, `ApplyModels("Fable , Opus")`},
	} {
		h.auto.calls = nil
		resp := h.postJSON("/api/auto/model", tc.body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d: %s", tc.body, resp.StatusCode, readBody(t, resp))
		}
		if len(h.auto.calls) != 1 || h.auto.calls[0] != tc.want {
			t.Errorf("%s: calls %v, want [%s]", tc.body, h.auto.calls, tc.want)
		}
	}
	h.auto.calls = nil
	if resp := h.postJSON("/api/auto/model", `{}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a missing model must be rejected: %d", resp.StatusCode)
	}
	if len(h.auto.calls) != 0 {
		t.Errorf("nothing should reach the engine on a bad request: %v", h.auto.calls)
	}
}

func TestAutoStart_ManagedExternally(t *testing.T) {
	h := newHarness(t)
	h.auto.mu.Lock()
	h.auto.view.ManagedBy = "flakelab-tycswap-autoswitch.timer"
	h.auto.view.Available = false
	h.auto.mu.Unlock()
	for _, dry := range []bool{false, true} {
		resp := h.postJSON("/api/auto/start", map[string]any{"dryRun": dry})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("managed start = %d: %s", resp.StatusCode, readBody(t, resp))
		}
		resp.Body.Close()
	}
	if len(h.auto.Calls()) != 0 {
		t.Fatal("managed engine reached Start")
	}
}

func TestMutationStateBarrier(t *testing.T) {
	h := newHarness(t)
	before := decodeJSON(t, h.get("/api/state"))["sequence"].(float64)
	reply := decodeJSON(t, h.postJSON("/api/settings/autoswitch.model", map[string]any{"value": "Opus"}))
	barrier := reply["stateSequence"].(float64)
	after := decodeJSON(t, h.get("/api/state"))["sequence"].(float64)
	if !(before < barrier && barrier < after) {
		t.Fatalf("state barrier: %v < %v < %v", before, barrier, after)
	}
}
