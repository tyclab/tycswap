package reporting

import (
	"reflect"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/testutil"
)

// TestResolvePollInputsKeysOnTheLowestBar: with no engine pinning the poll
// plan, cadence follows settings.json, and the one figure the planner
// escalates on is the lowest bar in force; the per-model bar joins only while
// autoswitch.model counts something (DESIGN A34). A file that still carries
// the single legacy "threshold" key seeds the 7d bar with it.
func TestResolvePollInputsKeysOnTheLowestBar(t *testing.T) {
	ClearPollPolicyInputs()
	t.Cleanup(ClearPollPolicyInputs)
	s := newStore(t, testutil.FixedClock(t, "2026-07-17T09:00:00Z"), &oauth.FakeClient{})
	for _, tc := range []struct {
		name, body string
		want       float64
		models     []string
	}{
		{"defaults", `{}`, 85, nil},
		{"7d lowest", `{"autoswitch":{"fiveHourThreshold":90,"sevenDayThreshold":80}}`, 80, nil},
		{"model bar not in force", `{"autoswitch":{"fiveHourThreshold":90,"sevenDayThreshold":95,"modelThreshold":60}}`, 90, nil},
		{"model bar in force", `{"autoswitch":{"fiveHourThreshold":90,"sevenDayThreshold":95,"modelThreshold":60,"model":"all"}}`, 60, []string{"all"}},
		{"legacy threshold", `{"autoswitch":{"threshold":70}}`, 70, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeSettings(t, s, tc.body)
			threshold, models := resolvePollInputs(s)
			if threshold != tc.want || !reflect.DeepEqual(append([]string{}, models...), append([]string{}, tc.models...)) {
				t.Errorf("resolvePollInputs = %v %v, want %v %v", threshold, models, tc.want, tc.models)
			}
		})
	}
}

func TestIndependentGroupPoliciesKeepUsableOpusPolling(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	now := clock.Seconds(clk)
	calls := 0
	week := clk.Now().Add(48 * time.Hour).Format(time.RFC3339)
	s := newStore(t, clk, recordingUsage(&calls, map[string]any{
		"five_hour": map[string]any{"utilization": 10.0},
		"seven_day": map[string]any{"utilization": 20.0, "resets_at": week},
		"limits":    []any{map[string]any{"scope": map[string]any{"model": map[string]any{"display_name": "Fable"}}, "percent": 100.0, "resets_at": week}},
	}))
	SetScopedPollInputs(s.BackupDir(), "fable", 85, []string{"Fable"})
	SetScopedPollInputs(s.BackupDir(), "opus", 85, nil)
	t.Cleanup(func() { ClearScopedPollInputs(s.BackupDir(), "fable"); ClearScopedPollInputs(s.BackupDir(), "opus") })
	infos := []AccountInfo{{Number: 1, Email: "a@example.com", Creds: oauthCreds("at", "rt", 0)}}
	CollectUsageEntries(s, infos, nil)
	entries := CollectUsageEntries(s, infos, map[string]bool{})
	if calls != 1 {
		t.Fatalf("group policies duplicated fetches: %d", calls)
	}
	if at := entries["1"].NextPollAt; at == nil || *at-now > 1200 {
		t.Fatalf("usable Opus parked behind full Fable: %v", at)
	}
	ClearScopedPollInputs(s.BackupDir(), "opus")
	policies := resolvePollPolicies(s)
	hasFable := false
	for _, policy := range policies {
		if len(policy.models) == 1 && policy.models[0] == "Fable" {
			hasFable = true
		}
	}
	if len(policies) != 2 || !hasFable {
		t.Fatalf("stopping Opus removed Fable policy: %+v", policies)
	}
	other := newStore(t, clk, &oauth.FakeClient{})
	if len(resolvePollPolicies(other)[0].models) != 0 {
		t.Fatal("poll policy leaked across stores")
	}
}
