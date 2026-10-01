// sse.go — the Server-Sent-Events fan-out: one hub, one buffered channel per
// subscriber, drop-on-slow so a stalled browser never blocks the poll loop.
//
// Implements DESIGN A25 "GET /api/events → SSE, a state event per poll tick
// and on every mutation", extended with `auto` events (one per auto-switch
// engine event, one state per batch of them). ?tokenStatus=1 subscribes to
// the enriched state documents. Wire format per event: "event: <name>\ndata: <json>\n\n"; a
// ": ping\n\n" comment every pingInterval keeps proxies and browsers from
// timing the stream out.
package web

import (
	"net/http"
	"sync"
	"time"
)

// subBuffer is the per-subscriber backlog. A subscriber that falls this far
// behind loses intermediate events; the next state supersedes them anyway.
const subBuffer = 16

// event is one named SSE payload.
type event struct {
	name string
	data []byte
}

// sub is one subscriber: its channel and whether it asked for token status.
type sub struct {
	tokenStatus bool
}

type hub struct {
	mu   sync.Mutex
	subs map[chan event]sub
}

func newHub() *hub { return &hub{subs: map[chan event]sub{}} }

// subscribe registers a new subscriber and returns its channel plus the
// unsubscribe func (idempotent). tokenStatus subscribers receive the state
// documents enriched with each row's token status.
func (h *hub) subscribe(tokenStatus bool) (<-chan event, func()) {
	ch := make(chan event, subBuffer)
	h.mu.Lock()
	h.subs[ch] = sub{tokenStatus: tokenStatus}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
		})
	}
}

// publish delivers a named event to every subscriber without blocking; a full
// buffer drops it for that subscriber.
func (h *hub) publish(name string, body []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- event{name: name, data: body}:
		default:
		}
	}
}

// wantsTokenStatus reports whether any subscriber asked for token status.
func (h *hub) wantsTokenStatus() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs {
		if s.tokenStatus {
			return true
		}
	}
	return false
}

// publishState delivers a state document: withTS to the subscribers that
// asked for token status (when non-nil), plain to everyone else.
func (h *hub) publishState(plain, withTS []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, s := range h.subs {
		body := plain
		if s.tokenStatus && withTS != nil {
			body = withTS
		}
		select {
		case ch <- event{name: "state", data: body}:
		default:
		}
	}
}

// count returns the live subscriber count (tests assert cleanup with it).
func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// handleEvents streams events until the client disconnects or the server
// winds down.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	wantTS := isTruthy(r.URL.Query().Get("tokenStatus"))
	ch, unsubscribe := s.hub.subscribe(wantTS)
	defer unsubscribe()

	// Initial state so a fresh subscriber never waits for a tick.
	body, err := s.stateJSON(stateOpts{tokenStatus: wantTS})
	if err != nil {
		s.d.Logger("web: state: " + err.Error())
	} else if !writeEvent(w, flusher, "state", body) {
		return
	}

	ping := time.NewTicker(s.pingInterval)
	defer ping.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case ev := <-ch:
			if !writeEvent(w, flusher, ev.name, ev.data) {
				return
			}
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeEvent writes one named event; false means the connection is gone.
func writeEvent(w http.ResponseWriter, f http.Flusher, name string, body []byte) bool {
	if _, err := w.Write([]byte("event: " + name + "\ndata: ")); err != nil {
		return false
	}
	if _, err := w.Write(body); err != nil {
		return false
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return false
	}
	f.Flush()
	return true
}
