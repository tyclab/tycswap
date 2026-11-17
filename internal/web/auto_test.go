// Tests for the auto-switch routes and the `auto` SSE stream: start and stop,
// the removed routes, error mapping, nil-façade 503s, and the fan-out of
// engine events to every subscriber (the tray's notifications) followed by a
// state broadcast.
package web

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
)

// The one Auto-switch switch posts start (on) and stop (off); a dry run is
// no longer started from the dashboard (DESIGN A93).
func TestAutoStartAndStop(t *testing.T) {
	h := newHarness(t)
	for _, body := range []any{map[string]any{"dryRun": false}, map[string]any{}, nil} {
		resp := h.postJSON("/api/auto/start", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("body %v: status %d: %s", body, resp.StatusCode, readBody(t, resp))
		}
		if res, _ := decodeJSON(t, resp)["result"].(map[string]any); res["running"] != true {
			t.Errorf("body %v: result %v", body, res)
		}
	}
	resp := h.post("/api/auto/stop")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: status %d", resp.StatusCode)
	}
	if got := h.auto.Calls(); !reflect.DeepEqual(got, []string{"Start", "Start", "Start", "Stop"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestAutoStartRefusesADryRun(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/api/auto/start", map[string]any{"dryRun": true})
	if body := string(readBody(t, resp)); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "auto --dry-run") {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if calls := h.auto.Calls(); len(calls) != 0 {
		t.Fatalf("a dry-run request reached the engine host: %v", calls)
	}
}

// Wake, the live 7d slider and the model retarget left the page with their
// routes (DESIGN A93).
func TestRemovedAutoRoutesAnswer404(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/api/auto/wake", "/api/auto/threshold", "/api/auto/model"} {
		if resp := h.postJSON(p, map[string]any{"threshold": 80, "model": "all"}); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, resp.StatusCode)
		}
	}
	if calls := h.auto.Calls(); len(calls) != 0 {
		t.Errorf("a removed route reached the engine host: %v", calls)
	}
}

func TestAuto_ErrorMapping(t *testing.T) {
	h := newHarness(t)
	h.auto.mu.Lock()
	h.auto.errs["Start"] = cerr.Validation("engine already running")
	h.auto.errs["Stop"] = cerr.Config("no engine")
	h.auto.mu.Unlock()
	if resp := h.postJSON("/api/auto/start", map[string]any{}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("start status %d", resp.StatusCode)
	}
	if resp := h.post("/api/auto/stop"); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("stop status %d", resp.StatusCode)
	}
}

func TestAuto_Nil503(t *testing.T) {
	h := newHarness(t, withNoAuto())
	for _, p := range []string{"/api/auto/start", "/api/auto/stop"} {
		resp := h.postJSON(p, map[string]any{})
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

// The state's auto section carries no event ring: the page has no event log
// (DESIGN A93), and nil maps and pointers serialise as null.
func TestAuto_StateHasNoEventRing(t *testing.T) {
	h := newHarness(t)
	h.auto.mu.Lock()
	h.auto.view = AutoView{Available: true}
	h.auto.mu.Unlock()
	var st map[string]any
	_ = json.Unmarshal(readBody(t, h.get("/api/state")), &st)
	auto := st["auto"].(map[string]any)
	if _, present := auto["events"]; present {
		t.Fatalf("auto.events = %v, want no such key", auto["events"])
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

func TestMutationStateBarrier(t *testing.T) {
	h := newHarness(t)
	before := decodeJSON(t, h.get("/api/state"))["sequence"].(float64)
	reply := decodeJSON(t, h.postJSON("/api/settings/autoswitch.strategy", map[string]any{"value": "best"}))
	barrier := reply["stateSequence"].(float64)
	after := decodeJSON(t, h.get("/api/state"))["sequence"].(float64)
	if !(before < barrier && barrier < after) {
		t.Fatalf("state barrier: %v < %v < %v", before, barrier, after)
	}
}
