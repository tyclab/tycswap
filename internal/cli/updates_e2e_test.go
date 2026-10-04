package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/ccversion"
	"github.com/tyclab/tycswap/internal/update"
	"github.com/tyclab/tycswap/internal/web"
)

// End to end for DESIGN A44, with nothing faked between the sources and what
// the user sees: a release endpoint, Claude Code's own release channel and a
// `claude` binary in a temporary home, the real release and Claude Code
// checks, the tray shell and the app's dashboard server. Only the tray (a
// recorder) and Claude Code (a script that does what `claude --version` and
// `claude update` do) stand in.

type e2e struct {
	t            *testing.T
	sh           *appShell
	ft           *fakeTray
	srv          *web.Server
	base, cookie string
	client       *http.Client
	claudeLog    string
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in for claude is a shell script")
	}
	root := t.TempDir()
	e := &e2e{t: t, claudeLog: filepath.Join(root, "claude.log")}

	// tycswap's release endpoint: a newer release is out.
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v99.1.0"}`))
	}))
	t.Cleanup(releases.Close)
	prevEndpoint := update.Endpoint
	update.Endpoint = releases.URL
	t.Cleanup(func() { update.Endpoint = prevEndpoint })
	// Claude Code's release channel: 2.1.281 is the newest on "latest".
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("2.1.281\n"))
	}))
	t.Cleanup(channel.Close)

	// Claude Code, as far as `--version` and `update` go: the version lives in
	// a file the update rewrites.
	versionFile := filepath.Join(root, "claude.version")
	writeTestFile(t, versionFile, "2.1.280\n", 0o644)
	claude := filepath.Join(root, "bin", "claude")
	writeTestFile(t, claude, `#!/bin/sh
echo "$*" >> '`+e.claudeLog+`'
case "$1" in
--version) echo "$(cat '`+versionFile+`') (Claude Code)" ;;
update) echo "Updating Claude Code to 2.1.281"; echo 2.1.281 > '`+versionFile+`' ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`, 0o755)
	env := ccversion.DefaultEnv()
	env.Home = filepath.Join(root, "home")
	env.Getenv = func(string) string { return "" }
	env.LookPath = func(string) (string, error) { return claude, nil }
	env.Exists = func(string) bool { return false }
	env.Endpoints = ccversion.Endpoints{Downloads: channel.URL}

	e.ft = &fakeTray{}
	e.sh = newAppShell(e.ft, shellActions{
		LatestVersion:   latestReleaseTag(filepath.Join(root, "backup")),
		CheckClaudeCode: func() ccversion.Status { return ccversion.Check(context.Background(), env) },
		RunClaudeCode: func(c ccversion.Command, in *ccversion.Installed) (string, error) {
			return ccversion.Run(context.Background(), env, c, in)
		},
		Ask:     func(string, string, string, string) (bool, error) { return true, nil },
		Current: "v0.6.0",
	})
	in, err := ccversion.Find(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	e.sh.setClaudeCodeInstalled(in)

	updates := &updatesFacade{}
	e.srv, err = web.New(web.Deps{
		Facade:             &fakeFacade{dir: root}, // no switcher, so no keychain
		Updates:            updates,
		UpdatesOwnSchedule: true,
		Sessions:           func() web.SessionsView { return web.SessionsView{} },
		Interval:           time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.sh.act.UpdatesChanged = e.srv.Refresh
	updates.sh.Store(e.sh)
	e.srv.OnState(e.sh.update)
	e.sh.update(e.srv.Snapshot())
	launch, err := e.srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- e.srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-served })

	// Sign in the way the browser does: the launch URL sets the cookie.
	e.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := e.client.Get(launch)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		e.cookie = c.Name + "=" + c.Value
	}
	e.base = launch[:strings.Index(launch, "/?token=")]
	return e
}

func (e *e2e) api(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.base+path, strings.NewReader(body))
	req.Header.Set("Cookie", e.cookie)
	req.Header.Set("X-CSRF-Token", e.srv.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		e.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	return resp.StatusCode, out
}

// updates is the dashboard's Updates section of GET /api/state, once no
// check runs any more.
func (e *e2e) updates() map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, st := e.api(http.MethodGet, "/api/state", "")
		u, _ := st["updates"].(map[string]any)
		if u == nil {
			e.t.Fatalf("state.updates = %v", st["updates"])
		}
		if u["checking"] == false || time.Now().After(deadline) {
			return u
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUpdatesEndToEnd(t *testing.T) {
	e := newE2E(t)

	// At start: the quick look only. Nothing waits, and the card does not
	// claim a check: nothing has asked the network yet.
	if iconKinds(e.ft) != "" || e.sh.updateWaiting() {
		t.Fatalf("badge with nothing checked: %s", iconKinds(e.ft))
	}
	u := e.updates()
	if u["checkedAt"] != nil {
		t.Errorf("checked at %v after the local look alone", u["checkedAt"])
	}
	if cc, _ := u["claudeCode"].(map[string]any); cc["installed"] != "2.1.280" || cc["state"] != "checking" {
		t.Errorf("card before the check: %v", cc)
	}

	// 1. The dashboard's "Check now": a newer tycswap and a newer Claude Code.
	if code, _ := e.api(http.MethodPost, "/api/updates/check", "{}"); code != http.StatusAccepted {
		t.Fatalf("check: %d", code)
	}
	u = e.updates()
	app, _ := u["app"].(map[string]any)
	cc, _ := u["claudeCode"].(map[string]any)
	if u["available"] != true || u["checkedAt"] == nil || app["latest"] != "v99.1.0" || app["available"] != true ||
		cc["state"] != "update" || cc["installed"] != "2.1.280" || cc["latest"] != "2.1.281" || cc["command"] != "claude update" || cc["available"] != true {
		t.Fatalf("dashboard after the check: %v", u)
	}
	// The two checks run side by side, so the count may pass through 1.
	if got := iconKinds(e.ft); !strings.HasSuffix(got, "badge:2") {
		t.Errorf("tray icons = %s, want the last to be badge:2", got)
	}
	if it, ok := e.ft.item("claude-code"); !ok || it.Title != "Update Claude Code 2.1.280 → 2.1.281…" {
		t.Errorf("tray row = %+v", it)
	}
	if tip := e.ft.tooltipNow(); !strings.Contains(tip, "updates available") {
		t.Errorf("tooltip = %q", tip)
	}

	// 2. "Update Claude Code" on the dashboard: Claude Code's own updater,
	// then a fresh look that finds the new version; tycswap still waits.
	code, res := e.api(http.MethodPost, "/api/updates/apply", `{"target":"claude-code"}`)
	if code != http.StatusOK || !strings.HasPrefix(res["message"].(string), "Claude Code is updated.") {
		t.Fatalf("apply: %d %v", code, res)
	}
	if out, _ := res["output"].(string); !strings.Contains(out, "Updating Claude Code to 2.1.281") {
		t.Errorf("output = %q", out)
	}
	log, _ := os.ReadFile(e.claudeLog)
	if !strings.Contains(string(log), "update\n") {
		t.Errorf("claude ran:\n%s", log)
	}
	u = e.updates()
	cc, _ = u["claudeCode"].(map[string]any)
	if u["available"] != true || cc["state"] != "latest" || cc["installed"] != "2.1.281" {
		t.Errorf("dashboard after the update: %v", u)
	}
	if got := iconKinds(e.ft); !strings.HasSuffix(got, "badge:2,badge") {
		t.Errorf("tray icons = %s, want badge:2 then badge", got)
	}
	if _, ok := e.ft.item("claude-code"); !ok {
		t.Error("the latest Claude Code is not named in the App section")
	} else if it, _ := e.ft.item("claude-code"); it.Kind != 0 || !it.Disabled {
		t.Errorf("row after the update = %+v", it)
	}
}
