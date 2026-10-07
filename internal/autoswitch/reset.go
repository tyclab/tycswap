package autoswitch

import (
	"math"
	"strconv"
	"time"

	"github.com/tyclab/tycswap/internal/oauth"
)

func usageDict(value any) map[string]any {
	m, _ := value.(map[string]any)
	return m
}

func accountHeadroom(value map[string]any, models []string) *float64 {
	return oauth.AccountHeadroom(oauth.NewUsage(value), models)
}

// bindingPct is the utilization of the binding window (100 - headroom), or nil.
func bindingPct(value map[string]any, models []string) *float64 {
	h := accountHeadroom(value, models)
	if h == nil {
		return nil
	}
	v := 100.0 - *h
	return &v
}

func renewalTS(value map[string]any, models []string) *float64 {
	return oauth.RenewalTS(oauth.NewUsage(value), models)
}

// windowPcts returns the ordered "5h","7d",scoped label→pct windows the
// decision reads (05§9 _window_pcts).
func windowPcts(value map[string]any, models []string) []WindowPct {
	var out []WindowPct
	for _, w := range oauth.RelevantWindows(oauth.NewUsage(value), models) {
		out = append(out, WindowPct{Name: w.Label, Pct: w.Pct})
	}
	return out
}

// limitingResetTS returns the epoch (seconds) when the last of a usage map's
// ≥100% relevant windows resets, or nil (poll_policy.limiting_reset_ts).
func limitingResetTS(value map[string]any, models []string) *float64 {
	var latest *float64
	for _, w := range oauth.RelevantWindows(oauth.NewUsage(value), models) {
		if w.Pct < 100.0 {
			continue
		}
		ts := parseResetTS(w.ResetsAt)
		if ts != nil && (latest == nil || *ts > *latest) {
			latest = ts
		}
	}
	return latest
}

// parseResetTS parses an ISO-8601 reset timestamp to epoch seconds, or nil for
// an empty/unparseable value (poll_policy.parse_reset_ts; Z or +00:00).
func parseResetTS(resetsAt string) *float64 {
	if resetsAt == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, resetsAt); err == nil {
			v := float64(t.UnixNano()) / 1e9
			return &v
		}
	}
	return nil
}

// earliestRecovery returns the earliest epoch (seconds) any account becomes
// usable again, or nil when unprovable (05§11). Per exhausted account (≥1 window
// ≥100%) the recovery is the LATEST reset among its ≥100% windows; the answer is
// the MINIMUM across all exhausted accounts. A blocked window with no parseable
// reset makes the whole answer unprovable → nil.
func (e *Engine) earliestRecovery(usage map[string]any) *float64 {
	var earliest *float64
	for _, value := range usage {
		d := usageDict(value)
		if d == nil {
			continue
		}
		blocked := false
		for _, w := range oauth.RelevantWindows(oauth.NewUsage(d), e.models) {
			if w.Pct >= 100.0 {
				blocked = true
				break
			}
		}
		if !blocked {
			continue
		}
		usableAt := limitingResetTS(d, e.models)
		if usableAt == nil {
			return nil
		}
		if earliest == nil || *usableAt < *earliest {
			earliest = usableAt
		}
	}
	return earliest
}

func formatRecoveryISO(epoch float64) string {
	sec := math.Floor(epoch)
	usRounded, _ := strconv.ParseFloat(strconv.FormatFloat((epoch-sec)*1e6, 'f', 0, 64), 64)
	us := int64(usRounded)
	t := time.Unix(int64(sec), us*1000).UTC()
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05") + "Z"
	}
	return t.Format("2006-01-02T15:04:05.000000") + "Z"
}
