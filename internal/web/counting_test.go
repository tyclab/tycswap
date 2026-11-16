package web

import (
	"encoding/json"
	"testing"
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
