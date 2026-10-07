package autoswitch

import (
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

func TestModelGuardPreservesSessionModel(t *testing.T) {
	for _, strategy := range []string{"best", "soonest-reset"} {
		for _, model := range []string{"Fable", "all", ""} {
			for _, atLimit := range []float64{90, 100} {
				t.Run(strategy+model+pctLabel(atLimit), func(t *testing.T) {
					f := newFake()
					f.current = strp("1")
					f.switchable = []string{"1", "2", "3"}
					f.emails = map[string]string{"1": "a@x", "2": "b@x", "3": "c@x"}
					active := usageOf(atLimit, 40)
					active["scoped"] = []any{scopedWin("Fable", 30, "")}
					compatible := usageOf(10, 50)
					compatible["scoped"] = []any{scopedWin("Fable", 20, "")}
					f.entries = map[string]usage.UsageEntry{"1": dictEntry(active), "2": dictEntry(usageOf(0, 0)), "3": dictEntry(compatible)}
					s := settings.Default()
					s.Strategy = strategy
					if model != "" {
						s.Model = &model
					}
					rec := &recorder{}
					e := build(t, f, s, rec, newClk(), true)
					if got := e.Tick(); got != Switched {
						t.Fatalf("outcome %v", got)
					}
					want := 3
					if model == "" {
						want = 2
					}
					if got := switchTarget(t, rec); got != want {
						t.Fatalf("target %d, want %d", got, want)
					}
					if model != "" {
						f.switchable = []string{"1", "2"}
						rec = &recorder{}
						e = build(t, f, s, rec, newClk(), true)
						if got := e.Tick(); got != Blocked {
							t.Fatalf("incompatible fallback: %v", got)
						}
						if got := reasonOf(t, rec.last("no-switch")); got != "no-compatible-model-target" {
							t.Fatalf("reason %s", got)
						}
					}
				})
			}
		}
	}
}

func TestModelGuardRejectsUnknownFailover(t *testing.T) {
	for _, model := range []string{"Fable", "all"} {
		f := twoAccounts(newClk(), nilEntry(), dictEntry(usageOf(10, 0)))
		s := settings.Default()
		s.Model = &model
		s.UnhealthyTicks = 1
		rec := &recorder{}
		e := build(t, f, s, rec, newClk(), false)
		if got := e.Tick(); got != Blocked {
			t.Fatalf("%s failover = %v", model, got)
		}
		if rec.last("switch") != nil {
			t.Fatal("unknown model account selected")
		}
	}
}
