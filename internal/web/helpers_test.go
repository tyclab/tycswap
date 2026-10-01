// Test harness for the dashboard: fake Facade / AccountOps / Settings / Auto /
// Transfer / Sessions / Kill seams, a
// loopback server driven by a hand-fed tick channel, a hand-fed auto-event
// channel and a clock.Fake, plus small request/SSE helpers. No test here
// signals a real process or touches $HOME.
package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/usage"
)

// fixedRand yields 48 fixed bytes: the CSRF token, then the cookie value,
// then the one-time launch token (the order New reads them).
const (
	fixedToken  = "000102030405060708090a0b0c0d0e0f" // CSRF token (page <meta>, X-CSRF-Token)
	fixedCookie = "101112131415161718191a1b1c1d1e1f" // session cookie value
	fixedLaunch = "202122232425262728292a2b2c2d2e2f" // ?token= in the printed URL, single-use
)

func fixedRand() io.Reader {
	b := make([]byte, 48)
	for i := range b {
		b[i] = byte(i)
	}
	return bytes.NewReader(b)
}

// secretSetupToken is the add-token input; it must never be echoed or logged.
// A recognisable, low-entropy fake: no test carries anything token-shaped.
const secretSetupToken = "test-setup-token-never-echoed"

var testNow = time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

// -- fakes -------------------------------------------------------------------

type fakeFacade struct {
	mu            sync.Mutex
	snap          *reporting.AccountsSnapshot
	calls         []string
	fetchArgs     []map[string]bool
	switchErr     error
	switchPayload map[string]any
	disableErr    error
	errs          map[string]error // by method name
	lastToken     string           // AddAccountFromToken's token, kept out of calls
	postAddSnap   *reporting.AccountsSnapshot
}

func (f *fakeFacade) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeFacade) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeFacade) errFor(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errs == nil {
		return nil
	}
	return f.errs[name]
}

func (f *fakeFacade) AccountsSnapshot(fetch map[string]bool) *reporting.AccountsSnapshot {
	f.mu.Lock()
	f.fetchArgs = append(f.fetchArgs, fetch)
	snap := f.snap
	if f.postAddSnap != nil && fetch != nil {
		snap = f.postAddSnap // store-only lookup after add-token
	}
	f.mu.Unlock()
	return snap
}

func (f *fakeFacade) SwitchTo(id string, jsonOut bool) (map[string]any, error) {
	f.record(fmt.Sprintf("SwitchTo(%s,%v)", id, jsonOut))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.switchErr != nil {
		return nil, f.switchErr
	}
	return f.switchPayload, nil
}

func (f *fakeFacade) Switch(strategy *string, jsonOut bool, models []string, modelSrc *string) (map[string]any, error) {
	st := "<nil>"
	if strategy != nil {
		st = *strategy
	}
	src := "<nil>"
	if modelSrc != nil {
		src = *modelSrc
	}
	f.record(fmt.Sprintf("Switch(%s,%v,%v,%s)", st, jsonOut, models, src))
	if err := f.errFor("Switch"); err != nil {
		return nil, err
	}
	return map[string]any{"strategy": st}, nil
}

func (f *fakeFacade) SetAccountDisabled(id string, disabled bool) error {
	f.record(fmt.Sprintf("SetAccountDisabled(%s,%v)", id, disabled))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disableErr
}

func (f *fakeFacade) RemoveAccount(id string, yes bool) error {
	f.record(fmt.Sprintf("RemoveAccount(%s,%v)", id, yes))
	return f.errFor("RemoveAccount")
}

func (f *fakeFacade) AddAccount(slot *int, assumeYes bool, alias *string) error {
	s := "<nil>"
	if slot != nil {
		s = fmt.Sprint(*slot)
	}
	a := "<nil>"
	if alias != nil {
		a = *alias
	}
	f.record(fmt.Sprintf("AddAccount(%s,%v,%s)", s, assumeYes, a))
	return f.errFor("AddAccount")
}

func (f *fakeFacade) AddAccountFromToken(token string, email, slotArg *string, assumeYes bool) error {
	e := "<nil>"
	if email != nil {
		e = *email
	}
	s := "<nil>"
	if slotArg != nil {
		s = *slotArg
	}
	f.mu.Lock()
	f.lastToken = token
	f.mu.Unlock()
	f.record(fmt.Sprintf("AddAccountFromToken(<token>,%s,%s,%v)", e, s, assumeYes))
	return f.errFor("AddAccountFromToken")
}

func (f *fakeFacade) BackupDir() string                                      { return "/tmp/backups" }
func (f *fakeFacade) SetPollPolicyInputs(threshold float64, models []string) {}
func (f *fakeFacade) ClearPollPolicyInputs()                                 {}

type fakeOps struct {
	mu          sync.Mutex
	calls       []string
	errs        map[string]error
	listPayload any
	listErr     error
}

func (o *fakeOps) record(s string) {
	o.mu.Lock()
	o.calls = append(o.calls, s)
	o.mu.Unlock()
}

func (o *fakeOps) Calls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.calls...)
}

func (o *fakeOps) err(name string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.errs[name]
}

func (o *fakeOps) SetAlias(id, alias string) (string, string, error) {
	o.record(fmt.Sprintf("SetAlias(%s,%s)", id, alias))
	if err := o.err("SetAlias"); err != nil {
		return "", "", err
	}
	return id, strings.ToLower(alias), nil
}

func (o *fakeOps) UnsetAlias(id string) (string, error) {
	o.record(fmt.Sprintf("UnsetAlias(%s)", id))
	if err := o.err("UnsetAlias"); err != nil {
		return "", err
	}
	return id, nil
}

func (o *fakeOps) MoveAccount(account, target string) (string, string, bool, error) {
	o.record(fmt.Sprintf("MoveAccount(%s,%s)", account, target))
	if err := o.err("MoveAccount"); err != nil {
		return "", "", false, err
	}
	return account, target, target == "2", nil
}

func (o *fakeOps) SwapAccounts(first, second string) (string, string, error) {
	o.record(fmt.Sprintf("SwapAccounts(%s,%s)", first, second))
	if err := o.err("SwapAccounts"); err != nil {
		return "", "", err
	}
	return first, second, nil
}

func (o *fakeOps) ApproveAPIKeySwitch(num string) { o.record("ApproveAPIKeySwitch(" + num + ")") }

func (o *fakeOps) SwitchToForce(id string, jsonOut, force bool) (map[string]any, error) {
	o.record(fmt.Sprintf("SwitchToForce(%s,%v,%v)", id, jsonOut, force))
	if err := o.err("SwitchToForce"); err != nil {
		return nil, err
	}
	return map[string]any{"forced": true}, nil
}

func (o *fakeOps) ListAccounts(showTokenStatus, jsonOut bool, fetch map[string]bool) (any, error) {
	fetchDesc := "nil"
	if fetch != nil {
		fetchDesc = fmt.Sprintf("len=%d", len(fetch))
	}
	o.record(fmt.Sprintf("ListAccounts(%v,%v,%s)", showTokenStatus, jsonOut, fetchDesc))
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.listPayload, o.listErr
}

type fakeSettings struct {
	mu       sync.Mutex
	views    []SettingView
	calls    []string
	setErr   error
	unsetErr error
	unsetOK  bool
}

func (s *fakeSettings) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *fakeSettings) Effective() []SettingView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.views
}

func (s *fakeSettings) Set(dotted, raw string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf("Set(%s,%s)", dotted, raw))
	if s.setErr != nil {
		return nil, s.setErr
	}
	return raw, nil
}

func (s *fakeSettings) Unset(dotted string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf("Unset(%s)", dotted))
	return s.unsetOK, s.unsetErr
}

type fakeAuto struct {
	mu    sync.Mutex
	view  AutoView
	calls []string
	errs  map[string]error
}

func (a *fakeAuto) Calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (a *fakeAuto) View() AutoView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.view
}

func (a *fakeAuto) op(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, name)
	return a.errs[name]
}

func (a *fakeAuto) Start(dryRun bool) error { return a.op(fmt.Sprintf("Start(%v)", dryRun)) }
func (a *fakeAuto) Stop() error             { return a.op("Stop") }
func (a *fakeAuto) Wake() error             { return a.op("Wake") }
func (a *fakeAuto) ApplyModels(model string) error {
	return a.op(fmt.Sprintf("ApplyModels(%q)", model))
}

func (a *fakeAuto) ApplyThreshold(t float64) error {
	return a.op(fmt.Sprintf("ApplyThreshold(%g)", t))
}

type fakeTransfer struct {
	mu    sync.Mutex
	calls []string
	errs  map[string]error
}

func (f *fakeTransfer) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeTransfer) Export(path, account string, full bool) (TransferResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("Export(%s,%s,%v)", path, account, full))
	if err := f.errs["Export"]; err != nil {
		return TransferResult{}, err
	}
	return TransferResult{Path: path, Messages: []string{"Exported 2 account(s) to " + path}}, nil
}

func (f *fakeTransfer) Import(path string, force bool) (TransferResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("Import(%s,%v)", path, force))
	if err := f.errs["Import"]; err != nil {
		return TransferResult{}, err
	}
	return TransferResult{Path: path}, nil
}

// -- fixtures ----------------------------------------------------------------

func sampleSnapshot() *reporting.AccountsSnapshot {
	return &reporting.AccountsSnapshot{
		ActiveNumber: "1",
		TakenAt:      float64(testNow.Unix()),
		Accounts: []reporting.AccountSnapshot{
			{
				Number: "1", Email: "alice@example.com", Alias: "work", OrgName: "Acme", OrgUUID: "org-1",
				IsActive: true, Kind: "oauth", Switchable: true, RotationEligible: true,
				AtLimit: true, LimitingWindows: []string{"seven_day"},
				Usage: usage.UsageEntry{
					LastGood: map[string]any{
						"five_hour": map[string]any{"pct": 42, "resets_at": "2026-09-19T12:00:00Z"},
						"seven_day": map[string]any{"pct": 100, "resets_at": "2026-09-22T00:00:00Z"},
						"scoped": []any{
							map[string]any{"name": "opus", "pct": 12.5, "resets_at": "2026-09-22T00:00:00Z"},
						},
					},
					FetchedAt: f64(1758276000),
					AgeS:      f64(10.26),
				},
			},
			{
				Number: "2", Email: "bob@example.com", Kind: "api_key", Switchable: true, Disabled: true,
				Usage: usage.UsageEntry{Sentinel: "api key"},
			},
			{
				Number: "3", Email: "carol@example.com", Kind: "oauth", Switchable: false,
			},
		},
	}
}

func sampleSessions() SessionsView {
	return SessionsView{
		Claude: []procdetect.ClaudeSession{
			{PID: 4242, SessionID: "sess-1", CWD: "/work/repo", StartedAt: 1758276000000, Kind: "interactive", Entrypoint: "cli", Status: str("busy")},
			{PID: 4343, SessionID: "sess-2", CWD: "/work/other", StartedAt: 1758276100000, Kind: "bg", Entrypoint: "claude-vscode"},
		},
		IDE: []procdetect.IdeInstance{
			{Port: 51234, PID: 5555, IDEName: "Visual Studio Code", WorkspaceFolders: []string{"/work/repo"}},
		},
	}
}

func sampleSettings() []SettingView {
	return []SettingView{
		{Key: "autoswitch.threshold", Kind: "float", Value: 90.0, Default: 90.0, IsDefault: true, Description: "Switch when the binding 5h/7d window reaches this pct", Min: f64(50), Max: f64(99.9)},
		{Key: "autoswitch.codexThreshold", Kind: "float", Value: 0.0, Default: 0.0, IsDefault: true, Description: "Codex-only switch threshold (0 = use autoswitch.threshold)", Min: f64(0), Max: f64(99.9)},
		{Key: "autoswitch.codexEnabled", Kind: "bool", Value: true, Default: true, IsDefault: true, Description: "Also auto-switch Codex accounts"},
		{Key: "autoswitch.includeApiKeyAccounts", Kind: "bool", Value: false, Default: false, IsDefault: true, Description: "Allow rotating onto managed API-key accounts"},
		{Key: "autoswitch.strategy", Kind: "choice", Value: "soonest-reset", Default: "best", IsDefault: false, Choices: []string{"best", "soonest-reset"}, Description: "How auto-switch orders qualifying targets"},
	}
}

func sampleAuto() AutoView {
	return AutoView{
		Available: true, Running: true, DryRun: true, StartedAt: f64(1758276000), Threshold: 85,
		Settings: map[string]any{"autoswitch.threshold": 85.0, "autoswitch.strategy": "soonest-reset", "autoswitch.codexEnabled": true},
		Events: []AutoEventView{
			{At: 1758276001, Kind: "poll", Message: "polled 3 accounts"},
			{At: 1758276002, Kind: "switch", Message: "switched to #2", Account: "2", Fields: map[string]any{"from": "1"}},
		},
		Quarantine: map[string]any{"quarantine": map[string]any{"3": map[string]any{"reason": "invalid_grant", "at": 1758275000.0}}},
	}
}

// -- harness -----------------------------------------------------------------

type harness struct {
	t      *testing.T
	s      *Server
	fa     *fakeFacade
	ops    *fakeOps
	set    *fakeSettings
	tr     *fakeTransfer
	auto   *fakeAuto
	clk    *clock.Fake
	tick   chan time.Time
	autoEv chan AutoEventView
	base   string
	client *http.Client

	mu       sync.Mutex
	sessions SessionsView
	killed   []int
	killErr  error
	logs     []string
}

type option func(*harness, *Deps)

func withNoTransfer() option   { return func(h *harness, d *Deps) { d.Transfer = nil } }
func withNoAccounts() option   { return func(h *harness, d *Deps) { d.Accounts = nil } }
func withNoSettings() option   { return func(h *harness, d *Deps) { d.Settings = nil } }
func withNoAuto() option       { return func(h *harness, d *Deps) { d.Auto = nil } }
func withNoAutoEvents() option { return func(h *harness, d *Deps) { d.AutoEvents = nil } }
func withRand(r io.Reader) option {
	return func(h *harness, d *Deps) { d.Rand = r }
}

// newHarness builds, binds and serves a Server on 127.0.0.1:0 with every seam
// faked. Serve runs until the test ends.
func newHarness(t *testing.T, opts ...option) *harness {
	t.Helper()
	h := &harness{
		t:  t,
		fa: &fakeFacade{snap: sampleSnapshot(), switchPayload: map[string]any{"switched": true}},
		ops: &fakeOps{errs: map[string]error{}, listPayload: map[string]any{
			"schemaVersion": 1, "activeAccountNumber": 1,
			"accounts": []any{
				map[string]any{"number": 1, "email": "alice@example.com", "tokenStatus": "oauth: fresh, refresh token yes, expires 12:00 in 2h"},
				map[string]any{"number": 2, "email": "bob@example.com"},
			},
		}},
		set:      &fakeSettings{views: sampleSettings(), unsetOK: true},
		auto:     &fakeAuto{view: sampleAuto(), errs: map[string]error{}},
		tr:       &fakeTransfer{errs: map[string]error{}},
		clk:      clock.NewFake(testNow),
		tick:     make(chan time.Time),
		autoEv:   make(chan AutoEventView),
		sessions: sampleSessions(),
	}
	d := Deps{
		Facade:   h.fa,
		Accounts: h.ops,
		Settings: h.set,
		Auto:     h.auto,
		Transfer: h.tr,
		Sessions: func() SessionsView {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.sessions
		},
		SessionTitle: func(dir, cwd, id string) string {
			if id == "sess-1" {
				return "Fix the flaky test"
			}
			return ""
		},
		Kill: func(pid int) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.killed = append(h.killed, pid)
			return h.killErr
		},
		Clock:    h.clk,
		Rand:     fixedRand(),
		Interval: time.Hour,
		Logger: func(s string) {
			h.mu.Lock()
			h.logs = append(h.logs, s)
			h.mu.Unlock()
		},
		Ticker:     func(time.Duration) (<-chan time.Time, func()) { return h.tick, func() {} },
		AutoEvents: h.autoEv,
	}
	for _, o := range opts {
		o(h, &d)
	}
	s, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.pingInterval = time.Hour
	h.s = s
	url, err := s.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.base = strings.TrimSuffix(url[:strings.Index(url, "/?token=")], "/")
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()
	h.client = &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("Serve did not return after cancel")
		}
	})
	return h
}

// fireTick drives one poll tick through the Ticker seam.
func (h *harness) fireTick() {
	h.t.Helper()
	select {
	case h.tick <- h.clk.Now():
	case <-time.After(5 * time.Second):
		h.t.Fatal("Serve loop did not accept a tick")
	}
}

// fireAuto pushes one engine event through the AutoEvents seam.
func (h *harness) fireAuto(ev AutoEventView) {
	h.t.Helper()
	select {
	case h.autoEv <- ev:
	case <-time.After(5 * time.Second):
		h.t.Fatal("Serve loop did not accept an auto event")
	}
}

func (h *harness) Killed() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int(nil), h.killed...)
}

func (h *harness) Logs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.logs...)
}

func (h *harness) setSessions(v SessionsView) {
	h.mu.Lock()
	h.sessions = v
	h.mu.Unlock()
}

// newReq builds a request against the live server. The Host header is the
// bound 127.0.0.1:<port> unless a test overrides req.Host.
func (h *harness) newReq(method, path string, body io.Reader) *http.Request {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, body)
	if err != nil {
		h.t.Fatalf("NewRequest: %v", err)
	}
	return req
}

func (h *harness) withCookie(req *http.Request) *http.Request {
	req.AddCookie(&http.Cookie{Name: h.s.cookieName(), Value: h.s.cookie})
	return req
}

func (h *harness) withCSRF(req *http.Request) *http.Request {
	req.Header.Set(csrfHeader, h.s.Token())
	return req
}

// authed sets both the cookie and the CSRF header.
func (h *harness) authed(req *http.Request) *http.Request {
	return h.withCSRF(h.withCookie(req))
}

func (h *harness) do(req *http.Request) *http.Response {
	h.t.Helper()
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	h.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// get/post are the authenticated shorthands.
func (h *harness) get(path string) *http.Response {
	h.t.Helper()
	return h.do(h.authed(h.newReq(http.MethodGet, path, nil)))
}

func (h *harness) post(path string) *http.Response {
	h.t.Helper()
	return h.do(h.authed(h.newReq(http.MethodPost, path, nil)))
}

// send performs an authenticated request with a JSON body (v marshalled, or a
// raw string sent as-is).
func (h *harness) send(method, path string, v any) *http.Response {
	h.t.Helper()
	var body io.Reader
	switch x := v.(type) {
	case nil:
	case string:
		body = strings.NewReader(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			h.t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	req := h.authed(h.newReq(method, path, body))
	req.Header.Set("Content-Type", "application/json")
	return h.do(req)
}

func (h *harness) postJSON(path string, v any) *http.Response {
	h.t.Helper()
	return h.send(http.MethodPost, path, v)
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// -- SSE reader --------------------------------------------------------------

type sseEvent struct {
	name    string
	data    string
	comment string
}

// sseStream parses a text/event-stream body on a goroutine and hands each
// event (or comment) over a channel.
type sseStream struct {
	events <-chan sseEvent
	resp   *http.Response
}

func (h *harness) openSSE() *sseStream {
	h.t.Helper()
	resp := h.get("/api/events")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("SSE status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		h.t.Fatalf("SSE content-type %q", ct)
	}
	return &sseStream{events: readSSE(resp), resp: resp}
}

// readSSE parses a text/event-stream body on a goroutine.
func readSSE(resp *http.Response) <-chan sseEvent {
	ch := make(chan sseEvent, 64)
	go func() {
		defer close(ch)
		r := bufio.NewReader(resp.Body)
		var cur sseEvent
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if cur.name != "" || cur.data != "" || cur.comment != "" {
					ch <- cur
				}
				cur = sseEvent{}
			case strings.HasPrefix(line, ":"):
				cur.comment = strings.TrimSpace(line[1:])
			case strings.HasPrefix(line, "event:"):
				cur.name = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				if cur.data != "" {
					cur.data += "\n"
				}
				cur.data += strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			}
		}
	}()
	return ch
}

// next returns the next event or fails after timeout.
func (st *sseStream) next(t *testing.T, timeout time.Duration) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-st.events:
		if !ok {
			t.Fatal("SSE stream closed")
		}
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for SSE event")
	}
	return sseEvent{}
}

// nextNamed skips comments and other events, returning the next event with
// the given name.
func (st *sseStream) nextNamed(t *testing.T, name string, timeout time.Duration) sseEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for %s event", name)
		}
		ev := st.next(t, remaining)
		if ev.name == name {
			return ev
		}
	}
}

// nextState skips comments and returns the next state event.
func (st *sseStream) nextState(t *testing.T, timeout time.Duration) sseEvent {
	t.Helper()
	return st.nextNamed(t, "state", timeout)
}

// expectNone asserts no event arrives within d.
func (st *sseStream) expectNone(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case ev, ok := <-st.events:
		if ok {
			t.Fatalf("unexpected SSE event %+v", ev)
		}
	case <-time.After(d):
	}
}

func (st *sseStream) close() { _ = st.resp.Body.Close() }

// waitFor polls cond until true or the timeout elapses.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
