package autoswitch

import (
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

// liveBars are the operator's bars on 2026-11-17: 5h 90, 7d 98, model 95,
// hysteresis 10.
func liveBars() settings.AutoSwitchSettings {
	s := settings.Default()
	s.FiveHourThreshold, s.SevenDayThreshold, s.ModelThreshold = 90, 98, 95
	s.HysteresisPct = 10
	return s
}

// liveAbsent5h is the pool of 2026-11-17 21:08-21:13 (tycswap 0.13.1) once the
// policy had dropped Fable as unservable: active 12 over its 5h bar, 11 at its
// 5h bar, 3 and 8 (verified Ultra) with no 5h window, 4 and 7 with a week all
// but spent. renew3 and renew8 are when 3's and 8's weeks renew.
func liveAbsent5h(clk *clock.Fake, renew3, renew8 time.Duration) *fakeSwitcher {
	at := func(d time.Duration) string { return clk.Now().Add(d).UTC().Format(time.RFC3339) }
	fable := func(pct float64) []any { return []any{scopedWin("Fable", pct, at(96*time.Hour))} }
	f := newFake()
	f.current = strp("12")
	f.switchable = []string{"3", "4", "7", "8", "11", "12"}
	f.entries = map[string]usage.UsageEntry{
		"12": dictEntry(map[string]any{"five_hour": win(93, at(3*time.Hour)), "seven_day": win(3, at(150*time.Hour)), "scoped": fable(3)}),
		"11": dictEntry(map[string]any{"five_hour": win(90, at(3*time.Hour)), "seven_day": win(51, at(100*time.Hour)), "scoped": fable(69)}),
		"3":  dictEntry(map[string]any{"seven_day": win(90, at(renew3)), "scoped": fable(100)}),
		"8":  dictEntry(map[string]any{"seven_day": win(97, at(renew8)), "scoped": fable(100)}),
		"4":  dictEntry(map[string]any{"seven_day": win(99, at(30*time.Hour)), "scoped": fable(100)}),
		"7":  dictEntry(map[string]any{"seven_day": win(99, at(40*time.Hour)), "scoped": fable(100)}),
	}
	for _, num := range f.switchable {
		f.emails[num] = "acct" + num + "@x.com"
		f.creds[num] = farFutureCreds(clk, "r"+num)
	}
	return f
}

// The live case: a 5h move off 12 takes an account with no 5h window that is
// below its own bars, picked by the strategy's order between 3 (7d 90) and 8
// (7d 97); 11 stays out on its 5h bar, 4 and 7 on their weekly bar. Before
// DESIGN A96 every tick said no-qualifying-candidate until 12 hit 100 %.
func TestFiveHourMoveTakesAnAccountWithoutAFiveHourWindow(t *testing.T) {
	for _, c := range []struct {
		name, strategy string
		renew3, renew8 time.Duration
		want           string
	}{
		{"best takes the most weekly room", "best", 100 * time.Hour, 50 * time.Hour, "3"},
		{"soonest-reset, 3 renews first", "soonest-reset", 20 * time.Hour, 50 * time.Hour, "3"},
		{"soonest-reset, 8 renews first", "soonest-reset", 100 * time.Hour, 50 * time.Hour, "8"},
	} {
		t.Run(c.name, func(t *testing.T) {
			clk := newClk()
			dir := t.TempDir()
			f := liveAbsent5h(clk, c.renew3, c.renew8)
			s := liveBars()
			s.Strategy = c.strategy
			rec := &recorder{}
			e := build(t, f, s, rec, clk, false, WithUnservable([]string{"Fable"}),
				WithLogger(logging.NewWithClock(dir, false, clk)))

			if got := e.Tick(); got != Switched {
				t.Fatalf("outcome = %v, %+v; want a proactive switch", got, rec.last("no-switch"))
			}
			sw := rec.last("switch").(SwitchEvent)
			if sw.Trigger != "proactive" || sw.Axis != "5h" {
				t.Errorf("trigger/axis = %q/%q, want proactive/5h", sw.Trigger, sw.Axis)
			}
			if got := deref(f.current); got != c.want {
				t.Errorf("switched to %s, want %s", got, c.want)
			}
			if n := rec.count("blocked-with-room"); n != 0 {
				t.Errorf("blocked-with-room reported %d times", n)
			}
			if w := blockedWarnings(t, dir); len(w) != 0 {
				t.Errorf("logged %q", w)
			}

			// No flapping, an hour on (past the cooldown): the new account has
			// no 5h window and its week is under its bar, so no window of it is hot.
			clk.Advance(time.Hour)
			rec.reset()
			if got := e.Tick(); got != NoAction || reasonOf(t, rec.last("no-switch")) != "below-threshold" {
				t.Fatalf("next tick on %s: outcome %v, kinds %v; want below-threshold", c.want, got, rec.kinds())
			}
		})
	}
}

// pairOn is active 1 and candidate 2 under the given bars, deciding only.
func pairOn(t *testing.T, s settings.AutoSwitchSettings, active, cand map[string]any) (*recorder, *Engine) {
	t.Helper()
	f := newFake()
	f.current = strp("1")
	f.switchable = []string{"1", "2"}
	f.emails = map[string]string{"1": "a@x", "2": "b@x"}
	f.entries = map[string]usage.UsageEntry{"1": dictEntry(active), "2": dictEntry(cand)}
	rec := &recorder{}
	return rec, build(t, f, s, rec, newClk(), true)
}

// wantHeld fails unless the tick kept the active account for want of a
// qualifying candidate.
func wantHeld(t *testing.T, rec *recorder, got TickOutcome) {
	t.Helper()
	if got != Blocked || reasonOf(t, rec.last("no-switch")) != "no-qualifying-candidate" {
		t.Fatalf("outcome = %v, kinds %v; want Blocked no-qualifying-candidate", got, rec.kinds())
	}
}

// An account without a 5h window is free on a 5h move only below every bar of
// its own: at its weekly bar, or over a counted model window's bar, it is no
// target (DESIGN A96).
func TestFiveHourMoveHoldsAnAbsentFiveHourAccountToItsOwnBars(t *testing.T) {
	t.Run("week at its bar", func(t *testing.T) {
		rec, e := pairOn(t, liveBars(), usageOf(93, 3), map[string]any{"seven_day": win(98, "")})
		wantHeld(t, rec, e.Tick())
	})
	t.Run("counted model window over its bar", func(t *testing.T) {
		s := liveBars()
		m := "Fable"
		s.Model = &m
		rec, e := pairOn(t, s,
			map[string]any{"five_hour": win(93, ""), "seven_day": win(3, ""), "scoped": []any{scopedWin("Fable", 3, "")}},
			map[string]any{"seven_day": win(50, ""), "scoped": []any{scopedWin("Fable", 96, "")}})
		wantHeld(t, rec, e.Tick())
	})
}

// A candidate that reports a 5h window is judged on it as before: at the 5h
// bar, or below it but not better than the active account by the margin, it
// is no target; past both it is (DESIGN A34).
func TestFiveHourMoveKeepsTheBarAndMarginForAReportedWindow(t *testing.T) {
	t.Run("at the 5h bar", func(t *testing.T) {
		rec, e := pairOn(t, liveBars(), usageOf(93, 3), usageOf(90, 10))
		wantHeld(t, rec, e.Tick())
	})
	t.Run("inside the margin", func(t *testing.T) {
		rec, e := pairOn(t, liveBars(), usageOf(93, 3), usageOf(85, 10)) // 15 - 7 < 10
		wantHeld(t, rec, e.Tick())
	})
	t.Run("past both", func(t *testing.T) {
		rec, e := pairOn(t, liveBars(), usageOf(93, 3), usageOf(80, 10)) // 20 - 7 >= 10
		if got := e.Tick(); got != Switched {
			t.Fatalf("outcome = %v, kinds %v; want Switched", got, rec.kinds())
		}
	})
}

// A move the week made is judged on the week, 5h window or not: a candidate
// without one that is not better there by the margin stays out, one that is
// moves, and one that reports no week is no target (DESIGN A34, A96).
func TestWeekMoveIsStillJudgedOnTheWeek(t *testing.T) {
	t.Run("no weekly window", func(t *testing.T) {
		rec, e := pairOn(t, liveBars(), usageOf(10, 98), map[string]any{"five_hour": win(10, "")})
		wantHeld(t, rec, e.Tick())
	})
	t.Run("inside the margin", func(t *testing.T) {
		rec, e := pairOn(t, liveBars(), usageOf(10, 98), map[string]any{"seven_day": win(90, "")}) // 10 - 2 < 10
		wantHeld(t, rec, e.Tick())
	})
	t.Run("past the margin", func(t *testing.T) {
		rec, e := pairOn(t, liveBars(), usageOf(10, 98), map[string]any{"seven_day": win(85, "")}) // 15 - 2 >= 10
		if got := e.Tick(); got != Switched {
			t.Fatalf("outcome = %v, kinds %v; want Switched", got, rec.kinds())
		}
		if sw := rec.last("switch").(SwitchEvent); sw.Axis != "7d" {
			t.Errorf("axis = %q, want 7d", sw.Axis)
		}
	})
}
