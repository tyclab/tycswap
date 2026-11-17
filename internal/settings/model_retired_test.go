package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// autoswitch.model is removed (DESIGN A97): it is no setting, a file that
// holds it loads as one without it, and every write drops it.
func TestModelSettingIsRetired(t *testing.T) {
	if _, err := SpecFor("autoswitch.model"); err == nil {
		t.Fatal("autoswitch.model is still a setting")
	}
	for _, v := range []string{`"all"`, `"Fable"`, `"none"`, `123`} {
		root := t.TempDir()
		writeSettingsJSON(t, root, `{"schemaVersion":1,"autoswitch":{"model":`+v+`,"fiveHourThreshold":90}}`)
		want := Default()
		want.FiveHourThreshold = 90
		if got := Load(root); !reflect.DeepEqual(got, want) {
			t.Errorf("model=%s: Load = %+v, want %+v", v, got, want)
		}
	}
	writes := map[string]func(root string) error{
		"set":   func(root string) error { _, err := SetSetting(root, "autoswitch.cooldownSeconds", "120"); return err },
		"seed":  func(root string) error { _, err := SeedSetting(root, "autoswitch.enabled", "true"); return err },
		"unset": func(root string) error { _, err := UnsetSetting(root, "autoswitch.fiveHourThreshold"); return err },
		"save":  func(root string) error { return Save(root, Load(root)) },
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, Filename)
			if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"autoswitch":{"model":"all","fiveHourThreshold":90,"includeApiKeyAccounts":true}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, e := range EffectiveSettings(root) {
				if e.Spec.JSONKey == "model" {
					t.Fatalf("listed: %s", e.Spec.Dotted())
				}
			}
			if err := write(root); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), `"model"`) || !strings.Contains(string(data), "includeApiKeyAccounts") {
				t.Fatalf("%s: settings.json %s, want model dropped and the unknown key kept", name, data)
			}
		})
	}
}
