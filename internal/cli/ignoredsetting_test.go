package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/web"
)

// The notice every decider says when the stored autoswitch.model is ignored;
// the value itself (Fable, then ESC [2J) never appears.
const ignoredModelNotice = "autoswitch.model: the stored value contains a control character"

func writeIgnoredModel(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"autoswitch":{"model":"Fable\u001b[2J"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func saysIgnoredOnce(text string) bool {
	return strings.Count(text, ignoredModelNotice) == 1 && !strings.ContainsRune(text, 0x1b)
}

// --switch --strategy without --model decides with the stored model: an
// ignored one is said on stderr, and with --json in the result's warnings.
func TestSwitchStrategySaysAStoredModelIsIgnored(t *testing.T) {
	sw := fixtureSwitcher(t)
	writeIgnoredModel(t, sw.BackupDir())
	best := "best"
	var stderr bytes.Buffer
	var payload any
	_ = dispatchSwitch(&parsed{strategy: &best, json: true}, sw, &payload, &stderr)
	result, _ := payload.(map[string]any)
	warnings, _ := result["warnings"].([]string)
	if !saysIgnoredOnce(strings.Join(warnings, "\n")) || stderr.Len() != 0 {
		t.Errorf("--json: warnings %q, stderr %q", warnings, stderr.String())
	}
	stderr.Reset()
	payload = nil
	_ = dispatchSwitch(&parsed{strategy: &best}, sw, &payload, &stderr)
	if !saysIgnoredOnce(stderr.String()) || !strings.Contains(stderr.String(), "warning: ") {
		t.Errorf("human: stderr %q", stderr.String())
	}
}

// list and status mark accounts at a model's limit with the stored model; an
// ignored one is said on stderr, which leaves --json on stdout intact.
func TestListAndStatusSayAStoredModelIsIgnored(t *testing.T) {
	cleanHome(t)
	writeIgnoredModel(t, paths.GetBackupRoot())
	for _, args := range [][]string{{"list"}, {"list", "--json"}, {"status"}, {"status", "--json"}} {
		_, out, stderr := runCLI(t, args, false, false)
		if !saysIgnoredOnce(stderr) || strings.Contains(out, ignoredModelNotice) {
			t.Errorf("%v: stdout %q, stderr %q", args, out, stderr)
		}
	}
}

// tycswap start picks its login with the stored model; an ignored one is
// said before it picks.
func TestStartSaysAStoredModelIsIgnored(t *testing.T) {
	calls := startTestLaunch(t)
	writeIgnoredModel(t, paths.GetBackupRoot())
	previous := newSwitcher
	newSwitcher = func(store.Options) (*core.Switcher, error) { return nil, errors.New("unavailable") }
	t.Cleanup(func() { newSwitcher = previous })
	_, _, stderr := runCLI(t, []string{"start", "command"}, false, false)
	if *calls != 1 || !saysIgnoredOnce(stderr) || !strings.Contains(stderr, "tycswap start: "+ignoredModelNotice) {
		t.Errorf("calls %d, stderr %q", *calls, stderr)
	}
}

// Both settings menus say the notice once: the menu's own list, and the
// prompt for config set; config get says it itself, so its prompt does not.
func TestSettingsMenusSayAStoredValueIsIgnoredOnce(t *testing.T) {
	cleanHome(t)
	writeIgnoredModel(t, paths.GetBackupRoot())
	oldAsk, oldExec := askMenu, menuExecute
	t.Cleanup(func() { askMenu, menuExecute = oldAsk, oldExec })
	askMenu = func(ioStreams, string, []string, string) (int, string, bool) { return 0, "", false }
	menuExecute = func(ioStreams, ...string) int { return 0 }
	var stderr bytes.Buffer
	menuSettings(ioStreams{err: &stderr})
	if !saysIgnoredOnce(stderr.String()) {
		t.Errorf("menu: stderr %q", stderr.String())
	}
	for _, tc := range []struct {
		args []string
		says bool
	}{{[]string{"config", "set"}, true}, {[]string{"config", "unset"}, true}, {[]string{"config", "get"}, false}} {
		stderr.Reset()
		promptMissingArguments(tc.args, ioStreams{err: &stderr})
		if saysIgnoredOnce(stderr.String()) != tc.says || (!tc.says && strings.Contains(stderr.String(), ignoredModelNotice)) {
			t.Errorf("%v: stderr %q, want the notice %v", tc.args, stderr.String(), tc.says)
		}
	}
}

// The tray's Model limits switch says an ignored stored model, not the plain
// "off" it shows when nothing is set.
func TestTraySaysAStoredModelIsIgnored(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.act.SetModelLimits = func(bool) error { return nil }
	st := sampleState()
	st.Settings = append(st.Settings, web.SettingView{Key: "autoswitch.model", Notice: ignoredModelNotice + ", so it is ignored"})
	sh.update(st)
	it, ok := ft.item("model-limits")
	if !ok || it.Checked || !strings.Contains(it.Sub, "control character") || !strings.Contains(it.Sub, "Settings") {
		t.Errorf("switch = %+v (ok=%v)", it, ok)
	}
}
