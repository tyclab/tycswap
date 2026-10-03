// usagepark_test.go — the auto engine over the real store, usage collector and
// poll policy, with a fake clock and a stub usage endpoint (DESIGN A31): an
// inactive account at the limit of a per-model window that resets days out
// keeps a trusted measurement while it waits, and is not polled more than
// once per park.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/usage"
)

func TestEngineKeepsAnAtLimitCandidateTrusted(t *testing.T) {
	home := t.TempDir()
	testutil.Setenv(t, "HOME", home)
	testutil.Unsetenv(t, "CLAUDE_CONFIG_DIR")
	testutil.Unsetenv(t, "XDG_DATA_HOME")
	testutil.Setenv(t, "NO_COLOR", "1")
	clk := testutil.FixedClock(t, "2026-07-17T09:00:00Z")
	start := clk.Now()
	weekly := start.Add(5 * 24 * time.Hour).UTC().Format(time.RFC3339)

	// Account 1 (active) is lightly used. Account 2's 5h window has reset, but
	// its per-model weekly window is at 100% for five more days.
	var mu sync.Mutex
	calls := map[string]int{}
	fc := &oauth.FakeClient{UsageFn: func(_ context.Context, token string) (map[string]any, error) {
		mu.Lock()
		calls[token]++
		mu.Unlock()
		if token == "tok-b" {
			return map[string]any{
				"five_hour": map[string]any{"utilization": 0.0},
				"seven_day": map[string]any{"utilization": 40.0, "resets_at": weekly},
				"limits": []any{map[string]any{
					"scope":     map[string]any{"model": map[string]any{"display_name": "Fable"}},
					"percent":   100.0,
					"resets_at": weekly,
				}},
			}, nil
		}
		return map[string]any{
			"five_hour": map[string]any{"utilization": 10.0},
			"seven_day": map[string]any{"utilization": 10.0},
		}, nil
	}}

	sw, err := New(store.Options{Clock: clk, OAuth: fc, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	if err := sw.SetupDirectories(); err != nil {
		t.Fatalf("SetupDirectories: %v", err)
	}
	credsA, credsB := oauthCreds("tok-a"), oauthCreds("tok-b")
	seedManaged(t, sw, "1", "a@x.com", credsA)
	seedManaged(t, sw, "2", "b@x.com", credsB)
	writeSeqDirect(t, sw, ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
		"1": rawRecord(map[string]any{"email": "a@x.com", "organizationUuid": ""}),
		"2": rawRecord(map[string]any{"email": "b@x.com", "organizationUuid": ""}),
	})
	seedLive(t, sw, "a@x.com", credsA)

	// Count every model window, as autoswitch.model "all" does.
	s := settings.Default()
	all := "all"
	s.Model = &all
	s.IntervalSeconds = 15
	t.Cleanup(sw.ClearPollPolicyInputs)
	engine := autoswitch.NewEngine(autoswitchAdapter{sw}, s, func(autoswitch.Event) {}, false,
		autoswitch.WithClock(clk), autoswitch.WithOAuthClient(fc),
		autoswitch.WithRNG(func() float64 { return 0.5 }))

	const step = 15 * time.Second
	const span = 3 * time.Hour
	for elapsed := time.Duration(0); elapsed <= span; elapsed += step {
		if got := engine.Tick(); got != autoswitch.NoAction {
			t.Fatalf("at +%v: tick = %v, want NoAction (active below threshold)", elapsed, got)
		}
		// What list, status and the dashboard read from the store between
		// fetches: the measurement must still be decision-grade.
		e := sw.UsageEntriesByAccount(map[string]bool{})["2"]
		if e.DecisionValue() == nil {
			age, due := "unknown", "none"
			if e.AgeS != nil {
				age = (time.Duration(*e.AgeS) * time.Second).String()
			}
			if e.NextPollAt != nil {
				due = time.Unix(int64(*e.NextPollAt), 0).Sub(clk.Now()).String()
			}
			t.Fatalf("at +%v: account 2 reads usage unavailable (measurement age %s, next poll in %s)", elapsed, age, due)
		}
		clk.Advance(step)
	}

	// Refetched at least once per trust ceiling, at most once per park (the
	// shortest, with the downward jitter at its full JitterFrac).
	mu.Lock()
	got := calls["tok-b"]
	mu.Unlock()
	lo := 1 + int(span.Seconds()/usage.TrustMaxAgeS)
	hi := 1 + int(span.Seconds()/(usage.ParkCapS*(1-usage.JitterFrac)))
	if got < lo || got > hi {
		t.Errorf("account 2 fetched %d times in %v, want %d..%d", got, span, lo, hi)
	}
}
