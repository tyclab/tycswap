// Tests for the dashboard's remembered view choices on disk (DESIGN A27).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestUIPrefsRoundTrip(t *testing.T) {
	root := t.TempDir()
	p := uiPrefs{path: uiPrefsPath(root)}
	if got := p.Folded(); len(got) != 0 {
		t.Fatalf("fresh prefs = %v", got)
	}
	if err := p.SetFolded("accounts-card", true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetFolded("updates-card", true); err != nil {
		t.Fatal(err)
	}
	if got := p.Folded(); !reflect.DeepEqual(got, map[string]bool{"accounts-card": true, "updates-card": true}) {
		t.Errorf("folded = %v", got)
	}
	if err := p.SetFolded("accounts-card", false); err != nil {
		t.Fatal(err)
	}
	if got := p.Folded(); !reflect.DeepEqual(got, map[string]bool{"updates-card": true}) {
		t.Errorf("after unfold = %v", got)
	}
	raw, err := os.ReadFile(filepath.Join(root, "ui_prefs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if json.Unmarshal(raw, &file) != nil || file["version"] != float64(1) {
		t.Errorf("file = %s", raw)
	}
	if fi, _ := os.Stat(filepath.Join(root, "ui_prefs.json")); fi != nil && fi.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		t.Errorf("mode %v, want 0600", fi.Mode())
	}
	// A corrupt file is the zero state and is replaced by the next write.
	if err := os.WriteFile(p.path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := p.Folded(); len(got) != 0 {
		t.Errorf("corrupt file = %v", got)
	}
	if err := p.SetFolded("updates-card", true); err != nil {
		t.Fatal(err)
	}
	if got := p.Folded(); !reflect.DeepEqual(got, map[string]bool{"updates-card": true}) {
		t.Errorf("after repair = %v", got)
	}
}
