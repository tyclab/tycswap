package reporting

import (
	"testing"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/testutil"
)

// 2026-11-04 12:54: a status line reported Account-6 near its 5h bar, but the
// engine only nominated the fetch and the store served the 137-second-old
// reading. Marking the poll due goes through the store's own gate (DESIGN A75).
func TestMarkUsageDueBringsTheNextFetchForward(t *testing.T) {
	for name, tc := range map[string]struct {
		row     map[string]any
		wantDue bool
	}{
		"scheduled":   {map[string]any{}, true},
		"recent-429":  {map[string]any{"last429At": -60.0}, false},
		"backing-off": {map[string]any{"backoffUntil": 100.0}, false},
		"dead-token":  {map[string]any{"authDeadStrikes": 1}, true},
	} {
		t.Run(name, func(t *testing.T) {
			clk := testutil.FixedClock(t, fixedNow)
			now := clock.Seconds(clk)
			calls := 0
			s := newStore(t, clk, recordingUsage(&calls, map[string]any{"five_hour": map[string]any{"utilization": 97.0}}))
			writeSequenceRaw(t, s, `{"activeAccountNumber": 2, "lastUpdated": "x", "sequence": [1, 2],
  "accounts": {"1": {"email": "a@example.com", "organizationUuid": ""}, "2": {"email": "b@example.com", "organizationUuid": ""}}}`)
			row := map[string]any{"email": "a@example.com", "organizationUuid": "", "fetchedAt": now - 30,
				"nextPollAt": now + 120, "pollIntervalS": 180.0,
				"lastGood": map[string]any{"five_hour": map[string]any{"pct": 42.0}}}
			for k, v := range tc.row {
				if f, ok := v.(float64); ok && k != "authDeadStrikes" {
					v = now + f
				}
				row[k] = v
			}
			writeUsageRows(t, s, map[string]any{"1": row})
			infos := []AccountInfo{{Number: 1, Email: "a@example.com", Creds: oauthCreds("at", "rt", 0)}}
			CollectUsageEntries(s, infos, map[string]bool{"1": true})
			if calls != 0 {
				t.Fatalf("a 30-second-old reading was fetched again before its plan: %d", calls)
			}
			if got := MarkUsageDue(s, "1"); got != tc.wantDue {
				t.Fatalf("MarkUsageDue = %v, want %v", got, tc.wantDue)
			}
			CollectUsageEntries(s, infos, map[string]bool{"1": true})
			wantCalls := 0
			if tc.wantDue && name != "dead-token" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("fetches after MarkUsageDue = %d, want %d", calls, wantCalls)
			}
		})
	}
}
