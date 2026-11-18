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
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/brand"
	codexapi "github.com/tyclab/tycswap/internal/codex/api"
	codexswitcher "github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/launch"
	"github.com/tyclab/tycswap/internal/platform"
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
