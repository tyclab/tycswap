package autoswitch

import (
	"fmt"
	"testing"
)

func TestUltraRotationWithoutSessionWindow(t *testing.T) {
	weekly := func(pct float64) map[string]any {
		return map[string]any{"seven_day": win(pct, "")}
	}
	for _, tc := range []struct {
		name              string
		active, candidate map[string]any
		want              TickOutcome
	}{
		{"healthy-week", weekly(20), weekly(5), NoAction},
		{"weekly-trigger", weekly(90), weekly(5), Switched},
		{"session-trigger-to-ultra", usageOf(99, 20), weekly(5), Switched},
		{"weekly-exhausted-target", weekly(90), weekly(100), Blocked},
		{"session-exhausted-target", weekly(90), usageOf(100, 5), Blocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, rec, engine, _ := twoAxis(t, tc.active, tc.candidate)
			if got := engine.Tick(); got != tc.want {
				t.Fatalf("tick = %v, want %v; events %v", got, tc.want, rec.kinds())
			}
			if tc.want == Switched && switchTarget(t, rec) != 2 {
				t.Fatal("wrong target")
			}
		})
	}
	for _, modelPct := range []float64{20, 100} {
		t.Run(fmt.Sprintf("model-target-%.0f", modelPct), func(t *testing.T) {
			f, rec, _, s := twoAxis(t, weekly(90), weekly(5))
			for _, e := range f.entries {
				e.LastGood["scoped"] = []any{scopedWin("Fable", modelPct, "")}
			}
			s.Model = strp("Fable")
			engine := build(t, f, s, rec, newClk(), true)
			if got := engine.Tick(); (got == Switched) != (modelPct < s.ModelThreshold) {
				t.Fatalf("tick = %v; events %v", got, rec.kinds())
			}
		})
	}
}
