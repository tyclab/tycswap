// Tests for the A25 security model: token bootstrap → cookie plus the CSRF
// token in the redirect's fragment, cookie-gated API, CSRF + Origin +
// Sec-Fetch-Site on mutations, Host pinning, and the guarded, token-free
// index page.
package web

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// allRoutes is every API route, used by the table-driven auth tests.
var allRoutes = []struct{ method, path string }{
	{"GET", "/api/state"},
	{"GET", "/api/state?tokenStatus=1"},
	{"GET", "/api/events"},
	{"POST", "/api/switch"},
	{"POST", "/api/switch/claude:1"},
	{"POST", "/api/switch/claude:1?force=1"},
	{"POST", "/api/accounts/add"},
	{"POST", "/api/accounts/add-token"},
	{"POST", "/api/accounts/swap"},
	{"POST", "/api/accounts/claude:1/disable"},
	{"POST", "/api/accounts/claude:1/enable"},
	{"POST", "/api/accounts/claude:1/remove"},
	{"POST", "/api/accounts/claude:1/alias"},
	{"POST", "/api/accounts/claude:1/move"},
	{"POST", "/api/sessions/4242/stop"},
	{"GET", "/api/settings"},
	{"POST", "/api/settings/autoswitch.threshold"},
	{"DELETE", "/api/settings/autoswitch.threshold"},
	{"POST", "/api/settings/autoswitch.threshold/unset"},
	{"POST", "/api/auto/start"},
	{"POST", "/api/auto/stop"},
	{"POST", "/api/auto/wake"},
	{"POST", "/api/auto/threshold"},
	{"POST", "/api/auto/model"},
}

// mutatingRoutes are the non-GET routes (POST and DELETE).
func mutatingRoutes() []struct{ method, path string } {
	var out []struct{ method, path string }
	for _, r := range allRoutes {
		if r.method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

// facadesUntouched fails when any façade recorded a call. allowList is kept
// for the callers' sake; no remaining façade has a read the state builder
// performs on their behalf.
func facadesUntouched(t *testing.T, h *harness, allowList bool) {
	t.Helper()
	_ = allowList
	if len(h.fa.Calls())+len(h.Killed())+len(h.ops.Calls())+len(h.set.Calls())+len(h.auto.Calls()) != 0 {
		t.Fatalf("a facade was reached: fa=%v kill=%v ops=%v set=%v auto=%v",
			h.fa.Calls(), h.Killed(), h.ops.Calls(), h.set.Calls(), h.auto.Calls())
	}
}

func decodeError(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(readBody(t, resp), &body); err != nil {
		t.Fatalf("error body is not JSON: %v", err)
	}
	msg, _ := body["error"].(string)
	if msg == "" {
		t.Fatalf("error body lacks \"error\": %v", body)
	}
	return msg
}

func TestTokenBootstrap_SetsCookieAndRedirects(t *testing.T) {
	h := newHarness(t)
	if h.s.Token() != fixedToken {
		t.Fatalf("token = %q, want fixed %q", h.s.Token(), fixedToken)
	}
	resp := h.do(h.newReq(http.MethodGet, "/?token="+fixedLaunch, nil))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", resp.StatusCode)
	}
	// The launch token is single-use: a replay (history, scrollback, a ps
	// snapshot of the launcher) is refused and sets no cookie.
	if again := h.do(h.newReq(http.MethodGet, "/?token="+fixedLaunch, nil)); again.StatusCode != http.StatusForbidden || len(again.Cookies()) != 0 {
		t.Fatalf("replayed launch token: status %d cookies %v, want 403 and none", again.StatusCode, again.Cookies())
	}
	// The CSRF token itself is not a launch token.
	if wrong := h.do(h.newReq(http.MethodGet, "/?token="+fixedToken, nil)); wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("CSRF token accepted as launch token: %d", wrong.StatusCode)
	}
	// The CSRF token rides in the redirect's fragment and nowhere else: a
	// fragment never reaches a server, and the page itself (fetched with the
	// cookie alone) must not carry it.
	if loc := resp.Header.Get("Location"); loc != "/#csrf="+fixedToken {
		t.Fatalf("Location %q, want /#csrf=<token>", loc)
	}
	var c *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == h.s.cookieName() {
			c = ck
		}
	}
	if c == nil {
		t.Fatalf("no %s cookie set; Set-Cookie=%v", h.s.cookieName(), resp.Header["Set-Cookie"])
	}
	// The cookie is a secret of its own: cookies are host- not port-scoped, so
	// a value equal to the CSRF token would hand the whole session to any
	// other 127.0.0.1 server the user browses to.
	if c.Value != fixedCookie {
		t.Errorf("cookie value %q, want the session cookie %q", c.Value, fixedCookie)
	}
	if c.Value == h.s.Token() || c.Value == fixedLaunch {
		t.Error("cookie value must differ from the CSRF and launch tokens")
	}
	if !c.HttpOnly {
		t.Error("cookie not HttpOnly")
	}
	// Lax on purpose: the file:// redirect page the launcher uses makes the
	// bootstrap navigation cross-site, and Strict would drop the cookie on the
	// 303's follow-up request (seen live as a 401 after "Open dashboard").
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	if c.Secure {
		t.Error("cookie must not be Secure over plain loopback http")
	}
}

func TestTokenBootstrap_WrongTokenRejected(t *testing.T) {
	h := newHarness(t)
	resp := h.do(h.newReq(http.MethodGet, "/?token=deadbeef", nil))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Fatalf("cookie set on bad token: %v", resp.Cookies())
	}
}

func TestIndex_RequiresCookie(t *testing.T) {
	h := newHarness(t)
	resp := h.do(h.newReq(http.MethodGet, "/", nil))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	if body := string(readBody(t, resp)); strings.Contains(body, fixedToken) {
		t.Fatal("401 page leaks the token")
	}
}

// The page is fetched with the cookie alone, and a 127.0.0.1 cookie reaches
// every loopback port, so the page must not carry the CSRF token: whoever
// holds the cookie would otherwise read the second factor from it.
func TestIndex_CookieOnlyPageCarriesNoToken(t *testing.T) {
	h := newHarness(t)
	resp := h.do(h.withCookie(h.newReq(http.MethodGet, "/", nil)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type %q", ct)
	}
	body := string(readBody(t, resp))
	if strings.Contains(body, fixedToken) || strings.Contains(body, `name="csrf"`) {
		t.Fatalf("the cookie-gated page carries the CSRF token; body head: %.300s", body)
	}
	if strings.Contains(body, "{{") {
		t.Fatal("index left template markers unexpanded")
	}
	for _, hdr := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options"} {
		if resp.Header.Get(hdr) == "" {
			t.Errorf("missing %s header", hdr)
		}
	}
}

// The attack the fragment design closes: a cookie leaked to another loopback
// port is replayed against the page, which used to carry the CSRF token in a
// <meta>. Now the page yields no usable token and a cookie-only client cannot
// reach a mutating route.
func TestCookieOnlyClientCannotMutate(t *testing.T) {
	h := newHarness(t)
	page := string(readBody(t, h.do(h.withCookie(h.newReq(http.MethodGet, "/", nil)))))
	for _, cand := range regexp.MustCompile(`[0-9a-f]{32}`).FindAllString(page, -1) {
		if cand == fixedToken {
			t.Fatal("the page leaks the CSRF token to a cookie-only client")
		}
	}
	for _, asset := range []string{"/static/app.js", "/static/style.css"} {
		if b := readBody(t, h.do(h.withCookie(h.newReq(http.MethodGet, asset, nil)))); strings.Contains(string(b), fixedToken) {
			t.Fatalf("%s leaks the CSRF token to a cookie-only client", asset)
		}
	}
	for _, r := range mutatingRoutes() {
		resp := h.do(h.withCookie(h.newReq(r.method, r.path, strings.NewReader("{}"))))
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with the cookie alone: status %d, want 403", r.method, r.path, resp.StatusCode)
		}
	}
	facadesUntouched(t, h, false)
}

func TestAPI_MissingCookie401_EveryRoute(t *testing.T) {
	h := newHarness(t)
	for _, r := range allRoutes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			req := h.newReq(r.method, r.path, nil)
			h.withCSRF(req) // CSRF alone must not suffice
			resp := h.do(req)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", resp.StatusCode)
			}
			decodeError(t, resp)
		})
	}
	facadesUntouched(t, h, false)
}

func TestAPI_WrongCookie401(t *testing.T) {
	h := newHarness(t)
	req := h.newReq(http.MethodGet, "/api/state", nil)
	req.AddCookie(&http.Cookie{Name: h.s.cookieName(), Value: strings.Repeat("f", len(fixedToken))})
	if resp := h.do(req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	req = h.newReq(http.MethodGet, "/api/state", nil)
	req.AddCookie(&http.Cookie{Name: h.s.cookieName(), Value: fixedToken[:10]})
	if resp := h.do(req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("short cookie status %d, want 401", resp.StatusCode)
	}
}

func TestPOST_MissingOrWrongCSRF403_EveryRoute(t *testing.T) {
	h := newHarness(t)
	for _, r := range mutatingRoutes() {
		t.Run("missing "+r.path, func(t *testing.T) {
			resp := h.do(h.withCookie(h.newReq(r.method, r.path, nil)))
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status %d, want 403", resp.StatusCode)
			}
			decodeError(t, resp)
		})
		t.Run("wrong "+r.path, func(t *testing.T) {
			req := h.withCookie(h.newReq(r.method, r.path, nil))
			req.Header.Set(csrfHeader, strings.Repeat("0", len(fixedToken)))
			resp := h.do(req)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status %d, want 403", resp.StatusCode)
			}
		})
	}
	facadesUntouched(t, h, false)
}

func TestPOST_OriginAndSecFetchSite403_EveryMutatingRoute(t *testing.T) {
	h := newHarness(t)
	for _, r := range mutatingRoutes() {
		t.Run("origin "+r.method+" "+r.path, func(t *testing.T) {
			req := h.authed(h.newReq(r.method, r.path, nil))
			req.Header.Set("Origin", "http://evil.example")
			if resp := h.do(req); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status %d, want 403", resp.StatusCode)
			}
		})
		t.Run("sfs "+r.method+" "+r.path, func(t *testing.T) {
			req := h.authed(h.newReq(r.method, r.path, nil))
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			if resp := h.do(req); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status %d, want 403", resp.StatusCode)
			}
		})
	}
	facadesUntouched(t, h, false)
}

func TestPOST_OriginRules(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name   string
		origin string
		want   int
	}{
		{"own origin", h.base, http.StatusOK},
		{"own origin upper-case scheme host", strings.ToUpper(h.base), http.StatusOK},
		{"no origin", "", http.StatusOK},
		{"other port", "http://127.0.0.1:1", http.StatusForbidden},
		{"other host", "http://evil.example", http.StatusForbidden},
		{"https same host", strings.Replace(h.base, "http://", "https://", 1), http.StatusForbidden},
		{"null", "null", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := h.authed(h.newReq(http.MethodPost, "/api/accounts/claude:2/enable", nil))
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			resp := h.do(req)
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d (%s)", resp.StatusCode, tc.want, readBody(t, resp))
			}
		})
	}
}

func TestPOST_SecFetchSiteRules(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		sfs  string
		want int
	}{
		{"", http.StatusOK},
		{"same-origin", http.StatusOK},
		{"none", http.StatusOK},
		{"cross-site", http.StatusForbidden},
		{"same-site", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run("sfs="+tc.sfs, func(t *testing.T) {
			req := h.authed(h.newReq(http.MethodPost, "/api/accounts/claude:2/enable", nil))
			if tc.sfs != "" {
				req.Header.Set("Sec-Fetch-Site", tc.sfs)
			}
			resp := h.do(req)
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestGET_NeedsCSRFButIgnoresOrigin(t *testing.T) {
	// Reads need BOTH factors — the cookie alone may have leaked to another
	// loopback port — but not the Origin / Sec-Fetch-Site rules, which only
	// guard writes.
	h := newHarness(t)
	req := h.withCookie(h.newReq(http.MethodGet, "/api/state", nil))
	req.Header.Set(csrfHeader, h.s.Token())
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp := h.do(req); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET with cookie+CSRF status %d, want 200", resp.StatusCode)
	}
	// Cookie alone: refused.
	if resp := h.do(h.withCookie(h.newReq(http.MethodGet, "/api/state", nil))); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET with cookie only status %d, want 403", resp.StatusCode)
	}
	// CSRF alone (no cookie): refused.
	bare := h.newReq(http.MethodGet, "/api/state", nil)
	bare.Header.Set(csrfHeader, h.s.Token())
	if resp := h.do(bare); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET with CSRF only status %d, want 401", resp.StatusCode)
	}
	// EventSource cannot set headers: the token may ride in ?csrf= on the
	// event stream, and on no other route — a token in a URL leaks.
	if resp := h.do(h.withCookie(h.newReq(http.MethodGet, "/api/state?csrf="+h.s.Token(), nil))); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /api/state with ?csrf= status %d, want 403", resp.StatusCode)
	}
	ev := h.do(h.withCookie(h.newReq(http.MethodGet, "/api/events?csrf="+h.s.Token(), nil)))
	if ev.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/events with ?csrf= status %d, want 200", ev.StatusCode)
	}
	_ = ev.Body.Close()
	// …but never on a mutation.
	post := h.withCookie(h.newReq(http.MethodPost, "/api/accounts/add?csrf="+h.s.Token(), strings.NewReader("{}")))
	if resp := h.do(post); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST with ?csrf= status %d, want 403", resp.StatusCode)
	}
}

func TestBadHost421(t *testing.T) {
	h := newHarness(t)
	port := h.base[strings.LastIndex(h.base, ":")+1:]
	cases := []struct {
		host string
		want int
	}{
		{"127.0.0.1:" + port, http.StatusOK},
		{"localhost:" + port, http.StatusOK},
		{"LOCALHOST:" + port, http.StatusMisdirectedRequest},
		{"127.0.0.1:1", http.StatusMisdirectedRequest},
		{"127.0.0.1", http.StatusMisdirectedRequest},
		{"localhost", http.StatusMisdirectedRequest},
		{"evil.example:" + port, http.StatusMisdirectedRequest},
		{"10.0.0.7:" + port, http.StatusMisdirectedRequest},
		{"[::1]:" + port, http.StatusMisdirectedRequest},
		{"127.0.0.1.nip.io:" + port, http.StatusMisdirectedRequest},
	}
	for _, tc := range cases {
		t.Run("host="+tc.host, func(t *testing.T) {
			req := h.authed(h.newReq(http.MethodGet, "/api/state", nil))
			req.Host = tc.host
			resp := h.do(req)
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want == http.StatusMisdirectedRequest {
				decodeError(t, resp)
			}
		})
	}
	// The Host check precedes auth and applies to the page and static files too.
	req := h.newReq(http.MethodGet, "/static/app.js", nil)
	req.Host = "evil.example:" + port
	if resp := h.do(req); resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("static with bad host: %d, want 421", resp.StatusCode)
	}
}

func TestHostAllowed_Unit(t *testing.T) {
	// The HTTP client substitutes the URL host for an empty req.Host, so the
	// empty / malformed cases are checked on the predicate directly.
	s, _ := New(Deps{Facade: &fakeFacade{}})
	for _, bad := range []string{"", "127.0.0.1", "localhost", ":", "127.0.0.1:", "[::1]:80", "127.0.0.1:80:80", "user@127.0.0.1:80"} {
		if s.hostAllowed(bad) {
			t.Errorf("hostAllowed(%q) = true before Start", bad)
		}
	}
	// Before Start any port on the two allowed hosts passes (handler mounted
	// elsewhere); after Start only the bound port does.
	if !s.hostAllowed("127.0.0.1:12345") || !s.hostAllowed("localhost:1") {
		t.Error("pre-Start host check must accept any port on loopback names")
	}
	if _, err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })
	p := strconv.Itoa(s.Port())
	if !s.hostAllowed("127.0.0.1:"+p) || !s.hostAllowed("localhost:"+p) {
		t.Error("bound port refused")
	}
	if s.hostAllowed("127.0.0.1:12345") || s.hostAllowed("localhost:1") {
		t.Error("foreign port accepted after Start")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newHarness(t)
	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/switch/claude:1"},
		{http.MethodDelete, "/api/state"},
		{http.MethodPut, "/api/auto/start"},
	}
	for _, tc := range cases {
		resp := h.do(h.authed(h.newReq(tc.method, tc.path, nil)))
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status %d, want 405", tc.method, tc.path, resp.StatusCode)
		}
	}
	facadesUntouched(t, h, false)
}

func TestUnknownAPIRoute404(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/api/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
	resp = h.do(h.newReq(http.MethodGet, "/api/nope", nil))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated unknown api route: %d, want 401 (auth precedes routing)", resp.StatusCode)
	}
}
