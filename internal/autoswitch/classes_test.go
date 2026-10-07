package autoswitch

import (
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

// threeBars is the settings every test here starts from: one bar per window,
// deliberately different so a test can tell which one fired.
func threeBars() settings.AutoSwitchSettings {
	s := settings.Default()
	s.FiveHourThreshold, s.SevenDayThreshold, s.ModelThreshold = 97, 85, 80
	s.HysteresisPct = 10
	return s
}

// twoAxis builds a fixture whose accounts differ per window.
// usageOf(fiveHour, sevenDay) is the existing helper's argument order.
func twoAxis(t *testing.T, active, other map[string]any) (*fakeSwitcher, *recorder, *Engine, settings.AutoSwitchSettings) {
	t.Helper()
	f := newFake()
	f.current = strp("1")
	f.switchable = []string{"1", "2"}
	f.emails = map[string]string{"1": "a@x", "2": "b@x"}
	f.entries = map[string]usage.UsageEntry{"1": dictEntry(active), "2": dictEntry(other)}
	s := threeBars()
	rec := &recorder{}
	return f, rec, build(t, f, s, rec, newClk(), true), s
}

// Each window is judged against its own bar (DESIGN A34): a 5h window at 90 %
// is over these settings' 85 % 7d bar but under their 97 % 5h bar, so nothing
// moves, and the line names the window that is closest to its own bar.
func TestFiveHourBelowItsOwnThresholdDoesNotSwitch(t *testing.T) {
	_, rec, e, _ := twoAxis(t, usageOf(90, 10), usageOf(5, 5))
	if got := e.Tick(); got == Switched {
		t.Fatalf("a 90%% 5h window must not move anyone: %v", rec.last("switch"))
	}
	ns, _ := rec.last("no-switch").(NoSwitchEvent)
	if ns.Reason != "below-threshold" {
		t.Errorf("reason = %q, want below-threshold", ns.Reason)
	}
	// The line names the window that is closest to its own bar.
	if ns.Detail != "5h 90% < 97%" {
		t.Errorf("detail = %q, want %q", ns.Detail, "5h 90% < 97%")
	}
}

// The 7d window has its own bar too: it is the account's whole budget, and
// reaching it means move on.
func TestSevenDayOverItsThresholdSwitches(t *testing.T) {
	_, rec, e, _ := twoAxis(t, usageOf(10, 90), usageOf(5, 5))
	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, want Switched (kinds=%v)", got, rec.kinds())
	}
	if got := switchTarget(t, rec); got != 2 {
		t.Errorf("switched to %v, want 2", got)
	}
}

// Past its own gate, the 5h window does move — the point is the height of the
// gate, not ignoring the window.
func TestFiveHourAtItsOwnThresholdSwitches(t *testing.T) {
	_, rec, e, _ := twoAxis(t, usageOf(98, 10), usageOf(5, 5))
	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, want Switched at 98%% of the 5h window", got)
	}
	if got := switchTarget(t, rec); got != 2 {
		t.Errorf("switched to %v, want 2", got)
	}
}

// The per-model weekly window has the third bar, and it applies only while
// autoswitch.model counts that window.
func TestModelWindowHasItsOwnThreshold(t *testing.T) {
	withFable := func(five, seven, fable float64) map[string]any {
		return map[string]any{
			"five_hour": win(five, ""),
			"seven_day": win(seven, ""),
			"scoped":    []any{scopedWin("Fable", fable, "")},
		}
	}
	// Fable at 82 is over the 80 model bar but under the 85 weekly one; the
	// account-wide windows are cold.
	build3 := func(t *testing.T, models *string) (*recorder, *Engine) {
		t.Helper()
		f := newFake()
		f.current = strp("1")
		f.switchable = []string{"1", "2"}
		f.emails = map[string]string{"1": "a@x", "2": "b@x"}
		f.entries = map[string]usage.UsageEntry{
			"1": dictEntry(withFable(10, 10, 82)),
			"2": dictEntry(withFable(5, 5, 5)),
		}
		s := threeBars()
		s.Model = models
		rec := &recorder{}
		return rec, build(t, f, s, rec, newClk(), true)
	}

	t.Run("not counted", func(t *testing.T) {
		rec, e := build3(t, nil)
		if got := e.Tick(); got == Switched {
			t.Fatalf("an uncounted Fable window must not move anyone: %v", rec.last("switch"))
		}
	})

	t.Run("counted", func(t *testing.T) {
		all := "all"
		rec, e := build3(t, &all)
		if got := e.Tick(); got != Switched {
			t.Fatalf("outcome = %v, want Switched on the model bar (kinds=%v)", got, rec.kinds())
		}
		sw := rec.last("switch").(SwitchEvent)
		if sw.Axis != "model" {
			t.Errorf("axis = %q, want model", sw.Axis)
		}
	})
}

// A 5h-driven move must not land on an account whose WEEK is nearly gone,
// however much 5h room it has: that trades the scarce resource for the cheap
// one, which is the whole point of the split.
func TestSessionMoveWillNotSpendANearlyExhaustedWeek(t *testing.T) {
	f := newFake()
	f.current = strp("1")
	f.switchable = []string{"1", "2", "3"}
	f.emails = map[string]string{"1": "a@x", "2": "b@x", "3": "c@x"}
	f.entries = map[string]usage.UsageEntry{
		"1": dictEntry(usageOf(98, 10)), // 5h nearly spent, week fine
		"2": dictEntry(usageOf(0, 99)),  // wide open on 5h, week all but gone
		"3": dictEntry(usageOf(40, 20)), // healthy on both
	}
	rec := &recorder{}
	e := build(t, f, threeBars(), rec, newClk(), true)

	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, want Switched (kinds=%v)", got, rec.kinds())
	}
	if got := switchTarget(t, rec); got != 3 {
		t.Errorf("switched to %v, want 3 — #2 has the most 5h room but almost no week left", got)
	}
}

// "best" ranks by the weekly axis: 5h room is abundant and expires, so it must
// not decide where the work goes.
func TestBestRanksByTheWeeklyAxis(t *testing.T) {
	f := newFake()
	f.current = strp("1")
	f.switchable = []string{"1", "2", "3"}
	f.emails = map[string]string{"1": "a@x", "2": "b@x", "3": "c@x"}
	f.entries = map[string]usage.UsageEntry{
		"1": dictEntry(usageOf(10, 95)), // 7d over its bar → move
		// The fullest-window order and the weekly order disagree: #2's
		// fullest window (40) beats #3's (70), #3's week (10) beats #2's (40).
		"2": dictEntry(usageOf(1, 40)),
		"3": dictEntry(usageOf(70, 10)),
	}
	s := threeBars()
	s.Strategy = "best"
	rec := &recorder{}
	e := build(t, f, s, rec, newClk(), true)

	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, want Switched (kinds=%v)", got, rec.kinds())
	}
	if got := switchTarget(t, rec); got != 3 {
		t.Errorf("switched to %v, want 3 (most weekly room)", got)
	}
}

// The hysteresis margin is measured on the window that made the tick move: the
// candidate is 15 points better on the week that decided, although its fullest
// window (89) is barely better than the active account's (90).
func TestHysteresisIsMeasuredOnTheDecidingWindow(t *testing.T) {
	_, rec, e, _ := twoAxis(t, usageOf(0, 90), usageOf(89, 75))
	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, want Switched (kinds=%v)", got, rec.kinds())
	}
	if got := switchTarget(t, rec); got != 2 {
		t.Errorf("switched to %v, want 2", got)
	}
}

// When more than one window is over its bar, the costliest one names the move:
// losing the week costs days, the 5h window costs a wait.
func TestTheCostliestHotWindowNamesTheMove(t *testing.T) {
	_, rec, e, _ := twoAxis(t, usageOf(99, 95), usageOf(5, 5))
	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, want Switched (kinds=%v)", got, rec.kinds())
	}
	if sw := rec.last("switch").(SwitchEvent); sw.Axis != "7d" {
		t.Errorf("axis = %q, want 7d (both hot, the week is the costlier)", sw.Axis)
	}
}

// The log has to say WHICH window moved the user, because the answer means
// either "wait an hour" or "wait days" (DESIGN A34).
func TestSwitchEventNamesTheAxis(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active map[string]any
		want   string
	}{
		{"seven-day", usageOf(10, 90), "7d"},
		{"five-hour", usageOf(98, 10), "5h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, rec, e, _ := twoAxis(t, tc.active, usageOf(5, 5))
			if got := e.Tick(); got != Switched {
				t.Fatalf("outcome = %v, want Switched (kinds=%v)", got, rec.kinds())
			}
			sw := rec.last("switch").(SwitchEvent)
			if sw.Axis != tc.want {
				t.Errorf("axis = %q, want %q", sw.Axis, tc.want)
			}
			if sw.JSON()["axis"] != tc.want {
				t.Errorf("json axis = %v, want %q", sw.JSON()["axis"], tc.want)
			}
			if !contains(sw.Human(), tc.want) {
				t.Errorf("human = %q, want it to name %q", sw.Human(), tc.want)
			}
		})
	}
}

// switchTarget is the slot the engine decided on, from the recorded event.
func switchTarget(t *testing.T, rec *recorder) any {
	t.Helper()
	ev, ok := rec.last("switch").(SwitchEvent)
	if !ok {
		t.Fatalf("no switch event recorded; kinds=%v", rec.kinds())
	}
	return ev.ToRef["number"]
}

func TestHeadroomByClassSplitsTheAxes(t *testing.T) {
	lastGood := map[string]any{
		"five_hour": win(80, ""),
		"seven_day": win(30, ""),
		"scoped":    []any{scopedWin("Fable", 60, "")},
	}
	h := usage.AccountHeadroomByClass(lastGood, nil)
	if h.Session == nil || *h.Session != 20 {
		t.Errorf("session = %v, want 20", h.Session)
	}
	if h.Week == nil || *h.Week != 70 {
		t.Errorf("week = %v, want 70", h.Week)
	}
	if h.Model != nil {
		t.Errorf("model = %v, want nil while the window is not counted", h.Model)
	}
	if w := h.Weekly(); w == nil || *w != 70 {
		t.Errorf("weekly = %v, want the week's 70", w)
	}
	if b := h.Binding(); b == nil || *b != 20 {
		t.Errorf("binding = %v, want the smallest, 20", b)
	}

	counted := usage.AccountHeadroomByClass(lastGood, []string{"all"})
	if counted.Model == nil || *counted.Model != 40 {
		t.Errorf("model = %v, want 40 once counted", counted.Model)
	}
	if w := counted.Weekly(); w == nil || *w != 40 {
		t.Errorf("weekly = %v, want the tighter model window's 40", w)
	}

	full := usage.AccountHeadroomByClass(usageOf(100, 10), nil)
	if !full.Exhausted(usage.ClassSession) || full.WeeklyExhausted() {
		t.Error("a full 5h window exhausts the session axis only")
	}
	spentWeek := usage.AccountHeadroomByClass(usageOf(10, 100), nil)
	if !spentWeek.WeeklyExhausted() || spentWeek.Exhausted(usage.ClassSession) {
		t.Error("a full 7d window exhausts the weekly axis only")
	}
}

// Missing model data must not silently downgrade a running session.
func TestModelMoveRejectsACandidateWithoutThatWindow(t *testing.T) {
	f := newFake()
	f.current = strp("1")
	f.switchable = []string{"1", "2"}
	f.emails = map[string]string{"1": "a@x", "2": "b@x"}
	f.entries = map[string]usage.UsageEntry{
		"1": dictEntry(map[string]any{
			"five_hour": win(10, ""), "seven_day": win(10, ""),
			"scoped": []any{scopedWin("Fable", 95, "")},
		}),
		"2": dictEntry(usageOf(5, 5)), // no Fable window at all
	}
	s := threeBars()
	all := "all"
	s.Model = &all
	rec := &recorder{}
	e := build(t, f, s, rec, newClk(), true)

	if got := e.Tick(); got != Blocked {
		t.Fatalf("outcome = %v, want Blocked (kinds=%v)", got, rec.kinds())
	}
	if got := reasonOf(t, rec.last("no-switch")); got != "no-compatible-model-target" {
		t.Fatalf("reason %s", got)
	}
}

// TestPollCadenceKeysOnTheLowestBarInForce: the poll planner escalates on one
// figure, compared against the binding headroom, so the engine hands it the
// lowest bar in force; the model bar joins only while autoswitch.model counts
// something (DESIGN A34). NewEngine pins it, ApplyThreshold (the 7d bar) and
// ApplyModels re-pin it.
func TestPollCadenceKeysOnTheLowestBarInForce(t *testing.T) {
	s := settings.Default() // 5h 85, 7d 97, model 95
	if got := pollThreshold(s, nil); got != 85 {
		t.Errorf("default bars: %v, want the 5h bar 85", got)
	}
	s.FiveHourThreshold = 99
	if got := pollThreshold(s, nil); got != 97 {
		t.Errorf("5h 99, no models: %v, want the 7d bar 97 (the model bar is not in force)", got)
	}
	if got := pollThreshold(s, []string{"all"}); got != 95 {
		t.Errorf("5h 99, models counted: %v, want the model bar 95", got)
	}

	f := newFake()
	e := NewEngine(f, settings.Default(), func(Event) {}, true)
	pinned := func() float64 {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.pollInputs[len(f.pollInputs)-1].threshold
	}
	if got := pinned(); got != 85 {
		t.Errorf("NewEngine pinned %v, want 85", got)
	}
	e.ApplyThreshold(60)
	if got := pinned(); got != 60 {
		t.Errorf("after ApplyThreshold(60) pinned %v, want 60 (the 7d bar is now the lowest)", got)
	}
	if got := e.currentSettings(); got.SevenDayThreshold != 60 || got.FiveHourThreshold != 85 || got.ModelThreshold != 95 {
		t.Errorf("ApplyThreshold moved %+v, want the 7d bar only", got)
	}
}
