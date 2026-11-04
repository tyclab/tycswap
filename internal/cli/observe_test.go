package cli

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestObserveNearBar(t *testing.T) {
	s := settings.Default()
	s.FiveHourThreshold = 96 // the operator's bar; 7d keeps 97: the margin starts at 81 and 82
	for name, tc := range map[string]struct {
		raw  string
		want bool
	}{
		"5h-below-margin":   {`{"rate_limits":{"five_hour":{"used_percentage":80.9,"resets_at":1},"seven_day":{"used_percentage":20,"resets_at":2}}}`, false},
		"5h-in-margin":      {`{"rate_limits":{"five_hour":{"used_percentage":81,"resets_at":1},"seven_day":{"used_percentage":20,"resets_at":2}}}`, true},
		"7d-in-margin":      {`{"rate_limits":{"five_hour":{"used_percentage":10,"resets_at":1},"seven_day":{"used_percentage":82,"resets_at":2}}}`, true},
		"over-the-limit":    {`{"rate_limits":{"five_hour":{"used_percentage":100,"resets_at":1}}}`, true},
		"ultra-week-near":   {`{"rate_limits":{"seven_day":{"used_percentage":85,"resets_at":2}}}`, true},
		"ultra-week-low":    {`{"rate_limits":{"seven_day":{"used_percentage":50,"resets_at":2}}}`, false},
		"spend-limit-only":  {`{"rate_limits":{"spend_limit":{"used_percentage":99,"resets_at":3}}}`, false},
		"before-a-response": {`{"model":{"id":"claude-opus-5-5"}}`, false},
		"not-json":          {`five_hour 99`, false},
		"text-percentage":   {`{"rate_limits":{"five_hour":{"used_percentage":"99"}}}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := observedNearBar([]byte(tc.raw), s); got != tc.want {
				t.Fatalf("near = %v, want %v", got, tc.want)
			}
		})
	}
	lower := settings.Default()
	lower.FiveHourThreshold = 80
	if !observedNearBar([]byte(`{"rate_limits":{"five_hour":{"used_percentage":66}}}`), lower) {
		t.Fatal("the margin does not follow the configured 5h bar")
	}
}

// At the 2026-11-04 burn (58 points in at most 356 s) one fetch per 15 s
// leaves at most 2.5 points between readings.
func TestObserveSlotThrottles(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 11, 4, 10, 15, 0, 0, time.UTC)
	for i, step := range []struct {
		at   time.Duration
		want bool
	}{{0, true}, {10 * time.Second, false}, {16 * time.Second, true}, {20 * time.Second, false}} {
		if got := claimObserveSlot(dir, t0.Add(step.at)); got != step.want {
			t.Fatalf("claim %d at +%v = %v, want %v", i, step.at, got, step.want)
		}
	}
}

func TestObservedGroupFollowsTheSessionProfile(t *testing.T) {
	root := t.TempDir()
	for name, tc := range map[string]struct {
		dir  string
		want groups.ID
	}{
		"default":     {"", ""},
		"opus":        {groups.ProfileDir(root, groups.Opus), groups.Opus},
		"fable-slash": {groups.ProfileDir(root, groups.Fable) + "/", groups.Fable},
		"other":       {filepath.Join(root, "elsewhere"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			testutil.Setenv(t, "CLAUDE_CONFIG_DIR", tc.dir)
			if got := observedGroup(root); got != tc.want {
				t.Fatalf("group = %q, want %q", got, tc.want)
			}
		})
	}
}
