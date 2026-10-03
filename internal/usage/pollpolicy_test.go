package usage

import (
	"math"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
)

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func rngHalf() float64 { return 0.5 }

// fh builds a usage map with a single five_hour window at pct.
func fh(pct float64) map[string]any {
	return map[string]any{"five_hour": map[string]any{"pct": pct}}
}

// fhReset builds a five_hour window at pct resetting at the given ISO string.
func fhReset(pct float64, iso string) map[string]any {
	return map[string]any{"five_hour": map[string]any{"pct": pct, "resets_at": iso}}
}

// TestPlanAfterFetchWorkedValues pins the 04§3.4 worked values with jitter
// zeroed by rng=0.5 (mandatory WP2 test).
func TestPlanAfterFetchWorkedValues(t *testing.T) {
	const now = 1000.0
	cases := []struct {
		name         string
		in           PlanInput
		wantInterval float64
		wantNextPoll float64
	}{
		{
			name:         "first fetch active",
			in:           PlanInput{IsActive: true, Now: now},
			wantInterval: 180, wantNextPoll: 1180,
		},
		{
			name:         "first fetch candidate",
			in:           PlanInput{IsActive: false, Now: now},
			wantInterval: 300, wantNextPoll: 1300,
		},
		{
			name:         "unmoved candidate prev=300 decays to 450",
			in:           PlanInput{PrevIntervalS: fp(300), PrevUsage: fh(10), NewUsage: fh(10), Now: now},
			wantInterval: 450, wantNextPoll: 1450,
		},
		{
			name:         "unmoved candidate prev=500 caps at 600",
			in:           PlanInput{PrevIntervalS: fp(500), PrevUsage: fh(10), NewUsage: fh(10), Now: now},
			wantInterval: 600, wantNextPoll: 1600,
		},
		{
			name:         "unmoved active prev=250 caps at 300",
			in:           PlanInput{IsActive: true, PrevIntervalS: fp(250), PrevUsage: fh(10), NewUsage: fh(10), Now: now},
			wantInterval: 300, wantNextPoll: 1300,
		},
		{
			name:         "moved candidate prev=600 halves to 300",
			in:           PlanInput{PrevIntervalS: fp(600), PrevUsage: fh(10), NewUsage: fh(20), Now: now},
			wantInterval: 300, wantNextPoll: 1300,
		},
		{
			name:         "moved prev=200 floors at 180",
			in:           PlanInput{PrevIntervalS: fp(200), PrevUsage: fh(10), NewUsage: fh(20), Now: now},
			wantInterval: 180, wantNextPoll: 1180,
		},
		{
			name:         "sub-delta wiggle is not movement",
			in:           PlanInput{PrevIntervalS: fp(300), PrevUsage: fh(10), NewUsage: fh(10.5), Now: now},
			wantInterval: 450, wantNextPoll: 1450,
		},
		{
			name:         "urgent active in band",
			in:           PlanInput{IsActive: true, PrevIntervalS: fp(180), PrevUsage: fh(78), NewUsage: fh(82), Threshold: 90, Now: now},
			wantInterval: 60, wantNextPoll: 1060,
		},
		{
			name:         "candidate same inputs never urgent",
			in:           PlanInput{IsActive: false, PrevIntervalS: fp(180), PrevUsage: fh(78), NewUsage: fh(82), Threshold: 90, Now: now},
			wantInterval: 180, wantNextPoll: 1180,
		},
		{
			name:         "urgent suppressed by recent 429",
			in:           PlanInput{IsActive: true, PrevIntervalS: fp(180), PrevUsage: fh(78), NewUsage: fh(82), Threshold: 90, Recent429: true, Now: now},
			wantInterval: 360, wantNextPoll: 1360,
		},
		{
			name:         "urgent base 60 unmoved snaps to 180",
			in:           PlanInput{IsActive: true, PrevIntervalS: fp(60), PrevUsage: fh(10), NewUsage: fh(10), Now: now},
			wantInterval: 180, wantNextPoll: 1180,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.RNG = rngHalf
			next, interval := PlanAfterFetch(in)
			if !approxEq(interval, tc.wantInterval) {
				t.Errorf("interval = %v, want %v", interval, tc.wantInterval)
			}
			if !approxEq(next, tc.wantNextPoll) {
				t.Errorf("nextPoll = %v, want %v", next, tc.wantNextPoll)
			}
		})
	}
}

// TestPlanAfterFetchResetCapping covers the future-reset cap (04§3.4 step 9,
// worked values).
func TestPlanAfterFetchResetCapping(t *testing.T) {
	const now = 1784277975.0
	iso := func(off float64) string {
		return time.Unix(int64(now+off), 0).UTC().Format(time.RFC3339)
	}

	t.Run("future reset caps at reset+slack", func(t *testing.T) {
		in := PlanInput{NewUsage: fhReset(40, iso(90)), Now: now, RNG: rngHalf}
		next, interval := PlanAfterFetch(in)
		if !approxEq(interval, 300) {
			t.Errorf("interval = %v, want 300", interval)
		}
		want := now + 90 + ResetSlackS
		if !approxEq(next, want) {
			t.Errorf("nextPoll = %v, want %v (reset+slack)", next, want)
		}
	})
}

// TestPlanAfterFetchAtLimitPark covers the at-limit park (04§3.4 step 9,
// DESIGN A31): an account with no headroom waits for the reset that frees it,
// but never longer than ParkCapS, so its cached measurement is polled again
// before it leaves the decision-trust ceiling (TrustMaxAgeS). The capped park
// is jittered downward only; at rng 0.5 it is ParkCapS·(1−JitterFrac/2).
func TestPlanAfterFetchAtLimitPark(t *testing.T) {
	const now = 1784277975.0
	const day = 86400.0
	iso := func(off float64) string {
		return time.Unix(int64(now+off), 0).UTC().Format(time.RFC3339)
	}
	// modelAt is a measurement whose 5h window has reset and whose per-model
	// weekly window is at pct until it resets off seconds from now.
	modelAt := func(pct, off float64) map[string]any {
		return map[string]any{
			"five_hour": map[string]any{"pct": 0.0},
			"seven_day": map[string]any{"pct": 40.0, "resets_at": iso(3 * day)},
			"scoped":    []any{map[string]any{"name": "Fable", "pct": pct, "resets_at": iso(off)}},
		}
	}
	all := []string{"all"}
	const parkAtHalf = ParkCapS * (1 - JitterFrac/2) // 3249 s

	cases := []struct {
		name         string
		in           PlanInput
		wantInterval float64
		wantNextPoll float64
	}{
		{
			name:         "inactive, model window at 100% resets in 5 days: capped",
			in:           PlanInput{NewUsage: modelAt(100, 5*day), Models: all, Now: now},
			wantInterval: 300, wantNextPoll: now + parkAtHalf,
		},
		{
			name:         "inactive, 5h window at 100% resets in 2 hours: capped",
			in:           PlanInput{NewUsage: fhReset(100, iso(7200)), Now: now},
			wantInterval: 300, wantNextPoll: now + parkAtHalf,
		},
		{
			name:         "inactive, model window at 100% resets in 10 minutes: parks at the reset",
			in:           PlanInput{NewUsage: modelAt(100, 600), Models: all, Now: now},
			wantInterval: 300, wantNextPoll: now + 600,
		},
		{
			name:         "active, model window at 100% resets in 5 days: capped",
			in:           PlanInput{IsActive: true, NewUsage: modelAt(100, 5*day), Models: all, Now: now},
			wantInterval: 180, wantNextPoll: now + parkAtHalf,
		},
		{
			name:         "model window not counted: headroom left, normal cadence",
			in:           PlanInput{NewUsage: modelAt(100, 5*day), Now: now},
			wantInterval: 300, wantNextPoll: now + 300,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.RNG = rngHalf
			next, interval := PlanAfterFetch(in)
			if !approxEq(interval, tc.wantInterval) {
				t.Errorf("interval = %v, want %v", interval, tc.wantInterval)
			}
			if !approxEq(next, tc.wantNextPoll) {
				t.Errorf("nextPoll = now%+.0f, want now%+.0f", next-now, tc.wantNextPoll-now)
			}
		})
	}

	// The capped park is jittered downward only: a park far from its reset is
	// due between ParkCapS·(1−JitterFrac) and ParkCapS out, so at least one
	// interval out and before the trust ceiling. A park at a nearer reset is
	// the reset itself, at every jitter.
	for _, tc := range []struct {
		rng  float64
		want float64
	}{
		{0, now + ParkCapS},                // 3420 s
		{0.5, now + parkAtHalf},            // 3249 s, the midpoint
		{1, now + ParkCapS*(1-JitterFrac)}, // 3078 s
	} {
		rng := func() float64 { return tc.rng }
		next, interval := PlanAfterFetch(PlanInput{NewUsage: modelAt(100, 5*day), Models: all, Now: now, RNG: rng})
		if !approxEq(next, tc.want) {
			t.Errorf("rng=%v: nextPoll = now%+.1f, want now%+.1f", tc.rng, next-now, tc.want-now)
		}
		if next > now+ParkCapS || next < now+interval {
			t.Errorf("rng=%v: nextPoll = now%+.0f, want within [now+%v, now+%v]", tc.rng, next-now, interval, ParkCapS)
		}
		if next-now >= TrustMaxAgeS {
			t.Errorf("rng=%v: nextPoll = now%+.0f reaches the trust ceiling %v", tc.rng, next-now, TrustMaxAgeS)
		}
		if next, _ := PlanAfterFetch(PlanInput{NewUsage: modelAt(100, 600), Models: all, Now: now, RNG: rng}); !approxEq(next, now+600) {
			t.Errorf("rng=%v, reset in 10 minutes: nextPoll = now%+.1f, want the reset, now+600", tc.rng, next-now)
		}
	}
}

// TestAtLimitParksFetchedTogetherFallDueApart replays, over the real store,
// three at-limit candidates fetched in one pass (as the escalation pass
// fetches them) and then polled the engine's way: every 15 s tick fetches the
// one candidate DueCandidate picks (DESIGN A31 item 3). Each account keeps one
// fixed jitter draw at every park, so their due times stay apart and each
// reads as decision-grade after every tick for three hours. The replay shows
// the spread and that every park ends inside the trust ceiling, not that
// collisions never happen: in production every plan draws afresh, and two
// accounts can still fall due on one tick. Without the jitter all three fall
// due on one tick and two wait for theirs.
func TestAtLimitParksFetchedTogetherFallDueApart(t *testing.T) {
	const start = 1784277975.0
	const tick = 15.0
	clk := fakeAt(start)
	s := NewStore(t.TempDir(), clk)
	weekly := time.Unix(int64(start+5*86400), 0).UTC().Format(time.RFC3339)
	atLimit := map[string]any{"seven_day": map[string]any{"pct": 100.0, "resets_at": weekly}}
	cands := []string{"2", "3", "4"}
	ids := map[string]Identity{"2": {Email: "b@x.com"}, "3": {Email: "c@x.com"}, "4": {Email: "d@x.com"}}
	draw := map[string]float64{"2": 0.1, "3": 0.5, "4": 0.9}

	fetch := func(nums ...string) {
		t.Helper()
		recs := make(map[string]FetchRecord, len(nums))
		for _, n := range nums {
			recs[n] = FetchRecord{Usage: atLimit}
		}
		if err := s.Record(recs, ids); err != nil {
			t.Fatal(err)
		}
		plans := make(map[string]PollPlan, len(nums))
		for _, n := range nums {
			r := draw[n]
			next, interval := PlanAfterFetch(PlanInput{NewUsage: atLimit, Now: clock.Seconds(clk), RNG: func() float64 { return r }})
			plans[n] = PollPlan{NextPollAt: &next, IntervalS: &interval}
		}
		if err := s.SetPollPlan(plans, ids); err != nil {
			t.Fatal(err)
		}
	}

	fetch(cands...)
	due := map[float64]string{}
	for n, e := range s.Entries(ids) {
		if prev, ok := due[*e.NextPollAt]; ok {
			t.Fatalf("accounts %s and %s fetched together fall due together, at now%+.0f", prev, n, *e.NextPollAt-start)
		}
		due[*e.NextPollAt] = n
	}

	for elapsed := tick; elapsed <= 3*3600; elapsed += tick {
		clk.Advance(time.Duration(tick) * time.Second)
		if pick := DueCandidate(cands, s.Entries(ids), clock.Seconds(clk)); pick != "" {
			fetch(pick)
		}
		for n, e := range s.Entries(ids) {
			if e.DecisionValue() == nil {
				t.Fatalf("at +%.0fs: account %s reads usage unavailable (age %.0fs, due at +%.0fs)",
					elapsed, n, *e.AgeS, *e.NextPollAt-start)
			}
		}
	}
}

// TestPlanAfterFetchJitterBounds pins the jitter endpoints (04§3.4 step 8).
func TestPlanAfterFetchJitterBounds(t *testing.T) {
	const now = 1000.0
	for _, tc := range []struct {
		name string
		rng  float64
		want float64
	}{
		{"rng=0 lower bound", 0.0, 1000 + 300*0.9},
		{"rng=1 upper bound", 1.0, 1000 + 300*1.1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := PlanInput{IsActive: false, Now: now, RNG: func() float64 { return tc.rng }}
			next, interval := PlanAfterFetch(in)
			if !approxEq(interval, 300) {
				t.Errorf("interval = %v, want 300", interval)
			}
			if !approxEq(next, tc.want) {
				t.Errorf("nextPoll = %v, want %v", next, tc.want)
			}
		})
	}
}

// TestFailureBackoffS pins the three backoff regimes (04§2.6 worked values).
func TestFailureBackoffS(t *testing.T) {
	t.Run("no retry-after exponential", func(t *testing.T) {
		want := []float64{30, 60, 120, 240, 480, 600, 600}
		for i, w := range want {
			n := i + 1
			if got := failureBackoffS(n, nil); !approxEq(got, w) {
				t.Errorf("n=%d got %v want %v", n, got, w)
			}
		}
	})
	t.Run("retry-after 0 edge floor", func(t *testing.T) {
		want := []float64{300, 300, 300, 300, 480, 600, 600}
		zero := 0.0
		for i, w := range want {
			n := i + 1
			if got := failureBackoffS(n, &zero); !approxEq(got, w) {
				t.Errorf("n=%d got %v want %v", n, got, w)
			}
		}
	})
	t.Run("retry-after N burst rule", func(t *testing.T) {
		cases := []struct {
			n    int
			ra   float64
			want float64
		}{
			{1, 90.0, 90},    // server floor beats computed 30
			{5, 10.0, 480},   // own curve wins
			{1, 5000.0, 900}, // capped at RETRY_AFTER_FLOOR_CAP_S
			{1, 300.0, 300},  // measured burst honored exactly
		}
		for _, c := range cases {
			ra := c.ra
			if got := failureBackoffS(c.n, &ra); !approxEq(got, c.want) {
				t.Errorf("(%d, %v) got %v want %v", c.n, c.ra, got, c.want)
			}
		}
		if got := failureBackoffS(50, nil); !approxEq(got, 600) {
			t.Errorf("(50, nil) got %v want 600", got)
		}
	})
}

func TestParseResetTS(t *testing.T) {
	cases := []struct {
		in   string
		want *float64
	}{
		{"", nil},
		{"not-a-date", nil},
		{"2026-07-05T20:39:00Z", fp(float64(time.Date(2026, 7, 5, 20, 39, 0, 0, time.UTC).Unix()))},
		{"2026-07-05T20:39:00+00:00", fp(float64(time.Date(2026, 7, 5, 20, 39, 0, 0, time.UTC).Unix()))},
	}
	for _, c := range cases {
		got := parseResetTS(c.in)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%q: got %v, want nil", c.in, *got)
		case c.want != nil && got == nil:
			t.Errorf("%q: got nil, want %v", c.in, *c.want)
		case c.want != nil && got != nil && !approxEq(*got, *c.want):
			t.Errorf("%q: got %v, want %v", c.in, *got, *c.want)
		}
	}
}

// TestAccountHeadroom pins binding-window selection incl. scoped models
// (04§1.20 via the local map-based helpers).
func TestAccountHeadroom(t *testing.T) {
	usage := map[string]any{
		"five_hour": map[string]any{"pct": 22.0},
		"seven_day": map[string]any{"pct": 61.0},
		"scoped":    []any{map[string]any{"name": "Fable", "pct": 100.0}},
	}
	// Without models: 7d binds (61 highest) -> headroom 39.
	if h := accountHeadroom(usage, nil); h == nil || !approxEq(*h, 39) {
		t.Errorf("headroom no-models = %v, want 39", h)
	}
	// With Fable: the 100% scoped window binds -> headroom 0.
	if h := accountHeadroom(usage, []string{"Fable"}); h == nil || !approxEq(*h, 0) {
		t.Errorf("headroom with Fable = %v, want 0", h)
	}
	// Only spend -> unknown (nil), distinct from at-limit 0.
	spendOnly := map[string]any{"spend": map[string]any{"pct": 14.58}}
	if h := accountHeadroom(spendOnly, nil); h != nil {
		t.Errorf("spend-only headroom = %v, want nil (unknown)", h)
	}
}
