package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/web"
)

// The notice the settings screens give for a stored value read without its
// control characters; the readers that only decide with the value give none.
const cleanedNotice = "autoswitch.model: the stored value held terminal control characters"

// A list main counted Fable from: ESC [2J and BEL after Opus.
func writeControlModel(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"autoswitch":{"model":"Fable,Opus\u001b[2J\u0007"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --switch --strategy without --model decides with the stored list read
// clean: Fable still counts, nothing is dropped and no control character
// reaches the result.
func TestSwitchStrategyDecidesWithTheCleanedModel(t *testing.T) {
	sw := fixtureSwitcher(t)
	writeControlModel(t, sw.BackupDir())
	best := "best"
	var payload any
	_ = dispatchSwitch(&parsed{strategy: &best, json: true}, sw, &payload)
	result, _ := payload.(map[string]any)
	encoded, _ := json.Marshal(result)
	if !reflect.DeepEqual(result["models"], []string{"Fable", "Opus[2J"}) || result["modelSource"] != "autoswitch.model" ||
		strings.Contains(string(encoded), `\u001b`) || strings.Contains(string(encoded), cleanedNotice) {
		t.Errorf("result = %s", encoded)
	}
}

// list, status and start decide with the clean list and say nothing about
// it: nothing they show depends on the control characters.
func TestListStatusAndStartUseTheCleanedModel(t *testing.T) {
	cleanHome(t)
	writeControlModel(t, paths.GetBackupRoot())
	for _, args := range [][]string{{"list"}, {"list", "--json"}, {"status"}, {"status", "--json"}} {
		_, out, stderr := runCLI(t, args, false, false)
		if strings.ContainsAny(out+stderr, "\x1b\a") || strings.Contains(out+stderr, cleanedNotice) {
			t.Errorf("%v: stdout %q, stderr %q", args, out, stderr)
		}
	}
	calls := startTestLaunch(t)
	writeControlModel(t, paths.GetBackupRoot())
	previous := newSwitcher
	newSwitcher = func(store.Options) (*core.Switcher, error) { return nil, errors.New("unavailable") }
	t.Cleanup(func() { newSwitcher = previous })
	_, _, stderr := runCLI(t, []string{"start", "command"}, false, false)
	if *calls != 1 || strings.Count(stderr, "tycswap start:") != 1 || strings.Contains(stderr, cleanedNotice) {
		t.Errorf("start: calls %d, stderr %q", *calls, stderr)
	}
}

// Both settings menus show the setting, so they say the notice once: the
// menu's own list and the config set/unset prompts; config get says it
// itself, so its prompt does not.
func TestSettingsMenusSayACleanedValueOnce(t *testing.T) {
	cleanHome(t)
	writeControlModel(t, paths.GetBackupRoot())
	oldAsk, oldExec := askMenu, menuExecute
	t.Cleanup(func() { askMenu, menuExecute = oldAsk, oldExec })
	askMenu = func(ioStreams, string, []string, string) (int, string, bool) { return 0, "", false }
	menuExecute = func(ioStreams, ...string) int { return 0 }
	var stderr bytes.Buffer
	menuSettings(ioStreams{err: &stderr})
	if strings.Count(stderr.String(), cleanedNotice) != 1 || strings.ContainsRune(stderr.String(), 0x1b) {
		t.Errorf("menu: stderr %q", stderr.String())
	}
	for _, tc := range []struct {
		args []string
		says int
	}{{[]string{"config", "set"}, 1}, {[]string{"config", "unset"}, 1}, {[]string{"config", "get"}, 0}} {
		stderr.Reset()
		promptMissingArguments(tc.args, ioStreams{err: &stderr})
		if strings.Count(stderr.String(), cleanedNotice) != tc.says {
			t.Errorf("%v: stderr %q, want the notice %d time(s)", tc.args, stderr.String(), tc.says)
		}
	}
}

// The tray counts the clean list like every other reader: the Fable window
// named in it counts towards the title and the tooltip.
func TestTrayCountsTheCleanedModel(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	st := sampleState()
	st.Settings = append(st.Settings, web.SettingView{Key: "autoswitch.model", Value: "Fable,Opus[2J", Notice: cleanedNotice + ", which were removed"})
	st.Accounts[1] = acct(1, "alice@example.com", "work", true, 45.0, 30.0, "Fable", 97.0)
	sh.update(st)
	if ft.title != "#1 · 97%" || !strings.Contains(ft.tooltip, "Fable 97%") || strings.Contains(ft.tooltip, "not counted") || strings.Contains(ft.tooltip, "control") {
		t.Errorf("title %q, tooltip %q", ft.title, ft.tooltip)
	}
}
