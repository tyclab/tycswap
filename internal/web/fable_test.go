// Tests for the Fable store on the dashboard (DESIGN A104): the state
// document's fable fields and the switch route's store=fable.
package web

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/reporting"
)

// fakeFable is the FableOps seam: recorded calls; during, when set, runs
// inside each call.
type fakeFable struct {
	mu     sync.Mutex
	calls  []string
	during func()
}

func (f *fakeFable) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	during := f.during
	f.mu.Unlock()
	if during != nil {
		during()
	}
}

func (f *fakeFable) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeFable) SwitchFable(id string, jsonOut bool) (map[string]any, error) {
	f.record("SwitchFable(" + id + ")")
	n := 2
	return map[string]any{"switched": true, "to": map[string]any{"number": &n, "email": "bob@example.com"}, "reason": "switched", "store": "fable"}, nil
}

func (f *fakeFable) EmptyFable(jsonOut bool) (map[string]any, error) {
	f.record("EmptyFable()")
	return map[string]any{"switched": true, "to": nil, "reason": "emptied", "store": "fable"}, nil
}

// withFable serves an install with Fable logins: account 1 holds the Fable
// store, account 2 a login to sign in again.
func withFable(f *fakeFable) option {
	return func(h *harness, d *Deps) {
		d.Fable = f
		snap := sampleSnapshot()
		snap.Fable = map[string]any{"state": "on", "storeDir": "/tmp/backups/fable/store", "number": 1, "email": "alice@example.com", "source": "backup"}
		snap.Accounts[0].FableLogin = &reporting.FableLogin{Live: true, Status: reporting.FableLoginOK}
		snap.Accounts[1].FableLogin = &reporting.FableLogin{Status: reporting.FableSignInAgain}
		h.fa.snap = snap
		h.auto.view.Fable = &FableAutoView{LastTickAt: f64(1758275900), Caption: "Counting 5h + 7d + Fable", Ranking: []string{"2"}, Quarantine: map[string]any{}}
	}
}

func stateOf(t *testing.T, h *harness) map[string]any {
	t.Helper()
	var st map[string]any
	if err := json.Unmarshal(readBody(t, h.get("/api/state")), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStateCarriesTheFableStore(t *testing.T) {
	h := newHarness(t, withFable(&fakeFable{}))
	st := stateOf(t, h)
	fable, _ := st["fable"].(map[string]any)
	if fable["state"] != "on" || fable["number"] != 1.0 || fable["storeDir"] != "/tmp/backups/fable/store" {
		t.Fatalf("fable = %v", st["fable"])
	}
	rows := st["accounts"].([]any)
	if got := rows[0].(map[string]any)["fableLogin"]; !reflect.DeepEqual(got, map[string]any{"live": true, "status": "ok"}) {
		t.Errorf("row 1 fableLogin = %v", got)
	}
	if got := rows[1].(map[string]any)["fableLogin"]; !reflect.DeepEqual(got, map[string]any{"live": false, "status": "sign in again"}) {
		t.Errorf("row 2 fableLogin = %v", got)
	}
	auto := st["auto"].(map[string]any)["fable"].(map[string]any)
	if auto["caption"] != "Counting 5h + 7d + Fable" || !reflect.DeepEqual(auto["ranking"], []any{"2"}) || auto["lastTickAt"] != 1758275900.0 {
		t.Errorf("auto.fable = %v", auto)
	}
	if _, has := auto["heldUntil"]; has {
		t.Errorf("auto.fable.heldUntil without a hold: %v", auto["heldUntil"])
	}
}

// The in-process tray reads the state's rows without a JSON round trip: a
// row's fableLogin is the same map either way.
func TestStateRowFableLoginIsAMap(t *testing.T) {
	h := newHarness(t, withFable(&fakeFable{}))
	st := h.s.buildState(stateOpts{})
	if got, ok := st.Accounts[0]["fableLogin"].(map[string]any); !ok || got["live"] != true || got["status"] != "ok" {
		t.Fatalf("fableLogin = %#v", st.Accounts[0]["fableLogin"])
	}
}

func TestStateWithoutFable(t *testing.T) {
	h := newHarness(t)
	raw := string(readBody(t, h.get("/api/state")))
	if strings.Contains(raw, "fable") {
		t.Fatalf("a state without Fable logins mentions fable: %s", raw)
	}
}

func TestFableSwitchRoute(t *testing.T) {
	f := &fakeFable{}
	h := newHarness(t, withFable(f))
	resp := h.post("/api/switch/claude:2?store=fable")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if res := decodeJSON(t, resp)["result"].(map[string]any); res["store"] != "fable" || res["switched"] != true {
		t.Fatalf("result = %v", res)
	}
	if resp := h.post("/api/switch/claude:9?store=fable&empty=1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("empty: status %d", resp.StatusCode)
	}
	if got := f.Calls(); !reflect.DeepEqual(got, []string{"SwitchFable(2)", "EmptyFable()"}) {
		t.Fatalf("fable calls %v", got)
	}
	if got := h.fa.Calls(); len(got) != 0 {
		t.Fatalf("the default store was reached: %v", got)
	}
}

// The Fable switch and the empty take the mutation path as every switch
// does (DESIGN A104): under the mutation lock, a reply whose stateSequence
// is newer than the state read before, and a state on the stream.
func TestFableSwitchRouteIsAMutation(t *testing.T) {
	f := &fakeFable{}
	h := newHarness(t, withFable(f))
	var unlocked atomic.Int32
	f.during = func() {
		if h.s.mutMu.TryLock() {
			h.s.mutMu.Unlock()
			unlocked.Add(1)
		}
	}
	stream := h.openSSE()
	defer stream.close()
	stream.nextState(t, timeout)
	for _, path := range []string{"/api/switch/claude:2?store=fable", "/api/switch/claude:9?store=fable&empty=1"} {
		before := decodeJSON(t, h.get("/api/state"))["sequence"].(float64)
		reply := decodeJSON(t, h.post(path))
		if barrier, ok := reply["stateSequence"].(float64); !ok || barrier <= before {
			t.Errorf("%s: stateSequence %v, state before it %v", path, reply["stateSequence"], before)
		}
		stream.nextState(t, timeout)
	}
	if n := unlocked.Load(); n != 0 {
		t.Errorf("%d Fable store changes ran outside the mutation lock", n)
	}
}

func TestFableSwitchRouteRefusals(t *testing.T) {
	for _, tc := range []struct {
		path string
		want int
		opts []option
	}{
		{"/api/switch/claude:2?store=default", http.StatusBadRequest, nil},
		{"/api/switch/claude:2?store=opus", http.StatusBadRequest, nil},
		{"/api/switch/codex:1?store=fable", http.StatusBadRequest, []option{withCodex()}},
		{"/api/switch/claude:2?store=fable&force=1", http.StatusBadRequest, nil},
		{"/api/switch/claude:2?store=fable&confirmAuthChange=1", http.StatusBadRequest, nil},
	} {
		f := &fakeFable{}
		h := newHarness(t, append([]option{withFable(f)}, tc.opts...)...)
		resp := h.post(tc.path)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.path, resp.StatusCode, tc.want)
		}
		decodeError(t, resp)
		if calls := append(f.Calls(), h.fa.Calls()...); len(calls) != 0 {
			t.Errorf("%s reached %v", tc.path, calls)
		}
	}
	h := newHarness(t)
	if resp := h.post("/api/switch/claude:2?store=fable"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("without the Fable seam: status %d, want 503", resp.StatusCode)
	}
}

// The page with and without a Fable store (DESIGN A104), over the node
// harness the Auto tab tests use (A93).
func TestAppJSFable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; Fable page test skipped")
	}
	path := filepath.Join(t.TempDir(), "app.js")
	if err := os.WriteFile(path, []byte(staticFile(t, "app.js")), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "testdata/fable.cjs", path).CombinedOutput(); err != nil {
		t.Fatalf("fable page: %v\n%s", err, out)
	}
}

// The remote tray's bearer reaches the Fable switch as it reaches the
// default one (DESIGN A45, A104).
func TestFableSwitchRouteTakesTheBearer(t *testing.T) {
	f := &fakeFable{}
	h := newHarness(t, withRemoteToken(remoteToken), withFable(f))
	if resp := h.do(bearer(h.newReq(http.MethodPost, "/api/switch/claude:3?store=fable", nil), remoteToken)); resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer: status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if resp := h.do(bearer(h.newReq(http.MethodPost, "/api/switch/claude:3?store=fable", nil), "wrong")); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: status %d, want 401", resp.StatusCode)
	}
	if got := f.Calls(); !reflect.DeepEqual(got, []string{"SwitchFable(3)"}) {
		t.Fatalf("fable calls %v", got)
	}
}

// A session's store comes from its status line's record (DESIGN A102): while
// an account has a Fable login, a session recorded on the Fable store says
// "fable", one recorded on the default store "default", and one without a
// record says nothing. Without a Fable login no session carries a store.
func TestSessionsNameTheirStore(t *testing.T) {
	dir := t.TempDir()
	records := `{"schemaVersion":1,"sessions":{"sess-1":{"family":"fable","seenAt":1758276000,"store":"fable"},"sess-2":{"family":"opus","seenAt":1758276000}}}`
	if err := os.WriteFile(filepath.Join(dir, "default_models.json"), []byte(records), 0o600); err != nil {
		t.Fatal(err)
	}
	withRecords := func(h *harness, d *Deps) {
		h.fa.backupDir = dir
		h.sessions.Claude = append(h.sessions.Claude, procdetect.ClaudeSession{PID: 4444, SessionID: "sess-3", CWD: "/work/third", StartedAt: 1758276200000, Kind: "interactive"})
	}
	stores := func(h *harness) map[string]any {
		out := map[string]any{}
		for _, c := range stateOf(t, h)["sessions"].(map[string]any)["claude"].([]any) {
			row := c.(map[string]any)
			out[row["sessionId"].(string)] = row["store"]
		}
		return out
	}
	if got := stores(newHarness(t, withFable(&fakeFable{}), withRecords)); !reflect.DeepEqual(got, map[string]any{"sess-1": "fable", "sess-2": "default", "sess-3": nil}) {
		t.Errorf("with a Fable login: stores %v", got)
	}
	if got := stores(newHarness(t, withRecords)); !reflect.DeepEqual(got, map[string]any{"sess-1": nil, "sess-2": nil, "sess-3": nil}) {
		t.Errorf("without a Fable login: stores %v", got)
	}
}
