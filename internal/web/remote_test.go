// Tests for the remote token (DESIGN A25): a bearer that stands in for the
// cookie + CSRF pair on reads, writes and the SSE stream, never sets a
// cookie, is ignored while no token is configured, keeps the Origin /
// Sec-Fetch-Site rules, and the launch route that hands a bearer client — and
// only a bearer client — a one-time dashboard URL.
package web

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// remoteToken is the configured bearer; it is none of the three start tokens.
const remoteToken = "303132333435363738393a3b3c3d3e3f"

func withRemoteToken(tok string) option {
	return func(h *harness, d *Deps) { d.RemoteToken = tok }
}

// bearer sets the Authorization header and nothing else: no cookie, no CSRF.
func bearer(req *http.Request, tok string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+tok)
	return req
}

func TestBearer_ReadsWritesAndEvents(t *testing.T) {
	h := newHarness(t, withRemoteToken(remoteToken))

	resp := h.do(bearer(h.newReq(http.MethodGet, "/api/state", nil), remoteToken))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/state with bearer: %d (%s)", resp.StatusCode, readBody(t, resp))
	}
	var st State
	if err := json.Unmarshal(readBody(t, resp), &st); err != nil || len(st.Accounts) != 3 {
		t.Fatalf("state = %+v, %v", st, err)
	}
	// A bearer is not a browser session: nothing is set for it to keep.
	if len(resp.Cookies()) != 0 {
		t.Errorf("bearer response set a cookie: %v", resp.Cookies())
	}

	resp = h.do(bearer(h.newReq(http.MethodPost, "/api/switch/claude:2", nil), remoteToken))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/switch/2 with bearer: %d (%s)", resp.StatusCode, readBody(t, resp))
	}
	if calls := h.fa.Calls(); len(calls) != 1 || calls[0] != "SwitchTo(2,true)" {
		t.Errorf("facade calls = %v", calls)
	}

	// The stream too: the first frame is the state, as for a browser.
	resp = h.do(bearer(h.newReq(http.MethodGet, "/api/events", nil), remoteToken))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("GET /api/events with bearer: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); !strings.HasPrefix(got, "event: state\ndata:") {
		t.Fatalf("stream begins %q", got)
	}
	_ = resp.Body.Close()

	// Static assets stay cookie-only: a Go client never fetches them, so the
	// bearer buys nothing there and is refused.
	if resp := h.do(bearer(h.newReq(http.MethodGet, "/static/app.js", nil), remoteToken)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("static with bearer: %d, want 401", resp.StatusCode)
	}
}

func TestBearer_WrongOrMissingIs401(t *testing.T) {
	h := newHarness(t, withRemoteToken(remoteToken))
	cases := []struct {
		name string
		hdr  string
	}{
		{"wrong", "Bearer " + strings.Repeat("f", len(remoteToken))},
		{"short", "Bearer " + remoteToken[:8]},
		{"other scheme", "Basic " + remoteToken},
		{"bare", remoteToken},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, r := range []struct{ method, path string }{{"GET", "/api/state"}, {"POST", "/api/switch/claude:1"}, {"GET", "/api/events"}} {
				req := h.newReq(r.method, r.path, nil)
				if tc.hdr != "" {
					req.Header.Set("Authorization", tc.hdr)
				}
				if resp := h.do(req); resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s %s: status %d, want 401", r.method, r.path, resp.StatusCode)
				}
			}
		})
	}
	facadesUntouched(t, h, false)
}

// The browser session is untouched by a configured remote token.
func TestBearer_CookieAndCSRFStillWork(t *testing.T) {
	h := newHarness(t, withRemoteToken(remoteToken))
	if resp := h.get("/api/state"); resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie+CSRF GET: %d", resp.StatusCode)
	}
	if resp := h.post("/api/accounts/claude:2/enable"); resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie+CSRF POST: %d", resp.StatusCode)
	}
	// Cookie alone is still not enough, token or no token.
	if resp := h.do(h.withCookie(h.newReq(http.MethodGet, "/api/state", nil))); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie only: %d, want 403", resp.StatusCode)
	}
}

// Without a RemoteToken the header means nothing: today's behaviour.
func TestBearer_IgnoredWhenNoTokenConfigured(t *testing.T) {
	h := newHarness(t)
	for _, tok := range []string{remoteToken, "", fixedToken, fixedCookie} {
		req := h.newReq(http.MethodGet, "/api/state", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if resp := h.do(req); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("bearer %q with no RemoteToken: %d, want 401", tok, resp.StatusCode)
		}
	}
	// Not even the empty token matches an empty configuration.
	req := h.newReq(http.MethodGet, "/api/state", nil)
	req.Header.Set("Authorization", "Bearer ")
	if resp := h.do(req); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("empty bearer: %d, want 401", resp.StatusCode)
	}
	facadesUntouched(t, h, false)
}

// POST /api/launch hands out the one-time dashboard URL; its token redeems
// exactly once, then a fresh one is minted.
func TestLaunchRoute_URLRedeemsOnce(t *testing.T) {
	h := newHarness(t, withRemoteToken(remoteToken), withRand(io.MultiReader(fixedRand(), rand.Reader)))
	launch := func() string {
		t.Helper()
		resp := h.do(bearer(h.newReq(http.MethodPost, "/api/launch", nil), remoteToken))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /api/launch: %d (%s)", resp.StatusCode, readBody(t, resp))
		}
		var body struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(readBody(t, resp), &body); err != nil || !strings.HasPrefix(body.URL, h.base+"/?token=") {
			t.Fatalf("launch body = %+v, %v", body, err)
		}
		return body.URL
	}
	first := launch()
	if first != h.s.URL() {
		t.Fatalf("first launch URL = %q, want the unused start URL %q", first, h.s.URL())
	}
	if again := launch(); again != first {
		t.Fatalf("second launch before use = %q, want %q", again, first)
	}
	path := strings.TrimPrefix(first, h.base)
	if resp := h.do(h.newReq(http.MethodGet, path, nil)); resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) != 1 {
		t.Fatalf("redeem: %d cookies %v, want 303 and one", resp.StatusCode, resp.Cookies())
	}
	if resp := h.do(h.newReq(http.MethodGet, path, nil)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("replay: %d, want 403", resp.StatusCode)
	}
	if second := launch(); second == first {
		t.Fatal("a redeemed URL was handed out again")
	}
}

// The launch route is the bearer's alone: a browser session holds its cookie
// already, and a launch URL in its hands would only transplant that session
// into another profile or machine. The pair that opens every other route
// gets 403 here; no credential at all stays 401 (auth_test's allRoutes).
func TestLaunchRoute_BearerOnly(t *testing.T) {
	h := newHarness(t, withRemoteToken(remoteToken))
	if resp := h.post("/api/launch"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie+CSRF launch: %d, want 403", resp.StatusCode)
	}
	if resp := h.do(bearer(h.newReq(http.MethodPost, "/api/launch", nil), remoteToken)); resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer launch: %d, want 200", resp.StatusCode)
	}
	// With no RemoteToken configured nothing can reach it.
	h2 := newHarness(t)
	if resp := h2.post("/api/launch"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("launch without a configured token: %d, want 403", resp.StatusCode)
	}
}

// The Origin / Sec-Fetch-Site rules are not waived for a bearer (A25): a
// request that carries them and points elsewhere is refused with the token
// right, and no façade is reached.
func TestBearer_OriginRulesStillApply(t *testing.T) {
	h := newHarness(t, withRemoteToken(remoteToken))
	for _, r := range mutatingRoutes() {
		t.Run("origin "+r.method+" "+r.path, func(t *testing.T) {
			req := bearer(h.newReq(r.method, r.path, nil), remoteToken)
			req.Header.Set("Origin", "http://evil.example")
			if resp := h.do(req); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status %d, want 403", resp.StatusCode)
			}
		})
		t.Run("sfs "+r.method+" "+r.path, func(t *testing.T) {
			req := bearer(h.newReq(r.method, r.path, nil), remoteToken)
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			if resp := h.do(req); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status %d, want 403", resp.StatusCode)
			}
		})
	}
	facadesUntouched(t, h, false)
	// Same-origin headers, as a browser on the page would send them, pass.
	req := bearer(h.newReq(http.MethodPost, "/api/switch/claude:2", nil), remoteToken)
	req.Header.Set("Origin", "http://"+req.Host)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if resp := h.do(req); resp.StatusCode != http.StatusOK {
		t.Fatalf("same-origin bearer POST: %d (%s)", resp.StatusCode, readBody(t, resp))
	}
}
