// auto_test.go — the Codex loop that rides along in `tycswap auto`.
package cli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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
