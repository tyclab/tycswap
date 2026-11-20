package autoswitch

import (
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

func TestScheduledOpusUsesEarlierBindingReset(t *testing.T) {
	for _, tc := range []struct {
		name                string
		hours               int
		stillOpus, reserved bool
		want                TickOutcome
	}{
		{"earlier", 1, true, false, Switched},
		{"equal", 4, true, false, NoAction},
		{"later", 8, true, false, NoAction},
		{"model changed", 1, false, false, NoAction},
		{"reserved", 1, true, true, NoAction},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newClk()
			row := func(hours int) usage.UsageEntry {
				reset := baseTime.Add(time.Duration(hours) * time.Hour).Format(time.RFC3339)
				return dictEntry(map[string]any{"five_hour": win(10, ""), "seven_day": win(16, reset), "scoped": []any{scopedWin("Opus", 55, reset)}})
			}
			f := twoAccounts(clk, row(4), row(tc.hours))
			rec := &recorder{}
			engine := build(t, f, settings.Default(), rec, clk, false, WithOpusDrain(func() bool { return tc.stillOpus }), WithReserved(func(num string) bool { return tc.reserved && num == "2" }))
			if got := engine.Tick(); got != tc.want {
				t.Fatalf("got %v want %v, events %v", got, tc.want, rec.kinds())
			}
			if tc.want == Switched {
				if ev := rec.last("switch").(SwitchEvent); ev.Trigger != "drain" || deref(f.current) != "2" {
					t.Fatalf("unexpected event %+v", ev)
				}
				if got := engine.Tick(); got != NoAction {
					t.Fatalf("moved away from earlier reset: %v", got)
				}
			}
		})
	}
}

func TestSwitchCommitRejectsChangedActiveAccount(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(usageOf(10, 96)), dictEntry(usageOf(10, 10)))
	rec := &recorder{}
	engine := build(t, f, settings.Default(), rec, clk, false)
	f.current = strp("3")
	result, err := engine.perform("1", "2", f.emails["2"], "at-limit", "7d")
	if err != nil || result.Outcome != NoAction || deref(f.current) != "3" || rec.last("no-switch").(NoSwitchEvent).Reason != "selection-changed" {
		t.Fatalf("result %+v err %v, current %s, events %v", result, err, deref(f.current), rec.kinds())
	}
}
