package web

import (
	"encoding/json"
	"testing"
	"time"
)

// The caption says in one line what auto-switch counts and why, for each
// way the count comes about (DESIGN A91).
func TestCountingCaption(t *testing.T) {
	for name, tc := range map[string]struct {
		in   CountingView
		want string
	}{
		"explicit-model": {CountingView{Mode: "setting", Setting: "Fable", Models: []string{"Fable"}},
			"counting 5h + 7d + Fable (autoswitch.model)"},
		"explicit-all": {CountingView{Mode: "setting", Setting: "all", Models: []string{"all"}},
			"counting 5h + 7d + every model window (autoswitch.model)"},
		"sessions-fable-counted": {CountingView{Mode: "sessions", Models: []string{"Fable"}, Sessions: []string{"fable", "opus"}},
			"counting 5h + 7d + Fable (live Fable sessions)"},
		"sessions-fable-unservable": {CountingView{Mode: "sessions", Models: []string{}, Sessions: []string{"fable"}, Unservable: []string{"Fable"}},
			"counting 5h + 7d; Fable not counted: no usable account can serve it"},
		"sessions-no-fable-session": {CountingView{Mode: "sessions", Models: []string{}, Sessions: []string{"opus"}, Reserved: []string{"3", "11"}},
			"counting 5h + 7d; no live Fable session — accounts with Fable room reserved: 3, 11"},
		"sessions-none-reported": {CountingView{Mode: "sessions", Models: []string{}},
			"counting 5h + 7d; no live session has reported its model"},
		"hosted": {CountingView{Mode: "hosted", Models: []string{}},
			"counting 5h + 7d (the engine hosted here does not follow sessions)"},
	} {
		if got := countingCaption(tc.in); got != tc.want {
			t.Errorf("%s: caption %q, want %q", name, got, tc.want)
		}
	}
}

// The state document carries the engine host's count with its caption, and
// the last tick of a scheduler that runs the ticks elsewhere.
func TestStateCarriesCountingAndLastTick(t *testing.T) {
	h := newHarness(t)
	at := 1758276100.0
	h.auto.mu.Lock()
	h.auto.view.Counting = &CountingView{Mode: "sessions", Models: []string{}, Sessions: []string{"opus"}, Unservable: []string{}, Reserved: []string{"2"}}
	h.auto.view.LastTickAt = &at
	h.auto.mu.Unlock()
	var st struct {
		Auto struct {
			LastTickAt *float64 `json:"lastTickAt"`
			Counting   struct {
				Mode     string   `json:"mode"`
				Reserved []string `json:"reserved"`
				Caption  string   `json:"caption"`
			} `json:"counting"`
		} `json:"auto"`
	}
	if err := json.Unmarshal(readBody(t, h.get("/api/state")), &st); err != nil {
		t.Fatal(err)
	}
	c := st.Auto.Counting
	if c.Mode != "sessions" || len(c.Reserved) != 1 || c.Caption != "counting 5h + 7d; no live Fable session — accounts with Fable room reserved: 2" {
		t.Fatalf("counting = %+v", c)
	}
	if st.Auto.LastTickAt == nil || *st.Auto.LastTickAt != at {
		t.Fatalf("lastTickAt = %v, want %v", st.Auto.LastTickAt, at)
	}
}

// The dashboard's Claude rows carry usage read by the engine's room rule, so
// the Auto tab's Next best ranks a renewed account as the engine does: a
// window whose reset has passed reads 0 % with no reset time (DESIGN A91).
func TestStateRowsReadRenewedWindows(t *testing.T) {
	h := newHarness(t)
	past := testNow.Add(-time.Minute).UTC().Format(time.RFC3339)
	snap := sampleSnapshot()
	snap.Accounts[0].Usage.LastGood = map[string]any{
		"five_hour": map[string]any{"pct": 100.0, "resets_at": past, "countdown": "0m", "clock": "09:59"},
		"seven_day": map[string]any{"pct": 30.0, "resets_at": "2026-09-22T00:00:00Z"},
	}
	h.fa.mu.Lock()
	h.fa.snap = snap
	h.fa.mu.Unlock()
	var st struct {
		Accounts []struct {
			Usage struct {
				FiveHour map[string]any `json:"fiveHour"`
				SevenDay map[string]any `json:"sevenDay"`
			} `json:"usage"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(readBody(t, h.get("/api/state")), &st); err != nil {
		t.Fatal(err)
	}
	five, week := st.Accounts[0].Usage.FiveHour, st.Accounts[0].Usage.SevenDay
	if five["pct"] != 0.0 || five["resetsAt"] != nil || five["countdown"] != nil || week["pct"] != 30.0 || week["resetsAt"] != "2026-09-22T00:00:00Z" {
		t.Fatalf("row 1: 5h %v, 7d %v; want 5h renewed (0 %%, no reset), 7d as measured", five, week)
	}
}
