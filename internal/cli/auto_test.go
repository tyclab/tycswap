// auto_test.go — the Codex loop that rides along in `tycswap auto`, and the
// on/off every tick path honours (DESIGN A83).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	codexauto "github.com/tyclab/tycswap/internal/codex/autoswitch"
	codexswitcher "github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

// stop must not return while a tick is in flight: the process would otherwise
// exit mid-switch, or a Codex line could follow the loop's own stop report.
// The tick ignores its context, as a SwitchTo holding the store lock does.
func TestStopCodexLoopWaitsForAnInFlightTick(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var ticks, finished atomic.Int32
	stop := startCodexLoop(true, time.Hour, func(context.Context) {
		if ticks.Add(1) == 1 {
			close(started)
		}
		<-release
		finished.Add(1)
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not tick")
	}

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("stop returned while a tick was still running")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return after the tick finished")
	}
	if finished.Load() != ticks.Load() {
		t.Fatalf("stop returned with %d of %d ticks unfinished", ticks.Load()-finished.Load(), ticks.Load())
	}
}

// TestCodexThresholdFallsBackToTheSevenDayBar: the Codex engine keeps one bar
// for its windows. It is autoswitch.codexThreshold when set (not 0), else the
// Claude 7d bar, the bar a pre-A34 autoswitch.threshold seeds, so a migrated
// settings file keeps its Codex behaviour (DESIGN A34).
func TestCodexThresholdFallsBackToTheSevenDayBar(t *testing.T) {
	s := settings.Default()
	s.FiveHourThreshold, s.SevenDayThreshold = 80, 96
	if got := codexThreshold(s); got != 96 {
		t.Errorf("codexThreshold 0: bar %v, want the 7d bar 96", got)
	}
	s.CodexThreshold = 88
	if got := codexThreshold(s); got != 88 {
		t.Errorf("codexThreshold 88: bar %v, want 88", got)
	}
}

// TestNewCodexAutoEngineForChecksSwitcherSettingAndPresence: the constructor
// both hosts share (`tycswap auto`, the dashboard) builds no engine without a
// Codex switcher, with autoswitch.codexEnabled off, or on a machine without
// Codex accounts; otherwise its bar is codexThreshold and its margin the
// Claude hysteresis.
func TestNewCodexAutoEngineForChecksSwitcherSettingAndPresence(t *testing.T) {
	prev := codexIsPresent
	t.Cleanup(func() { codexIsPresent = prev })
	present := true
	codexIsPresent = func() bool { return present }
	sw := codexswitcher.New(codexswitcher.Options{Root: t.TempDir(), Stdout: io.Discard})
	s := settings.Default()
	s.SevenDayThreshold, s.HysteresisPct = 93, 7

	if eng := newCodexAutoEngineFor(nil, s); eng != nil {
		t.Error("an engine without a Codex switcher")
	}
	eng := newCodexAutoEngineFor(sw, s)
	if eng == nil || eng.Threshold != 93 || eng.Hysteresis != 7 {
		t.Fatalf("engine = %+v, want the 7d bar 93 and hysteresis 7", eng)
	}
	s.CodexEnabled = false
	if eng := newCodexAutoEngineFor(sw, s); eng != nil {
		t.Error("an engine with autoswitch.codexEnabled off")
	}
	s.CodexEnabled, present = true, false
	if eng := newCodexAutoEngineFor(sw, s); eng != nil {
		t.Error("an engine on a machine without Codex accounts")
	}
	codexIsPresent = func() bool { panic("broken store") }
	if eng := newCodexAutoEngineFor(sw, s); eng != nil {
		t.Error("a panicking presence check still built an engine")
	}
}

// TestAutoOnOffStatus: `auto on|off` write autoswitch.enabled, say so and log
// it; `auto status` reads it back; anything else after them is a usage error.
func TestAutoOnOffStatus(t *testing.T) {
	cleanHome(t)
	root := paths.GetBackupRoot()
	for _, tc := range []struct {
		verb, said, status string
		on                 bool
	}{
		{"off", "Auto-switch off.\n", "Auto-switch: off\n", false},
		{"on", "Auto-switch on.\n", "Auto-switch: on\n", true},
	} {
		if code, out, errb := runSub(t, "auto", tc.verb); code != 0 || out != tc.said {
			t.Fatalf("auto %s: exit %d out %q err %q", tc.verb, code, out, errb)
		}
		if got := settings.Load(root).Enabled; got != tc.on {
			t.Fatalf("after auto %s: enabled = %v", tc.verb, got)
		}
		if code, out, _ := runSub(t, "auto", "status"); code != 0 || out != tc.status {
			t.Fatalf("auto status after %s: exit %d out %q", tc.verb, code, out)
		}
	}
	code, out, _ := runSub(t, "auto", "status", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil ||
		!reflect.DeepEqual(got, map[string]any{"schemaVersion": float64(1), "enabled": true}) {
		t.Fatalf("auto status --json: exit %d out %q", code, out)
	}
	logged, _ := os.ReadFile(filepath.Join(root, "tycswap.log"))
	for _, line := range []string{"INFO - Auto-switch turned off (cli)\n", "INFO - Auto-switch turned on (cli)\n"} {
		if n := strings.Count(string(logged), line); n != 1 {
			t.Errorf("tycswap.log has %d of %q:\n%s", n, line, logged)
		}
	}
	for _, argv := range [][]string{{"on", "--json"}, {"off", "--if-unset"}, {"off", "now"}, {"status", "--if-unset"}, {"on", "--once"}} {
		code, out, errb := runSub(t, append([]string{"auto"}, argv...)...)
		if code != 2 || out != "" || !strings.Contains(errb, "tycswap auto: error: unrecognized arguments: "+argv[1]) {
			t.Errorf("auto %v: exit %d out %q err %q, want the usage error", argv, code, out, errb)
		}
	}
	if !settings.Load(root).Enabled {
		t.Error("a usage error changed the setting")
	}
}

// TestAutoOnIfUnset: a provisioner's seed writes only an absent key, so a
// user's later "off" survives it.
func TestAutoOnIfUnset(t *testing.T) {
	cleanHome(t)
	root := paths.GetBackupRoot()
	if code, out, errb := runSub(t, "auto", "on", "--if-unset"); code != 0 || out != "Auto-switch on.\n" || !settings.Load(root).Enabled {
		t.Fatalf("absent key: exit %d out %q err %q enabled %v", code, out, errb, settings.Load(root).Enabled)
	}
	if code, out, _ := runSub(t, "auto", "on", "--if-unset"); code != 0 || out != "Auto-switch left as it was: on.\n" {
		t.Fatalf("key on: exit %d out %q", code, out)
	}
	if _, err := settings.SetSetting(root, "autoswitch.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runSub(t, "auto", "on", "--if-unset"); code != 0 || out != "Auto-switch left as it was: off.\n" || settings.Load(root).Enabled {
		t.Fatalf("key false: exit %d out %q enabled %v", code, out, settings.Load(root).Enabled)
	}
}

// useSwitcher makes every command in the test run over sw.
func useSwitcher(t *testing.T, sw *core.Switcher) {
	t.Helper()
	prev := newSwitcher
	newSwitcher = func(store.Options) (*core.Switcher, error) { return sw, nil }
	t.Cleanup(func() { newSwitcher = prev; setSigintNote(""); setSigintJSON(false) })
}

// useCodexSource makes `tycswap auto` tick Codex over src, and counts the
// Codex engines it builds.
func useCodexSource(t *testing.T, src *fakeCodexSource) *atomic.Int32 {
	t.Helper()
	var built atomic.Int32
	prev := newCodexAutoEngineFor
	newCodexAutoEngineFor = func(*codexswitcher.Switcher, settings.AutoSwitchSettings) *codexauto.AutoSwitcher {
		built.Add(1)
		return codexauto.New(src, 90, 10)
	}
	t.Cleanup(func() { newCodexAutoEngineFor = prev })
	return &built
}

// countSessionPolicy counts the scheduled tick's session-policy reads, the
// step that scans the running processes.
func countSessionPolicy(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	prev := defaultSessionPolicy
	defaultSessionPolicy = func(s *store.Store, base settings.AutoSwitchSettings) (settings.AutoSwitchSettings, func(string) bool) {
		calls.Add(1)
		return prev(s, base)
	}
	t.Cleanup(func() { defaultSessionPolicy = prev })
	return &calls
}

// TestAutoOnceDisabledExits2: the timer's tick on a store that never turned
// auto-switch on says so in one line (and once on stderr) and exits 2. It
// still retires a former session group, which never moves the default login,
// but returns before the session policy's process scan and before a Codex
// engine is built. Turned on, the same store ticks both engines.
func TestAutoOnceDisabledExits2(t *testing.T) {
	sw := fixtureSwitcher(t)
	useSwitcher(t, sw)
	src := &fakeCodexSource{activePct: 98, accounts: 2}
	codexBuilt := useCodexSource(t, src)
	policy := countSessionPolicy(t)
	pendingFableGroup(t, sw.BackupDir())

	code, out, errb := runCodex(t, "", "auto", "--once", "--json")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 2 || len(lines) != 1 {
		t.Fatalf("exit %d, stdout %q (stderr %q); want exit 2 and one line", code, out, errb)
	}
	ev := decodeJSON(t, lines[0])
	if ev["event"] != "no-switch" || ev["reason"] != "disabled" || ev["detail"] != autoswitch.DisabledDetail {
		t.Fatalf("event = %v", ev)
	}
	if n := strings.Count(errb, "Auto-switch is off; tycswap auto on turns it on.\n"); n != 1 {
		t.Fatalf("stderr %q, want the off notice once", errb)
	}
	if pending, err := groups.Pending(sw.BackupDir(), groups.Fable); err != nil || pending {
		t.Fatalf("the former group was not retired while off: pending %v, %v", pending, err)
	}
	code, out, _ = runCodex(t, "", "auto", "--once")
	if code != 2 || strings.Count(out, "\n") != 1 || !strings.Contains(out, "no switch: disabled (auto-switch is off; tycswap auto on turns it on)") {
		t.Fatalf("human: exit %d, stdout %q", code, out)
	}
	if src.Snapshots() != 0 || codexBuilt.Load() != 0 || policy.Load() != 0 {
		t.Fatalf("while off: %d Codex ticks, %d Codex engines built, %d session-policy scans; want none", src.Snapshots(), codexBuilt.Load(), policy.Load())
	}

	if _, err := settings.SetSetting(sw.BackupDir(), "autoswitch.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	_, out, errb = runCodex(t, "", "auto", "--once", "--json")
	if first := decodeJSON(t, strings.SplitN(out, "\n", 2)[0]); first["event"] != "poll" || src.Snapshots() != 1 || !strings.Contains(out, `"event":"codex"`) {
		t.Fatalf("on: stdout %q, Codex ticks %d; want a poll first and one Codex tick", out, src.Snapshots())
	}
	if policy.Load() != 1 || strings.Contains(errb, "Auto-switch is off") {
		t.Fatalf("on: %d session-policy scans, stderr %q", policy.Load(), errb)
	}
}

// TestAutoObserveDisabledRecordsModelOnly: while off a status line near a bar
// still records its session's model (DESIGN A77) but marks no account due
// and runs no tick; turned on, the same observation does both.
func TestAutoObserveDisabledRecordsModelOnly(t *testing.T) {
	sw := rotatedSwitcher(t)
	useSwitcher(t, sw)
	policy := countSessionPolicy(t)
	num := sw.CurrentAccountNumber()
	if num == nil {
		t.Fatal("fixture has no live account")
	}
	ids := map[string]usage.Identity{*num: {Email: dryEmail, OrgUUID: "org-five"}}
	before := sw.Usage.Entries(ids)[*num].NextPollAt

	code, out, errb := runCodex(t, nearBarLine, "auto", "--observe", "--json")
	if code != 2 || errb != "" || policy.Load() != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q, %d session-policy scans; want 2, a quiet stderr, no scan", code, out, errb, policy.Load())
	}
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 1 || decodeJSON(t, lines[0])["reason"] != "disabled" {
		t.Fatalf("stdout %q, want the one no-switch disabled line", out)
	}
	var models struct {
		Sessions map[string]any `json:"sessions"`
	}
	raw, _ := os.ReadFile(filepath.Join(sw.BackupDir(), "default_models.json"))
	if json.Unmarshal(raw, &models) != nil || models.Sessions["s1"] == nil {
		t.Fatalf("the session's model was not recorded: %s", raw)
	}
	if after := sw.Usage.Entries(ids)[*num].NextPollAt; !reflect.DeepEqual(after, before) {
		t.Fatalf("Account-%s marked due while off: next poll %v, was %v", *num, after, before)
	}
	for _, name := range []string{"observe_state.json", autoswitch.StateFilename} {
		if _, err := os.Stat(filepath.Join(sw.BackupDir(), name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s exists after an observation while off: %v", name, err)
		}
	}

	if _, err := settings.SetSetting(sw.BackupDir(), "autoswitch.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	_, out, _ = runCodex(t, nearBarLine, "auto", "--observe", "--json")
	if _, err := os.Stat(filepath.Join(sw.BackupDir(), "observe_state.json")); err != nil || !strings.Contains(out, `"event":"poll"`) {
		t.Fatalf("on: stdout %q, fetch slot %v; want the slot claimed and a tick", out, err)
	}
}

// nearBarLine is a status line whose five-hour window is over its bar.
const nearBarLine = `{"session_id":"s1","model":{"id":"claude-opus-5-5"},"rate_limits":{"five_hour":{"used_percentage":99,"resets_at":1}}}`

// TestAutoObserveDryRunWhileOffDecides: a dry run decides while off, the
// status line's included: it warns and ticks.
func TestAutoObserveDryRunWhileOffDecides(t *testing.T) {
	sw := rotatedSwitcher(t)
	useSwitcher(t, sw)
	_, out, errb := runCodex(t, nearBarLine, "auto", "--observe", "--dry-run", "--json")
	if !strings.Contains(out, `"event":"config-warning"`) || !strings.Contains(out, `"event":"poll"`) || strings.Contains(out, `"reason":"disabled"`) {
		t.Fatalf("stdout %q (stderr %q), want the dry run's warning and a tick", out, errb)
	}
}

// TestAutoForegroundNotice: a foreground run says once on stderr that
// auto-switch is off, and the loop's banner shows only while it is on.
func TestAutoForegroundNotice(t *testing.T) {
	merged := settings.Default()
	for _, tc := range []struct {
		name           string
		on, loop, json bool
		out, err       string
	}{
		{"loop off", false, true, false, "", "Auto-switch is off; tycswap auto on turns it on.\n"},
		{"once off", false, false, false, "", "Auto-switch is off; tycswap auto on turns it on.\n"},
		{"loop on", true, true, false, "Auto-switch running: 5h 85% · 7d 97% · model 95%, every 60s — Ctrl-C to stop\n", ""},
		{"loop on json", true, true, true, "", ""},
		{"once on", true, false, false, "", ""},
	} {
		var out, errb strings.Builder
		autoForegroundNotice(ioStreams{out: &out, err: &errb}, tc.on, tc.loop, tc.json, false, merged)
		if out.String() != tc.out || errb.String() != tc.err {
			t.Errorf("%s: stdout %q stderr %q, want %q %q", tc.name, out.String(), errb.String(), tc.out, tc.err)
		}
	}
}
