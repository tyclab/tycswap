package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// autoswitch.handoverWaitMinutes left with the handover (DESIGN A95): it is
// no setting, a file that holds it loads, and every write drops it.
func TestHandoverWaitMinutesIsRetired(t *testing.T) {
	if _, err := SpecFor("autoswitch.handoverWaitMinutes"); err == nil {
		t.Fatal("handoverWaitMinutes is still a setting")
	}
	stored := `{"schemaVersion":1,"autoswitch":{"handoverWaitMinutes":45,"fiveHourThreshold":90,"includeApiKeyAccounts":true}}`
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
			if err := os.WriteFile(path, []byte(stored), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := Load(root); got.FiveHourThreshold != 90 {
				t.Fatalf("load = %+v", got)
			}
			for _, e := range EffectiveSettings(root) {
				if strings.Contains(e.Spec.JSONKey, "handover") {
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
			if strings.Contains(string(data), "handoverWaitMinutes") {
				t.Fatalf("%s kept the retired key: %s", name, data)
			}
			if !strings.Contains(string(data), "includeApiKeyAccounts") {
				t.Fatalf("%s dropped an unknown key: %s", name, data)
			}
		})
	}
}
