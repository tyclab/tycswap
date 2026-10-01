// Tests for GET /api/events: initial state, one event per tick, one per
// mutation, ping comments, disconnect cleanup, and a fan-out to concurrent
// subscribers under the race detector.
package web

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

const timeout = 5 * time.Second

func TestSSE_InitialThenTickThenMutation(t *testing.T) {
	h := newHarness(t)
	st := h.openSSE()
	defer st.close()

	// 1. initial state, immediately
	ev := st.nextState(t, timeout)
	var doc map[string]any
	if err := json.Unmarshal([]byte(ev.data), &doc); err != nil {
		t.Fatalf("initial data not JSON: %v", err)
	}
	if doc["serverTime"] != "2026-09-19T10:00:00Z" {
		t.Fatalf("initial serverTime %v", doc["serverTime"])
	}
	st.expectNone(t, 50*time.Millisecond)

	// 2. one per tick, carrying the advanced clock
	h.clk.Advance(5 * time.Second)
	h.fireTick()
	ev = st.nextState(t, timeout)
	if err := json.Unmarshal([]byte(ev.data), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["serverTime"] != "2026-09-19T10:00:05Z" {
		t.Fatalf("tick serverTime %v", doc["serverTime"])
	}
	st.expectNone(t, 50*time.Millisecond)

	h.clk.Advance(5 * time.Second)
	h.fireTick()
	ev = st.nextState(t, timeout)
	_ = json.Unmarshal([]byte(ev.data), &doc)
	if doc["serverTime"] != "2026-09-19T10:00:10Z" {
		t.Fatalf("second tick serverTime %v", doc["serverTime"])
	}

	// 3. one per mutation (success and failure alike)
	h.clk.Advance(time.Second)
	if resp := h.post("/api/accounts/claude:2/enable"); resp.StatusCode != http.StatusOK {
		t.Fatalf("enable status %d", resp.StatusCode)
	}
	ev = st.nextState(t, timeout)
	_ = json.Unmarshal([]byte(ev.data), &doc)
	if doc["serverTime"] != "2026-09-19T10:00:11Z" {
		t.Fatalf("post-mutation serverTime %v", doc["serverTime"])
	}
	st.expectNone(t, 50*time.Millisecond)

	h.clk.Advance(time.Second)
	if resp := h.post("/api/sessions/99999/stop"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stop unknown status %d", resp.StatusCode)
	}
	// A refused stop (nothing mutated) does not broadcast.
	st.expectNone(t, 50*time.Millisecond)

	h.fa.mu.Lock()
	h.fa.errs = map[string]error{"RemoveAccount": errFake}
	h.fa.mu.Unlock()
	if resp := h.post("/api/accounts/claude:1/remove"); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("remove status %d", resp.StatusCode)
	}
	ev = st.nextState(t, timeout)
	_ = json.Unmarshal([]byte(ev.data), &doc)
	if doc["serverTime"] != "2026-09-19T10:00:12Z" {
		t.Fatalf("post-failed-mutation serverTime %v", doc["serverTime"])
	}
}

func TestSSE_WireFormat(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/api/events")
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control %q", cc)
	}
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); len(got) < 18 || got[:18] != "event: state\ndata:" {
		t.Fatalf("stream begins %q, want event: state\\ndata:", got)
	}
	_ = resp.Body.Close()
}

func TestSSE_Ping(t *testing.T) {
	h := newHarness(t)
	h.s.pingInterval = 20 * time.Millisecond
	st := h.openSSE()
	defer st.close()
	st.nextState(t, timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ev := st.next(t, timeout)
		if ev.comment == "ping" && ev.name == "" && ev.data == "" {
			return
		}
	}
	t.Fatal("no ping comment received")
}

func TestSSE_DisconnectCleansUp(t *testing.T) {
	h := newHarness(t)
	if h.s.hub.count() != 0 {
		t.Fatalf("initial subscribers %d", h.s.hub.count())
	}
	st := h.openSSE()
	st.nextState(t, timeout)
	waitFor(t, timeout, func() bool { return h.s.hub.count() == 1 })
	st.close()
	waitFor(t, timeout, func() bool { return h.s.hub.count() == 0 })
	// Broadcasting to nobody is fine and the loop keeps ticking.
	h.fireTick()
	h.fireTick()
}

func TestSSE_ConcurrentSubscribersReceiveBroadcast(t *testing.T) {
	h := newHarness(t)
	const n = 12
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

	// Concurrent mutations from several goroutines while everybody listens.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "/api/accounts/claude:2/enable"
			if i%2 == 1 {
				path = "/api/switch/claude:2"
			}
			resp, err := h.client.Do(h.authed(h.newReq(http.MethodPost, path, nil)))
			if err != nil {
				t.Error(err)
				return
			}
			_ = resp.Body.Close()
		}(i)
	}
	wg.Wait()
	h.clk.Advance(time.Minute)
	h.fireTick()

	// Every subscriber sees the tick state (4 mutation states + 1 tick state).
	for i, st := range streams {
		seen := 0
		for seen < 5 {
			ev := st.nextState(t, timeout)
			seen++
			var doc map[string]any
			if err := json.Unmarshal([]byte(ev.data), &doc); err != nil {
				t.Fatalf("sub %d: bad JSON: %v", i, err)
			}
		}
	}
	if got := len(h.fa.Calls()); got != 4 {
		t.Fatalf("facade calls %d, want 4", got)
	}
}

func TestSSE_ServerShutdownEndsStreams(t *testing.T) {
	// Serve's cancel closes s.done, which must end every open stream so
	// Shutdown does not hang on them. The harness's Cleanup asserts Serve
	// returns within 5 s; an open stream here exercises that path.
	h := newHarness(t)
	st := h.openSSE()
	st.nextState(t, timeout)
	// Leave the stream open; Cleanup cancels Serve.
	_ = st
}

func TestHub_DropsWhenSubscriberIsSlow(t *testing.T) {
	hb := newHub()
	ch, unsub := hb.subscribe(false)
	defer unsub()
	for i := 0; i < subBuffer+5; i++ {
		hb.publish("state", []byte{byte(i)})
	}
	if len(ch) != subBuffer {
		t.Fatalf("buffered %d, want %d (extra drops, never blocks)", len(ch), subBuffer)
	}
	if first := <-ch; first.name != "state" || len(first.data) != 1 || first.data[0] != 0 {
		t.Fatalf("first buffered event %+v", first)
	}
	unsub()
	unsub() // idempotent
	if hb.count() != 0 {
		t.Fatalf("count %d after unsubscribe", hb.count())
	}
}
