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

// Concurrent redemptions of the launch token agree on exactly one winner, and
// the token is dead for everyone afterwards. Run with -race.
func TestConsumeLaunchConcurrent(t *testing.T) {
	h := newHarness(t)
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok := h.s.consumeLaunch(fixedLaunch)
			_ = h.s.URL()
			if ok {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d of %d concurrent redemptions succeeded, want exactly 1", won, n)
	}
	if h.s.consumeLaunch(fixedLaunch) {
		t.Fatal("the token redeemed again after the race")
	}
	if resp := h.do(h.newReq(http.MethodGet, "/?token="+fixedLaunch, nil)); resp.StatusCode != http.StatusForbidden || len(resp.Cookies()) != 0 {
		t.Fatalf("redeemed token over HTTP: %d with cookies %v, want 403 and none", resp.StatusCode, resp.Cookies())
	}
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
// state document for the batch, not one per event. The burst is queued while
// Serve is held inside a tick's state build, so the drain loop sees every
// event at once.
func TestAutoEventsCoalesced(t *testing.T) {
	const burst = 5
	h := newHarness(t, withAutoEventBuffer(burst))
	st := h.openSSE()
	defer st.close()
	st.nextState(t, timeout) // the subscriber's initial state

	gate := make(chan struct{})
	h.fa.mu.Lock()
	h.fa.gate = gate
	h.fa.mu.Unlock()
	h.fireTick() // Serve is now blocked in buildState, behind the gate
	for i := 0; i < burst; i++ {
		h.fireAuto(AutoEventView{At: float64(10 + i), Kind: "poll"})
	}
	h.fa.mu.Lock()
	h.fa.gate = nil
	h.fa.mu.Unlock()
	close(gate)

	// The tick's state, then burst `auto` frames, then one state for them all.
	autos, states := 0, 0
	for autos < burst || states < 2 {
		switch ev := st.next(t, timeout); ev.name {
		case "auto":
			autos++
		case "state":
			states++
		}
	}
	st.expectNone(t, 200*time.Millisecond)
	if autos != burst || states != 2 {
		t.Fatalf("%d auto frames and %d state frames for a burst of %d events, want %d and 2 (one for the tick, one for the batch)", autos, states, burst, burst)
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
	// A record whose file name disagrees with its pid field was not written
	// by that process (Claude Code writes sessions/<pid>.json): not listed,
	// so it can never be stopped as that pid.
	forged := `{"pid":` + strconv.Itoa(pid) + `,"sessionId":"forged","cwd":"/elsewhere","startedAt":1,"kind":"interactive","entrypoint":"cli"}`
	if err := os.WriteFile(filepath.Join(prof, "sessions", "999999.json"), []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	v := SessionsIn(backup)()
	for _, c := range v.Claude {
		if c.SessionID == "forged" {
			t.Fatalf("a session file named 999999.json carrying pid %d was listed: %+v", pid, c)
		}
	}
	// The test's own PID is alive on every platform (kill(pid, 0) on unix,
	// OpenProcess on Windows), and the file is named after it, so the
	// profile session must be listed; anything else is a regression.
	var found *procdetect.ClaudeSession
	for i := range v.Claude {
		if v.Claude[i].PID == pid {
			found = &v.Claude[i]
		}
	}
	if found == nil {
		t.Fatalf("the profile session for this process (pid %d) was not listed: %+v", pid, v)
	}
	if found.SessionID != "sess-run" || found.CWD != "/work/run" {
		t.Fatalf("listed session %+v, want the profile's record", *found)
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

// An IDE instance whose lock file appears under the default directory and
// under a session profile is one instance, listed once; a different port is
// another instance.
func TestSessionsInDedupsIDEInstances(t *testing.T) {
	backup := t.TempDir()
	def := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", def)
	pid := strconv.Itoa(os.Getpid())
	lock := func(dir, port, folder string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "ide"), 0o700); err != nil {
			t.Fatal(err)
		}
		body := `{"pid":` + pid + `,"ideName":"Editor","workspaceFolders":["` + folder + `"]}`
		if err := os.WriteFile(filepath.Join(dir, "ide", port+".lock"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prof := filepath.Join(backup, "sessions", "2-bob_example.com")
	lock(def, "51234", "/work/repo")
	lock(prof, "51234", "/work/repo") // the same instance, seen from the profile
	lock(prof, "51235", "/work/other")
	v := SessionsIn(backup)()
	if len(v.IDE) != 2 {
		t.Fatalf("IDE instances %+v, want the one on 51234 once and the one on 51235", v.IDE)
	}
	ports := map[int]int{}
	for _, i := range v.IDE {
		ports[i.Port]++
	}
	if ports[51234] != 1 || ports[51235] != 1 {
		t.Fatalf("ports %v, want 51234 and 51235 once each", ports)
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
	// A key may be bare (style:) or quoted ('style':, "onclick":); both
	// forms must be caught, and a key is what follows "{", "," or space.
	unsafeKey := regexp.MustCompile(`el\('[a-z]+', \{[^}]*[\s{,]['"]?(href|src|srcdoc|style|formaction|action|on[a-z]+)['"]?\s*:`)
	if m := unsafeKey.FindString(js); m != "" {
		t.Fatalf("a call site passes an unsafe attribute: %s", m)
	}
	// The check itself must see every spelling.
	for _, bad := range []string{
		`el('a', { href: u })`, `el('a', { 'href': u })`, `el('a', { "href": u })`,
		`el('img', { class: 'x', src: u })`, `el('div', { text: t, 'style': s })`, `el('b', { onclick: f })`,
	} {
		if !unsafeKey.MatchString(bad) {
			t.Errorf("the unsafe-attribute check misses %s", bad)
		}
	}
	for _, ok := range []string{`el('details', { class: 'group', open: true })`, `el('span', { 'data-started': s, text: '' })`, `el('span', { class: 'action-x', text: 'icon' })`} {
		if unsafeKey.MatchString(ok) {
			t.Errorf("the unsafe-attribute check flags a safe call site: %s", ok)
		}
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
