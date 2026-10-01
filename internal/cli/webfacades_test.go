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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
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
	// The fixture's settings.json sets autoswitch.threshold to 80.
	th, ok := byKey["autoswitch.threshold"]
	if !ok || th.Kind != "float" || th.Min == nil || *th.Min != 50 || th.Max == nil || *th.Max != 99.9 || th.IsDefault || th.Value != float64(80) || th.Default != float64(90) || th.Description == "" {
		t.Fatalf("threshold view = %+v", th)
	}
	for _, k := range []string{"autoswitch.codexThreshold", "autoswitch.codexEnabled", "autoswitch.includeApiKeyAccounts", "autoswitch.strategy", "autoswitch.model"} {
		if _, ok := byKey[k]; !ok {
			t.Errorf("setting %s missing", k)
		}
	}
	for _, k := range []string{"autoswitch.fiveHourThreshold", "autoswitch.sevenDayThreshold", "autoswitch.modelThreshold"} {
		if _, ok := byKey[k]; ok {
			t.Errorf("unexpected setting %s", k)
		}
	}
	if _, err := f.Set("autoswitch.threshold", "77"); err != nil {
		t.Fatal(err)
	}
	for _, v := range f.Effective() {
		if v.Key == "autoswitch.threshold" && (v.IsDefault || v.Value != float64(77)) {
			t.Errorf("after Set: %+v", v)
		}
	}
	for _, tc := range [][2]string{{"autoswitch.threshold", "abc"}, {"autoswitch.threshold", "120"}, {"autoswitch.nope", "1"}, {"autoswitch.codexEnabled", "maybe"}} {
		_, err := f.Set(tc[0], tc[1])
		var ce *cerr.Error
		if !errors.As(err, &ce) || ce.Kind != cerr.KindValidation {
			t.Errorf("Set(%s, %s) = %v, want a validation error", tc[0], tc[1], err)
		}
	}
	if _, err := f.Unset("autoswitch.nope"); err == nil {
		t.Error("unknown key unset")
	}
	removed, err := f.Unset("autoswitch.threshold")
	if err != nil || !removed {
		t.Fatalf("Unset = %v, %v", removed, err)
	}
	if removed, _ := f.Unset("autoswitch.threshold"); removed {
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
	a := newAutoFacade(sw)
	t.Cleanup(func() {
		_ = a.Stop()
		if !a.waitStopped(10 * time.Second) {
			t.Error("engine goroutine still running at cleanup")
		}
	})
	v := a.View()
	if !v.Available || v.Running || v.StartedAt != nil || v.Settings["autoswitch.threshold"] != float64(80) || v.Threshold != 80 || v.Events == nil || v.Quarantine == nil {
		t.Fatalf("idle view = %+v", v)
	}
	for _, err := range []error{a.Stop(), a.Wake(), a.ApplyThreshold(50), a.ApplyModels("all")} {
		if err == nil || !strings.Contains(err.Error(), "not running") {
			t.Errorf("idle op err = %v", err)
		}
	}
	if err := a.ApplyThreshold(101); err == nil || !strings.Contains(err.Error(), "between 0 and 100") {
		t.Errorf("bounds err = %v", err)
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
	if err := a.Start(false); err != nil {
		t.Fatal(err)
	}
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
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

// Stop waits for the engine's loop to return, so Stop then Start never runs
// two engines side by side.
func TestAutoFacadeStopThenStartNeverOverlaps(t *testing.T) {
	sw := fixtureSwitcher(t)
	a := newAutoFacade(sw)
	live := &int32Counter{}
	release := make(chan struct{})
	a.newEngine = func(settings.AutoSwitchSettings, func(autoswitch.Event), bool) autoEngine {
		return &fakeEngine{stop: make(chan struct{}), live: live, release: release}
	}
	if err := a.Start(true); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- a.Stop() }()
	// While the old loop is still finishing, Start is refused, not doubled.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := a.Start(true); err != nil && strings.Contains(err.Error(), "still stopping") {
			break
		} else if err == nil {
			t.Fatal("Start succeeded while the previous engine was still running")
		}
		if time.Now().After(deadline) {
			t.Fatal("Start never reported the engine as stopping")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-stopped:
		t.Fatal("Stop returned before the engine's loop did")
	default:
	}
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
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
	a := newAutoFacade(sw)
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

// The wired dashboard serves: the launch URL sets the port-scoped cookie, the
// page carries the CSRF token, and /api/state lists the fixture's accounts
// with provider keys.
func TestNewDashboardServes(t *testing.T) {
	sw := fixtureSwitcher(t)
	prev := newSwitcher
	newSwitcher = func(store.Options) (*core.Switcher, error) { return sw, nil }
	t.Cleanup(func() { newSwitcher = prev })
	var errBuf strings.Builder
	srv, _, code := newDashboard(5, false, ioStreams{out: io.Discard, err: &errBuf})
	if code != 0 {
		t.Fatalf("newDashboard: %d %s", code, errBuf.String())
	}
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
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), `<meta name="csrf" content="`+srv.Token()+`">`) || !strings.Contains(string(page), "<title>tycswap</title>") {
		t.Fatalf("page %d: %.200s", resp.StatusCode, page)
	}
	base := launch[:strings.Index(launch, "/?token=")]
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
	for _, row := range st.Accounts {
		if !strings.HasPrefix(row["key"].(string), "claude:") {
			t.Errorf("row key %v", row["key"])
		}
		if _, ok := row["tokenStatus"].(string); !ok {
			t.Errorf("row %v lacks tokenStatus", row["number"])
		}
	}
}
