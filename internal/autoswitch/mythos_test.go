package autoswitch

import (
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
)

// servesOption restricts the engine's targets to the marked accounts, as the
// policy does while a Mythos session runs (DESIGN A106).
func servesOption(marked ...string) Option {
	set := map[string]bool{}
	for _, num := range marked {
		set[num] = true
	}
	return WithServes("Mythos", func(num string) bool { return set[num] })
}

func detailOfLast(t *testing.T, rec *recorder, kind string) string {
	t.Helper()
	ev, ok := rec.last(kind).(NoSwitchEvent)
	if !ok {
		t.Fatalf("no %s event (kinds=%v)", kind, rec.kinds())
	}
	return ev.Detail
}

// The live shape of 2026-11-18: the login on a marked account at its 5-hour
// bar, a Mythos session live and unmarked accounts with room. The login moves
// to the other marked account, or stays where it is, never to an unmarked one.
func TestMythosLoginStaysAmongMarkedAccounts(t *testing.T) {
	for name, tc := range map[string]struct {
		marked     map[string]any
		wantOut    TickOutcome
		wantActive string
	}{
		"marked-with-room": {usageOf(10, 30), Switched, "3"},
		"marked-full":      {usageOf(100, 30), Blocked, "1"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, active := range []float64{90, 100} {
				clk := newClk()
				f := threeAccounts(clk, dictEntry(usageOf(active, 50)), dictEntry(usageOf(0, 5)), dictEntry(tc.marked))
				rec := &recorder{}
				s := settings.Default()
				s.FiveHourThreshold = 90
				e := build(t, f, s, rec, clk, false, servesOption("1", "3"))
				if got := e.Tick(); got != tc.wantOut {
					t.Fatalf("5h %v: outcome = %v, want %v (kinds=%v)", active, got, tc.wantOut, rec.kinds())
				}
				if deref(f.current) != tc.wantActive {
					t.Fatalf("5h %v: current = %q, want %q", active, deref(f.current), tc.wantActive)
				}
				if tc.wantOut == Blocked {
					if got := reasonOf(t, rec.last("no-switch")); got != "no-compatible-model-target" {
						t.Fatalf("reason %s", got)
					}
					if d := detailOfLast(t, rec, "no-switch"); !strings.Contains(d, "Mythos") {
						t.Fatalf("detail %q names no Mythos", d)
					}
				}
			}
		})
	}
}

// The login on an unmarked account while a Mythos session runs moves to a
// marked account below every bar, whatever the cooldown, naming no window.
func TestMythosUnmarkedLoginMovesToAMarkedAccount(t *testing.T) {
	for _, cooldown := range []bool{false, true} {
		clk := newClk()
		f := threeAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(0, 5)), dictEntry(usageOf(20, 40)))
		rec := &recorder{}
		e := build(t, f, settings.Default(), rec, clk, false, servesOption("3"))
		if cooldown {
			if _, err := e.mutateState(func(st map[string]any) { st["lastSwitchAt"] = e.nowSeconds() }); err != nil {
				t.Fatal(err)
			}
		}
		if got := e.Tick(); got != Switched {
			t.Fatalf("cooldown %v: outcome = %v, want Switched (kinds=%v)", cooldown, got, rec.kinds())
		}
		ev := rec.last("switch").(SwitchEvent)
		if ev.Trigger != "model-missing" || ev.Axis != "" || deref(f.current) != "3" {
			t.Fatalf("cooldown %v: trigger %q axis %q current %q", cooldown, ev.Trigger, ev.Axis, deref(f.current))
		}
	}
}

// Without a marked account below every bar the unmarked login stays and the
// tick says why.
func TestMythosUnmarkedLoginStaysWithoutAServingTarget(t *testing.T) {
	clk := newClk()
	f := threeAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(0, 5)), dictEntry(usageOf(95, 40)))
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, servesOption("3"))
	if got := e.Tick(); got != NoAction {
		t.Fatalf("outcome = %v, want NoAction (kinds=%v)", got, rec.kinds())
	}
	if got := reasonOf(t, rec.last("no-switch")); got != "model-missing-no-target" {
		t.Fatalf("reason %s", got)
	}
	if d := detailOfLast(t, rec, "no-switch"); !strings.Contains(d, "Mythos") {
		t.Fatalf("detail %q names no Mythos", d)
	}
	if deref(f.current) != "1" {
		t.Fatalf("moved to %q", deref(f.current))
	}
}

// The Fable reservation's hand-over (A77) is bounded by the same filter: a
// reserved marked login does not leave for an unmarked account.
func TestMythosBoundsTheReservedHandOver(t *testing.T) {
	clk := newClk()
	f := threeAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(0, 5)), dictEntry(usageOf(5, 10)))
	f.reserved["1"] = true
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, reservedOption(f), servesOption("1", "3"))
	if got := e.Tick(); got != Switched || deref(f.current) != "3" {
		t.Fatalf("outcome = %v current %q, want Switched to 3 (kinds=%v)", got, deref(f.current), rec.kinds())
	}
	clk = newClk()
	f = threeAccounts(clk, dictEntry(usageOf(10, 10)), dictEntry(usageOf(0, 5)), dictEntry(usageOf(5, 10)))
	f.reserved["1"] = true
	rec = &recorder{}
	e = build(t, f, settings.Default(), rec, clk, false, reservedOption(f), servesOption("1"))
	if got := e.Tick(); got != NoAction || deref(f.current) != "1" {
		t.Fatalf("outcome = %v current %q, want NoAction on 1 (kinds=%v)", got, deref(f.current), rec.kinds())
	}
	if got := reasonOf(t, rec.last("no-switch")); got != "reserved-no-target" {
		t.Fatalf("reason %s", got)
	}
}

// A nil filter serves every account: the tick is the one without the option.
func TestMythosNilFilterServesEveryAccount(t *testing.T) {
	clk := newClk()
	f := twoAccounts(clk, dictEntry(usageOf(90, 50)), dictEntry(usageOf(0, 5)))
	rec := &recorder{}
	e := build(t, f, settings.Default(), rec, clk, false, WithServes("Mythos", nil))
	if got := e.Tick(); got != Switched || deref(f.current) != "2" {
		t.Fatalf("outcome = %v current %q (kinds=%v)", got, deref(f.current), rec.kinds())
	}
}
