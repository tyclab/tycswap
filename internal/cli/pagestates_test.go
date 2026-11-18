package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

// pageRow is one next-best row as the page shows it: its verdict and
// whether its model cell is a counted window.
type pageRow struct {
	Number  string `json:"number"`
	Verdict string `json:"verdict"`
	Model   string `json:"model"`
	Counted *bool  `json:"counted"`
}

type pageChip struct {
	Text  string `json:"text"`
	Title string `json:"title"`
}

// pageView is what testdata/pagestates.cjs reports for one state.
type pageView struct {
	Name     string    `json:"name"`
	Problems []string  `json:"problems"`
	Best     *string   `json:"best"`
	AtLimit  []string  `json:"atLimit"`
	NextBest []pageRow `json:"nextBest"`
	Chips    struct {
		NextBest *pageChip `json:"nextBest"`
		Accounts *pageChip `json:"accounts"`
	} `json:"chips"`
}

// pageCase is one state the dashboard serves, under a name.
type pageCase struct {
	Name  string    `json:"name"`
	State web.State `json:"state"`
}

// renderPage runs the production app.js and index.html, as the dashboard
// serves them, over each state and returns what the page shows, by name.
func renderPage(t *testing.T, get func(string) string, cases []pageCase) map[string]pageView {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; page test skipped")
	}
	dir := t.TempDir()
	raw, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"app.js": get("/static/app.js"), "index.html": get("/"), "cases.json": string(raw)}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, runErr := exec.Command(node, "testdata/pagestates.cjs", filepath.Join(dir, "app.js"), filepath.Join(dir, "index.html"), filepath.Join(dir, "cases.json")).CombinedOutput()
	views := map[string]pageView{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var v pageView
		if json.Unmarshal([]byte(line), &v) != nil {
			t.Fatalf("page harness: %v\n%s", runErr, out)
		}
		if len(v.Problems) > 0 {
			t.Errorf("%s: %s", v.Name, strings.Join(v.Problems, "\n  "))
		}
		views[v.Name] = v
	}
	if runErr != nil || len(views) != len(cases) {
		t.Fatalf("page harness: %v, %d of %d states\n%s", runErr, len(views), len(cases), out)
	}
	return views
}

func writeSettings(t *testing.T, sw *core.Switcher, body string) {
	t.Helper()
	if err := os.WriteFile(settings.SettingsPath(sw.BackupDir()), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The whole page renders the states the real dashboard serves, scheduler-run
// and hosted, on and off, without an exception, a toast or an
// undefined/null/NaN text; a counted model window is drawn as counted, and
// the "limits ignored" chip shows only beside an engine hosted here, which
// counts the 5-hour and weekly limits only.
func TestDashboardPageRendersEveryState(t *testing.T) {
	room := map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 30}}
	spent := map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 100}}
	on := `{"schemaVersion":1,"autoswitch":{"enabled":true}}`
	off := `{"schemaVersion":1,"autoswitch":{"enabled":false}}`
	var cases []pageCase
	var get func(string) string
	for _, tc := range []struct {
		name, model, body string
		usage             map[string][3]float64
	}{
		{"owner-on-reserved", "claude-opus-5-5", on, room},
		{"owner-off", "claude-opus-5-5", off, room},
		{"owner-fable-counted", "claude-fable-5-1", on, room},
		{"owner-fable-unservable", "claude-fable-5-1", on, spent},
		{"owner-retired-model-key", "claude-fable-5-1", `{"schemaVersion":1,"autoswitch":{"enabled":true,"model":"all"}}`, spent},
	} {
		sw := countingStore(t, tc.model, tc.usage)
		writeSettings(t, sw, tc.body)
		d, _, g := serveDashboardPage(t, sw, testOwner)
		get = g
		cases = append(cases, pageCase{tc.name, d.srv.Snapshot()})
	}
	sw := countingStore(t, "claude-fable-5-1", room)
	writeSettings(t, sw, off)
	d, post, _ := serveDashboardPage(t, sw, "")
	countingEngines(d.auto)
	cases = append(cases, pageCase{"unhosted-off", d.srv.Snapshot()})
	if status, _ := post("/api/auto/start", `{}`); status != http.StatusOK {
		t.Fatalf("start = %d", status)
	}
	cases = append(cases, pageCase{"hosted-on-fable-session", d.srv.Snapshot()})
	if status, _ := post("/api/auto/stop", ""); status != http.StatusOK {
		t.Fatalf("stop = %d", status)
	}
	cases = append(cases, pageCase{"hosted-off", d.srv.Snapshot()})
	d.auto.stopKeepingChoice()

	views := renderPage(t, get, cases)
	counted := func(name string) bool {
		v := views[name]
		if len(v.NextBest) != 1 || v.NextBest[0].Model != "Fable" || v.NextBest[0].Counted == nil {
			t.Fatalf("%s: next-best rows %+v, want account 2 with a Fable cell", name, v.NextBest)
		}
		return *v.NextBest[0].Counted
	}
	if !counted("owner-fable-counted") || counted("owner-on-reserved") || counted("owner-fable-unservable") {
		t.Errorf("Fable cell counted: fable-counted %v, reserved %v, unservable %v; want only the first",
			counted("owner-fable-counted"), counted("owner-on-reserved"), counted("owner-fable-unservable"))
	}
	for name, v := range views {
		hosted := strings.HasPrefix(name, "hosted-")
		for where, c := range map[string]*pageChip{"next-best": v.Chips.NextBest, "accounts": v.Chips.Accounts} {
			if hosted != (c != nil) {
				t.Errorf("%s: %s chip %+v, want one only beside the hosted engine", name, where, c)
			}
		}
	}
}
