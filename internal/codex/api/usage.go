// usage.go maps the ChatGPT usage response onto tycswap's five_hour/seven_day window shape so Claude-side consumers need no branching.
// Wire shape read off the live endpoint (2026-08-16): windows nest under rate_limit, used_percent is pct, reset_at is epoch seconds
// (pace parses ISO). primary_window is not necessarily 5-hour: Plus reports 604800 s, so windows are classified by limit_window_seconds.
// An absent window stays absent, never 0% used.

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

const (
	// codex-auth's wording, so users of either tool read the other's output; an HTTP failure is "http <status>".
	SentinelMissingAuth = "MissingAuth"
	SentinelNetwork     = "network"
	SentinelBadResponse = "bad-response"
)

// RetryAfterS is the server's Retry-After in seconds; honouring it avoids being rate-limited harder.
type UsageFetch struct {
	Usage       map[string]any
	Sentinel    string
	RetryAfterS *float64
}

const maxISOEpoch = 253402300799

// "+00:00" (not "Z") keeps the string byte-identical to what claude-swap persists.
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
				slot = "five_hour"
			} else {
				slot = "seven_day"
			}
			if _, taken := result[slot]; !taken {
				result[slot] = w
			}
		}
	}

	// A plan-only map renders as a blank row with no explanation; that is how the first live run failed.
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

// Delta-seconds only: the endpoint only sends seconds, and a misparsed HTTP-date could park an account for hours.
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
