// auto_test.go — the Codex loop that rides along in `tycswap auto`.
package cli

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	codexswitcher "github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/settings"
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
