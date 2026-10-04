// Tests for what the app and the dashboard remember on disk (DESIGN A27,
// A43): the folded cards and the auto-switch choice, in one file.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/store"
)

func TestUIPrefsRoundTrip(t *testing.T) {
	root := t.TempDir()
	p := uiPrefs{path: appStatePath(root)}
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

// No file, an unreadable one or a corrupt one all mean "off": what the app
// did before it remembered anything (A43).
func TestAppStateDefaultsToOff(t *testing.T) {
	dir := t.TempDir()
	p := appStatePath(dir)
	if loadAppState(p).AutoSwitch {
		t.Error("missing file reads as on")
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if loadAppState(p).AutoSwitch {
		t.Error("corrupt file reads as on")
	}
}

func TestAppStateRoundTrip(t *testing.T) {
	p := appStatePath(filepath.Join(t.TempDir(), "backup"))
	if err := saveAppState(p, appState{AutoSwitch: true}); err != nil {
		t.Fatal(err)
	}
	if st := loadAppState(p); !st.AutoSwitch || st.Version != appStateVersion {
		t.Errorf("state = %+v", st)
	}
	if fi, err := os.Stat(p); err == nil && fi.Mode().Perm() != 0o600 && runtime.GOOS != "windows" {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
}

// The facade records only where it was told to (the app), and only the
// user's real choice.
func TestAutoFacadeRemembers(t *testing.T) {
	a := newAutoFacade(&core.Switcher{Store: &store.Store{}}, nil)
	a.remember(true) // `web`: no state path, nothing written
	a.statePath = appStatePath(t.TempDir())
	a.remember(true)
	if !loadAppState(a.statePath).AutoSwitch {
		t.Fatal("on not recorded")
	}
	a.remember(false)
	if loadAppState(a.statePath).AutoSwitch {
		t.Fatal("off not recorded")
	}
}

// The folded cards share the file with the auto-switch choice (A43):
// writing one keeps the other.
func TestUIPrefsKeepTheAutoSwitchChoice(t *testing.T) {
	path := appStatePath(t.TempDir())
	if err := updateAppState(path, func(st *appState) { st.AutoSwitch = true }); err != nil {
		t.Fatal(err)
	}
	p := uiPrefs{path: path}
	if err := p.SetFolded("accounts-card", true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetFolded("updates-card", true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetFolded("updates-card", false); err != nil {
		t.Fatal(err)
	}
	st := loadAppState(path)
	if !st.AutoSwitch || !reflect.DeepEqual(st.Folded, map[string]bool{"accounts-card": true}) {
		t.Errorf("state %+v", st)
	}
	if got := p.Folded(); !reflect.DeepEqual(got, map[string]bool{"accounts-card": true}) {
		t.Errorf("Folded = %v", got)
	}
	// And the other way round: recording auto-switch keeps the folds.
	if err := updateAppState(path, func(st *appState) { st.AutoSwitch = false }); err != nil {
		t.Fatal(err)
	}
	if got := p.Folded(); !reflect.DeepEqual(got, map[string]bool{"accounts-card": true}) {
		t.Errorf("Folded after the auto-switch write = %v", got)
	}
}
