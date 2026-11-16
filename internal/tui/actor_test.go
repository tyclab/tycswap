package tui

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
)

// The terminal UI names itself on every change it makes, once and only after
// the change succeeded.
func TestTUIActionsNameTheirActor(t *testing.T) {
	m, f, codex, _ := twoProviders(t)
	var lines []string
	WithActionLog(func(change, actor string) { lines = append(lines, change+" by "+actor) })(m)
	take := func() []string {
		out := lines
		lines = nil
		return out
	}
	f.switchToResult = map[string]any{"switched": true}
	spec, err := settings.SpecFor("autoswitch.cooldownSeconds")
	if err != nil {
		t.Fatal(err)
	}
	screen := &settingsScreen{}
	for _, step := range []struct {
		name string
		run  func()
		want []string
	}{
		{"disable", func() { deliver(t, m, m.toggleDisabled("claude:1")) }, []string{"Disabled account 1 by tui"}},
		{"switch", func() { deliver(t, m, m.doSwitch("1")) }, []string{"Switched to account 1 by tui"}},
		{"codex-enable", func() { deliver(t, m, m.toggleDisabled("codex:1")) }, []string{"Enabled Codex account 1 by tui"}},
		{"codex-switch", func() { deliver(t, m, m.doSwitch("codex:1")) }, []string{"Switched to Codex account 1 by tui"}},
		{"set", func() { deliver(t, m, screen.save(m, spec, "120")) }, []string{"Set autoswitch.cooldownSeconds=120 by tui"}},
		{"refused-set", func() { deliver(t, m, screen.save(m, spec, "nope")) }, nil},
		{"idle-switch", func() {
			f.switchToResult = map[string]any{"switched": false, "reason": "already-active"}
			deliver(t, m, m.doSwitch("1"))
		}, nil},
		{"refused-disable", func() {
			f.disabledErr = errors.New("roster locked")
			deliver(t, m, m.toggleDisabled("claude:1"))
		}, nil},
		{"refused-codex", func() {
			codex.err = errors.New("no stored credentials")
			deliver(t, m, m.doSwitch("codex:1"))
		}, nil},
	} {
		step.run()
		if got := take(); !reflect.DeepEqual(got, step.want) {
			t.Errorf("%s: logged %q, want %q", step.name, got, step.want)
		}
	}
}
