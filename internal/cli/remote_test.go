// Tests for `app --remote` (DESIGN A45): the flag grammar and its conflicts,
// the token file the headless app writes and takes away, the client against
// a real web.Server with fakes of its own (the web package's fakes stay
// unexported) and against bare httptest servers for what a real one never
// does (redirects, a foreign launch URL, an endless line, silence), the
// event loop with its reconnect, watchdog and token re-read, the autostart
// arguments, the tray-less exit and the whole command over a fake tray.
// Everything is offline and under a temp HOME.
package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/web"
)

// -- flags -------------------------------------------------------------------

func TestParseAppArgsRemote(t *testing.T) {
	testutil.Unsetenv(t, remoteTokenFileEnv())
	o, err := parseAppArgs([]string{"--remote", "http://localhost:7337/", "--token-file", `\\wsl.localhost\Ubuntu\home\me\remote.token`, "--open", "--no-update-check"})
	if err != nil {
		t.Fatal(err)
	}
	if o.remote != "http://localhost:7337" || o.tokenFile != `\\wsl.localhost\Ubuntu\home\me\remote.token` || !o.open || !o.noUpdates {
		t.Errorf("parsed = %+v", o)
	}
	// The token file may come from the environment instead.
	testutil.Setenv(t, remoteTokenFileEnv(), "/mnt/wsl/remote.token")
	if o, err := parseAppArgs([]string{"--remote=http://127.0.0.1:7337"}); err != nil || o.tokenFile != "/mnt/wsl/remote.token" || o.remote != "http://127.0.0.1:7337" {
		t.Errorf("env token file: %+v, %v", o, err)
	}
	testutil.Unsetenv(t, remoteTokenFileEnv())
	// Neither given: the error names both.
	_, err = parseAppArgs([]string{"--remote", "http://127.0.0.1:7337"})
	if err == nil || !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), remoteTokenFileEnv()) {
		t.Errorf("no token file: %v", err)
	}
	// Flags that shape a local dashboard contradict --remote, by name.
	for _, tc := range []struct {
		argv []string
		name string
	}{
		{[]string{"--remote", "http://127.0.0.1:7337", "--token-file", "/t", "--headless"}, "--headless"},
		{[]string{"--remote", "http://127.0.0.1:7337", "--token-file", "/t", "--port", "0"}, "--port"},
		{[]string{"--remote", "http://127.0.0.1:7337", "--token-file", "/t", "--interval", "5"}, "--interval"},
	} {
		_, err := parseAppArgs(tc.argv)
		if err == nil || !strings.Contains(err.Error(), "not allowed with "+tc.name) {
			t.Errorf("%v: err = %v, want it to name %s", tc.argv, err, tc.name)
		}
	}
	if _, err := parseAppArgs([]string{"--token-file", "/t"}); err == nil || !strings.Contains(err.Error(), "only meaningful with --remote") {
		t.Errorf("--token-file alone: %v", err)
	}
	// Loopback, http, a port, nothing else.
	for _, bad := range []string{"https://127.0.0.1:7337", "http://10.0.0.7:7337", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:70000", "http://localhost:7337/api", "http://localhost:7337/?token=x", "http://me@localhost:7337", "http://[::1]:7337", "127.0.0.1:7337", "http://127.0.0.1.nip.io:7337"} {
		if _, err := parseAppArgs([]string{"--remote", bad, "--token-file", "/t"}); err == nil || !strings.Contains(err.Error(), "argument --remote: expected http://127.0.0.1:<port> or http://localhost:<port>") {
			t.Errorf("--remote %q: err = %v", bad, err)
		}
	}
	if _, err := parseAppArgs([]string{"--remote"}); err == nil {
		t.Error("--remote without a value accepted")
	}
	// The autostart command inherits the remote flags.
	o, err = parseAppArgs([]string{"--remote", "http://127.0.0.1:7337", "--token-file", "/t", "--autostart", "status"})
	if err != nil || o.autostart != "status" {
		t.Fatalf("autostart with remote: %+v, %v", o, err)
	}
	if got := strings.Join(autostartConfig(o).Args, " "); got != "app --remote http://127.0.0.1:7337 --token-file /t" {
		t.Errorf("autostart args = %q", got)
	}
	if got := autostartConfig(o).Label; got != remoteAutostartLabel() || got != "io.github.tyclab.tycswap.remote" {
		t.Errorf("autostart label = %q, want %q", got, remoteAutostartLabel())
	}
	if c := autostartConfig(appOptions{}); c.Args != nil || c.Label != "" {
		t.Error("a local app's autostart entry stays the default `app` under the default name")
	}
}

// -- the token file ----------------------------------------------------------

func TestRemoteTokenFile(t *testing.T) {
	tok, err := mintRemoteToken(strings.NewReader(strings.Repeat("ab", 16)))
	if _, decErr := hex.DecodeString(tok); err != nil || len(tok) != 32 || decErr != nil {
		t.Fatalf("mint = %q, %v", tok, err)
	}
	if _, err := mintRemoteToken(strings.NewReader("short")); err == nil {
		t.Error("a short read must fail, not yield a weak token")
	}
	root := filepath.Join(t.TempDir(), "tycswap")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := remoteTokenPath(root)
	if path != filepath.Join(root, "remote.token") {
		t.Errorf("path = %q", path)
	}
	if err := writeRemoteToken(path, tok); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(raw)) != tok {
		t.Fatalf("file = %q, %v", raw, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o, want 600", fi.Mode().Perm())
		}
	}
	// No temp file is left beside it.
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want the token only", len(entries))
	}
	// A second start overwrites, and removal is idempotent.
	if err := writeRemoteToken(path, "second"); err != nil {
		t.Fatal(err)
	}
	removeRemoteToken(path)
	removeRemoteToken(path)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("token file still there after remove: %v", err)
	}
}

// syncBuffer is a bytes.Buffer the command writes while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// stopViaNotifyContext replaces the signal seam: the returned cancel is the
// test's Ctrl-C / SIGTERM.
func stopViaNotifyContext(t *testing.T) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	prev := notifyContext
	notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return context.WithCancel(ctx)
	}
	t.Cleanup(func() { notifyContext = prev; cancel() })
	return cancel
}

// waitUntil polls cond for up to five seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The headless app — the distro side of A45 — mints a token, hands it to its
// server, writes it where stderr says and takes it away when it is stopped.
// Driven through the command, not the helpers: this is the seam the whole
// feature hangs on.
func TestHeadlessAppWritesTheRemoteToken(t *testing.T) {
	home := appLockHome(t)
	stop := stopViaNotifyContext(t)
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run("tycswap", []string{"app", "--headless", "--port", "0", "--no-update-check"},
			ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
	}()
	var path, url string
	waitUntil(t, "the Dashboard and Remote token lines", func() bool {
		for _, line := range strings.Split(errb.String(), "\n") {
			if rest, ok := strings.CutPrefix(line, "Remote token: "); ok {
				path = strings.TrimSuffix(rest, " (for tycswap app --remote)")
			}
			if rest, ok := strings.CutPrefix(line, "Dashboard: "); ok {
				url = rest
			}
		}
		return path != "" && url != ""
	})
	if !strings.HasPrefix(path, home) || filepath.Base(path) != "remote.token" || filepath.Dir(path) != filepath.Dir(appLockPath()) {
		t.Errorf("token path %q, want remote.token beside app.lock under %q", path, home)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if tok := strings.TrimSpace(string(raw)); len(tok) != 2*remoteTokenBytes || strings.Trim(tok, "0123456789abcdef") != "" {
		t.Fatalf("token file holds %q", raw)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o, want 600", fi.Mode().Perm())
		}
	}
	// The server honours exactly that token: the client reads the file
	// and gets the state, and a wrong token is refused.
	base := url[:strings.Index(url, "/?token=")]
	rc := newRemoteClient(base, path)
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.State(); err != nil {
		t.Fatalf("GET /api/state with the token from the file: %v", err)
	}
	wrong := newRemoteClient(base, tokenFile(t, strings.Repeat("0", 32)))
	_ = wrong.loadToken()
	if _, err := wrong.State(); err == nil || !strings.Contains(err.Error(), "refused the token") {
		t.Errorf("a wrong token: %v", err)
	}
	// Ctrl-C / SIGTERM: the app ends with 0 and the token is gone.
	stop()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit = %d, stderr = %q", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("app --headless did not stop")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("token file still there after the app stopped: %v", err)
	}
}

// -- a dashboard with fakes --------------------------------------------------

// remoteFakeFacade is the least web.Facade a server needs: one roster.
type remoteFakeFacade struct {
	mu        sync.Mutex
	calls     []string
	switchErr error
}

func (f *remoteFakeFacade) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *remoteFakeFacade) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *remoteFakeFacade) AccountsSnapshot(map[string]bool) *reporting.AccountsSnapshot {
	return &reporting.AccountsSnapshot{
		ActiveNumber: "1",
		Accounts: []reporting.AccountSnapshot{
			{Number: "1", Email: "alice@example.com", Alias: "work", Kind: "oauth", IsActive: true, Switchable: true},
			{Number: "2", Email: "bob@example.com", Kind: "oauth", Switchable: true},
		},
	}
}

func (f *remoteFakeFacade) SwitchTo(id string, jsonOut bool) (map[string]any, error) {
	f.record("SwitchTo(" + id + ")")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.switchErr != nil {
		return nil, f.switchErr
	}
	return map[string]any{"switched": true}, nil
}

func (f *remoteFakeFacade) Switch(*string, bool, []string, *string) (map[string]any, error) {
	return nil, errors.New("not in this test")
}
func (f *remoteFakeFacade) SetAccountDisabled(string, bool) error                    { return nil }
func (f *remoteFakeFacade) RemoveAccount(string, bool) error                         { return nil }
func (f *remoteFakeFacade) AddAccount(*int, bool, *string) error                     { return nil }
func (f *remoteFakeFacade) AddAccountFromToken(string, *string, *string, bool) error { return nil }
func (f *remoteFakeFacade) BackupDir() string                                        { return "/tmp/backups" }
func (f *remoteFakeFacade) SetPollPolicyInputs(float64, []string)                    {}
func (f *remoteFakeFacade) ClearPollPolicyInputs()                                   {}

// remoteFakeAuto records engine calls and reports a settable running flag;
// applyErr is what ApplyModels returns while running.
type remoteFakeAuto struct {
	mu       sync.Mutex
	calls    []string
	running  bool
	applyErr error
}

func (a *remoteFakeAuto) record(s string) {
	a.mu.Lock()
	a.calls = append(a.calls, s)
	a.mu.Unlock()
}

func (a *remoteFakeAuto) Calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (a *remoteFakeAuto) View() web.AutoView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return web.AutoView{Available: true, Running: a.running, Threshold: 97, Events: []web.AutoEventView{}}
}

func (a *remoteFakeAuto) Start(dryRun bool) error {
	a.record("Start(" + map[bool]string{true: "dry", false: "real"}[dryRun] + ")")
	a.mu.Lock()
	a.running = true
	a.mu.Unlock()
	return nil
}

func (a *remoteFakeAuto) Stop() error {
	a.record("Stop")
	a.mu.Lock()
	a.running = false
	a.mu.Unlock()
	return nil
}

func (a *remoteFakeAuto) Wake() error { a.record("Wake"); return nil }
func (a *remoteFakeAuto) ApplyThreshold(t float64) error {
	a.record("ApplyThreshold(" + strconv.FormatFloat(t, 'g', -1, 64) + ")")
	return nil
}
func (a *remoteFakeAuto) ApplyModels(model string) error {
	a.record("ApplyModels(" + model + ")")
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return cerr.Validation("auto-switch is not running")
	}
	return a.applyErr
}

type remoteFakeSettings struct {
	mu    sync.Mutex
	calls []string
}

func (s *remoteFakeSettings) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *remoteFakeSettings) Effective() []web.SettingView {
	return []web.SettingView{{Key: "autoswitch.sevenDayThreshold", Value: 97.0}}
}

func (s *remoteFakeSettings) Set(dotted, raw string) (any, error) {
	s.mu.Lock()
	s.calls = append(s.calls, "Set("+dotted+","+raw+")")
	s.mu.Unlock()
	return raw, nil
}

func (s *remoteFakeSettings) Unset(dotted string) (bool, error) {
	s.mu.Lock()
	s.calls = append(s.calls, "Unset("+dotted+")")
	s.mu.Unlock()
	return true, nil
}

// remoteFixture is a real web.Server over the fakes, serving on loopback with
// the remote token, exactly what the app in the distro runs.
type remoteFixture struct {
	srv    *web.Server
	base   string
	fa     *remoteFakeFacade
	auto   *remoteFakeAuto
	set    *remoteFakeSettings
	autoEv chan web.AutoEventView
	cancel context.CancelFunc
	served chan error
}

// startRemoteServer binds addr ("127.0.0.1:0" for any port) and serves until
// stop. The poll ticker never fires: every state the client sees is the
// initial SSE frame or one a mutation broadcast.
func startRemoteServer(t *testing.T, token, addr string) *remoteFixture {
	t.Helper()
	fx := &remoteFixture{
		fa:     &remoteFakeFacade{},
		auto:   &remoteFakeAuto{},
		set:    &remoteFakeSettings{},
		autoEv: make(chan web.AutoEventView),
		served: make(chan error, 1),
	}
	srv, err := web.New(web.Deps{
		Facade:      fx.fa,
		Auto:        fx.auto,
		AutoEvents:  fx.autoEv,
		Settings:    fx.set,
		Sessions:    func() web.SessionsView { return web.SessionsView{} },
		Kill:        func(int, int64) error { return nil },
		Interval:    time.Hour,
		Ticker:      func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} },
		RemoteToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	url, err := srv.Start(addr)
	if err != nil {
		t.Fatal(err)
	}
	fx.srv = srv
	fx.base = url[:strings.Index(url, "/?token=")]
	ctx, cancel := context.WithCancel(context.Background())
	fx.cancel = cancel
	go func() { fx.served <- srv.Serve(ctx) }()
	t.Cleanup(fx.stop)
	return fx
}

// stop ends the server and waits for Serve to return; a second call is a
// no-op.
func (fx *remoteFixture) stop() {
	fx.cancel()
	select {
	case <-fx.served:
	case <-time.After(5 * time.Second):
	}
	fx.served <- nil
}

// fireAuto pushes one engine event through the server's AutoEvents seam.
func (fx *remoteFixture) fireAuto(t *testing.T, ev web.AutoEventView) {
	t.Helper()
	select {
	case fx.autoEv <- ev:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve loop did not accept an auto event")
	}
}

const testRemoteToken = "404142434445464748494a4b4c4d4e4f"

// tokenFile writes tok to a file under a temp dir and returns its path.
func tokenFile(t *testing.T, tok string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "remote.token")
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestRemoteClient(t *testing.T, fx *remoteFixture, tok string) *remoteClient {
	t.Helper()
	rc := newRemoteClient(fx.base, tokenFile(t, tok))
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	return rc
}

// -- the client --------------------------------------------------------------

func TestRemoteClientCalls(t *testing.T) {
	fx := startRemoteServer(t, testRemoteToken, "127.0.0.1:0")
	rc := newTestRemoteClient(t, fx, testRemoteToken)

	st, err := rc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Accounts) != 2 || rowNumber(st.Accounts[0]) != "1" || !boolOf(st.Accounts[0]["isActive"]) || st.Auto == nil || st.Auto.Running {
		t.Errorf("state = %+v", st)
	}
	if rc.AutoRunning() {
		t.Error("AutoRunning before the engine started")
	}
	if err := rc.Switch("2"); err != nil {
		t.Fatal(err)
	}
	if calls := fx.fa.Calls(); len(calls) != 1 || calls[0] != "SwitchTo(2)" {
		t.Errorf("facade calls = %v", calls)
	}
	// A mutation refreshes the cache at once: the shell repaints from it
	// right after the click, before the stream's broadcast arrives.
	if err := rc.AutoStart(); err != nil {
		t.Fatal(err)
	}
	if !rc.AutoRunning() {
		t.Error("AutoRunning should be true right after AutoStart, without a State()")
	}
	if err := rc.AutoStop(); err != nil {
		t.Fatal(err)
	}
	if rc.AutoRunning() {
		t.Error("AutoRunning should be false right after AutoStop")
	}
	if got := strings.Join(fx.auto.Calls(), ","); got != "Start(real),Stop" {
		t.Errorf("auto calls = %s", got)
	}
	// The model switch mirrors the local one: setting first, then the engine
	// — whose "not running" is no failure while it is off.
	if err := rc.SetModel(true); err != nil {
		t.Fatal(err)
	}
	if err := rc.SetModel(false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fx.set.Calls(), ","); got != "Set(autoswitch.model,all),Unset(autoswitch.model)" {
		t.Errorf("settings calls = %s", got)
	}
	if got := strings.Join(fx.auto.Calls(), ","); !strings.HasSuffix(got, "ApplyModels(all),ApplyModels()") {
		t.Errorf("auto calls = %s", got)
	}
	// With the engine running, a failed retarget is reported.
	if err := rc.AutoStart(); err != nil {
		t.Fatal(err)
	}
	if !rc.AutoRunning() {
		t.Error("AutoRunning should follow the last state")
	}
	fx.auto.mu.Lock()
	fx.auto.applyErr = errors.New("retarget failed in the test")
	fx.auto.mu.Unlock()
	if err := rc.SetModel(true); err == nil || !strings.Contains(err.Error(), "retarget failed in the test") {
		t.Errorf("SetModel while running with a failing retarget = %v, want the engine's message", err)
	}
	fx.auto.mu.Lock()
	fx.auto.applyErr = nil
	fx.auto.mu.Unlock()
	// The tray's 7d threshold submenu moves the distro engine's bar (A34).
	if err := rc.SetThreshold(90); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fx.auto.Calls(), ","); !strings.HasSuffix(got, "ApplyThreshold(90)") {
		t.Errorf("auto calls = %s", got)
	}
	// Launch hands back the server's own one-time URL, rebuilt from its
	// checked parts.
	u, err := rc.Launch()
	if err != nil || u != fx.srv.URL() {
		t.Errorf("Launch = %q, %v; want %q", u, err, fx.srv.URL())
	}
	// A refused call carries the server's own message.
	fx.fa.mu.Lock()
	fx.fa.switchErr = cerr.AccountNotFound("Account 9 not found")
	fx.fa.mu.Unlock()
	if err := rc.Switch("9"); err == nil || err.Error() != "Account 9 not found" {
		t.Errorf("Switch(9) = %v, want the server's message", err)
	}
}

func TestRemoteClientReReadsTokenOn401(t *testing.T) {
	fx := startRemoteServer(t, testRemoteToken, "127.0.0.1:0")
	// The file held yesterday's token when the tray started…
	path := tokenFile(t, strings.Repeat("0", 32))
	rc := newRemoteClient(fx.base, path)
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	// …and the engine has since written today's.
	if err := os.WriteFile(path, []byte(testRemoteToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.State(); err != nil {
		t.Fatalf("State after the token changed: %v", err)
	}
	if rc.currentToken() != testRemoteToken {
		t.Error("the client should hold the re-read token")
	}
	// A file that stays wrong is reported as such, once, not retried forever.
	if err := os.WriteFile(path, []byte("ffffffffffffffffffffffffffffffff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.State(); err == nil || !strings.Contains(err.Error(), "refused the token in "+path) {
		t.Errorf("State with a wrong token: %v", err)
	}
	// An engine that is not there is a connection error, named with its URL.
	gone := newRemoteClient("http://127.0.0.1:1", path)
	if _, err := gone.State(); err == nil || !strings.Contains(err.Error(), "engine at http://127.0.0.1:1") {
		t.Errorf("unreachable engine: %v", err)
	}
}

// linkRecorder is the shell's side of the event loop: what arrived, in order.
type linkRecorder struct {
	mu     sync.Mutex
	events []string
	states chan web.State
	autos  chan web.AutoEventView
	links  chan bool
}

func newLinkRecorder() *linkRecorder {
	return &linkRecorder{states: make(chan web.State, 16), autos: make(chan web.AutoEventView, 16), links: make(chan bool, 16)}
}

func (r *linkRecorder) onState(st web.State) {
	r.mu.Lock()
	r.events = append(r.events, "state")
	r.mu.Unlock()
	r.states <- st
}

func (r *linkRecorder) onAuto(ev web.AutoEventView) {
	r.mu.Lock()
	r.events = append(r.events, "auto:"+ev.Kind)
	r.mu.Unlock()
	r.autos <- ev
}

func (r *linkRecorder) onLink(up bool) {
	r.mu.Lock()
	r.events = append(r.events, "link:"+map[bool]string{true: "up", false: "down"}[up])
	r.mu.Unlock()
	r.links <- up
}

func waitState(t *testing.T, ch <-chan web.State) web.State {
	t.Helper()
	select {
	case st := <-ch:
		return st
	case <-time.After(5 * time.Second):
		t.Fatal("no state frame")
	}
	return web.State{}
}

func waitLink(t *testing.T, ch <-chan bool, want bool) {
	t.Helper()
	select {
	case up := <-ch:
		if up != want {
			t.Fatalf("link = %v, want %v", up, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no link change to %v", want)
	}
}

// The event loop delivers state and auto frames, reports an outage once when
// the engine goes away, and reconnects — reporting the link back once — when
// an engine answers on the same address again.
func TestRemoteEventsLoopDeliversAndReconnects(t *testing.T) {
	fastRemote(t, remoteStaleAfter)
	fx := startRemoteServer(t, testRemoteToken, "127.0.0.1:0")
	rc := newTestRemoteClient(t, fx, testRemoteToken)
	rec := newLinkRecorder()
	if down, _ := rc.Offline(); !down {
		t.Fatal("before the first frame the engine counts as unreachable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rc.run(ctx, rec.onState, rec.onAuto, rec.onLink); close(done) }()

	// The initial frame.
	if st := waitState(t, rec.states); len(st.Accounts) != 2 {
		t.Errorf("first state = %+v", st)
	}
	if down, _ := rc.Offline(); down {
		t.Error("reachable after the first frame")
	}
	// An engine event arrives as an auto frame, followed by a fresh state.
	fx.fireAuto(t, web.AutoEventView{Kind: "switch", Account: "2", Message: "switched"})
	select {
	case ev := <-rec.autos:
		if ev.Kind != "switch" || ev.Account != "2" {
			t.Errorf("auto frame = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no auto frame")
	}
	waitState(t, rec.states)
	// A mutation's broadcast reaches the stream and the client's cache too.
	if err := rc.AutoStart(); err != nil {
		t.Fatal(err)
	}
	if st := waitState(t, rec.states); st.Auto == nil || !st.Auto.Running || !rc.AutoRunning() {
		t.Errorf("state after AutoStart = %+v, AutoRunning = %v", st.Auto, rc.AutoRunning())
	}

	// The engine goes away: one outage notice, the link down.
	fx.stop()
	waitLink(t, rec.links, false)
	if down, why := rc.Offline(); !down || why != "engine unreachable at "+fx.base {
		t.Errorf("Offline = %v, %q", down, why)
	}
	// The loop keeps retrying without announcing the same outage again.
	time.Sleep(150 * time.Millisecond)
	select {
	case up := <-rec.links:
		t.Fatalf("unexpected link change %v during the outage", up)
	default:
	}

	// A new engine on the same address (the app in the distro came back,
	// with a new token in the same file): the link comes back once. The
	// file changes first, so the reconnect finds the new token in place;
	// the 401-driven re-read on the stream has its own test below.
	port := fx.base[strings.LastIndex(fx.base, ":")+1:]
	if err := os.WriteFile(rc.tokenFile, []byte("505152535455565758595a5b5c5d5e5f\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startRemoteServer(t, "505152535455565758595a5b5c5d5e5f", "127.0.0.1:"+port)
	waitLink(t, rec.links, true)
	if st := waitState(t, rec.states); st.Auto == nil || st.Auto.Running {
		t.Errorf("state from the new engine = %+v", st.Auto)
	}
	if down, _ := rc.Offline(); down {
		t.Error("reachable again")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	joined := strings.Join(rec.events, ",")
	if strings.Count(joined, "link:down") != 1 || strings.Count(joined, "link:up") != 1 {
		t.Errorf("events = %s, want exactly one link:down and one link:up", joined)
	}
}

// fastRemote shrinks the loop's cadence for a test: retries in
// milliseconds, the silence watchdog at stale.
func fastRemote(t *testing.T, stale time.Duration) {
	t.Helper()
	prevMin, prevMax, prevStale := remoteRetryMin, remoteRetryMax, remoteStaleAfter
	remoteRetryMin, remoteRetryMax, remoteStaleAfter = 10*time.Millisecond, 50*time.Millisecond, stale
	t.Cleanup(func() { remoteRetryMin, remoteRetryMax, remoteStaleAfter = prevMin, prevMax, prevStale })
}

// startLoop runs rc.run on a goroutine and, at cleanup, ends it and waits
// for it to return — registered after fastRemote, so the loop is gone
// before the timings go back.
func startLoop(t *testing.T, rc *remoteClient, rec *linkRecorder) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rc.run(ctx, rec.onState, rec.onAuto, rec.onLink); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("run did not return after cancel")
		}
	})
}

// A stream the engine refuses with 401 is a token problem, not an outage:
// Offline names the file, and once the file holds the right token the next
// attempt's re-read gets in — the stream-side counterpart of
// TestRemoteClientReReadsTokenOn401. The server is up with token B from
// the start while the file holds A, so the 401 is certain, not a matter
// of scheduling.
func TestRemoteStreamRefusedTokenNamesTheFile(t *testing.T) {
	fastRemote(t, remoteStaleAfter)
	const current = "505152535455565758595a5b5c5d5e5f"
	fx := startRemoteServer(t, current, "127.0.0.1:0")
	path := tokenFile(t, testRemoteToken) // yesterday's
	rc := newRemoteClient(fx.base, path)
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	rec := newLinkRecorder()
	startLoop(t, rc, rec)
	want := "engine at " + fx.base + " refused the token in " + path
	waitUntil(t, "the refused-token reason", func() bool {
		down, why := rc.Offline()
		return down && why == want
	})
	// The engine was never reached with a good token: nothing to announce.
	select {
	case up := <-rec.links:
		t.Fatalf("link change %v before the link ever worked", up)
	default:
	}
	if err := os.WriteFile(path, []byte(current+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitState(t, rec.states)
	if down, why := rc.Offline(); down {
		t.Errorf("Offline after the re-read = %v, %q", down, why)
	}
	select {
	case up := <-rec.links:
		t.Fatalf("link change %v: the first success is silent", up)
	default:
	}
}

// An engine that is not up when the tray starts — the Run key fires before
// the distro's user service — is shown as unreachable but not announced:
// an outage is a link that worked and broke.
func TestRemoteFirstFailureIsNoOutage(t *testing.T) {
	fastRemote(t, remoteStaleAfter)
	rc := newRemoteClient("http://127.0.0.1:1", tokenFile(t, testRemoteToken))
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	rec := newLinkRecorder()
	startLoop(t, rc, rec)
	time.Sleep(150 * time.Millisecond)
	select {
	case up := <-rec.links:
		t.Fatalf("link change %v for an engine that was never there", up)
	default:
	}
	if down, why := rc.Offline(); !down || why != "engine unreachable at http://127.0.0.1:1" {
		t.Errorf("Offline = %v, %q", down, why)
	}
}

// sseFake is a bare event-stream server for what a web.Server never does:
// one state frame, then whatever `after` writes — nothing, pings or an
// endless line — until the client goes away.
type sseFake struct {
	srv   *httptest.Server
	conns atomic.Int32
}

func startSSEFake(t *testing.T, after func(w http.ResponseWriter, flush func(), r *http.Request)) *sseFake {
	t.Helper()
	f := &sseFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" {
			http.NotFound(w, r)
			return
		}
		f.conns.Add(1)
		fl := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: state\ndata: {\"accounts\":[]}\n\n"))
		fl.Flush()
		after(w, fl.Flush, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newFakeStreamClient(t *testing.T, f *sseFake) *remoteClient {
	t.Helper()
	rc := newRemoteClient(f.srv.URL, tokenFile(t, testRemoteToken))
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	return rc
}

// A stream that falls silent is dead: the watchdog ends it, the loop
// reports the outage and reconnects.
func TestRemoteStreamWatchdogEndsASilentStream(t *testing.T) {
	fastRemote(t, 50*time.Millisecond)
	f := startSSEFake(t, func(_ http.ResponseWriter, _ func(), r *http.Request) { <-r.Context().Done() })
	rc := newFakeStreamClient(t, f)
	rec := newLinkRecorder()
	startLoop(t, rc, rec)
	waitState(t, rec.states)
	waitLink(t, rec.links, false)
	waitLink(t, rec.links, true)
	waitState(t, rec.states)
	if n := f.conns.Load(); n < 2 {
		t.Errorf("connections = %d, want a reconnect", n)
	}
}

// Pings keep a stream alive: with pings well inside the watchdog nothing
// happens for longer than the watchdog would wait.
func TestRemoteStreamPingsKeepItAlive(t *testing.T) {
	fastRemote(t, 100*time.Millisecond)
	f := startSSEFake(t, func(w http.ResponseWriter, flush func(), r *http.Request) {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				flush()
			}
		}
	})
	rc := newFakeStreamClient(t, f)
	rec := newLinkRecorder()
	startLoop(t, rc, rec)
	waitState(t, rec.states)
	time.Sleep(300 * time.Millisecond)
	select {
	case up := <-rec.links:
		t.Fatalf("link change %v on a pinging stream", up)
	default:
	}
	if n := f.conns.Load(); n != 1 {
		t.Errorf("connections = %d, want the one", n)
	}
	if down, _ := rc.Offline(); down {
		t.Error("a pinging stream counts as reachable")
	}
}

// A line that never ends, or data that never stops accumulating, ends the
// stream with an error instead of growing the tray's memory.
func TestRemoteStreamFrameBound(t *testing.T) {
	prev := remoteMaxFrame
	remoteMaxFrame = 64 << 10
	t.Cleanup(func() { remoteMaxFrame = prev })
	chunk := bytes.Repeat([]byte("x"), 8<<10)
	for name, after := range map[string]func(w http.ResponseWriter, flush func(), r *http.Request){
		"one endless line": func(w http.ResponseWriter, flush func(), r *http.Request) {
			_, _ = w.Write([]byte("event: state\ndata: "))
			for r.Context().Err() == nil {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				flush()
			}
		},
		"endless data lines": func(w http.ResponseWriter, flush func(), r *http.Request) {
			_, _ = w.Write([]byte("event: state\n"))
			for r.Context().Err() == nil {
				if _, err := w.Write(append(append([]byte("data: "), chunk...), '\n')); err != nil {
					return
				}
				flush()
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := startSSEFake(t, after)
			rc := newFakeStreamClient(t, f)
			done := make(chan error, 1)
			go func() {
				got, err := rc.follow(context.Background(), nil, nil, nil)
				if !got {
					err = errors.New("the first frame did not arrive: " + err.Error())
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "stream frame too large") {
					t.Errorf("follow = %v, want the frame bound", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("follow did not return on an oversized frame")
			}
		})
	}
}

// The client follows no redirect: a listener on the configured port that
// answers 3xx gets "engine answered 302 Found" and the destination never
// sees a request, let alone the bearer.
func TestRemoteClientFollowsNoRedirect(t *testing.T) {
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)
	rogue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusFound
		if r.Method == http.MethodPost {
			code = http.StatusTemporaryRedirect // would re-send the body too
		}
		http.Redirect(w, r, other.URL+r.URL.Path, code)
	}))
	t.Cleanup(rogue.Close)
	rc := newRemoteClient(rogue.URL, tokenFile(t, testRemoteToken))
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.State(); err == nil || !strings.Contains(err.Error(), "engine answered 302") {
		t.Errorf("State over a redirect: %v", err)
	}
	if err := rc.AutoStart(); err == nil || !strings.Contains(err.Error(), "engine answered 307") {
		t.Errorf("AutoStart over a redirect: %v", err)
	}
	if got, err := rc.follow(context.Background(), nil, nil, nil); got || err == nil || !strings.Contains(err.Error(), "engine answered 302") {
		t.Errorf("follow over a redirect: %v, %v", got, err)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the redirect's destination saw %d requests, want none", n)
	}
}

// Only the engine's own one-time URL reaches the browser opener: anything
// else a listener on the port answers is refused before ShellExecute would
// launch whatever its scheme is registered for.
func TestRemoteLaunchURLIsChecked(t *testing.T) {
	const base = "http://127.0.0.1:7337"
	const tok = "000102030405060708090a0b0c0d0e0f"
	for _, good := range []string{base + "/?token=" + tok, "http://localhost:7337/?token=" + tok, base + "?token=" + tok} {
		if got, err := checkLaunchURL(base, good); err != nil || !strings.HasSuffix(got, ":7337/?token="+tok) || !strings.HasPrefix(got, "http://") {
			t.Errorf("checkLaunchURL(%q) = %q, %v", good, got, err)
		}
	}
	for _, bad := range []string{
		"file:///C:/Windows/System32/calc.exe",
		"ms-settings:",
		"https://127.0.0.1:7337/?token=" + tok,
		"http://127.0.0.1:7338/?token=" + tok,
		"http://10.0.0.7:7337/?token=" + tok,
		"http://127.0.0.1:7337/evil?token=" + tok,
		"http://127.0.0.1:7337/?token=" + tok + "&x=1",
		"http://127.0.0.1:7337/?token=" + tok[:10],
		"http://127.0.0.1:7337/?token=" + strings.ToUpper(tok),
		"http://127.0.0.1:7337/?token=" + tok + "#frag",
		"http://me@127.0.0.1:7337/?token=" + tok,
		"http://127.0.0.1:7337/",
		"",
	} {
		if got, err := checkLaunchURL(base, bad); err == nil || got != "" {
			t.Errorf("checkLaunchURL(%q) = %q, %v; want refused", bad, got, err)
		}
	}
	// Through the client: a rogue answer never comes back as a URL to open.
	rogue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"file:///C:/Windows/System32/calc.exe"}`))
	}))
	t.Cleanup(rogue.Close)
	rc := newRemoteClient(rogue.URL, tokenFile(t, testRemoteToken))
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	if u, err := rc.Launch(); err == nil || u != "" || err.Error() != "engine returned an unexpected dashboard URL" {
		t.Errorf("Launch = %q, %v", u, err)
	}
}

// -- the command -------------------------------------------------------------

// A remote without a tray is pointless: exit 1 and say what to do instead.
func TestRemoteModeNeedsATray(t *testing.T) {
	appLockHome(t)
	prev := newTray
	newTray = func(tray.Icon, tray.Options) (tray.Tray, error) { return nil, tray.ErrUnsupported }
	t.Cleanup(func() { newTray = prev })
	code, _, errStr := runCLI(t, []string{"app", "--remote", "http://127.0.0.1:1", "--token-file", tokenFile(t, testRemoteToken)}, false, false)
	if code != 1 || !strings.Contains(errStr, "remote mode needs a tray; run tycswap app in the distro instead") {
		t.Errorf("exit = %d, stderr = %q", code, errStr)
	}
	// The remote lock is taken beside app.lock, and released again.
	if remoteLockPath() != filepath.Join(filepath.Dir(appLockPath()), "remote.lock") {
		t.Errorf("remote lock at %q", remoteLockPath())
	}
	lock, held, err := acquireRemoteLock()
	if err != nil || !held {
		t.Fatalf("remote lock after the exit: %v, %v", held, err)
	}
	_ = lock.Release()
}

// Two remote trays cannot run: the second says so like the local app does.
func TestRemoteModeIsSingleInstance(t *testing.T) {
	appLockHome(t)
	lock, held, err := acquireRemoteLock()
	if err != nil || !held {
		t.Fatalf("first lock: %v, %v", held, err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	prev := newTray
	newTray = func(tray.Icon, tray.Options) (tray.Tray, error) { t.Fatal("no tray should be built"); return nil, nil }
	t.Cleanup(func() { newTray = prev })
	code, _, errStr := runCLI(t, []string{"app", "--remote", "http://127.0.0.1:1", "--token-file", tokenFile(t, testRemoteToken)}, false, false)
	if code != 1 || !strings.Contains(errStr, "already running") {
		t.Errorf("exit = %d, stderr = %q", code, errStr)
	}
	// The local app's lock is a different one: a remote tray does not block
	// it (A45: one instance per side).
	if appIsRunning() {
		t.Error("a remote tray must not count as the local app")
	}
}

// Usage errors from the remote flags exit 2 through the command.
func TestRemoteModeUsageErrors(t *testing.T) {
	testutil.Unsetenv(t, remoteTokenFileEnv())
	code, _, errStr := runCLI(t, []string{"app", "--remote", "http://127.0.0.1:7337"}, false, false)
	if code != 2 || !strings.Contains(errStr, "--token-file") {
		t.Errorf("exit = %d, stderr = %q", code, errStr)
	}
	code, _, errStr = runCLI(t, []string{"app", "--remote", "http://127.0.0.1:7337", "--token-file", "/t", "--headless"}, false, false)
	if code != 2 || !strings.Contains(errStr, "not allowed with --headless") {
		t.Errorf("exit = %d, stderr = %q", code, errStr)
	}
}

// blockingTray is the fake tray with the real one's Run contract — it
// blocks until Quit — and a record of every title and the first menu, in
// the order the shell painted them.
type blockingTray struct {
	fakeTray
	titles    []string
	firstMenu []tray.Item
	done      chan struct{}
	once      sync.Once
}

func (b *blockingTray) SetTitle(s string) {
	b.mu.Lock()
	b.titles = append(b.titles, s)
	b.mu.Unlock()
	b.fakeTray.SetTitle(s)
}

func (b *blockingTray) SetMenu(m []tray.Item) {
	b.mu.Lock()
	if b.firstMenu == nil {
		b.firstMenu = m
	}
	b.mu.Unlock()
	b.fakeTray.SetMenu(m)
}

func (b *blockingTray) Run() error { <-b.done; return nil }
func (b *blockingTray) Quit()      { b.once.Do(func() { close(b.done) }); b.fakeTray.Quit() }

// The whole command over a fake tray: the first paint says unreachable
// before the stream connects, the engine's state follows, a click reaches
// the engine, "Open dashboard" opens the checked launch URL, Quit exits 0
// and frees remote.lock — and an engine that was there from the start is
// not announced as an outage.
func TestRemoteModeRunsTheTray(t *testing.T) {
	appLockHome(t)
	fx := startRemoteServer(t, testRemoteToken, "127.0.0.1:0")
	ft := &blockingTray{done: make(chan struct{})}
	var opts atomic.Pointer[tray.Options]
	prev := newTray
	newTray = func(_ tray.Icon, o tray.Options) (tray.Tray, error) { opts.Store(&o); return ft, nil }
	t.Cleanup(func() { newTray = prev })
	opened := stubOpener(t, "windows")
	path := tokenFile(t, testRemoteToken)
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run("tycswap", []string{"app", "--remote", fx.base, "--token-file", path, "--no-update-check"},
			ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
	}()
	waitUntil(t, "the engine's state in the title", func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		return strings.HasPrefix(ft.title, "#1")
	})
	ft.mu.Lock()
	titles := append([]string(nil), ft.titles...)
	first := ft.firstMenu
	ft.mu.Unlock()
	if len(titles) == 0 || titles[0] != "⚠" {
		t.Errorf("titles = %q, want the first to be the unreachable glyph", titles)
	}
	var offline bool
	for _, it := range first {
		offline = offline || it.ID == "offline"
	}
	if !offline {
		t.Error("the first menu should lead with the unreachable row")
	}
	if _, has := ft.item("offline"); has {
		t.Error("the row should be gone once the state arrived")
	}
	// A click goes through the shell to the engine in the distro.
	o := opts.Load()
	if o == nil || o.OnClick == nil {
		t.Fatal("the tray was built without a click handler")
	}
	o.OnClick("switch:2")
	if calls := fx.fa.Calls(); len(calls) != 1 || calls[0] != "SwitchTo(2)" {
		t.Errorf("facade calls = %v", calls)
	}
	o.OnClick("open")
	if len(*opened) != 1 || (*opened)[0] != fx.srv.URL() {
		t.Errorf("browser got %q, want the engine's launch URL %q", *opened, fx.srv.URL())
	}
	ft.mu.Lock()
	notes := append([]string(nil), ft.notes...)
	ft.mu.Unlock()
	for _, n := range notes {
		if strings.Contains(n, "unreachable") {
			t.Errorf("an engine up from the start was announced as an outage: %q", n)
		}
	}
	o.OnClick("quit")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit = %d, stderr = %q", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("app --remote did not exit on Quit")
	}
	if !strings.Contains(errb.String(), "Remote engine: "+fx.base+" (token from "+path+")") {
		t.Errorf("stderr = %q", errb.String())
	}
	lock, held, err := acquireRemoteLock()
	if err != nil || !held {
		t.Fatalf("remote lock after the exit: %v, %v", held, err)
	}
	_ = lock.Release()
}
