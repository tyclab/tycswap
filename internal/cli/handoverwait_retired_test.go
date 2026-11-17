package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/testutil"
)

// autoswitch.handoverWaitMinutes left with the handover: config list and the
// dashboard's Settings tab do not show it, config set refuses it, and a file
// that still holds it loads and loses it on the next write.
func TestHandoverWaitSettingIsGone(t *testing.T) {
	cleanHome(t)
	testutil.Setenv(t, "PATH", t.TempDir())
	root := paths.GetBackupRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, settings.Filename)
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"autoswitch":{"handoverWaitMinutes":45,"fiveHourThreshold":90}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errStr := runSub(t, "config", "list")
	if code != 0 || strings.Contains(out, "handoverWait") || !strings.Contains(out, "fiveHourThreshold") {
		t.Fatalf("config list = %d, stdout %q, stderr %q", code, out, errStr)
	}
	if code, _, errStr := runSub(t, "config", "set", "autoswitch.handoverWaitMinutes", "20"); code == 0 || !strings.Contains(errStr, "unknown setting") {
		t.Fatalf("config set = %d, stderr %q", code, errStr)
	}
	for _, v := range (settingsFacade{root: root}).Effective() {
		if strings.Contains(v.Key, "handover") {
			t.Fatalf("Settings tab lists %s", v.Key)
		}
	}
	if code, _, errStr := runSub(t, "config", "set", "autoswitch.cooldownSeconds", "120"); code != 0 {
		t.Fatalf("config set cooldown = %d, stderr %q", code, errStr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "handoverWait") || !strings.Contains(string(data), `"fiveHourThreshold": 90`) {
		t.Fatalf("settings.json after a write: %s", data)
	}
}
