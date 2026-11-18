// Tests for the poll plan of the Fable store's live account (DESIGN A102).
package reporting

import (
	"testing"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/usage"
)

// After a fetch the Fable store's live account 8 is planned as an active
// account, on the Fable scope's models too: its Fable window moving near the
// bar brings it to the urgent cadence. Account 7, fetched alike, is planned
// as an inactive account, as before.
func TestPollPlanOfTheFableLiveAccount(t *testing.T) {
	s, _ := fableIncident(t, nil, nil, "8", incidentSlots...)
	var calls int
	s.OAuth = recordingUsage(&calls, map[string]any{
		"five_hour": map[string]any{"utilization": 10.0}, "seven_day": map[string]any{"utilization": 20.0},
		"limits": []any{map[string]any{"scope": map[string]any{"model": map[string]any{"display_name": "Fable"}}, "percent": 92.0}},
	})
	now := clock.Seconds(s.Clk)
	rows := map[string]any{}
	for _, num := range []string{"7", "8"} {
		rows[num] = map[string]any{"email": "acct" + num + "@example.com", "organizationUuid": "", "fetchedAt": now - 400,
			"lastGood": map[string]any{"five_hour": map[string]any{"pct": 10.0}, "seven_day": map[string]any{"pct": 20.0},
				"scoped": []any{map[string]any{"name": "Fable", "pct": 80.0}}}}
	}
	writeUsageRows(t, s, rows)
	root := s.BackupDir()
	SetScopedPollInputs(root, paths.FableScopeDir(root), 95, []string{"Fable"})
	t.Cleanup(func() { ClearScopedPollInputs(root, paths.FableScopeDir(root)) })

	infos := BuildAccountsInfo(s)
	for _, info := range infos {
		if want := info.Number == 8; info.FableLive != want || info.IsActive != (info.Number == 3) {
			t.Fatalf("Account-%d: FableLive %v, IsActive %v", info.Number, info.FableLive, info.IsActive)
		}
	}
	UsageEntriesByAccount(s, map[string]bool{"7": true, "8": true})
	if calls != 2 {
		t.Fatalf("%d fetches, want 2", calls)
	}
	entries := UsageEntriesByAccount(s, map[string]bool{})
	if got := entries["8"].PollIntervalS; got == nil || *got != usage.UrgentIntervalS {
		t.Fatalf("Account-8 polls every %v s, want the urgent %v s", deref64(got), usage.UrgentIntervalS)
	}
	if got := entries["7"].PollIntervalS; got == nil || *got != usage.MinIntervalS {
		t.Fatalf("Account-7 polls every %v s, want an inactive account's %v s", deref64(got), usage.MinIntervalS)
	}
}

func deref64(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}
