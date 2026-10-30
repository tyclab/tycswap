package statusline

import (
	"testing"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/usage"
)

func TestRemovedSessionLimitReplacesCachedWindow(t *testing.T) {
	cache := usage.NewStore(t.TempDir(), clock.NewFake(now))
	ids := map[string]usage.Identity{"1": {Email: "ultra@example.invalid"}}
	raw := map[string]any{
		"five_hour": map[string]any{"utilization": 100.0},
		"seven_day": map[string]any{"utilization": 25.0},
		"limits":    []any{map[string]any{"percent": 40.0, "scope": map[string]any{"model": map[string]any{"display_name": "Fable"}}}},
	}
	for _, removed := range []bool{false, true} {
		if removed {
			raw["five_hour"] = nil
		}
		if err := cache.Record(map[string]usage.FetchRecord{"1": {Usage: oauth.BuildUsageResult(raw)}}, ids); err != nil {
			t.Fatal(err)
		}
		entry := cache.Entries(ids)["1"]
		_, present := jsonout.UsageToJSON(entry.LastGood)["fiveHour"]
		if present == removed {
			t.Fatalf("JSON session window present = %v, removed = %v", present, removed)
		}
		doc := Build(Input{Now: now, Records: []Record{{Slot: 1, Email: ids["1"].Email}}, Usage: map[int]usage.UsageEntry{1: entry}})
		u := doc.Accounts[Key(ids["1"].Email, "")].Usage
		if u == nil || (u.FiveHour == nil) != removed || u.SevenDay.Pct != 25 || len(u.Scoped) != 1 || u.Scoped[0].Pct != 40 {
			t.Fatalf("statusline usage = %#v, removed = %v", u, removed)
		}
		h := oauth.AccountHeadroom(oauth.NewUsage(entry.LastGood), []string{"Fable"})
		want := 0.0
		if removed {
			want = 60
		}
		if h == nil || *h != want {
			t.Fatalf("headroom = %v, want %v", h, want)
		}
	}
}
