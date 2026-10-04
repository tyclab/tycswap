package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/cerr"
	codexapi "github.com/tyclab/tycswap/internal/codex/api"
	codexauto "github.com/tyclab/tycswap/internal/codex/autoswitch"
	codexswitcher "github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/usage"
	"github.com/tyclab/tycswap/internal/web"
	"github.com/tyclab/tycswap/internal/wincred"
)

// fixtureSwitcher builds a real switcher over the Python fixture home with a
// FAKE Keychain and a FAKE OAuth client: these tests never reach the login
// keychain or the network.
func fixtureSwitcher(t *testing.T) *core.Switcher {
	t.Helper()
	fh := testutil.BuildFixtureHome(t)
	testutil.Setenv(t, "NO_COLOR", "1")
	if target := paths.GetBackupRoot(); target != fh.BackupRoot {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(fh.BackupRoot, target); err != nil {
			t.Fatal(err)
		}
	}
	sw, err := newSwitcher(store.Options{
		Keychain: keychain.NewFake(),
		OAuth:    &oauth.FakeClient{},
		WinCred:  wincred.NewFake(),
		Stderr:   io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sw
}

// TestSettingsFacade: every spec is listed with kind/default/bounds, Set and
// Unset round-trip through settings.json, and a bad key or value is a
// validation error (400 on the API), not a config failure (500).
func TestSettingsFacade(t *testing.T) {
	sw := fixtureSwitcher(t)
	f := settingsFacade{root: sw.BackupDir()}
	views := f.Effective()
	byKey := map[string]web.SettingView{}
	for _, v := range views {
		byKey[v.Key] = v
	}
	// The fixture's settings.json sets the single pre-A34 threshold key to 80,
	// which still steers the 7d bar: the view shows the file value, not the
	// default 97, and reports it as set (DESIGN A34).
	th, ok := byKey["autoswitch.sevenDayThreshold"]
	if !ok || th.Kind != "float" || th.Min == nil || *th.Min != 50 || th.Max == nil || *th.Max != 100 || th.IsDefault || th.Value != float64(80) || th.Default != float64(97) || th.Description == "" {
		t.Fatalf("7d threshold view = %+v", th)
	}
	for _, k := range []string{"autoswitch.fiveHourThreshold", "autoswitch.modelThreshold", "autoswitch.codexThreshold", "autoswitch.codexEnabled", "autoswitch.strategy", "autoswitch.model"} {
		if _, ok := byKey[k]; !ok {
			t.Errorf("setting %s missing", k)
		}
	}
	// includeApiKeyAccounts is gone (DESIGN A33), and so is the single
	// threshold (DESIGN A34).
	for _, k := range []string{"autoswitch.includeApiKeyAccounts", "autoswitch.threshold"} {
		if _, ok := byKey[k]; ok {
			t.Errorf("unexpected setting %s", k)
		}
	}
	if _, err := f.Set("autoswitch.sevenDayThreshold", "77"); err != nil {
		t.Fatal(err)
	}
	for _, v := range f.Effective() {
		if v.Key == "autoswitch.sevenDayThreshold" && (v.IsDefault || v.Value != float64(77)) {
			t.Errorf("after Set: %+v", v)
		}
	}
	for _, tc := range [][2]string{{"autoswitch.sevenDayThreshold", "abc"}, {"autoswitch.sevenDayThreshold", "120"}, {"autoswitch.nope", "1"}, {"autoswitch.codexEnabled", "maybe"}} {
		_, err := f.Set(tc[0], tc[1])
		var ce *cerr.Error
		if !errors.As(err, &ce) || ce.Kind != cerr.KindValidation {
			t.Errorf("Set(%s, %s) = %v, want a validation error", tc[0], tc[1], err)
		}
	}
	if _, err := f.Unset("autoswitch.nope"); err == nil {
		t.Error("unknown key unset")
	}
	// Unset drops the 7d key and the legacy one with it, so the bar is back
	// at its default rather than at the legacy value.
	removed, err := f.Unset("autoswitch.sevenDayThreshold")
	if err != nil || !removed {
		t.Fatalf("Unset = %v, %v", removed, err)
	}
	for _, v := range f.Effective() {
		if v.Key == "autoswitch.sevenDayThreshold" && (!v.IsDefault || v.Value != float64(97)) {
			t.Errorf("after Unset: %+v", v)
		}
	}
	if removed, _ := f.Unset("autoswitch.sevenDayThreshold"); removed {
		t.Error("second Unset reported a removal")
	}
	b, _ := json.Marshal(views[0])
	for _, k := range []string{`"key"`, `"kind"`, `"value"`, `"default"`, `"isDefault"`, `"description"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("SettingView JSON missing %s: %s", k, b)
		}
	}
}

func TestAutoFacade(t *testing.T) {
	sw := fixtureSwitcher(t)
	a := newAutoFacade(sw, nil)
	t.Cleanup(func() {
		_ = a.Stop()
		if !a.waitStopped(10 * time.Second) {
			t.Error("engine goroutine still running at cleanup")
		}
	})
	v := a.View()
	if !v.Available || v.Running || v.StartedAt != nil || v.Settings["autoswitch.sevenDayThreshold"] != float64(80) || v.Threshold != 80 || v.Events == nil || v.Quarantine == nil {
		t.Fatalf("idle view = %+v", v)
	}
	// The settings map is derived from the spec: one key per setting, and the
	// model is what the fixture's settings.json holds (a string when set, nil
	// when not), not a hand-kept copy.
	if len(v.Settings) != len(settings.SettingSpecs) {
		t.Fatalf("view settings has %d keys, want %d (one per spec)", len(v.Settings), len(settings.SettingSpecs))
	}
	var wantModel any
	if m := settings.Load(sw.BackupDir()).Model; m != nil {
		wantModel = *m
	}
	if got, ok := v.Settings["autoswitch.model"]; !ok || got != wantModel {
		t.Fatalf("model = %#v (present %v), want the loaded %#v", got, ok, wantModel)
	}
	for _, err := range []error{a.Stop(), a.Wake(), a.ApplyThreshold(50), a.ApplyModels("all")} {
		if err == nil || !strings.Contains(err.Error(), "not running") {
			t.Errorf("idle op err = %v", err)
		}
	}
	// The slider moves the 7d bar: autoswitch.sevenDayThreshold's 50–100.
	for _, bad := range []float64{0, 49.9, 100.1, 101} {
		if err := a.ApplyThreshold(bad); err == nil || !strings.Contains(err.Error(), "between 50 and 100") {
			t.Errorf("ApplyThreshold(%v) err = %v, want the settings bounds", bad, err)
		}
	}
	if err := a.Start(true); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(true); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("double start err = %v", err)
	}
	if v := a.View(); !v.Running || !v.DryRun || v.StartedAt == nil {
		t.Errorf("running view = %+v", v)
	}
	if err := a.ApplyThreshold(72); err != nil || a.View().Threshold != 72 {
		t.Errorf("ApplyThreshold: %v, view threshold %v", err, a.View().Threshold)
	}
	if err := a.ApplyModels("all"); err != nil || a.View().Settings["autoswitch.model"] != "all" {
		t.Errorf("ApplyModels: %v, view %v", err, a.View().Settings["autoswitch.model"])
	}
	if err := a.Wake(); err != nil {
		t.Errorf("Wake: %v", err)
	}
	select {
	case ev := <-a.Events():
		if ev.Kind == "" || ev.At == 0 {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Error("no auto event streamed within 5s")
	}
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
	if a.View().Running {
		t.Error("View reports running right after Stop")
	}
	// The stopped engine's threshold and models no longer steer the usage
	// poll plan (the pin is package-global; the TUI clears it the same way).
	if th, models, pinned := reporting.PollPolicyInputs(); pinned {
		t.Errorf("poll policy still pinned after Stop: threshold %v models %v", th, models)
	}
	if err := a.Start(false); err != nil {
		t.Fatal(err)
	}
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
}

// The quarantine section carries each entry's reason and "at" stamp, so the
// page's Since column has a value.
func TestAutoFacadeQuarantineCarriesReasonAndTime(t *testing.T) {
	sw := fixtureSwitcher(t)
	body := `{"schemaVersion":1,"quarantine":{"3":{"email":"c@example.com","reason":"invalid_grant","at":"2026-09-19T10:00:00Z"},"4":{"email":"d@example.com"}}}`
	if err := os.WriteFile(autoswitch.StatePath(sw.BackupDir()), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	q := newAutoFacade(sw, nil).View().Quarantine
	want := map[string]any{
		"3": map[string]any{"reason": "invalid_grant", "at": "2026-09-19T10:00:00Z"},
		"4": map[string]any{"reason": ""},
	}
	if !reflect.DeepEqual(q, want) {
		t.Fatalf("quarantine = %#v, want %#v", q, want)
	}
}

// waitStopped blocks until the most recently started engine goroutine has
// returned, or the timeout passes, so no tick can outlive a test's temp dir.
func (a *autoFacade) waitStopped(timeout time.Duration) bool {
	a.mu.Lock()
	done, codexDone := a.done, a.codexDone
	a.mu.Unlock()
	deadline := time.After(timeout)
	for _, loop := range []chan struct{}{done, codexDone} {
		if loop == nil {
			continue
		}
		select {
		case <-loop:
		case <-deadline:
			return false
		}
	}
	return true
}

// fakeEngine records whether two engines ever run at once.
type fakeEngine struct {
	stop    chan struct{}
	once    sync.Once
	live    *int32Counter
	release <-chan struct{} // RunLoop returns only after this closes (and stop)
}

type int32Counter struct {
	mu       sync.Mutex
	cur, max int
}

func (c *int32Counter) add(d int) {
	c.mu.Lock()
	c.cur += d
	if c.cur > c.max {
		c.max = c.cur
	}
	c.mu.Unlock()
}

func (e *fakeEngine) RunLoop() int {
	e.live.add(1)
	defer e.live.add(-1)
	<-e.stop
	<-e.release
	return 0
}
func (e *fakeEngine) Stop()                  { e.once.Do(func() { close(e.stop) }) }
func (e *fakeEngine) Wake()                  {}
func (e *fakeEngine) ApplyThreshold(float64) {}
func (e *fakeEngine) ApplyModels(string)     {}

// Stop waits (bounded) for the engine's loop to return; when a tick outlasts
// the wait, Stop reports a lock-kind error (409 on the API) instead of
// holding the caller, the engine counts as stopped, and Start keeps refusing
// until the loop has returned, so two engines never run side by side.
func TestAutoFacadeStopThenStartNeverOverlaps(t *testing.T) {
	sw := fixtureSwitcher(t)
	a := newAutoFacade(sw, nil)
	prevWait := autoStopWait
	autoStopWait = 50 * time.Millisecond
	t.Cleanup(func() { autoStopWait = prevWait })
	live := &int32Counter{}
	release := make(chan struct{})
	a.newEngine = func(settings.AutoSwitchSettings, func(autoswitch.Event), bool) autoEngine {
		return &fakeEngine{stop: make(chan struct{}), live: live, release: release}
	}
	if err := a.Start(true); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	err := a.Stop()
	var ce *cerr.Error
	if !errors.As(err, &ce) || ce.Kind != cerr.KindLock || !strings.Contains(err.Error(), "stopping") {
		t.Fatalf("Stop with the loop still running = %v, want a lock-kind \"stopping\" error", err)
	}
	if waited := time.Since(began); waited > 5*time.Second {
		t.Fatalf("Stop held the caller for %v", waited)
	}
	if a.View().Running {
		t.Error("View reports running after Stop")
	}
	// While the old loop is still finishing, Start is refused, not doubled.
	if err := a.Start(true); err == nil || !strings.Contains(err.Error(), "still stopping") {
		t.Fatalf("Start while the previous loop runs = %v, want \"still stopping\"", err)
	}
	close(release)
	if !a.waitStopped(5 * time.Second) {
		t.Fatal("the released loop never returned")
	}
	for i := 0; i < 5; i++ {
		if err := a.Start(true); err != nil {
			t.Fatal(err)
		}
		if err := a.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	if live.max > 1 {
		t.Fatalf("%d engines ran at once", live.max)
	}
}

// TestAutoEventRingCaps: the ring never grows past autoEventRing and keeps the
// most recent events; the stream never blocks the engine.
func TestAutoEventRingCaps(t *testing.T) {
	sw := fixtureSwitcher(t)
	a := newAutoFacade(sw, nil)
	for i := 0; i < autoEventRing+25; i++ {
		a.onEvent(fakeAutoEvent{kind: "tick", human: "tick", fields: map[string]any{"n": i, "to": "2"}})
	}
	evs := a.View().Events
	if len(evs) != autoEventRing {
		t.Fatalf("ring = %d", len(evs))
	}
	if evs[len(evs)-1].Fields["n"] != autoEventRing+24 || evs[0].Fields["n"] != 25 {
		t.Errorf("ring keeps the wrong end: first n=%v last n=%v", evs[0].Fields["n"], evs[len(evs)-1].Fields["n"])
	}
	if evs[0].Account != "2" {
		t.Errorf("Account not lifted from fields: %+v", evs[0])
	}
	if n := len(a.stream); n != 64 {
		t.Errorf("stream buffered %d, want the full 64 and no block", n)
	}
}

type fakeAutoEvent struct {
	kind, human string
	fields      map[string]any
}

func (e fakeAutoEvent) Kind() string         { return e.kind }
func (e fakeAutoEvent) JSON() map[string]any { return e.fields }
func (e fakeAutoEvent) Human() string        { return e.human }

// The wired dashboard serves: the launch URL sets the port-scoped cookie and
// redirects to /#csrf=<token>, the page itself carries no token, and
// /api/state lists the fixture's accounts with provider keys. With Codex
// accounts at launch the Codex rows follow (without token status), the
// Codex engine is in the auto section, and a Codex row's switch works while
// its alias is refused; without them auto.codex is null and a Codex route
// answers 503 (DESIGN A47).
func TestNewDashboardServes(t *testing.T) {
	t.Run("claude-only", func(t *testing.T) { testNewDashboardServes(t, false) })
	t.Run("with-codex", func(t *testing.T) { testNewDashboardServes(t, true) })
}

func testNewDashboardServes(t *testing.T, codex bool) {
	sw := fixtureSwitcher(t)
	prev := newSwitcher
	newSwitcher = func(store.Options) (*core.Switcher, error) { return sw, nil }
	t.Cleanup(func() { newSwitcher = prev })
	prevPresent, prevQuiet := codexIsPresent, newQuietCodexSwitcher
	t.Cleanup(func() { codexIsPresent, newQuietCodexSwitcher = prevPresent, prevQuiet })
	codexIsPresent = func() bool { return codex }
	if codex {
		codexSw := fixtureCodex(t)
		newQuietCodexSwitcher = func() *codexswitcher.Switcher { return codexSw }
	}
	var errBuf strings.Builder
	d, code := newDashboard(context.Background(), 5, false, ioStreams{out: io.Discard, err: &errBuf}, dashboardOptions{})
	if code != 0 {
		t.Fatalf("newDashboard: %d %s", code, errBuf.String())
	}
	srv := d.srv
	launch, err := srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx) }()
	defer func() { cancel(); <-served }()

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(launch)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "<title>tycswap</title>") {
		t.Fatalf("page %d: %.200s", resp.StatusCode, page)
	}
	if strings.Contains(string(page), srv.Token()) {
		t.Fatal("the page carries the CSRF token")
	}
	if got := resp.Request.URL.Fragment; got != "csrf="+srv.Token() {
		t.Fatalf("redirect landed on fragment %q, want csrf=<token>", got)
	}
	base := launch[:strings.Index(launch, "/?token=")]
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("X-CSRF-Token", srv.Token())
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/api/state?tokenStatus=1", nil)
	req.Header.Set("X-CSRF-Token", srv.Token())
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var st web.State
	err = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || len(st.Accounts) == 0 || st.Settings == nil || st.Auto == nil {
		t.Fatalf("state %d %v: %+v", resp.StatusCode, err, st)
	}
	var codexKeys []string
	for _, row := range st.Accounts {
		key := row["key"].(string)
		_, hasTS := row["tokenStatus"].(string)
		switch {
		case strings.HasPrefix(key, "claude:"):
			if !hasTS {
				t.Errorf("row %v lacks tokenStatus", row["number"])
			}
		case strings.HasPrefix(key, "codex:"):
			codexKeys = append(codexKeys, key)
			if hasTS {
				t.Errorf("codex row %v carries tokenStatus", key)
			}
		default:
			t.Errorf("row key %v", row["key"])
		}
	}
	if !codex {
		if len(codexKeys) != 0 || st.Auto.Codex != nil {
			t.Fatalf("Claude-only dashboard lists codex rows %v, auto.codex %+v", codexKeys, st.Auto.Codex)
		}
		if status, _ := call(http.MethodPost, "/api/switch/codex:1", ""); status != http.StatusServiceUnavailable {
			t.Fatalf("codex switch without Codex: status %d, want 503", status)
		}
		// The tray's switch (the app builds this same dashboard): no Codex
		// switcher to reach.
		var ce *cerr.Error
		for _, key := range []string{"codex:1", "gemini:1"} {
			if _, err := d.switchTo(key); !errors.As(err, &ce) || ce.Kind != cerr.KindAccountNotFound {
				t.Fatalf("tray switch to %s without its accounts = %v", key, err)
			}
		}
		return
	}
	if !reflect.DeepEqual(codexKeys, []string{"codex:1", "codex:2"}) || st.Auto.Codex == nil || st.Auto.Codex.Running {
		t.Fatalf("codex rows %v, auto.codex %+v", codexKeys, st.Auto.Codex)
	}
	status, body := call(http.MethodPost, "/api/switch/codex:2", "")
	if res, _ := body["result"].(map[string]any); status != http.StatusOK || res["number"] != "2" || !reflect.DeepEqual(res["runningPids"], []any{float64(999)}) {
		t.Fatalf("codex switch: %d %v", status, body)
	}
	if status, _ := call(http.MethodPost, "/api/accounts/codex:1/alias", `{"alias":"x"}`); status != http.StatusNotFound {
		t.Fatalf("codex alias: status %d, want 404", status)
	}
	// The tray's switch dispatches a row key the same way: the Codex
	// switcher, with the running codex PIDs back for its notification.
	if pids, err := d.switchTo("codex:1"); err != nil || !reflect.DeepEqual(pids, []int{999}) {
		t.Fatalf("tray codex switch = %v, %v", pids, err)
	}
}

// ---- Codex in the dashboard (DESIGN A47) ----

// fixtureCodex adds Codex accounts to the fixture home: a@ (slot 1, the live
// login) and b@ (slot 2) in the Codex store under the backup root, and an
// offline Codex switcher over it whose usage is 10% for every account and
// whose switch reports codex running as pid 999.
func fixtureCodex(t *testing.T) *codexswitcher.Switcher {
	t.Helper()
	home := t.TempDir()
	testutil.Setenv(t, "CODEX_HOME", home)
	writeLiveAuth(t, seedOne(t))
	seedCodex(t, testAcctB, testUserB, "b@example.com")
	return codexswitcher.New(codexswitcher.Options{
		Platform: platform.Linux,
		Client: &codexapi.FakeClient{UsageFn: func(context.Context, string, string) codexapi.UsageFetch {
			return codexapi.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 10.0}, "plan": "pro"}}
		}},
		Stdout:      io.Discard,
		RunningPIDs: func() []int { return []int{999} },
	})
}

// fakeCodexSource is the Codex engine's Source: two accounts, the active one
// at activePct and the other at 12%, a switch that reports codex pid 777 or
// fails, and an optional gate that holds a switch in flight.
type fakeCodexSource struct {
	mu        sync.Mutex
	activePct float64
	accounts  int
	switched  []string
	switchErr error
	entered   chan struct{} // receives once per switch that started
	gate      chan struct{} // when non-nil, a switch waits for it to close
	snapshots int           // passes taken: one per tick
}

func (f *fakeCodexSource) AccountsSnapshot(context.Context, map[string]bool) reporting.AccountsSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots++
	rows := []reporting.AccountSnapshot{
		{Number: "1", IsActive: true, Switchable: true, Provider: reporting.ProviderCodex,
			Usage: usage.UsageEntry{LastGood: map[string]any{"five_hour": map[string]any{"pct": f.activePct}}}},
		{Number: "2", Switchable: true, Provider: reporting.ProviderCodex,
			Usage: usage.UsageEntry{LastGood: map[string]any{"five_hour": map[string]any{"pct": 12.0}}}},
	}
	return reporting.AccountsSnapshot{ActiveNumber: "1", Accounts: rows[:f.accounts], Provider: reporting.ProviderCodex}
}

func (f *fakeCodexSource) SwitchableAccountNumbers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []string{"1", "2"}[:f.accounts]
}

func (f *fakeCodexSource) SwitchTo(_ context.Context, id string) (codexswitcher.SwitchResult, error) {
	f.mu.Lock()
	entered, gate, err := f.entered, f.gate, f.switchErr
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	if err != nil {
		return codexswitcher.SwitchResult{}, err
	}
	f.mu.Lock()
	f.switched = append(f.switched, id)
	f.mu.Unlock()
	return codexswitcher.SwitchResult{Number: id, RunningPIDs: []int{777}}, nil
}

func (f *fakeCodexSource) Snapshots() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshots
}

func (f *fakeCodexSource) Switched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.switched...)
}

// codexFacade is the engine host over the fixture with a fake Claude engine
// (its loop returns once stopped) and the Codex engine over src, built by the
// host's seam with the bar and margin the settings give it.
func codexFacade(t *testing.T, src *fakeCodexSource) (*autoFacade, *int32Counter) {
	t.Helper()
	sw := fixtureSwitcher(t)
	a := newAutoFacade(sw, fixtureCodex(t))
	live := &int32Counter{}
	released := make(chan struct{})
	close(released)
	a.newEngine = func(settings.AutoSwitchSettings, func(autoswitch.Event), bool) autoEngine {
		return &fakeEngine{stop: make(chan struct{}), live: live, release: released}
	}
	a.newCodexEngine = func(s settings.AutoSwitchSettings) *codexauto.AutoSwitcher {
		return codexauto.New(src, codexThreshold(s), s.HysteresisPct)
	}
	t.Cleanup(func() {
		_ = a.Stop()
		if !a.waitStopped(10 * time.Second) {
			t.Error("an engine goroutine still running at cleanup")
		}
	})
	return a, live
}

// nextCodexEvent waits for the next Codex event on the host's stream.
func nextCodexEvent(t *testing.T, a *autoFacade) web.AutoEventView {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-a.Events():
			if ev.Provider == reporting.ProviderCodex {
				return ev
			}
		case <-deadline:
			t.Fatal("no Codex event within 5s")
		}
	}
}

// One Start runs both engines, as `tycswap auto` does: the Claude one and
// beside it the Codex one, which ticks at once; its switch reaches the event
// log as a provider-tagged switch event, and the view shows it running with
// its last tick. One Stop ends both.
func TestAutoFacade_StartsBothEngines(t *testing.T) {
	src := &fakeCodexSource{activePct: 98, accounts: 2}
	a, live := codexFacade(t, src)
	loaded := settings.Load(a.sw.BackupDir())
	if v := a.View().Codex; v == nil || v.Running || v.LastTick != nil || v.Enabled != loaded.CodexEnabled || v.Threshold != codexThreshold(loaded) {
		t.Fatalf("idle codex view = %+v", v)
	}
	if err := a.Start(false); err != nil {
		t.Fatal(err)
	}
	ev := nextCodexEvent(t, a)
	if ev.Kind != "switch" || ev.Account != "2" || !strings.HasPrefix(ev.Message, "codex: switched 1 (98%) -> 2 (12%)") ||
		!reflect.DeepEqual(ev.Fields, map[string]any{"outcome": "switched", "detail": "switched 1 (98%) -> 2 (12%)", "switchedTo": "2", "runningPids": []int{777}}) {
		t.Fatalf("codex event = %+v", ev)
	}
	v := a.View()
	if !v.Running || v.Codex == nil || !v.Codex.Running || v.Codex.LastTick == nil || v.Codex.LastTick.Outcome != "switched" ||
		v.Codex.LastTick.SwitchedTo == nil || *v.Codex.LastTick.SwitchedTo != "2" {
		t.Fatalf("running view = %+v codex %+v", v, v.Codex)
	}
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
	// Stop has waited for both loops, so the Claude engine's loop has run
	// (and returned) by now; asked before Stop, it might not have started.
	live.mu.Lock()
	claudeRan, claudeLive := live.max, live.cur
	live.mu.Unlock()
	if claudeRan != 1 || claudeLive != 0 || !reflect.DeepEqual(src.Switched(), []string{"2"}) {
		t.Fatalf("claude engines ran %d (live %d), codex switches %v", claudeRan, claudeLive, src.Switched())
	}
	if v := a.View(); v.Running || v.Codex.Running || v.Codex.LastTick == nil {
		t.Fatalf("stopped view = %+v codex %+v, want both stopped and the last tick kept", v, v.Codex)
	}
}

// autoswitch.codexEnabled off: Start builds no Codex engine (the shared
// constructor's rule, with Codex present), so nothing ticks; the view says
// the engine is off. On an install without Codex accounts at launch there is
// no Codex view at all.
func TestAutoFacade_CodexDisabledNoLoop(t *testing.T) {
	sw := fixtureSwitcher(t)
	if _, err := settings.SetSetting(sw.BackupDir(), "autoswitch.codexEnabled", "false"); err != nil {
		t.Fatal(err)
	}
	prev := codexIsPresent
	t.Cleanup(func() { codexIsPresent = prev })
	codexIsPresent = func() bool { return true }
	for _, codexSw := range []*codexswitcher.Switcher{fixtureCodex(t), nil} {
		a := newAutoFacade(sw, codexSw)
		a.newEngine = func(settings.AutoSwitchSettings, func(autoswitch.Event), bool) autoEngine {
			released := make(chan struct{})
			close(released)
			return &fakeEngine{stop: make(chan struct{}), live: &int32Counter{}, release: released}
		}
		if err := a.Start(false); err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		eng := a.codexEngine
		a.mu.Unlock()
		if eng != nil {
			t.Errorf("codex switcher %v: a Codex engine runs with autoswitch.codexEnabled off", codexSw != nil)
		}
		v := a.View().Codex
		if codexSw == nil && v != nil {
			t.Errorf("a Codex view without Codex accounts: %+v", v)
		}
		if codexSw != nil && (v == nil || v.Enabled || v.Running || v.LastTick != nil) {
			t.Errorf("codex view = %+v, want off, not running, no tick", v)
		}
		if err := a.Stop(); err != nil {
			t.Fatal(err)
		}
	}
}

// Only a switch or an error reaches the event log, as `tycswap auto` prints
// them, and under dry-run every tick does; each outcome takes the Claude
// engine's kind for it. Every tick is the view's last tick.
func TestAutoFacade_CodexEventsSwitchedAndErrorOnly(t *testing.T) {
	src := &fakeCodexSource{accounts: 2}
	a, _ := codexFacade(t, src)
	eng := codexauto.New(src, 90, 10)
	cases := []struct {
		name      string
		activePct float64
		accounts  int
		switchErr error
		dryRun    bool
		kind      string // "" = no event
		outcome   string
	}{
		{"below the bar", 50, 2, nil, false, "", "ok"},
		{"no rotatable account", 98, 0, nil, false, "", "no-accounts"},
		{"blocked", 98, 2, nil, false, "", "blocked"},
		{"switched", 98, 2, nil, false, "switch", "switched"},
		{"error", 98, 2, cerr.Switch("busy"), false, "error", "error"},
		{"dry-run below the bar", 50, 2, nil, true, "no-switch", "ok"},
	}
	for _, tc := range cases {
		src.mu.Lock()
		src.activePct, src.accounts, src.switchErr = tc.activePct, tc.accounts, tc.switchErr
		src.mu.Unlock()
		if tc.name == "blocked" {
			src.mu.Lock()
			src.activePct = 15 // the other account at 12% is not 10 points better
			src.mu.Unlock()
			eng.Threshold = 14
		} else {
			eng.Threshold = 90
		}
		before := len(a.View().Events)
		a.codexTick(context.Background(), eng, tc.dryRun)
		v := a.View()
		if v.Codex.LastTick == nil || v.Codex.LastTick.Outcome != tc.outcome {
			t.Errorf("%s: last tick %+v, want outcome %s", tc.name, v.Codex.LastTick, tc.outcome)
		}
		added := v.Events[before:]
		if tc.kind == "" {
			if len(added) != 0 {
				t.Errorf("%s: events %+v, want none", tc.name, added)
			}
			continue
		}
		if len(added) != 1 || added[0].Kind != tc.kind || added[0].Provider != "codex" || added[0].Fields["outcome"] != tc.outcome {
			t.Errorf("%s: events %+v, want one %s", tc.name, added, tc.kind)
		}
	}
	// A blocked tick under dry-run reads as the Claude engine's all-exhausted.
	src.mu.Lock()
	src.activePct, src.accounts, src.switchErr = 15, 2, nil
	src.mu.Unlock()
	eng.Threshold = 14
	a.codexTick(context.Background(), eng, true)
	if evs := a.View().Events; evs[len(evs)-1].Kind != "all-exhausted" {
		t.Errorf("blocked dry-run tick = %+v", evs[len(evs)-1])
	}
}

// Stop must not return while a Codex tick is in flight (mirroring
// TestStopCodexLoopWaitsForAnInFlightTick): a tick that ends within the
// wait is waited for; one that outlasts it makes Stop report a lock-kind
// error, and Start refuses until that loop has returned, so two Codex
// engines never run side by side.
func TestAutoFacade_StopWaitsForCodexTick(t *testing.T) {
	prevWait := autoStopWait
	t.Cleanup(func() { autoStopWait = prevWait })
	src := &fakeCodexSource{activePct: 98, accounts: 2, entered: make(chan struct{}, 1), gate: make(chan struct{})}
	a, _ := codexFacade(t, src)

	autoStopWait = 5 * time.Second
	if err := a.Start(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-src.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the Codex loop did not tick")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- a.Stop() }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) while a Codex tick was still running", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(src.gate)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop after the tick finished = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the tick finished")
	}
	if got := src.Switched(); len(got) != 1 {
		t.Fatalf("switches %v, want the in-flight one finished", got)
	}

	autoStopWait = 50 * time.Millisecond
	src.mu.Lock()
	src.gate = make(chan struct{})
	gate := src.gate
	src.mu.Unlock()
	if err := a.Start(false); err != nil {
		t.Fatal(err)
	}
	<-src.entered
	err := a.Stop()
	var ce *cerr.Error
	if !errors.As(err, &ce) || ce.Kind != cerr.KindLock || !strings.Contains(err.Error(), "stopping") {
		t.Fatalf("Stop with the Codex tick still running = %v, want a lock-kind \"stopping\" error", err)
	}
	if err := a.Start(false); err == nil || !strings.Contains(err.Error(), "still stopping") {
		t.Fatalf("Start while the Codex loop finishes = %v, want \"still stopping\"", err)
	}
	close(gate)
	if !a.waitStopped(5 * time.Second) {
		t.Fatal("the released Codex loop never returned")
	}
	src.mu.Lock()
	src.gate = nil
	src.mu.Unlock()
	if err := a.Start(false); err != nil {
		t.Fatal(err)
	}
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
}

// codexOps is `tycswap codex switch|disable|enable|remove -y|add` over the
// Codex switcher: a switch answers the running codex PIDs, and an unknown
// account is the switcher's not-found error (404 on the API).
func TestCodexOpsAdapter(t *testing.T) {
	fixtureSwitcher(t)
	ops := newCodexOps(fixtureCodex(t))
	res, err := ops.SwitchTo("2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res, map[string]any{"number": "2", "email": "b@example.com", "runningPids": []int{999}, "alreadyActive": false}) {
		t.Fatalf("switch result %v", res)
	}
	if res, err := ops.SwitchTo("b@example.com"); err != nil || res["alreadyActive"] != true {
		t.Fatalf("switch to the live account = %v, %v", res, err)
	}
	if err := ops.SetAccountDisabled("1", true); err != nil {
		t.Fatal(err)
	}
	if slots := testStore().Slots(); !slots[0].Disabled {
		t.Fatalf("slot 1 not disabled: %+v", slots[0])
	}
	if err := ops.RemoveAccount("1"); err != nil {
		t.Fatal(err)
	}
	if n := len(testStore().Slots()); n != 1 {
		t.Fatalf("%d slots after remove, want 1", n)
	}
	var ce *cerr.Error
	if _, err := ops.SwitchTo("9"); !errors.As(err, &ce) || ce.Kind != cerr.KindAccountNotFound {
		t.Fatalf("unknown account = %v, want not found", err)
	}
	writeLiveAuth(t, makeCodexAuth(t, "acct-c", "user-c", "c@example.com", time.Now().Unix()+3600))
	res, err = ops.AddCurrent()
	if err != nil || res["email"] != "c@example.com" || res["number"] == "" {
		t.Fatalf("add current = %v, %v", res, err)
	}
	if newCodexOps(nil) != nil {
		t.Error("a Codex façade without a Codex switcher")
	}
}

// Disable and remove hold the Codex store lock, as a Codex switch does end to
// end, so they wait for a Codex tick's switch instead of writing between its
// steps (the store's own writes skip the file lock while this process holds
// it), and a lock that stays held is a lock error with nothing written
// (DESIGN A47).
func TestCodexOpsWaitForTheCodexStoreLock(t *testing.T) {
	fixtureSwitcher(t)
	ops := newCodexOps(fixtureCodex(t))
	for _, tc := range []struct {
		name string
		call func() error
		done func() bool
	}{
		{"disable", func() error { return ops.SetAccountDisabled("2", true) }, func() bool { return testStore().Slots()[1].Disabled }},
		{"remove", func() error { return ops.RemoveAccount("2") }, func() bool { return len(testStore().Slots()) == 1 }},
	} {
		held := testStore().Lock()
		if ok, err := held.Acquire(time.Second); !ok || err != nil {
			t.Fatalf("%s: cannot take the lock: %v %v", tc.name, ok, err)
		}
		returned := make(chan error, 1)
		go func() { returned <- tc.call() }()
		select {
		case err := <-returned:
			_ = held.Release()
			t.Fatalf("%s returned (%v) while another holder had the Codex store lock", tc.name, err)
		case <-time.After(100 * time.Millisecond):
		}
		if tc.done() {
			t.Errorf("%s wrote while the lock was held", tc.name)
		}
		_ = held.Release()
		select {
		case err := <-returned:
			if err != nil {
				t.Fatalf("%s after the release: %v", tc.name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not finish after the lock was released", tc.name)
		}
		if !tc.done() {
			t.Errorf("%s did not write after the release", tc.name)
		}
	}

	prev := codexOpsLockWait
	codexOpsLockWait = 50 * time.Millisecond
	t.Cleanup(func() { codexOpsLockWait = prev })
	held := testStore().Lock()
	if ok, err := held.Acquire(time.Second); !ok || err != nil {
		t.Fatalf("cannot take the lock: %v %v", ok, err)
	}
	defer func() { _ = held.Release() }()
	var ce *cerr.Error
	for _, err := range []error{ops.SetAccountDisabled("1", true), ops.RemoveAccount("1")} {
		if !errors.As(err, &ce) || ce.Kind != cerr.KindLock {
			t.Errorf("with the lock held throughout: %v, want a lock error", err)
		}
	}
	if slots := testStore().Slots(); len(slots) != 1 || slots[0].Disabled {
		t.Errorf("a busy call wrote: %+v", slots)
	}
}
