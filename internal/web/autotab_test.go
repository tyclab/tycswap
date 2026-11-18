package web

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
)

// The Auto tab and the Settings rows run over a small DOM with the
// registry's labels (DESIGN A93): one switch, the heading's words, Counting
// now with no autoswitch.model warning (A97), and every tile and toast naming
// a setting as the registry does.
func TestAppJSAutoTab(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; Auto tab test skipped")
	}
	type entry struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	var registry []entry
	for _, spec := range settings.SettingSpecs {
		registry = append(registry, entry{spec.Dotted(), spec.Label()})
	}
	labels, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.js")
	if err := os.WriteFile(path, []byte(staticFile(t, "app.js")), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "testdata/autotab.cjs", path, string(labels)).CombinedOutput(); err != nil {
		t.Fatalf("auto tab: %v\n%s", err, out)
	}
}

// autoPanel is the Auto tab's markup; a Windows checkout has CRLF in it.
func autoPanel(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<section class="panel" id="panel-auto".*?</section>\s+<!-- =+ Sessions`).FindString(staticFile(t, "index.html"))
	if m == "" {
		t.Fatal("no Auto panel in index.html")
	}
	return m
}

// The Auto tab has the one Auto-switch switch and nothing else to change:
// no model select, Save, Start, Stop, dry run, Wake, slider or event log
// (DESIGN A93), and the script calls none of their routes.
func TestStaticAutoTabHasOneSwitch(t *testing.T) {
	panel := autoPanel(t)
	if n := strings.Count(panel, "<input"); n != 1 || !strings.Contains(panel, `<input type="checkbox" id="auto-switch" role="switch"> Auto-switch</label>`) {
		t.Errorf("the Auto tab has %d inputs, want the one Auto-switch switch:\n%s", n, panel)
	}
	for _, gone := range []string{"<select", "<button", "model-limits", "Count model limits", "data-action=\"auto-start\"", "/api/auto/stop",
		"Start dry-run", "Wake", "threshold-slider", "7d threshold, this run", "Event log", "auto-log", "engine"} {
		if strings.Contains(panel, gone) {
			t.Errorf("the Auto tab still has %q", gone)
		}
	}
	js := staticFile(t, "app.js")
	for _, gone := range []string{"/api/auto/wake", "/api/auto/threshold", "/api/auto/model", "addEventListener('auto'", "dryRun", "SETTING_LABELS", "modelPicker", "'auto-start'"} {
		if strings.Contains(js, gone) {
			t.Errorf("app.js still has %q", gone)
		}
	}
}

// The Dashboard tab shows the same Auto-switch switch in the Accounts card's
// head, under the same name, and stays visible while that card is folded.
func TestStaticDashboardHasTheAutoSwitch(t *testing.T) {
	index := staticFile(t, "index.html")
	head := regexp.MustCompile(`(?s)<div class="card" id="accounts-card">\s*<div class="card-head">.*?<div class="add-callout"`).FindString(index)
	if !strings.Contains(head, `<label class="switch" for="dash-auto-switch"><input type="checkbox" id="dash-auto-switch" role="switch"> Auto-switch</label>`) {
		t.Errorf("the Accounts card head lacks the Auto-switch switch:\n%s", head)
	}
	if strings.Contains(staticFile(t, "style.css"), ".card.folded > .card-head .switch {") {
		t.Error("a folded Accounts card hides the Auto-switch switch")
	}
}

// Row 13 of DESIGN A93: the page's dead branches and wrong texts are gone.
func TestStaticNoDeadAutoBranchesOrWrongTexts(t *testing.T) {
	js := staticFile(t, "app.js")
	for _, gone := range []string{"sv.readOnly", "sv.source", "a.managedBy", "a.available", "empty = ignore model limits"} {
		if strings.Contains(js, gone) {
			t.Errorf("app.js still has %q", gone)
		}
	}
	index := staticFile(t, "index.html")
	for _, gone := range []string{"Tray and dashboard edit the same settings", "The engine here runs inside", "± keys", "Model windows",
		"An auto-switch engine reads the settings when it starts", "5h threshold", "7d threshold", "<dt>Strategy</dt>", "Hysteresis, cooldown",
		"Settings in the tray and here share the same values"} {
		if strings.Contains(index, gone) {
			t.Errorf("index.html still says %q", gone)
		}
	}
}
