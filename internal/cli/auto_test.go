// auto_test.go — the Codex loop that rides along in `tycswap auto`.
package cli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	codexauto "github.com/tyclab/tycswap/internal/codex/autoswitch"
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

// TestCodexBarsFollowTheClaudeBars: with autoswitch.codexThreshold at 0 each
// Codex window is judged against the Claude bar for it; a non-zero value is
// one bar for both Codex windows (DESIGN A34).
func TestCodexBarsFollowTheClaudeBars(t *testing.T) {
	s := settings.Default()
	s.FiveHourThreshold, s.SevenDayThreshold = 80, 96
	if got, want := codexBars(s), (codexauto.Bars{FiveHour: 80, SevenDay: 96}); got != want {
		t.Errorf("codexThreshold 0: bars %+v, want %+v", got, want)
	}
	s.CodexThreshold = 88
	if got, want := codexBars(s), codexauto.SingleBar(88); got != want {
		t.Errorf("codexThreshold 88: bars %+v, want %+v", got, want)
	}
}
