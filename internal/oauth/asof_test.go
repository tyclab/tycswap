package oauth

import (
	"reflect"
	"testing"
	"time"
)

// A window whose reset has passed reads 0 %; one still running, one without
// a readable reset and the caller's map are left as they are.
func TestAsOfRenewsOnlyWindowsWhoseResetPassed(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	past, future := "2026-11-15T23:59:00Z", "2026-11-16T03:00:00+00:00"
	in := map[string]any{
		"five_hour": map[string]any{"pct": 100.0, "resets_at": past},
		"seven_day": map[string]any{"pct": 80.0, "resets_at": future},
		"scoped": []any{
			map[string]any{"name": "Fable", "pct": 100.0, "resets_at": past},
			map[string]any{"name": "Opus", "pct": 40.0, "resets_at": "soon"},
		},
	}
	before := map[string]any{}
	for k, v := range in {
		before[k] = v
	}
	u := NewUsage(AsOf(in, now))
	if u.FiveHour.Pct != 0 || u.SevenDay.Pct != 80 || u.Scoped[0].Pct != 0 || u.Scoped[1].Pct != 40 {
		t.Fatalf("as of now: 5h %v, 7d %v, Fable %v, Opus %v; want 0, 80, 0, 40", u.FiveHour.Pct, u.SevenDay.Pct, u.Scoped[0].Pct, u.Scoped[1].Pct)
	}
	if !reflect.DeepEqual(in, before) || in["five_hour"].(map[string]any)["pct"] != 100.0 || in["scoped"].([]any)[0].(map[string]any)["pct"] != 100.0 {
		t.Fatalf("AsOf changed its argument: %v", in)
	}
	untouched := map[string]any{"seven_day": map[string]any{"pct": 80.0, "resets_at": future}}
	if out := AsOf(untouched, now); reflect.ValueOf(out).Pointer() != reflect.ValueOf(untouched).Pointer() {
		t.Fatal("a map with no renewed window was copied")
	}
	if AsOf(nil, now) != nil {
		t.Fatal("a nil map did not stay nil")
	}
}
