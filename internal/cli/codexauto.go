package cli

import (
	"context"
	"time"

	codexauto "github.com/tyclab/tycswap/internal/codex/autoswitch"
	codexswitcher "github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/settings"
)

func newCodexAutoEngine(merged settings.AutoSwitchSettings, s ioStreams) *codexauto.AutoSwitcher {
	return newCodexAutoEngineFor(newCodexSwitcher(s), merged)
}

// newCodexAutoEngineFor returns the Codex auto-switcher over sw, or nil when
// sw is nil, autoswitch.codexEnabled is off or this machine has no Codex
// accounts. The threshold is autoswitch.codexThreshold, or the effective
// Claude 7d bar when that is 0 (codexThreshold). A broken Codex store must
// never stop the Claude loop starting, so a panic here is a nil engine. A
// package var: the app's tests hand its engine host a fake Codex source
// through it.
var newCodexAutoEngineFor = func(sw *codexswitcher.Switcher, merged settings.AutoSwitchSettings) (eng *codexauto.AutoSwitcher) {
	if sw == nil || !merged.CodexEnabled {
		return nil
	}
	defer func() {
		if recover() != nil {
			eng = nil
		}
	}()
	if !codexIsPresent() {
		return nil
	}
	return codexauto.New(sw, codexThreshold(merged), merged.HysteresisPct)
}

func codexThreshold(merged settings.AutoSwitchSettings) float64 {
	if merged.CodexThreshold != 0 {
		return merged.CodexThreshold
	}
	return merged.SevenDayThreshold
}

// codexTickShown is which Codex ticks a host reports: a switch or an error,
// and every tick under dry-run, which exists to show what would happen.
func codexTickShown(tick codexauto.Tick, dryRun bool) bool {
	return dryRun || tick.Outcome == codexauto.OutcomeSwitched || tick.Outcome == codexauto.OutcomeError
}

// codexTickFields are a tick's fields under the names of `tycswap auto
// --json`'s codex line: switchedTo is null unless the tick switched, and
// runningPids is never null.
func codexTickFields(tick codexauto.Tick) map[string]any {
	var switchedTo any
	if tick.SwitchedTo != "" {
		switchedTo = tick.SwitchedTo
	}
	pids := tick.RunningPIDs
	if pids == nil {
		pids = []int{}
	}
	return map[string]any{
		"outcome":     tick.Outcome,
		"detail":      tick.Detail,
		"switchedTo":  switchedTo,
		"runningPids": pids,
	}
}

// startCodexLoop runs tick on its own goroutine — once immediately, then every
// interval — until the returned stop is called (deviation 7: #252's thread
// waited one interval first). A separate goroutine rather than a hook in the
// Claude engine: a slow Codex fetch never delays a Claude switch, and a Codex
// panic never takes down `tycswap auto`. stop cancels an in-flight tick and then
// waits for the goroutine to return, so the process never exits in the middle
// of a Codex switch and no Codex line is printed after the loop has stopped.
func startCodexLoop(enabled bool, interval time.Duration, tick func(context.Context)) (stop func()) {
	if !enabled {
		return func() {}
	}
	if interval <= 0 {
		interval = time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	safeTick := func() {
		defer func() { _ = recover() }()
		tick(ctx)
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			safeTick()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
