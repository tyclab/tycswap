// The Fable store on the dashboard (DESIGN A104): the state document, the
// switch route and the Auto view, over a real switcher.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/brand"
	codexapi "github.com/tyclab/tycswap/internal/codex/api"
	codexswitcher "github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/launch"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/web"
)

// served is a dashboard over sw, without Codex, reached with its cookie and
// CSRF token.
type served struct {
	d    *dashboard
	base string
	call func(method, path string) (int, []byte)
}

func serveFableDashboard(t *testing.T, sw *core.Switcher) served {
	t.Helper()
	t.Setenv(brand.Sanitized().EnvPrefix+"_AUTO_MANAGED_BY", testOwner)
	useSwitcher(t, sw)
	prevPresent, prevQuiet := codexIsPresent, newQuietCodexSwitcher
	t.Cleanup(func() { codexIsPresent, newQuietCodexSwitcher = prevPresent, prevQuiet })
	codexIsPresent = func() bool { return false }
	t.Setenv("CODEX_HOME", t.TempDir())
	codexSw := codexswitcher.New(codexswitcher.Options{
		Platform: platform.Linux, Client: &codexapi.FakeClient{}, Stdout: io.Discard,
		RunningPIDs: func() []int { return nil },
	})
	newQuietCodexSwitcher = func() *codexswitcher.Switcher { return codexSw }
	var errBuf strings.Builder
	d, code := newDashboard(context.Background(), 5, false, ioStreams{out: io.Discard, err: &errBuf}, dashboardOptions{noUpdateSchedule: true})
	if code != 0 {
		t.Fatalf("newDashboard: %d %s", code, errBuf.String())
	}
	launchURL, err := d.srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done; d.auto.stopKeepingChoice() })
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	base := launchURL[:strings.Index(launchURL, "/?token=")]
	return served{d: d, base: base, call: func(method, path string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, nil)
		req.Header.Set("X-CSRF-Token", d.srv.Token())
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}}
}

var (
	stateSequence = regexp.MustCompile(`"(sequence|stateSequence)":\d+`)
	stateVersion  = regexp.MustCompile(`"version":"[^"]*"`)
	stateUpdates  = regexp.MustCompile(`"updates":.*?,"ui":`)
)

// normalizedState is GET /api/state with what changes from run to run
// replaced: the home, times, the document's sequence, the build version and
// the updates check, which runs beside the request.
func normalizedState(t *testing.T, srv served, home string) string {
	t.Helper()
	status, body := srv.call(http.MethodGet, "/api/state")
	if status != http.StatusOK {
		t.Fatalf("state: %d %s", status, body)
	}
	quoted, _ := json.Marshal(home)
	out := strings.ReplaceAll(string(body), strings.Trim(string(quoted), `"`), "<HOME>")
	out = stateSequence.ReplaceAllString(out, `"$1":<N>`)
	out = stateVersion.ReplaceAllString(out, `"version":"<V>"`)
	out = stateUpdates.ReplaceAllString(out, `"updates":<U>,"ui":`)
	return volatile.ReplaceAllString(out, "<T>") + "\n"
}

// Without a Fable login the state document is the one v0.15.1 served,
// byte for byte (DESIGN A104, plan 1.1): testdata/nofable-state.json was
// recorded from the tree before the Fable surfaces existed. The full bytes
// are compared on Linux, where it was recorded; every platform compares
// its key paths.
func TestDashboardStateWithoutFableLogins(t *testing.T) {
	for _, name := range launch.AuthOverrideEnvVars {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	sw := fixtureSwitcher(t)
	got := normalizedState(t, serveFableDashboard(t, sw), sw.Home)
	golden := filepath.Join("testdata", "nofable-state.json")
	if record := os.Getenv("TYCSWAP_NOFABLE_STATE_RECORD"); record != "" {
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if want := documentKeyPaths(string(raw)); documentKeyPaths(got) != want {
		t.Errorf("state key paths\n got %s\nwant %s", documentKeyPaths(got), want)
	}
	if runtime.GOOS == "linux" && got != string(raw) {
		t.Errorf("state document changed without a Fable login\n got %s\nwant %s", got, raw)
	}
	for _, key := range []string{`"fable"`, `"fableLogin"`} {
		if strings.Contains(got, key) {
			t.Errorf("the state document without Fable logins has %s", key)
		}
	}
}

func stateDoc(t *testing.T, srv served) map[string]any {
	t.Helper()
	status, body := srv.call(http.MethodGet, "/api/state")
	var st map[string]any
	if err := json.Unmarshal(body, &st); status != http.StatusOK || err != nil {
		t.Fatalf("state: %d %v %s", status, err, body)
	}
	return st
}

// The route switches the Fable store by hand and empties it: the default
// login and the roster stay byte for byte, and the switch holds the Fable
// engine in its own state file (DESIGN A94, A104).
func TestDashboardSwitchesTheFableStoreOnly(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	srv := serveFableDashboard(t, sw)
	kept := []string{filepath.Join(sw.Home, ".claude", ".credentials.json"), sw.Store.SequenceFile, filepath.Join(sw.Home, ".claude.json"),
		autoswitch.StatePath(sw.BackupDir())}
	before := map[string]string{}
	for _, path := range kept {
		before[path] = readOrAbsent(t, path)
	}
	status, body := srv.call(http.MethodPost, "/api/switch/claude:3?store=fable")
	if status != http.StatusOK || !strings.Contains(string(body), `"store":"fable"`) {
		t.Fatalf("switch: %d %s", status, body)
	}
	if live := storeLive(t, sw); !strings.Contains(live, "at-f3") {
		t.Fatalf("the Fable store holds %s, want account 3's Fable login", live)
	}
	var hold struct {
		By string `json:"lastSwitchBy"`
		To string `json:"lastSwitchTo"`
	}
	if err := json.Unmarshal([]byte(readOrAbsent(t, fableStatePath(sw))), &hold); err != nil || hold.By != "hand" || hold.To != "3" {
		t.Fatalf("Fable hold = %+v (%v)", hold, err)
	}
	st := stateDoc(t, srv)
	fable, _ := st["fable"].(map[string]any)
	auto, _ := st["auto"].(map[string]any)["fable"].(map[string]any)
	if fable["number"] != 3.0 || fable["state"] != "on" || auto["heldUntil"] == nil {
		t.Fatalf("after the switch: fable %v, auto.fable %v", fable, auto)
	}
	if status, body := srv.call(http.MethodPost, "/api/switch/claude:1?store=fable&empty=1"); status != http.StatusOK {
		t.Fatalf("empty: %d %s", status, body)
	}
	// One line per move, the dashboard's: the switch itself writes none.
	log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log"))
	for _, line := range []string{"Switched the Fable store to account 3", "Emptied the Fable store"} {
		if n := strings.Count(log, line); n != 1 || !strings.Contains(log, line+" by dashboard(") {
			t.Errorf("%d %q lines, want one, the dashboard's:\n%s", n, line, log)
		}
	}
	if fable := stateDoc(t, srv)["fable"].(map[string]any); fable["state"] != "off" || fable["number"] != nil {
		t.Fatalf("after emptying: fable %v", fable)
	}
	for _, path := range kept {
		if got := readOrAbsent(t, path); got != before[path] {
			t.Errorf("%s changed:\n%s\nwas\n%s", path, got, before[path])
		}
	}
	if status, _ := srv.call(http.MethodPost, "/api/switch/claude:3?store=default"); status != http.StatusBadRequest {
		t.Errorf("store=default: %d, want 400", status)
	}
}

// auto.fable reads the Fable state file and ranks the Fable pool as the
// Fable tick does: 2 holds the store at its Fable limit, 3 has Fable room,
// 4 has no Fable login.
func TestDashboardAutoFable(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	writeFile(t, fableStatePath(sw), `{"schemaVersion":1,"lastSeenAt":1795400000,"quarantine":{"4":{"email":"acct4@example.com","reason":"invalid_grant","at":"2026-11-18T02:00:00Z"}}}`)
	st := stateDoc(t, serveFableDashboard(t, sw))
	auto, _ := st["auto"].(map[string]any)["fable"].(map[string]any)
	want := map[string]any{
		"lastTickAt": 1795400000.0,
		"caption":    "Counting 5h + 7d + Fable",
		"ranking":    []any{"3"},
		"quarantine": map[string]any{"4": map[string]any{"reason": "invalid_grant", "at": "2026-11-18T02:00:00Z"}},
	}
	if !reflect.DeepEqual(auto, want) {
		t.Fatalf("auto.fable = %v\nwant %v", auto, want)
	}
	rows := map[string]any{}
	for _, r := range st["accounts"].([]any) {
		row := r.(map[string]any)
		rows[row["key"].(string)] = row["fableLogin"]
	}
	if !reflect.DeepEqual(rows, map[string]any{"claude:1": nil, "claude:2": map[string]any{"live": true, "status": "ok"},
		"claude:3": map[string]any{"live": false, "status": "ok"}, "claude:4": nil}) {
		t.Fatalf("fableLogin per row = %v", rows)
	}
}

// The whole page over the real dashboard's state with the Fable store on and
// off shows no undefined, null or NaN, no exception and no toast.
func TestDashboardPageWithTheFableStore(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	srv := serveFableDashboard(t, sw)
	state := func() web.State {
		status, body := srv.call(http.MethodGet, "/api/state")
		var st web.State
		if err := json.Unmarshal(body, &st); status != http.StatusOK || err != nil || st.Fable == nil {
			t.Fatalf("state: %d %v %s", status, err, body)
		}
		return st
	}
	cases := []pageCase{{Name: "fable on", State: state()}}
	if status, body := srv.call(http.MethodPost, "/api/switch/claude:2?store=fable&empty=1"); status != http.StatusOK {
		t.Fatalf("empty: %d %s", status, body)
	}
	cases = append(cases, pageCase{Name: "fable off", State: state()})
	renderPage(t, servedPage(func(path string) string { _, body := srv.call(http.MethodGet, path); return string(body) }), cases)
}
