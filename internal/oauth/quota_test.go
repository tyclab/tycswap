package oauth

import (
	"math"
	"testing"
)

func TestSharedQuotaOptionalSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		five any
		want bool
	}{
		{"absent", nil, true},
		{"reported", map[string]any{"pct": 40.0}, true},
		{"exhausted", map[string]any{"pct": 100.0}, true},
		{"empty", map[string]any{}, false},
		{"null-utilization", map[string]any{"pct": nil}, false},
		{"invalid", map[string]any{"pct": "unknown"}, false},
		{"nan", map[string]any{"pct": math.NaN()}, false},
		{"infinite", map[string]any{"pct": math.Inf(1)}, false},
		{"negative", map[string]any{"pct": -1.0}, false},
		{"non-window", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{"seven_day": map[string]any{"pct": 25.0}}
			if tc.five != nil {
				data["five_hour"] = tc.five
			}
			if got := HasSharedQuota(data); got != tc.want {
				t.Fatalf("HasSharedQuota = %v, want %v", got, tc.want)
			}
			data["seven_day"] = nil
			if HasSharedQuota(data) {
				t.Fatal("missing week accepted")
			}
		})
	}
	if HasSharedQuota(nil) || HasSharedQuota(map[string]any{"seven_day": map[string]any{"pct": -1.0}}) {
		t.Fatal("unknown or invalid week accepted")
	}
}

func TestUltraUsageNormalization(t *testing.T) {
	for _, raw := range []string{
		`{"seven_day":{"utilization":25},"five_hour":null}`,
		`{"seven_day":{"utilization":25}}`,
	} {
		data := buildUsageResult(rawJSON(t, raw), normNow)
		u := NewUsage(data)
		if !HasSharedQuota(data) || u.FiveHour != nil || u.SevenDay.Pct != 25 {
			t.Fatalf("weekly-only response = %#v", data)
		}
		if windows := RelevantWindows(u, nil); len(windows) != 1 || windows[0].Label != SevenDayLabel {
			t.Fatalf("unexpected windows: %#v", windows)
		}
		if h := AccountHeadroom(u, nil); h == nil || *h != 75 {
			t.Fatalf("headroom = %v", h)
		}
	}
	for _, value := range []any{false, "unknown", 0, map[string]any{}, map[string]any{"utilization": nil}} {
		data := buildUsageResult(map[string]any{"five_hour": value, "seven_day": map[string]any{"utilization": 25.0}}, normNow)
		if HasSharedQuota(data) {
			t.Fatalf("malformed session window %v normalized as absent: %#v", value, data)
		}
	}
}
