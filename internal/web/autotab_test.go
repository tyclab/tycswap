package web

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
