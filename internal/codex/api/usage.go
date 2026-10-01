// usage.go — fetching ChatGPT usage and shaping it the way the rest of tycswap
// already reads. Ports claude-swap PR #252 codex/usage.py.
//
// The whole point of this file is the mapping. tycswap's renderers, its pace
// calculation, its JSON output and its autoswitch comparison all consume a map
// with five_hour/seven_day windows of {"pct", "resets_at", "countdown",
// "clock"} (internal/oauth BuildUsageResult, jsonout.UsageToJSON). Producing
// exactly that shape from the ChatGPT response is what lets every Claude-side
// consumer handle a Codex account with no branching at all.
//
// The wire shape was read off the live endpoint (2026-08-16), not inferred from
// codex-auth, whose registry.json stores its own normalized primary/secondary
// view — the API does not return that. The response is:
//
//	plan_type: str
//	rate_limit:
//	  allowed, limit_reached: bool
//	  primary_window:   {used_percent, limit_window_seconds, reset_after_seconds, reset_at}
//	  secondary_window: {same} | null
//	credits: {has_credits, unlimited, overage_limit_reached, balance, ...}
//
// Four conversions matter and each is easy to get wrong: the windows are
// nested under rate_limit; ChatGPT's used_percent is tycswap's pct; reset_at is
// epoch seconds while pace parses an ISO string (a raw epoch would silently
// disable pace for every Codex row); and primary_window is NOT necessarily the
// 5-hour window. Its length is data, carried in limit_window_seconds, and live
// Plus accounts report a primary_window of 604800 s with no secondary. Mapping
// by position would label weekly usage 5-hourly and pace would never fire, so
// windows are classified by declared length, never by key name.
//
// An absent window is normal and stays absent — never 0% used. Failure is
// reported, never raised: a broken account shows its status in the usage
// column (codex-auth's wording) and drops out of autoswitch candidacy.

package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/oauth"
)

// Usage sentinels. SentinelMissingAuth is codex-auth's wording, kept so a user
// who knows one tool reads the other's output without translation. An HTTP
// failure is "http <status>".
const (
	SentinelMissingAuth = "MissingAuth"
	SentinelNetwork     = "network"
	SentinelBadResponse = "bad-response"
)

// UsageFetch is one usage fetch: either a usage map, or a sentinel explaining
// why not. RetryAfterS is the server's Retry-After in seconds when it sent a
// usable one — honouring it is the difference between backing off and being
// rate-limited harder.
type UsageFetch struct {
	Usage       map[string]any
	Sentinel    string
	RetryAfterS *float64
}

// maxISOEpoch is 9999-12-31T23:59:59Z, the largest instant Python's datetime
// can represent; beyond it fromtimestamp overflows and _iso yields None.
const maxISOEpoch = 253402300799

// isoFromEpoch converts epoch seconds to Python's datetime.isoformat() form in
// UTC ("2027-01-15T08:00:00+00:00", microseconds only when non-zero), or ""
// when epoch is not a positive in-range number. The "+00:00" offset (not "Z")
// keeps the string byte-identical to what claude-swap persists.
func isoFromEpoch(epoch any) string {
	f, ok := number(epoch)
	if !ok || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) || f > maxISOEpoch {
		return ""
	}
	sec := math.Floor(f)
	us := math.RoundToEven((f - sec) * 1e6)
	if us >= 1e6 {
		sec++
		us -= 1e6
	}
	t := time.Unix(int64(sec), int64(us)*1000).UTC()
	s := t.Format("2006-01-02T15:04:05")
	if us != 0 {
		s += fmt.Sprintf(".%06d", int64(us))
	}
	return s + "+00:00"
}

// window maps one ChatGPT rate-limit window onto tycswap's window shape, or nil
// when it carries no numeric used_percent. pct passes through as decoded
// (json.Number keeps int vs float), matching the Claude side's uncoerced pct.
// A window with no reset keeps its percentage and simply omits the rest.
func window(raw any, now time.Time) map[string]any {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	pct := m["used_percent"]
	if _, ok := number(pct); !ok {
		return nil
	}
	entry := map[string]any{"pct": pct}
	if resetsAt := isoFromEpoch(m["reset_at"]); resetsAt != "" {
		entry["resets_at"] = resetsAt
		if cd, ck, ok := oauth.FormatReset(resetsAt, now); ok {
			entry["countdown"] = cd
			entry["clock"] = ck
		}
	}
	return entry
}

// BuildUsageResult normalizes a wham/usage response into tycswap's usage map, or
// nil when the response carries no usable window. countdown/clock are computed
// against the current wall clock (fetch time), as on the Claude side.
func BuildUsageResult(data any) map[string]any {
	return buildUsageResult(data, time.Now().UTC())
}

// buildUsageResult is the now-injectable core of BuildUsageResult.
func buildUsageResult(data any, now time.Time) map[string]any {
	d, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	result := map[string]any{}

	if rl, ok := d["rate_limit"].(map[string]any); ok {
		// Classify by declared length, never by key name. If both windows
		// classify the same way (not observed, but nothing forbids it) the
		// FIRST wins: the API lists primary first, and averaging two windows of
		// one class would invent a number the server never reported.
		for _, key := range []string{"primary_window", "secondary_window"} {
			raw := rl[key]
			w := window(raw, now)
			if w == nil {
				continue
			}
			var slot string
			if length, ok := number(raw.(map[string]any)["limit_window_seconds"]); ok {
				if length >= WeeklyWindowMinS {
					slot = "seven_day"
				} else {
					slot = "five_hour"
				}
			} else if key == "primary_window" {
				// No declared length: fall back to the conventional positions.
				slot = "five_hour"
			} else {
				slot = "seven_day"
			}
			if _, taken := result[slot]; !taken {
				result[slot] = w
			}
		}
	}

	// No window at all is not usable usage: every consumer needs one, and a
	// plan-only map renders as a blank row with no explanation — which is
	// exactly how the first live run failed.
	if len(result) == 0 {
		return nil
	}

	if credits, ok := d["credits"].(map[string]any); ok && truthy(credits["has_credits"]) {
		result["spend"] = map[string]any{
			"unlimited": truthy(credits["unlimited"]),
			"balance":   credits["balance"],
		}
	}

	if plan := authfile.NormalizePlan(d["plan_type"]); plan != "" {
		result["plan"] = plan
	}
	return result
}

// retryAfterSeconds parses a Retry-After header, delta-seconds form only. RFC
// 9110 also permits an HTTP-date, but this endpoint has only ever sent seconds,
// and a misparsed date yielding a huge backoff would silently park an account
// for hours. Negative and NaN values are ignored.
func retryAfterSeconds(raw string) *float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || !(v >= 0) {
		return nil
	}
	return &v
}

// FetchUsage fetches one account's usage. It never fails loudly: every failure
// is a sentinel ("MissingAuth", "http <status>", "network", "bad-response").
func (c *HTTPClient) FetchUsage(ctx context.Context, accessToken, accountID string) UsageFetch {
	if accessToken == "" || accountID == "" {
		return UsageFetch{Sentinel: SentinelMissingAuth}
	}
	data, err := c.getJSON(ctx, c.UsageURL, accessToken, accountID)
	if err != nil {
		var se *StatusError
		switch {
		case errors.As(err, &se):
			ra := retryAfterSeconds(se.RetryAfter)
			suffix := ""
			if ra != nil {
				suffix = fmt.Sprintf(", retry-after %.0fs", *ra)
			}
			debugf("Codex usage fetch failed: http-%d%s", se.Code, suffix)
			return UsageFetch{Sentinel: "http " + strconv.Itoa(se.Code), RetryAfterS: ra}
		case errors.Is(err, errBadResponse):
			debugf("Codex usage fetch failed: bad-response")
			return UsageFetch{Sentinel: SentinelBadResponse}
		default:
			debugf("Codex usage fetch failed: network (%s)", transportKind(err))
			return UsageFetch{Sentinel: SentinelNetwork}
		}
	}
	usage := BuildUsageResult(data)
	if usage == nil {
		debugf("Codex usage fetch failed: bad-response (no rate-limit window)")
		return UsageFetch{Sentinel: SentinelBadResponse}
	}
	return UsageFetch{Usage: usage}
}
