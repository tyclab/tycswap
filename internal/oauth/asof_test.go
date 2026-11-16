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

// A renewed window's next reset is unknown (D4): AsOf drops its reset time
// and the fetch-time countdown, so nothing reads a renewal in the past; a
// window at 0 % whose reset passed loses it too. A running window keeps its
// own.
func TestAsOfDropsTheResetOfARenewedWindow(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	past, future := "2026-11-15T23:59:00Z", "2026-11-16T03:00:00Z"
	window := func(pct float64, reset string) map[string]any {
		return map[string]any{"pct": pct, "resets_at": reset, "countdown": "1m", "clock": "23:59"}
	}
	out := AsOf(map[string]any{
		"five_hour": window(100, past),
		"seven_day": window(0, past),
		"scoped":    []any{map[string]any{"name": "Fable", "pct": 100.0, "resets_at": past}, map[string]any{"name": "Opus", "pct": 40.0, "resets_at": future}},
	}, now)
	for _, w := range []map[string]any{out["five_hour"].(map[string]any), out["seven_day"].(map[string]any), out["scoped"].([]any)[0].(map[string]any)} {
		for _, key := range []string{"resets_at", "countdown", "clock"} {
			if _, kept := w[key]; kept {
				t.Errorf("renewed window %v keeps %s", w, key)
			}
		}
	}
	if opus := out["scoped"].([]any)[1].(map[string]any); opus["resets_at"] != future {
		t.Errorf("running window lost its reset: %v", opus)
	}
	if r := RenewalTS(NewUsage(AsOf(map[string]any{"seven_day": window(100, past)}, now)), nil); r != nil {
		t.Errorf("renewal of a renewed week = %v, want unknown", *r)
	}
}
