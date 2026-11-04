package reporting

import (
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

// MarkUsageDue makes num's next usage poll due now, so the next collection
// fetches it through the store's own gate (a claimed fetch or a dead token
// still holds), unless a backoff or a recent 429 holds the endpoint (DESIGN A75).
func MarkUsageDue(s *store.Store, num string) bool {
	data, err := s.ReadSequence()
	if err != nil {
		return false
	}
	rec, ok := recordFor(data, num)
	if !ok {
		return false
	}
	ids := map[string]usage.Identity{num: {Email: recStr(rec, "email"), OrgUUID: recStr(rec, "organizationUuid")}}
	now := clock.Seconds(s.Clk)
	entry := s.Usage.Entries(ids)[num]
	if entry.BackoffUntil != nil && now < *entry.BackoffUntil ||
		entry.Last429At != nil && now-*entry.Last429At < usage.Post429MinIntervalS {
		return false
	}
	return s.Usage.SetPollPlan(map[string]usage.PollPlan{num: {NextPollAt: &now, IntervalS: entry.PollIntervalS}}, ids) == nil
}
