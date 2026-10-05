// autoswitch_test.go — when the Codex engine moves, when it refuses, and what
// it admits. Ports claude-swap PR #252 tests/test_codex_autoswitch.py.
package autoswitch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/codex/api"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/usage"
)

var ctx = context.Background()

type accOpt struct {
	active, disabled bool
	weekly           *float64
}

func f(v float64) *float64 { return &v }

// acc builds a row; pct nil means no five_hour measurement.
func acc(number string, pct *float64, o accOpt) reporting.AccountSnapshot {
	u := map[string]any{}
	if pct != nil {
		u["five_hour"] = map[string]any{"pct": *pct}
	}
	if o.weekly != nil {
		// As read back from the usage store: a json.Number, not a float64.
		u["seven_day"] = map[string]any{"pct": json.Number(jsonNum(*o.weekly))}
	}
	if len(u) == 0 {
		u = nil
	}
	return reporting.AccountSnapshot{
		Number: number, Email: number + "@x", IsActive: o.active, Kind: "oauth", Switchable: true,
		Usage: usage.UsageEntry{LastGood: u}, Disabled: o.disabled, Provider: reporting.ProviderCodex,
	}
}

func jsonNum(v float64) string { b, _ := json.Marshal(v); return string(b) }

type fakeSwitcher struct {
	accounts  []reporting.AccountSnapshot
	rotatable []string // nil: every non-disabled account
	running   []int
	switched  []string
	snapPanic bool
	switchErr error
}

func (s *fakeSwitcher) AccountsSnapshot(context.Context, map[string]bool) reporting.AccountsSnapshot {
	if s.snapPanic {
		panic("store gone")
	}
	snap := reporting.AccountsSnapshot{Accounts: s.accounts, Provider: reporting.ProviderCodex}
	for _, a := range s.accounts {
		if a.IsActive {
			snap.ActiveNumber = a.Number
		}
	}
	return snap
}

func (s *fakeSwitcher) SwitchableAccountNumbers() []string {
	if s.rotatable != nil {
		return s.rotatable
	}
	var out []string
	for _, a := range s.accounts {
		if !a.Disabled {
			out = append(out, a.Number)
		}
	}
	return out
}

func (s *fakeSwitcher) SwitchTo(_ context.Context, number string) (switcher.SwitchResult, error) {
	if s.switchErr != nil {
		return switcher.SwitchResult{}, s.switchErr
	}
	s.switched = append(s.switched, number)
	return switcher.SwitchResult{Number: number, Email: number + "@x", RunningPIDs: append([]int{}, s.running...)}, nil
}

func auto(fake *fakeSwitcher, threshold float64) *AutoSwitcher { return New(fake, threshold, 10) }

// ---- the binding window ------------------------------------------------------

func TestBindingPctIsTheWorstWindow(t *testing.T) {
	if p := BindingPct(acc("1", f(20), accOpt{weekly: f(80)}).Usage.LastGood); p == nil || *p != 80 {
		t.Fatalf("BindingPct = %v, want 80", p)
	}
}

func TestBindingPctIsNilWithoutAMeasurement(t *testing.T) {
	if p := BindingPct(acc("1", nil, accOpt{}).Usage.LastGood); p != nil {
		t.Fatalf("BindingPct = %v, want nil", *p)
	}
	if p := BindingPct(map[string]any{"five_hour": map[string]any{"pct": "x"}}); p != nil {
		t.Fatalf("BindingPct of a non-numeric pct = %v", *p)
	}
}

// ---- when NOT to switch ------------------------------------------------------

func TestAnActiveAccountBelowThresholdIsLeftAlone(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(40), accOpt{active: true}), acc("2", f(5), accOpt{})}}
	tick := auto(fake, 90).Tick(ctx, false)
	if tick.Outcome != OutcomeOK || tick.Detail != "account 1 at 40% (below threshold)" || len(fake.switched) != 0 {
		t.Fatalf("tick = %+v switched=%v", tick, fake.switched)
	}
}

func TestAnUnmeasuredActiveAccountIsNeverSwitchedAwayFrom(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", nil, accOpt{active: true}), acc("2", f(1), accOpt{})}}
	tick := auto(fake, 90).Tick(ctx, false)
	if !strings.Contains(tick.Detail, "unknown") || tick.Outcome != OutcomeOK || len(fake.switched) != 0 {
		t.Fatalf("tick = %+v switched=%v", tick, fake.switched)
	}
}

func TestNoCandidateBelowThresholdBlocksRatherThanMoving(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(95), accOpt{active: true}), acc("2", f(97), accOpt{})}}
	tick := auto(fake, 90).Tick(ctx, false)
	if tick.Outcome != OutcomeBlocked || tick.Detail != "account 1 at 95% and no better candidate" || len(fake.switched) != 0 {
		t.Fatalf("tick = %+v switched=%v", tick, fake.switched)
	}
}

func TestACandidateInsideTheHysteresisMarginIsRefused(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(92), accOpt{active: true}), acc("2", f(89), accOpt{})}}
	if tick := New(fake, 90, 10).Tick(ctx, false); tick.Outcome != OutcomeBlocked || len(fake.switched) != 0 {
		t.Fatalf("tick = %+v", tick)
	}
}

func TestACandidateExactlyAtTheHysteresisMarginIsAccepted(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(92), accOpt{active: true}), acc("2", f(82), accOpt{})}}
	if tick := New(fake, 90, 10).Tick(ctx, false); tick.Outcome != OutcomeSwitched {
		t.Fatalf("tick = %+v", tick)
	}
}

func TestAnActiveAccountExactlyAtThresholdIsSwitched(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(90), accOpt{active: true}), acc("2", f(5), accOpt{})}}
	if tick := auto(fake, 90).Tick(ctx, false); tick.Outcome != OutcomeSwitched {
		t.Fatalf("tick = %+v", tick)
	}
}

func TestADisabledAccountIsNotACandidate(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(95), accOpt{active: true}), acc("2", f(2), accOpt{disabled: true})}}
	if tick := auto(fake, 90).Tick(ctx, false); tick.Outcome != OutcomeBlocked {
		t.Fatalf("tick = %+v", tick)
	}
}

func TestNoRotatableAccountsIsReportedNotCrashed(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(95), accOpt{active: true})}, rotatable: []string{}}
	if tick := auto(fake, 90).Tick(ctx, false); tick.Outcome != OutcomeNoAccounts || tick.Detail != "no rotatable accounts" {
		t.Fatalf("tick = %+v", tick)
	}
}

func TestNoActiveAccountIsAQuietNoOp(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(10), accOpt{}), acc("2", f(20), accOpt{})}}
	tick := auto(fake, 90).Tick(ctx, false)
	if tick.Outcome != OutcomeOK || tick.Detail != "no managed account active" || len(fake.switched) != 0 {
		t.Fatalf("tick = %+v", tick)
	}
}

func TestASnapshotFailureIsReportedNeverRaised(t *testing.T) {
	tick := New(&fakeSwitcher{snapPanic: true}, 90, 10).Tick(ctx, false)
	if tick.Outcome != OutcomeError || !strings.Contains(tick.Detail, "snapshot failed") {
		t.Fatalf("tick = %+v", tick)
	}
}

// ---- when to switch ----------------------------------------------------------

func TestAnExhaustedActiveAccountMovesToTheRoomiestCandidate(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{
		acc("1", f(95), accOpt{active: true}), acc("2", f(40), accOpt{}), acc("3", f(10), accOpt{}),
	}}
	tick := auto(fake, 90).Tick(ctx, false)
	if tick.Outcome != OutcomeSwitched || tick.SwitchedTo != "3" || !reflect.DeepEqual(fake.switched, []string{"3"}) {
		t.Fatalf("tick = %+v switched=%v", tick, fake.switched)
	}
	if tick.Detail != "switched 1 (95%) -> 3 (10%)" {
		t.Errorf("detail = %q", tick.Detail)
	}
}

func TestDryRunDecidesWithoutSwitching(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(95), accOpt{active: true}), acc("2", f(5), accOpt{})}}
	tick := auto(fake, 90).Tick(ctx, true)
	if tick.Outcome != OutcomeOK || !strings.Contains(tick.Detail, "would switch") || len(fake.switched) != 0 {
		t.Fatalf("tick = %+v switched=%v", tick, fake.switched)
	}
	if tick.Detail != "would switch 1 (95%) -> 2 (5%)" || tick.SwitchedTo != "" {
		t.Errorf("tick = %+v", tick)
	}
}

func TestTheWeeklyWindowCanBeWhatTriggersTheSwitch(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{
		acc("1", f(10), accOpt{active: true, weekly: f(98)}), acc("2", f(10), accOpt{weekly: f(5)}),
	}}
	if tick := auto(fake, 90).Tick(ctx, false); tick.Outcome != OutcomeSwitched {
		t.Fatalf("tick = %+v", tick)
	}
}

func TestASwitchFailureIsReportedNotRaised(t *testing.T) {
	fake := &fakeSwitcher{
		accounts:  []reporting.AccountSnapshot{acc("1", f(95), accOpt{active: true}), acc("2", f(5), accOpt{})},
		switchErr: cerr.Switch("no credentials"),
	}
	tick := auto(fake, 90).Tick(ctx, false)
	if tick.Outcome != OutcomeError || tick.Detail != "switch failed: no credentials" {
		t.Fatalf("tick = %+v", tick)
	}
	fake.switchErr = errors.New("plain")
	if tick := auto(fake, 90).Tick(ctx, false); tick.Outcome != OutcomeError {
		t.Fatalf("a non-domain error: tick = %+v", tick)
	}
}

// TestACandidateAtItsFiveHourLimitIsNeverATarget: a candidate whose 5h window
// is at 100 % is refused, however much weekly room it has, whatever triggered
// the move, with the 7d bar (97) as the bar or a codexThreshold of 90.
func TestACandidateAtItsFiveHourLimitIsNeverATarget(t *testing.T) {
	for _, bar := range []float64{97, 90} {
		fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{
			acc("1", f(10), accOpt{active: true, weekly: f(98)}), acc("2", f(100), accOpt{weekly: f(10)}),
		}}
		tick := auto(fake, bar).Tick(ctx, false)
		if tick.Outcome != OutcomeBlocked || len(fake.switched) != 0 {
			t.Errorf("bar %v: tick = %+v switched=%v, want blocked", bar, tick, fake.switched)
		}
	}
}

// TestACandidateExactlyAtTheBarIsRefused: a candidate sitting exactly at the
// bar would trigger again on the next tick, so it is refused even where the
// hysteresis margin alone would let it through (100 - 10 = 90).
func TestACandidateExactlyAtTheBarIsRefused(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(100), accOpt{active: true}), acc("2", f(90), accOpt{})}}
	tick := auto(fake, 90).Tick(ctx, false)
	if tick.Outcome != OutcomeBlocked || len(fake.switched) != 0 {
		t.Fatalf("tick = %+v switched=%v, want blocked", tick, fake.switched)
	}
}

// TestABarOfOneHundredNeverMovesProactively: with the 7d bar at 100 (reachable
// since the bars range to 100) the Codex engine moves only off an account at
// its limit, and is not clamped below 100.
func TestABarOfOneHundredNeverMovesProactively(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{
		acc("1", f(99.9), accOpt{active: true, weekly: f(50)}), acc("2", f(5), accOpt{weekly: f(5)}),
	}}
	if tick := auto(fake, 100).Tick(ctx, false); tick.Outcome != OutcomeOK || len(fake.switched) != 0 {
		t.Fatalf("99.9%% under a bar of 100: tick = %+v switched=%v", tick, fake.switched)
	}
	fake = &fakeSwitcher{accounts: []reporting.AccountSnapshot{
		acc("1", f(100), accOpt{active: true, weekly: f(50)}), acc("2", f(5), accOpt{weekly: f(5)}),
	}}
	if tick := auto(fake, 100).Tick(ctx, false); tick.Outcome != OutcomeSwitched || tick.SwitchedTo != "2" {
		t.Fatalf("at the limit under a bar of 100: tick = %+v", tick)
	}
}

// ---- the restart caveat, stated rather than hidden ---------------------------

func TestASwitchUnderARunningSessionSaysSo(t *testing.T) {
	fake := &fakeSwitcher{
		accounts: []reporting.AccountSnapshot{acc("1", f(95), accOpt{active: true}), acc("2", f(5), accOpt{})},
		running:  []int{4242},
	}
	tick := auto(fake, 90).Tick(ctx, false)
	if !reflect.DeepEqual(tick.RunningPIDs, []int{4242}) {
		t.Fatalf("RunningPIDs = %v", tick.RunningPIDs)
	}
	h := tick.Human()
	if !strings.Contains(h, "restart codex") || !strings.Contains(h, "4242") {
		t.Errorf("Human = %q", h)
	}
}

func TestASwitchWithNothingRunningStaysQuiet(t *testing.T) {
	fake := &fakeSwitcher{accounts: []reporting.AccountSnapshot{acc("1", f(95), accOpt{active: true}), acc("2", f(5), accOpt{})}}
	if h := auto(fake, 90).Tick(ctx, false).Human(); strings.Contains(h, "restart") {
		t.Errorf("Human = %q", h)
	}
}

func TestTheHumanLineNamesTheProvider(t *testing.T) {
	if h := (Tick{Outcome: "ok", Detail: "nothing to do"}).Human(); !strings.HasPrefix(h, "codex:") {
		t.Errorf("Human = %q", h)
	}
	if h := (Tick{Outcome: "ok"}).Human(); h != "codex: ok" {
		t.Errorf("Human without detail = %q", h)
	}
	if h := (Tick{Outcome: "switched", Detail: "d", RunningPIDs: []int{1, 2}}).Human(); h != "codex: d — restart codex (pid 1, 2) for it to take effect" {
		t.Errorf("Human = %q", h)
	}
}

// ---- the property the loop depends on ----------------------------------------

func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	seg := func(v any) string { raw, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(raw) }
	return seg(map[string]any{"alg": "none"}) + "." + seg(claims) + ".sig"
}

// TestRepeatedTicksDoNotRePollTheAPI pins that fetch=nil ("every account
// eligible") on every tick costs one round on a cold cache and nothing after.
func TestRepeatedTicksDoNotRePollTheAPI(t *testing.T) {
	home := testutil.IsolateHome(t)
	testutil.Setenv(t, "CODEX_HOME", filepath.Join(home, ".codex"))
	testutil.Setenv(t, "XDG_DATA_HOME", filepath.Join(home, "xdg"))
	clk := testutil.FixedClock(t, "2026-09-30T12:00:00Z")

	var calls atomic.Int32
	client := &api.FakeClient{UsageFn: func(context.Context, string, string) api.UsageFetch {
		calls.Add(1)
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 5.0}}}
	}}
	sw := switcher.New(switcher.Options{
		Root: filepath.Join(home, "store"), Keychain: keychain.NewFake(), Platform: platform.Linux,
		Clock: clk, Client: client, RunningPIDs: func() []int { return nil }, LockTimeout: 100 * time.Millisecond,
	})
	for _, n := range []string{"a", "b"} {
		acct, user := "acct-"+n, "user-"+n
		key := authfile.AccountKey(user, acct)
		if _, err := sw.Store().UpsertSlot(key, store.Upsert{Email: n + "@x", Plan: "pro"}); err != nil {
			t.Fatal(err)
		}
		tok := makeJWT(t, map[string]any{
			"exp": int64(clock.Seconds(clk)) + 3600, "email": n + "@x",
			authfile.AuthClaim: map[string]any{"chatgpt_account_id": acct, "chatgpt_user_id": user},
		})
		payload := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token": tok, "access_token": tok, "refresh_token": "rt", "account_id": acct,
		}}
		if err := sw.Store().WriteSnapshot(key, payload); err != nil {
			t.Fatal(err)
		}
	}

	a := New(sw, 90, 10)
	a.Tick(ctx, false)
	first := calls.Load()
	a.Tick(ctx, false)
	a.Tick(ctx, false)
	if first != 2 {
		t.Errorf("cold tick made %d requests, want 2", first)
	}
	if got := calls.Load(); got != first {
		t.Errorf("two later ticks added %d requests", got-first)
	}
}
