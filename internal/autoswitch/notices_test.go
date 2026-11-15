package autoswitch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
)

// A stored setting the settings package ignores is reported once per engine,
// at its first tick, whether auto-switch is on or off, so `auto`, the TUI's
// Auto view and the hosted engine all say it. The notice names the key and
// the repair, never the value.
func TestIgnoredStoredSettingIsReportedOnce(t *testing.T) {
	for _, on := range []bool{true, false} {
		f, rec, clk := overBar()
		f.backupDir = t.TempDir()
		if err := os.WriteFile(filepath.Join(f.backupDir, "settings.json"), []byte(`{"autoswitch":{"model":"Fable\u001b[2J"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		var opts []Option
		if !on {
			opts = append(opts, WithEnabled(off))
		}
		e := build(t, f, settings.Default(), rec, clk, true, opts...)
		e.Tick()
		e.Tick()
		var warns []string
		for _, ev := range rec.events {
			if w, ok := ev.(ConfigWarningEvent); ok && strings.Contains(w.Message, "control character") {
				warns = append(warns, w.Message)
			}
		}
		if len(warns) != 1 || !strings.HasPrefix(warns[0], "autoswitch.model: ") ||
			!strings.Contains(warns[0], "tycswap config unset autoswitch.model") ||
			strings.Contains(warns[0], "Fable") || strings.ContainsRune(warns[0], 0x1b) {
			t.Errorf("on=%v: warnings %q, want the notice once", on, warns)
		}
	}
}
