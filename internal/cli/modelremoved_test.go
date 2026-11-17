package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/testutil"
)

// The 2026-11-17 shape (DESIGN A97): every account has spent its Fable week,
// the live account 1 is at its 5-hour limit and account 2 sits below its
// 5-hour and weekly bars. A settings.json that still holds autoswitch.model
// ticks exactly as one without it: session following counts no Fable window
// no account can serve, so the tick moves to 2 instead of all-exhausted.
func TestStoredModelSettingIsNotRead(t *testing.T) {
	for name, stored := range map[string]string{
		"absent": "",
		"all":    `,"model":"all"`,
		"fable":  `,"model":"Fable"`,
		"none":   `,"model":"none"`,
	} {
		t.Run(name, func(t *testing.T) {
			sw := countingStore(t, "claude-opus-5-5", map[string][3]float64{"1": {100, 50, 100}, "2": {10, 40, 100}})
			body := `{"schemaVersion":1,"autoswitch":{"enabled":true` + stored + `}}`
			if err := os.WriteFile(settings.SettingsPath(sw.BackupDir()), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			useSwitcher(t, sw)
			useCodexSource(t, &fakeCodexSource{})
			_, out, errb := runCodex(t, "", "auto", "--once", "--dry-run", "--json")
			var events []string
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				ev := decodeJSON(t, line)
				switch ev["event"] {
				case "poll":
					got, _ := json.Marshal(ev["windowsPct"])
					events = append(events, "poll "+string(got))
				case "switch":
					to, _ := ev["to"].(map[string]any)
					events = append(events, fmt.Sprintf("switch to %v", to["number"]))
				case "codex":
				default:
					events = append(events, fmt.Sprint(ev["event"]))
				}
			}
			want := []string{`poll {"1":{"5h":100,"7d":50},"2":{"5h":10,"7d":40}}`, "switch to 2"}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events %q, want %q; stderr %q", events, want, errb)
			}
		})
	}
}

// config set|get|unset answer autoswitch.model as any unknown key; config
// list and the Settings tab have no row for it; a stored value survives a
// load and is gone after the next write of another setting (DESIGN A97).
func TestModelSettingIsGone(t *testing.T) {
	cleanHome(t)
	testutil.Setenv(t, "PATH", t.TempDir())
	root := paths.GetBackupRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, settings.Filename)
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"autoswitch":{"model":"all","fiveHourThreshold":90}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"config", "set", "autoswitch.model", "Fable"}, {"config", "get", "autoswitch.model"}, {"config", "unset", "autoswitch.model"}} {
		if code, _, errStr := runSub(t, argv...); code == 0 || !strings.Contains(errStr, `unknown setting "autoswitch.model"`) {
			t.Errorf("%v = %d, stderr %q; want unknown setting", argv, code, errStr)
		}
	}
	code, out, errStr := runSub(t, "config", "list")
	if code != 0 || strings.Contains(out, "autoswitch.model ") || strings.Contains(out, "Count model limits") || !strings.Contains(out, "fiveHourThreshold") {
		t.Fatalf("config list = %d, stdout %q, stderr %q", code, out, errStr)
	}
	facade := settingsFacade{root: root}
	for _, v := range facade.Effective() {
		if v.Key == "autoswitch.model" {
			t.Fatalf("Settings tab lists %s", v.Key)
		}
	}
	if _, err := facade.Set("autoswitch.model", "all"); err == nil || !strings.Contains(err.Error(), "unknown setting") {
		t.Fatalf("the Settings tab's save = %v, want unknown setting", err)
	}
	if _, err := facade.Unset("autoswitch.model"); err == nil || !strings.Contains(err.Error(), "unknown setting") {
		t.Fatalf("the Settings tab's reset = %v, want unknown setting", err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"model":"all"`) {
		t.Fatalf("the refused commands rewrote settings.json: %s", data)
	}
	if code, _, errStr := runSub(t, "config", "set", "autoswitch.cooldownSeconds", "120"); code != 0 {
		t.Fatalf("config set cooldown = %d, stderr %q", code, errStr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"model"`) || !strings.Contains(string(data), `"fiveHourThreshold": 90`) {
		t.Fatalf("settings.json after a write: %s", data)
	}
}

// --model is gone from auto and from switch --strategy: either exits 2 with
// one line, and no help lists it (DESIGN A97).
func TestModelFlagsAreGone(t *testing.T) {
	cleanHome(t)
	const line = "--model was removed: auto-switch counts the models the sessions run.\n"
	for _, argv := range [][]string{{"auto", "--model", "x"}, {"auto", "--once", "--model", "x"}, {"switch", "--strategy", "best", "--model", "x"}} {
		code, out, errStr := runSub(t, argv...)
		if code != 2 || out != "" || errStr != line {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want exit 2 and %q", argv, code, out, errStr, line)
		}
	}
	for _, argv := range [][]string{{"auto", "--help"}, {"switch", "--help"}, {"help"}} {
		_, out, _ := runSub(t, argv...)
		if strings.Contains(out, "--model ") || strings.Contains(out, "--model,") || strings.Contains(out, "autoswitch.model") {
			t.Errorf("%v still names --model or autoswitch.model:\n%s", argv, out)
		}
	}
}
