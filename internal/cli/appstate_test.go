// Tests for what the dashboard remembers on disk (DESIGN A27): the folded
// cards, and the one-time move of the app's auto-switch choice (A43) into
// settings.json.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/settings"
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

func readPrefsFile(t *testing.T, root string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(appStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("ui_prefs.json = %s: %v", raw, err)
	}
	return doc
}

// TestMigrateAutoSwitchChoice: the choice the app remembered (A43) seeds
// autoswitch.enabled once, only where settings.json has no such key; the old
// key leaves ui_prefs.json either way and the folded cards stay.
func TestMigrateAutoSwitchChoice(t *testing.T) {
	const prefs = `{"version":1,"autoSwitch":true,"folded":{"x":true}}`
	keepsFoldsDropsChoice := func(t *testing.T, root string) {
		t.Helper()
		doc := readPrefsFile(t, root)
		if _, ok := doc["autoSwitch"]; ok {
			t.Errorf("ui_prefs.json still has autoSwitch: %v", doc)
		}
		if folded, _ := doc["folded"].(map[string]any); folded["x"] != true {
			t.Errorf("ui_prefs.json lost folded.x: %v", doc)
		}
	}

	t.Run("seeds an absent key", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, appStatePath(root), prefs, 0o600)
		if err := migrateAutoSwitchChoice(root); err != nil {
			t.Fatal(err)
		}
		if !settings.Load(root).Enabled {
			t.Error("Enabled = false, want the remembered true")
		}
		keepsFoldsDropsChoice(t, root)
	})

	t.Run("keeps an explicit false", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, settings.SettingsPath(root), `{"schemaVersion":1,"autoswitch":{"enabled":false}}`, 0o600)
		writeTestFile(t, appStatePath(root), prefs, 0o600)
		if err := migrateAutoSwitchChoice(root); err != nil {
			t.Fatal(err)
		}
		if settings.Load(root).Enabled {
			t.Error("Enabled = true, want the explicit false kept")
		}
		keepsFoldsDropsChoice(t, root)
	})

	t.Run("no prefs file writes nothing", func(t *testing.T) {
		root := t.TempDir()
		if err := migrateAutoSwitchChoice(root); err != nil {
			t.Fatal(err)
		}
		if entries, _ := os.ReadDir(root); len(entries) != 0 {
			t.Errorf("wrote %v", entries)
		}
	})

	t.Run("no remembered choice writes nothing", func(t *testing.T) {
		root := t.TempDir()
		const plain = `{"version":1,"folded":{"x":true}}`
		writeTestFile(t, appStatePath(root), plain, 0o600)
		if err := migrateAutoSwitchChoice(root); err != nil {
			t.Fatal(err)
		}
		if raw, _ := os.ReadFile(appStatePath(root)); string(raw) != plain {
			t.Errorf("ui_prefs.json rewritten: %s", raw)
		}
		if _, err := os.Stat(settings.SettingsPath(root)); !os.IsNotExist(err) {
			t.Errorf("settings.json written: %v", err)
		}
	})

	t.Run("a second run changes nothing", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, appStatePath(root), prefs, 0o600)
		snapshot := func() [2][]byte {
			if err := migrateAutoSwitchChoice(root); err != nil {
				t.Fatal(err)
			}
			s, _ := os.ReadFile(settings.SettingsPath(root))
			p, _ := os.ReadFile(appStatePath(root))
			return [2][]byte{s, p}
		}
		first, second := snapshot(), snapshot()
		if !bytes.Equal(first[0], second[0]) || !bytes.Equal(first[1], second[1]) {
			t.Errorf("second run changed the files:\n%s\n%s\nthen\n%s\n%s", first[0], first[1], second[0], second[1])
		}
		if !settings.Load(root).Enabled {
			t.Error("Enabled = false after two runs")
		}
		keepsFoldsDropsChoice(t, root)
	})

	t.Run("keeps an explicit true", func(t *testing.T) {
		root := t.TempDir()
		const explicit = `{"schemaVersion":1,"autoswitch":{"enabled":true}}`
		writeTestFile(t, settings.SettingsPath(root), explicit, 0o600)
		writeTestFile(t, appStatePath(root), prefs, 0o600)
		if err := migrateAutoSwitchChoice(root); err != nil {
			t.Fatal(err)
		}
		if raw, _ := os.ReadFile(settings.SettingsPath(root)); string(raw) != explicit {
			t.Errorf("settings.json rewritten: %s", raw)
		}
		keepsFoldsDropsChoice(t, root)
	})

	// Only a JSON true was ever "on": the app's struct refused anything else.
	for name, choice := range map[string]string{"a remembered false": "false", "a non-bool choice": `"yes"`} {
		t.Run(name+" seeds nothing", func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, appStatePath(root), `{"version":1,"autoSwitch":`+choice+`,"folded":{"x":true}}`, 0o600)
			if err := migrateAutoSwitchChoice(root); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(settings.SettingsPath(root)); !os.IsNotExist(err) {
				t.Errorf("settings.json written: %v", err)
			}
			keepsFoldsDropsChoice(t, root)
		})
	}

	t.Run("a corrupt prefs file is left alone", func(t *testing.T) {
		root := t.TempDir()
		const corrupt = `{"autoSwitch":true`
		writeTestFile(t, appStatePath(root), corrupt, 0o600)
		if err := migrateAutoSwitchChoice(root); err != nil {
			t.Fatal(err)
		}
		if raw, _ := os.ReadFile(appStatePath(root)); string(raw) != corrupt {
			t.Errorf("ui_prefs.json rewritten: %s", raw)
		}
		if _, err := os.Stat(settings.SettingsPath(root)); !os.IsNotExist(err) {
			t.Errorf("settings.json written: %v", err)
		}
	})

	t.Run("a missing store root is not created", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "absent")
		if err := migrateAutoSwitchChoice(root); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("store root created: %v", err)
		}
	})

	// Another process saves a fold while the seed writes settings.json: the
	// rewrite that drops autoSwitch must start from that save, not from the
	// file as the seed first read it.
	t.Run("a fold saved during the seed stays", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, appStatePath(root), prefs, 0o600)
		prev := seedEnabled
		t.Cleanup(func() { seedEnabled = prev })
		seedEnabled = func(root string) (bool, error) {
			writeTestFile(t, appStatePath(root), `{"version":1,"autoSwitch":true,"folded":{"x":true,"y":true}}`, 0o600)
			return prev(root)
		}
		if err := migrateAutoSwitchChoice(root); err != nil {
			t.Fatal(err)
		}
		keepsFoldsDropsChoice(t, root)
		if folded, _ := readPrefsFile(t, root)["folded"].(map[string]any); folded["y"] != true {
			t.Errorf("the fold saved during the seed is gone: %v", folded)
		}
		if !settings.Load(root).Enabled {
			t.Error("Enabled = false, want the remembered true")
		}
	})
}

// TestAFailedSeedSurvivesAFoldSave: a seed that failed (settings.json was
// corrupt) keeps autoSwitch for the next run. A fold saved meanwhile edits
// ui_prefs.json in place, keeping that key and any key it does not know, so
// the run after the repair still seeds the choice.
func TestAFailedSeedSurvivesAFoldSave(t *testing.T) {
	root := t.TempDir()
	path := appStatePath(root)
	writeTestFile(t, settings.SettingsPath(root), "{not json", 0o600)
	writeTestFile(t, path, `{"version":1,"autoSwitch":true,"future":{"a":1}}`, 0o600)
	if err := migrateAutoSwitchChoice(root); err == nil {
		t.Fatal("the seed over a corrupt settings.json reported success")
	}
	p := uiPrefs{path: path}
	if err := p.SetFolded("accounts-card", true); err != nil {
		t.Fatal(err)
	}
	doc := readPrefsFile(t, root)
	if doc["autoSwitch"] != true || !reflect.DeepEqual(doc["future"], map[string]any{"a": 1.0}) || doc["version"] != 1.0 {
		t.Errorf("after a fold save: %v, want autoSwitch, future and version kept", doc)
	}
	if got := p.Folded(); !reflect.DeepEqual(got, map[string]bool{"accounts-card": true}) {
		t.Errorf("Folded = %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".ui_prefs.lock")); err != nil {
		t.Errorf("the save took no .ui_prefs.lock: %v", err)
	}

	if err := os.Remove(settings.SettingsPath(root)); err != nil {
		t.Fatal(err)
	}
	if err := migrateAutoSwitchChoice(root); err != nil {
		t.Fatal(err)
	}
	if !settings.Load(root).Enabled {
		t.Error("Enabled = false after the repair, want the remembered true")
	}
	doc = readPrefsFile(t, root)
	if _, ok := doc["autoSwitch"]; ok || doc["future"] == nil {
		t.Errorf("after the retry: %v, want autoSwitch gone and future kept", doc)
	}
}

// TestUIPrefsSaveCreatesTheStore: the first fold save on a machine whose
// store does not exist yet creates it private, with a private file.
func TestUIPrefsSaveCreatesTheStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backup")
	p := uiPrefs{path: appStatePath(root)}
	if err := p.SetFolded("updates-card", true); err != nil {
		t.Fatal(err)
	}
	if got := p.Folded(); !reflect.DeepEqual(got, map[string]bool{"updates-card": true}) {
		t.Errorf("Folded = %v", got)
	}
	if doc := readPrefsFile(t, root); doc["version"] != 1.0 {
		t.Errorf("file = %v", doc)
	}
	if runtime.GOOS == "windows" {
		return
	}
	for path, want := range map[string]os.FileMode{root: 0o700, appStatePath(root): 0o600} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want mode %o", path, fi, err, want)
		}
	}
}

// TestMigrateAutoSwitchChoiceBeforeEveryCommand: run() seeds the key before
// the command reads it. A seed that fails (here on a corrupt settings.json)
// is a debug line in the log, never the command's failure, and the old key
// stays for the next run to try again.
func TestMigrateAutoSwitchChoiceBeforeEveryCommand(t *testing.T) {
	cleanHome(t)
	root := paths.GetBackupRoot()
	writeTestFile(t, appStatePath(root), `{"version":1,"autoSwitch":true}`, 0o600)
	if code, out, errb := runConfig(t, "get", "autoswitch.enabled"); code != 0 || strings.TrimSpace(out) != "true" {
		t.Fatalf("get = %d %q (stderr %q), want 0 true", code, out, errb)
	}

	writeTestFile(t, settings.SettingsPath(root), "{not json", 0o600)
	writeTestFile(t, appStatePath(root), `{"version":1,"autoSwitch":true}`, 0o600)
	if code, out, errb := runConfig(t, "--debug", "get", "autoswitch.enabled"); code != 0 || strings.TrimSpace(out) != "false" {
		t.Fatalf("get over a corrupt settings.json = %d %q (stderr %q), want 0 false", code, out, errb)
	}
	if _, ok := readPrefsFile(t, root)["autoSwitch"]; !ok {
		t.Error("a failed seed dropped the remembered choice")
	}
	logged, _ := os.ReadFile(filepath.Join(root, "tycswap.log"))
	found := false
	for _, line := range strings.Split(string(logged), "\n") {
		found = found || strings.Contains(line, " - DEBUG - ") && strings.Contains(line, "not valid JSON")
	}
	if !found {
		t.Errorf("tycswap.log lacks the debug line:\n%s", logged)
	}
}
