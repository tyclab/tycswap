package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/settings"
)

// enabledStore writes a settings.json that turns auto-switch on and leaves
// every bar at its default.
func enabledStore(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(settings.SettingsPath(root), []byte(`{"schemaVersion":1,"autoswitch":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tickSwitch runs one scheduled tick through the CLI entry and returns its
// switch event ("" when none) and its no-switch reasons.
func tickSwitch(t *testing.T) (string, []string) {
	t.Helper()
	code, out, errb := runCodex(t, "", "auto", "--once", "--json")
	var switched string
	var held []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		ev := decodeJSON(t, line)
		switch ev["event"] {
		case "switch":
			to, _ := ev["to"].(map[string]any)
			switched = fmt.Sprintf("%v/%v/%v/dry=%v", ev["trigger"], ev["axis"], to["number"], ev["dryRun"])
		case "no-switch":
			held = append(held, fmt.Sprint(ev["reason"]))
		}
	}
	if code != 0 && switched != "" || errb != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errb)
	}
	return switched, held
}

// A scheduled tick counts the Fable window a live default session runs:
// the live account at 96 % of Fable, below its 5h and 7d bars, moves on the
// model axis to the account with Fable room (DESIGN A76, A97).
func TestScheduledTickCountsTheSessionsModel(t *testing.T) {
	sw := countingStore(t, "claude-fable-5-1", map[string][3]float64{"1": {10, 20, 96}, "2": {10, 20, 30}})
	enabledStore(t, sw.BackupDir())
	useSwitcher(t, sw)
	useCodexSource(t, &fakeCodexSource{})
	got, held := tickSwitch(t)
	if got != "proactive/model/2/dry=false" {
		t.Fatalf("switch %q (no-switch %v), want proactive/model/2/dry=false", got, held)
	}
	if num := sw.CurrentAccountNumber(); num == nil || *num != "2" {
		t.Fatalf("live account %v after the tick, want 2", num)
	}
}

// With no Fable session live, the accounts with Fable room are left to Fable
// work (DESIGN A71, A77): a tick moving off the live account lands on the
// account without Fable room though the reserved one has more, and a live
// account with Fable room hands over with trigger "reserved".
func TestScheduledTickLeavesFableRoomToFable(t *testing.T) {
	for name, tc := range map[string]struct {
		usage map[string][3]float64
		want  string
	}{
		"moving-off-skips-the-reserved": {
			usage: map[string][3]float64{"1": {90, 20, 100}, "2": {10, 10, 30}, "5": {20, 30, 100}},
			want:  "proactive/5h/5/dry=false",
		},
		"reserved-live-hands-over": {
			usage: map[string][3]float64{"1": {10, 20, 30}, "2": {10, 10, 30}, "5": {20, 30, 100}},
			want:  "reserved//5/dry=false",
		},
	} {
		t.Run(name, func(t *testing.T) {
			sw := countingStore(t, "claude-opus-5-5", tc.usage)
			enabledStore(t, sw.BackupDir())
			useSwitcher(t, sw)
			useCodexSource(t, &fakeCodexSource{})
			if got, held := tickSwitch(t); got != tc.want {
				t.Fatalf("switch %q (no-switch %v), want %q", got, held, tc.want)
			}
		})
	}
}

// tycswap start counts the live default session's model too: with a Fable
// session live it skips the marked login whose Fable week is spent, though
// it has the most 5h and 7d room, for the marked one with Fable room.
func TestStartSelectionCountsTheSessionsModel(t *testing.T) {
	sw := countingStore(t, "claude-fable-5-1", map[string][3]float64{"1": {10, 20, 30}, "2": {10, 10, 100}, "5": {20, 30, 10}})
	enabledStore(t, sw.BackupDir())
	for _, num := range []string{"2", "5"} {
		if err := sw.SetStartLogin(num, true); err != nil {
			t.Fatal(err)
		}
	}
	outcome := autoswitch.StartSelection(autoswitchAdapter{sw}, "", func() (autoswitch.StartSnapshot, error) {
		return readStartSnapshot(sw, "")
	}, autoswitch.WithClock(sw.Clk), autoswitch.WithOAuthClient(sw.OAuth), autoswitch.WithStatePath(sw.StatePath()))
	if !outcome.Switched || outcome.To != "5" {
		t.Fatalf("start selection %+v, want a switch to 5", outcome)
	}
}
