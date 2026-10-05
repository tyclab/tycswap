// Tests for construction defaults, the loopback-only bind, Serve's lifecycle,
// the embedded static assets, and the offline-only sanity check on index.html.
package web

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestNew_RequiresFacade(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("New with nil Facade must fail")
	}
}

func TestNew_DefaultsAndToken(t *testing.T) {
	s, err := New(Deps{Facade: &fakeFacade{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Token()) != 32 {
		t.Fatalf("token %q is not 128 bits hex", s.Token())
	}
	if _, err := regexp.MatchString("^[0-9a-f]{32}$", s.Token()); err != nil {
		t.Fatal(err)
	}
	if ok, _ := regexp.MatchString("^[0-9a-f]{32}$", s.Token()); !ok {
		t.Fatalf("token %q not lowercase hex", s.Token())
	}
	if s.d.Interval != defaultPeriod || s.d.Clock == nil || s.d.Rand == nil || s.d.Logger == nil || s.d.Sessions == nil || s.d.Kill == nil || s.d.Ticker == nil {
		t.Fatalf("defaults not applied: %+v", s.d)
	}
	if s.URL() != "" || s.Port() != 0 {
		t.Fatalf("URL/Port before Start: %q %d", s.URL(), s.Port())
	}
	// Two servers never share a token.
	s2, _ := New(Deps{Facade: &fakeFacade{}})
	if s2.Token() == s.Token() {
		t.Fatal("tokens collide")
	}
}

func TestNew_RandFailure(t *testing.T) {
	_, err := New(Deps{Facade: &fakeFacade{}, Rand: strings.NewReader("short")})
	if err == nil {
		t.Fatal("short Rand must fail New")
	}
}

func TestStart_LoopbackOnly(t *testing.T) {
	s, err := New(Deps{Facade: &fakeFacade{}, Rand: fixedRand()})
	if err != nil {
		t.Fatal(err)
	}
	url, err := s.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })
	addr := s.ln.Addr().(*net.TCPAddr)
	if !addr.IP.IsLoopback() {
		t.Fatalf("listener %s is not loopback", addr)
	}
	if addr.Port == 0 || s.Port() != addr.Port {
		t.Fatalf("port mismatch %d vs %d", s.Port(), addr.Port)
	}
	want := "http://127.0.0.1:" + itoa(addr.Port) + "/?token=" + fixedLaunch
	if url != want {
		t.Fatalf("url %q, want %q", url, want)
	}
	if s.URL() != url {
		t.Fatalf("URL() %q", s.URL())
	}
}

func TestStart_EmptyAddrDefaultsToLoopback(t *testing.T) {
	s, _ := New(Deps{Facade: &fakeFacade{}})
	url, err := s.Start("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("url %q", url)
	}
}

func TestStart_RefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "[::]:0", "192.168.1.10:0", "example.com:0", "nonsense"} {
		s, _ := New(Deps{Facade: &fakeFacade{}})
		if _, err := s.Start(addr); err == nil {
			_ = s.ln.Close()
			t.Errorf("Start(%q) succeeded; must refuse non-loopback", addr)
		}
	}
}

func TestServe_BeforeStartErrors(t *testing.T) {
	s, _ := New(Deps{Facade: &fakeFacade{}})
	if err := s.Serve(context.Background()); err == nil {
		t.Fatal("Serve before Start must fail")
	}
}

func TestServe_StopsOnCancelAndBroadcastsOnDefaultTicker(t *testing.T) {
	testutil.IsolateHome(t) // the default sessions source reads ~/.claude
	// Real time.Ticker seam with a short interval: the loop must broadcast on
	// its own and Serve must return nil once the context is cancelled.
	s, err := New(Deps{Facade: &fakeFacade{snap: sampleSnapshot()}, Interval: 10 * time.Millisecond, Clock: clock.NewFake(testNow)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	ch, unsub := s.hub.subscribe(false)
	defer unsub()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatal("default ticker never broadcast")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(timeout):
		t.Fatal("Serve did not stop on cancel")
	}
	// Listener is closed after Serve returns.
	if _, err := net.DialTimeout("tcp", s.ln.Addr().String(), 200*time.Millisecond); err == nil {
		t.Fatal("listener still accepting after Serve returned")
	}
}

func TestStatic_ContentTypesAndCookieGated(t *testing.T) {
	h := newHarness(t)
	cases := []struct{ path, ct string }{
		{"/static/app.js", "text/javascript; charset=utf-8"},
		{"/static/style.css", "text/css; charset=utf-8"},
	}
	for _, tc := range cases {
		// Without the cookie a page could fingerprint the dashboard's port by
		// loading an asset; with it (no CSRF header — <script> cannot send one)
		// the asset is served.
		if resp := h.do(h.newReq(http.MethodGet, tc.path, nil)); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without cookie: status %d, want 401", tc.path, resp.StatusCode)
		}
		resp := h.do(h.withCookie(h.newReq(http.MethodGet, tc.path, nil)))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", tc.path, resp.StatusCode)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); ct != tc.ct {
			t.Errorf("%s: content-type %q, want %q", tc.path, ct, tc.ct)
		}
		body := readBody(t, resp)
		if len(body) == 0 {
			t.Errorf("%s: empty body", tc.path)
		}
		if strings.Contains(string(body), fixedToken) {
			t.Errorf("%s: contains the token", tc.path)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", tc.path)
		}
	}
}

func TestStatic_RawIndexNotServedAndNoListing(t *testing.T) {
	// The raw template must not be reachable (it bypasses the guarded index
	// handler), and the directory must not list.
	h := newHarness(t)
	for _, p := range []string{"/static/", "/static", "/static/index.html", "/static/missing.js", "/static/../web.go"} {
		resp := h.do(h.newReq(http.MethodGet, p, nil))
		if resp.StatusCode == http.StatusOK {
			body := string(readBody(t, resp))
			if p == "/static/index.html" || strings.Contains(body, "{{") || strings.Contains(body, "<pre>") {
				t.Errorf("%s: served (%d) %.80q", p, resp.StatusCode, body)
			}
		}
	}
	resp := h.do(h.newReq(http.MethodGet, "/static/index.html", nil))
	if resp.StatusCode == http.StatusOK && strings.Contains(string(readBody(t, resp)), "{{.") {
		t.Fatal("raw index template is exposed")
	}
}

func TestIndexHTML_OnlyLocalReferences(t *testing.T) {
	// Offline guarantee: the page references only /static/ paths and no
	// http(s):// URL anywhere in any embedded asset.
	entries := map[string][]byte{}
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := staticFS.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		entries[p] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"static/index.html", "static/app.js", "static/style.css"} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("%s not embedded", name)
		}
	}
	index := string(entries["static/index.html"])
	refs := regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*"([^"]*)"`).FindAllStringSubmatch(index, -1)
	if len(refs) < 3 {
		t.Fatalf("expected favicon+script+stylesheet references, found %d", len(refs))
	}
	for _, m := range refs {
		if strings.HasPrefix(m[1], "#") {
			continue // in-page anchors
		}
		if !strings.HasPrefix(m[1], "/static/") {
			t.Errorf("index references %q; only /static/ paths allowed", m[1])
		}
	}
	// The tycswap mark is one embedded SVG, used as favicon and header logo.
	if !strings.Contains(index, `rel="icon" type="image/svg+xml" href="/static/icon.svg"`) {
		t.Error("index lacks the /static/icon.svg favicon")
	}
	if _, ok := entries["static/icon.svg"]; !ok {
		t.Error("static/icon.svg not embedded")
	}
	// The CSRF token reaches the page through the launch redirect's fragment
	// and lives in sessionStorage; the cookie-gated page must not template it.
	if strings.Contains(index, "csrf") || strings.Contains(index, "{{.CSRF}}") {
		t.Error("index templates the CSRF token; it must travel in the redirect fragment only")
	}
	if !strings.Contains(index, `aria-live=`) {
		t.Error("index lacks a live region for toasts")
	}
	if !strings.Contains(index, `<th scope="col">`) {
		t.Error("tables lack header cells")
	}
	if strings.Contains(index, "<style") || strings.Contains(index, "style=\"") || regexp.MustCompile(`<script[^>]*>[^<]*\S[^<]*</script>`).MatchString(index) {
		t.Error("index has inline style/script, which the CSP forbids")
	}
	// The only http:// allowed anywhere is the SVG XML namespace inside the
	// favicon data URI — an identifier, never fetched.
	urlRe := regexp.MustCompile(`(?i)https?://`)
	for name, b := range entries {
		scrubbed := strings.ReplaceAll(string(b), "http://www.w3.org/2000/svg", "")
		if urlRe.MatchString(scrubbed) {
			t.Errorf("%s contains an http(s):// URL; the UI must work offline", name)
		}
		if strings.Contains(string(b), "@import") || strings.Contains(string(b), "localStorage") {
			t.Errorf("%s uses @import or localStorage", name)
		}
	}
	css := string(entries["static/style.css"])
	for _, needle := range []string{
		"prefers-color-scheme: dark", ":root", `:root[data-theme="dark"]`, `:root:not([data-theme="light"])`, // dark declared both ways
		"prefers-reduced-motion", ":focus-visible",
		".meter", ".meter-track", ".meter-fill", ".tile", ".chip", ".seg", ".empty-state", // the component kit
	} {
		if !strings.Contains(css, needle) {
			t.Errorf("style.css lacks %q", needle)
		}
	}
	if strings.Contains(css, "::before { content: attr(data-label)") || strings.Contains(css, "attr(data-label)") {
		t.Error("style.css still uses td::before attribute labels for the phone layout")
	}
	if !strings.Contains(css, ".acct-tbl tr { display: grid") {
		t.Error("style.css lacks the phone card grid for account rows")
	}
	js := string(entries["static/app.js"])
	for _, needle := range []string{"#csrf=", "sessionStorage", "history.replaceState", "EventSource", "/api/state", "/api/events", "X-CSRF-Token", "renderActiveStrip", "hashchange", "function meter(", "function tile(", "function chip("} {
		if !strings.Contains(js, needle) {
			t.Errorf("app.js lacks %q", needle)
		}
	}
	if strings.Contains(js, "data-label': 'Slot'") {
		t.Error("app.js still emits data-label attribute labels on account cells")
	}
}

func TestIndexHTML_DesignSystemMarkup(t *testing.T) {
	src, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	index := string(src)
	for _, needle := range []string{
		`class="seg" role="radiogroup" aria-label="Switch strategy" id="strategy-seg"`, // segmented strategy control
		`data-strategy="best"`, `data-strategy="next-available"`,
		`id="hdr-acct"`, // header strip: active account + 5h / 7d / model windows
		`id="summary-tiles"`, `id="badge-dashboard"`, `id="badge-sessions"`, `id="badge-auto"`, `id="conn-age"`,
		`id="sess-filter"`, `id="sess-status"`, `id="sess-sort"`, `id="sessions-groups"`, `id="ide-tbl"`,
		`id="log-kind"`, `id="log-follow"`, `id="log-clear"`, `id="nextbest-tbl"`, `id="auto-settings"`,
		`id="model-limits-toggle"`, // persistent Off / All / Selected model control
		`class="empty-state"`, `<datalist id="threshold-ticks">`,
	} {
		if !strings.Contains(index, needle) {
			t.Errorf("index lacks %q", needle)
		}
	}
	if !strings.Contains(index, `<meta name="color-scheme" content="light dark">`) {
		t.Error("index lacks the color-scheme meta")
	}
	// Regression guard: soonest-reset is the AUTO-switch ordering only. Offered
	// as a manual strategy it once rotated a user onto a 96%-used account; the
	// server refuses it (web.Strategies) and the markup must never offer it.
	if strings.Contains(index, `data-strategy="soonest-reset"`) {
		t.Error("index offers soonest-reset as a manual switch strategy")
	}
	// Regression guard: a state tick must not rebuild a section the user is
	// editing (typing into a settings field lost focus and value every tick).
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"function renderGuarded(", "function editingInside(", "addEventListener('focusout'", "details.menu[open]", "renderGuarded('auto'", "renderGuarded('sessions'"} {
		if !strings.Contains(string(js), needle) {
			t.Errorf("app.js lacks the guarded-render piece %q", needle)
		}
	}
	// The settings editor is its own tab (A27): every key, not buried on the
	// Auto tab, which keeps only a short way there.
	for _, want := range []string{`data-tab="settings"`, `id="panel-settings"`, `id="settings-grid"`, `id="settings-empty"`, `href="#settings"`} {
		if !strings.Contains(index, want) {
			t.Errorf("index lacks %q", want)
		}
	}
}

func TestAppJS_NodeCheck(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; syntax check skipped")
	}
	src, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.js")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "--check", path).CombinedOutput()
	if err != nil {
		t.Fatalf("node --check failed: %v\n%s", err, out)
	}
}

func TestIndexHTML_HasEverySection(t *testing.T) {
	// The comprehensive dashboard: one tab per surface, the modal dialog and
	// the per-row action hooks the client wires up.
	src, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	index := string(src)
	for _, tab := range []string{"dashboard", "auto", "sessions", "settings", "guide"} {
		if !strings.Contains(index, `data-tab="`+tab+`"`) || !strings.Contains(index, `id="panel-`+tab+`"`) {
			t.Errorf("tab/panel %q missing", tab)
		}
	}
	for _, id := range []string{"modal", "modal-form", "threshold-slider", "nextbest-list", "auto-log", "quarantine-body", "settings-grid", "settings-empty", "hdr-acct", "token-status-toggle", "accounts-body", "toasts"} {
		if !strings.Contains(index, `id="`+id+`"`) {
			t.Errorf("element #%s missing", id)
		}
	}
	for _, action := range []string{"switch-strategy", "add-current", "add-token", "auto-start"} {
		if !strings.Contains(index, `data-action="`+action+`"`) {
			t.Errorf("toolbar action %q missing", action)
		}
	}
	js, _ := staticFS.ReadFile("static/app.js")
	for _, needle := range []string{"'force-switch'", "'alias'", "'move'", "'swap'", "'remove'", "/api/accounts/swap", "/api/auto/threshold", "/api/settings/", "?force=1", "?tokenStatus=1", "addEventListener('auto'", "lessSoonest", "lessBest", "997", "998", "999", "renderGuarded('settings'"} {
		if !strings.Contains(string(js), needle) {
			t.Errorf("app.js lacks %q", needle)
		}
	}
}

func TestDefaultKill_RefusesWhatItCannotVerify(t *testing.T) {
	// A PID that cannot exist, and a session file without a start time: both
	// are ErrNotTheProcess, never a signal. We never signal a live process
	// from tests.
	if err := DefaultKill(1<<30-1, testNow.UnixMilli()); !errors.Is(err, ErrNotTheProcess) {
		t.Fatalf("absurd pid: %v, want ErrNotTheProcess", err)
	}
	if err := DefaultKill(os.Getpid(), 0); !errors.Is(err, ErrNotTheProcess) {
		t.Fatalf("no startedAt: %v, want ErrNotTheProcess", err)
	}
}

// startMatches is the identity decision shared by every platform.
func TestStartMatches(t *testing.T) {
	recorded := testNow
	for _, tc := range []struct {
		d    time.Duration
		want bool
	}{
		{0, true}, {time.Second, true}, {-time.Second, true},
		{startTolerance, true}, {-startTolerance, true},
		{startTolerance + time.Second, false}, {-startTolerance - time.Second, false},
		{time.Hour, false}, {-24 * time.Hour, false},
	} {
		if got := startMatches(recorded, recorded.Add(tc.d)); got != tc.want {
			t.Errorf("startMatches(recorded, recorded%+v) = %v, want %v", tc.d, got, tc.want)
		}
	}
}

func TestDefaultSessions_DoesNotPanic(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	v := DefaultSessions()
	if v.Claude == nil || v.IDE == nil {
		t.Fatalf("DefaultSessions returned nil slices: %+v", v)
	}
	if len(v.Claude) != 0 || len(v.IDE) != 0 {
		t.Fatalf("empty config dir yielded sessions: %+v", v)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
