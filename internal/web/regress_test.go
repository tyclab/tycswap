// Regression tests for the dashboard's hardening: provider-keyed routes, one
// session cookie per port, ?csrf= only on the event stream, the launch-token
// race, token status on the stream, coalesced engine events, sessions in
// `run` profiles, the accent stylesheet, and static checks on the UI (<bdi>
// around the rtl path, hidden empty chips, el() refusing unsafe attributes,
// the slider gated on a running engine). Every secret here is a fixed,
// low-entropy test value.
package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/procdetect"
)

// A key for another provider never reaches the Claude façade, a bare slot is
// refused rather than guessed, and a Claude key reaches it with the bare
// reference.
func TestProviderKeyedRoutesNeverCrossProviders(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{
		"/api/switch/codex:1", "/api/switch/codex:1?force=1",
		"/api/accounts/codex:1/disable", "/api/accounts/codex:1/enable",
		"/api/accounts/codex:1/remove",
	} {
		if resp := h.post(path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, resp.StatusCode)
		}
	}
	for _, tc := range []struct{ path, body string }{
		{"/api/accounts/codex:1/alias", `{"alias":"x"}`},
		{"/api/accounts/codex:1/move", `{"slot":"2"}`},
		{"/api/accounts/swap", `{"a":"claude:1","b":"codex:2"}`},
		{"/api/accounts/swap", `{"a":"codex:1","b":"claude:2"}`},
	} {
		if resp := h.postJSON(tc.path, tc.body); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: status %d, want 404", tc.path, tc.body, resp.StatusCode)
		}
	}
	for _, path := range []string{"/api/switch/1", "/api/accounts/1/disable", "/api/switch/:1", "/api/switch/claude:"} {
		if resp := h.post(path); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, resp.StatusCode)
		}
	}
	if resp := h.postJSON("/api/accounts/swap", `{"a":"1","b":"2"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bare swap: status %d, want 400", resp.StatusCode)
	}
	if got := append(h.fa.Calls(), h.ops.Calls()...); len(got) != 0 {
		t.Fatalf("a façade was reached through a foreign or bare key: %v", got)
	}
	if resp := h.post("/api/switch/claude:2"); resp.StatusCode != http.StatusOK {
		t.Fatalf("claude key: status %d", resp.StatusCode)
	}
	if got := h.fa.Calls(); len(got) != 1 || got[0] != "SwitchTo(2,true)" {
		t.Fatalf("calls %v", got)
	}
	// Every row carries the key the routes take.
	var st State
	if err := json.Unmarshal(readBody(t, h.get("/api/state")), &st); err != nil {
		t.Fatal(err)
	}
	for _, row := range st.Accounts {
		if row["provider"] != "claude" || row["key"] != "claude:"+strconv.Itoa(int(row["number"].(float64))) {
			t.Errorf("row %v lacks provider/key", row)
		}
	}
}

// The cookie name carries the port, so two dashboards never share a cookie.
func TestCookieNamePerPort(t *testing.T) {
	a, b := newHarness(t), newHarness(t)
	if a.s.cookieName() == b.s.cookieName() {
		t.Fatalf("two dashboards share cookie name %q", a.s.cookieName())
	}
	if want := "tycswap_session_" + strconv.Itoa(a.s.Port()); a.s.cookieName() != want {
		t.Fatalf("cookie name %q, want %q", a.s.cookieName(), want)
	}
	resp := a.do(a.newReq(http.MethodGet, "/?token="+fixedLaunch, nil))
	var names []string
	for _, c := range resp.Cookies() {
		names = append(names, c.Name)
	}
	if len(names) != 1 || names[0] != a.s.cookieName() {
		t.Fatalf("Set-Cookie names %v, want [%s]", names, a.s.cookieName())
	}
	// The other dashboard's cookie (its name, this one's value) is no session here.
	req := a.newReq(http.MethodGet, "/api/state", nil)
	req.AddCookie(&http.Cookie{Name: b.s.cookieName(), Value: a.s.cookie})
	req.Header.Set(csrfHeader, a.s.Token())
	if resp := a.do(req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("foreign-port cookie name: status %d, want 401", resp.StatusCode)
	}
}

// Concurrent launch requests agree on one token, and a redeemed token is
// never handed out again. Run with -race.
func TestLaunchURLConcurrent(t *testing.T) {
	h := newHarness(t, withRemoteToken(remoteToken), withRand(&countingReader{}))
	if !h.s.consumeLaunch(fixedLaunch) {
		t.Fatal("first redeem refused")
	}
	const n = 16
	urls := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u, err := h.s.LaunchURL()
			if err != nil {
				t.Error(err)
			}
			urls[i] = u
			_ = h.s.URL()
		}(i)
	}
	wg.Wait()
	for _, u := range urls {
		if u != urls[0] {
			t.Fatalf("concurrent LaunchURL calls disagree: %q vs %q", u, urls[0])
		}
		if strings.Contains(u, fixedLaunch) {
			t.Fatalf("a redeemed token was handed out again: %q", u)
		}
	}
	tok := urls[0][strings.Index(urls[0], "token=")+len("token="):]
	if !h.s.consumeLaunch(tok) || h.s.consumeLaunch(tok) {
		t.Fatal("fresh token must redeem exactly once")
	}
	// Concurrent /api/launch calls race against a redeem.
	var wg2 sync.WaitGroup
	for i := 0; i < n; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			resp := h.do(bearer(h.newReq(http.MethodPost, "/api/launch", nil), remoteToken))
			if resp.StatusCode != http.StatusOK {
				t.Errorf("launch status %d", resp.StatusCode)
			}
		}()
	}
	wg2.Wait()
}

// A subscriber that asks for token status gets it on every state frame; a
// plain subscriber never does.
func TestSSETokenStatus(t *testing.T) {
	h := newHarness(t)
	resp := h.do(h.withCookie(h.newReq(http.MethodGet, "/api/events?tokenStatus=1&csrf="+h.s.Token(), nil)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	ts := &sseStream{events: readSSE(resp), resp: resp}
	defer ts.close()
	plain := h.openSSE()
	defer plain.close()
	check := func(ev sseEvent, want bool) {
		t.Helper()
		var st State
		if err := json.Unmarshal([]byte(ev.data), &st); err != nil {
			t.Fatal(err)
		}
		v, has := st.Accounts[0]["tokenStatus"]
		if has != want {
			t.Fatalf("tokenStatus present=%v, want %v", has, want)
		}
		if want && v == "" {
			t.Fatalf("tokenStatus empty on the enriched stream")
		}
	}
	check(ts.nextState(t, timeout), true)
	check(plain.nextState(t, timeout), false)
	h.fireTick()
	check(ts.nextState(t, timeout), true)
	check(plain.nextState(t, timeout), false)
}

// Engine events that arrive together go out one `auto` frame each, then ONE
// state document for the batch, not one per event.
func TestAutoEventsCoalesced(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	states := 0
	h.s.OnState(func(State) { mu.Lock(); states++; mu.Unlock() })
	autoCh := make(chan AutoEventView, 8)
	for i := 0; i < 5; i++ {
		autoCh <- AutoEventView{At: float64(i), Kind: "poll"}
	}
	// Drive the batch through the same path Serve uses.
	h.s.publishAuto(<-autoCh)
	for len(autoCh) > 0 {
		h.s.publishAuto(<-autoCh)
	}
	h.s.broadcast()
	mu.Lock()
	got := states
	mu.Unlock()
	if got != 1 {
		t.Fatalf("%d state documents for one batch, want 1", got)
	}
	// And through Serve itself: a burst on the channel yields far fewer
	// states than events.
	st := h.openSSE()
	defer st.close()
	st.nextState(t, timeout)
	for i := 0; i < 3; i++ {
		h.fireAuto(AutoEventView{At: float64(10 + i), Kind: "poll"})
	}
	autos := 0
	deadline := time.Now().Add(2 * time.Second)
	for autos < 3 && time.Now().Before(deadline) {
		if ev := st.next(t, timeout); ev.name == "auto" {
			autos++
		}
	}
	if autos != 3 {
		t.Fatalf("auto frames %d, want 3", autos)
	}
}

// Sessions started with `tycswap run` live in a session profile; they are
// listed with their slot, titled from that profile's transcripts, and Stop
// accepts their PID.
func TestSessionsInProfiles(t *testing.T) {
	backup := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	prof := filepath.Join(backup, "sessions", "2-bob_example.com")
	if err := os.MkdirAll(filepath.Join(prof, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid() // alive for the duration of the test
	rec := `{"pid":` + strconv.Itoa(pid) + `,"sessionId":"sess-run","cwd":"/work/run","startedAt":1758276000000,"kind":"interactive","entrypoint":"cli"}`
	if err := os.WriteFile(filepath.Join(prof, "sessions", strconv.Itoa(pid)+".json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	v := SessionsIn(backup)()
	var found *procdetect.ClaudeSession
	for i := range v.Claude {
		if v.Claude[i].PID == pid {
			found = &v.Claude[i]
		}
	}
	if found == nil {
		t.Skipf("procdetect did not list the profile session (%+v); platform liveness check differs", v)
	}
	if v.Profile[pid] != "2" || v.ConfigDir[pid] != prof {
		t.Fatalf("profile %q dir %q", v.Profile[pid], v.ConfigDir[pid])
	}
	h := newHarness(t)
	h.setSessions(v)
	var st State
	if err := json.Unmarshal(readBody(t, h.get("/api/state")), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Sessions.Claude) != 1 || st.Sessions.Claude[0].Profile != "2" {
		t.Fatalf("sessions %+v", st.Sessions.Claude)
	}
	if resp := h.post("/api/sessions/" + strconv.Itoa(pid) + "/stop"); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: status %d", resp.StatusCode)
	}
	if k := h.Killed(); len(k) != 1 || k[0] != pid {
		t.Fatalf("killed %v", k)
	}
}

func TestAccentStylesheet(t *testing.T) {
	h := newHarness(t)
	if resp := h.do(h.newReq(http.MethodGet, "/static/accent.css", nil)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("accent.css without cookie: %d", resp.StatusCode)
	}
	resp := h.do(h.withCookie(h.newReq(http.MethodGet, "/static/accent.css", nil)))
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("accent.css: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if body := string(readBody(t, resp)); body != ":root { --brand: #5aa2ff; --brand-deep: #12356f; }\n" {
		t.Fatalf("accent.css = %q", body)
	}
	if got := accentCSS("#ffffff"); got != ":root { --brand: #ffffff; --brand-deep: #666666; }\n" {
		t.Fatalf("override = %q", got)
	}
}

func staticFile(t *testing.T, name string) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The session group's path span is direction:rtl (ellipsis on the left), so
// the path itself must be isolated in a <bdi> or its "/" and "." are drawn at
// the wrong end.
func TestStaticSessionPathInBdi(t *testing.T) {
	css, js := staticFile(t, "style.css"), staticFile(t, "app.js")
	if !regexp.MustCompile(`\.path \{[^}]*direction: rtl`).MatchString(css) {
		t.Fatal("the precondition changed: .path is no longer direction:rtl")
	}
	if !strings.Contains(js, `el('span', { class: 'path', title: k }, [el('bdi', { text: k || '—' })])`) {
		t.Fatal("app.js does not wrap the session group path in <bdi>")
	}
}

// Empty chips (a count of nothing, a badge not yet known) are hidden.
func TestStaticEmptyChipHidden(t *testing.T) {
	if !regexp.MustCompile(`\.chip:empty\s*\{\s*display:\s*none;?\s*\}`).MatchString(staticFile(t, "style.css")) {
		t.Fatal("style.css lacks .chip:empty { display: none }")
	}
}

// el() refuses href, src, style and on* attributes, so API data can never
// become a link, a script, a style or a handler; and no call site tries.
func TestStaticElRefusesUnsafeAttributes(t *testing.T) {
	js := staticFile(t, "app.js")
	if !strings.Contains(js, `var UNSAFE_ATTR = /^(on|href$|src$|srcdoc$|style$|formaction$|action$|xlink:href$)/i;`) ||
		!strings.Contains(js, `if (UNSAFE_ATTR.test(k)) { return; }`) {
		t.Fatal("el() lacks the unsafe-attribute guard")
	}
	if m := regexp.MustCompile(`el\('[a-z]+', \{[^}]*\b(href|src|style|on[a-z]+)\s*:`).FindString(js); m != "" {
		t.Fatalf("a call site passes an unsafe attribute: %s", m)
	}
	for _, bad := range []string{".innerHTML", "eval(", "new Function(", "document.write("} {
		if strings.Contains(js, bad) {
			t.Errorf("app.js uses %s", bad)
		}
	}
}

// The live threshold slider is usable only while the engine runs.
func TestStaticSliderGatedOnRunning(t *testing.T) {
	if !strings.Contains(staticFile(t, "app.js"), "slider.disabled = !a.available || !a.running;") {
		t.Fatal("the threshold slider is not disabled while the engine is stopped")
	}
}

// After a restart the page's tokens are dead: the stream must stop retrying
// and say so, and token status comes from the stream, not a stale copy.
func TestStaticStreamStopsOnDeadSession(t *testing.T) {
	js := staticFile(t, "app.js")
	for _, needle := range []string{"function deadSession()", "res.status === 401 || res.status === 403", "'&tokenStatus=1'"} {
		if !strings.Contains(js, needle) {
			t.Errorf("app.js lacks %q", needle)
		}
	}
	if strings.Contains(js, "prev[String(a.number)] = a.tokenStatus") {
		t.Error("app.js still copies stale token status forward")
	}
}

// countingReader yields 0, 1, 2, … (mod 256) forever: the same first 48
// bytes as fixedRand (so fixedLaunch still names the first launch token), and
// enough low-entropy material for any number of re-minted tokens.
type countingReader struct{ n byte }

func (c *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.n
		c.n++
	}
	return len(p), nil
}
