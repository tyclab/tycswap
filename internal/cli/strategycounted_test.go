package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/tui"
	"github.com/tyclab/tycswap/internal/web"
)

// strategyStates are the review's reproduction (DESIGN A98): accounts 1
// (live), 2 (most 5h and 7d room, Fable week spent) and 5 (Fable room), as
// 5h, 7d and Fable per account, with the live default session's model.
var strategyStates = []struct {
	name, model string
	usage       map[string][3]float64
	best        string // the account Best lands on; "" stays (already-best)
	next        string // the account Next available lands on
}{
	// The live account has less room than 5 under the counted Fable window.
	{"fable-live-worse", "claude-fable-5-1", map[string][3]float64{"1": {10, 70, 20}, "2": {10, 20, 100}, "5": {10, 60, 10}}, "5", "5"},
	// The review's figures: under the counted Fable window the live account
	// (50 % room) beats 5 (40 %), so Best stays.
	{"fable-live-best", "claude-fable-5-1", map[string][3]float64{"1": {10, 50, 20}, "2": {10, 20, 100}, "5": {10, 60, 10}}, "", "5"},
	// No usable account can serve Fable: it is not counted, the pick is the
	// 5h and 7d one.
	{"fable-unservable", "claude-fable-5-1", map[string][3]float64{"1": {10, 50, 100}, "2": {10, 20, 100}, "5": {10, 60, 100}}, "2", "2"},
	// No live session has reported a model: nothing but 5h and 7d counts.
	{"no-session", "", map[string][3]float64{"1": {10, 50, 20}, "2": {10, 20, 100}, "5": {10, 60, 10}}, "2", "2"},
}

// switchLanding is where a switch result put the login: the target's
// number, or "" for a result that switched nothing, with its reason.
func switchLanding(t *testing.T, res map[string]any) (string, string) {
	t.Helper()
	reason, _ := res["reason"].(string)
	to, _ := jsonout.SwitchedTo(res)
	return to, reason
}

func wantLanding(t *testing.T, where, strategy string, res map[string]any, want string) {
	t.Helper()
	got, reason := switchLanding(t, res)
	if got != want || want == "" && reason != "already-best" {
		t.Errorf("%s %s: landed on %q (%s, warnings %v), want %q", where, strategy, got, reason, res["warnings"], want)
	}
}

// `tycswap switch --strategy best|next-available` judges the accounts on the
// windows the default-session policy counts (DESIGN A98).
func TestSwitchStrategyCountsTheSessionsModels(t *testing.T) {
	for _, tc := range strategyStates {
		for _, strategy := range []string{"best", "next-available"} {
			t.Run(tc.name+"/"+strategy, func(t *testing.T) {
				sw := countingStore(t, tc.model, tc.usage)
				useSwitcher(t, sw)
				code, out, errb := runCLI(t, []string{"switch", "--strategy", strategy, "--json"}, false, false)
				var res map[string]any
				if err := json.Unmarshal([]byte(out), &res); err != nil || code != 0 {
					t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errb)
				}
				want := map[string]string{"best": tc.best, "next-available": tc.next}[strategy]
				wantLanding(t, "CLI", strategy, res, want)
				if strings.Contains(out, "models") || strings.Contains(out, "counting") {
					t.Errorf("--json carries the counted set: %s", out)
				}
			})
		}
	}
}

// In text mode one line names the counted set with the policy's caption,
// only while a model window counts; a skipped account names its spent window.
func TestSwitchStrategySaysWhatItCounts(t *testing.T) {
	t.Run("fable", func(t *testing.T) {
		useSwitcher(t, countingStore(t, "claude-fable-5-1", strategyStates[0].usage))
		_, out, _ := runHuman(t, "switch", "--strategy", "next-available")
		if !strings.Contains(out, "Counting 5h + 7d + Fable (live Fable sessions)\n") || !strings.Contains(out, "Skipping Account-2 (at Fable limit)") {
			t.Fatalf("stdout %q, want the caption line and the skip naming Fable", out)
		}
	})
	t.Run("no-session", func(t *testing.T) {
		useSwitcher(t, countingStore(t, "", strategyStates[0].usage))
		if _, out, _ := runHuman(t, "switch", "--strategy", "best"); strings.Contains(out, "Counting") {
			t.Fatalf("stdout %q names a counted set while only 5h and 7d count", out)
		}
	})
}

// The dashboard's Switch now (POST /api/switch) and the TUI's best-switch
// (tui.Facade.Switch) take the same pick, and a models field in the request
// changes nothing.
func TestSwitchNowAndTheTUICountTheSessionsModels(t *testing.T) {
	for _, tc := range strategyStates {
		for _, strategy := range []string{"best", "next-available"} {
			want := map[string]string{"best": tc.best, "next-available": tc.next}[strategy]
			t.Run(tc.name+"/"+strategy+"/dashboard", func(t *testing.T) {
				sw := countingStore(t, tc.model, tc.usage)
				_, post := serveDashboard(t, sw, testOwner)
				status, res := post("/api/switch", `{"strategy":"`+strategy+`","models":["all"]}`)
				if status != http.StatusOK {
					t.Fatalf("POST /api/switch = %d %v", status, res)
				}
				wantLanding(t, "POST /api/switch", strategy, res, want)
			})
			t.Run(tc.name+"/"+strategy+"/tui", func(t *testing.T) {
				var facade tui.Facade = countingStore(t, tc.model, tc.usage)
				s := strategy
				res, err := facade.Switch(&s, true)
				if err != nil {
					t.Fatal(err)
				}
				wantLanding(t, "TUI", strategy, res, want)
			})
		}
	}
}

// For one state the account Best switches to is the page's best candidate,
// and Next available never lands on an account the page marks at limit.
func TestSwitchNowAgreesWithThePage(t *testing.T) {
	var cases []pageCase
	var page pageFiles
	landed := map[string]map[string]string{}
	for _, tc := range strategyStates {
		landed[tc.name] = map[string]string{}
		for _, strategy := range []string{"best", "next-available"} {
			t.Run(tc.name+"/"+strategy, func(t *testing.T) {
				sw := countingStore(t, tc.model, tc.usage)
				writeSettings(t, sw, `{"schemaVersion":1,"autoswitch":{"enabled":true}}`)
				d, post, get := serveDashboardPage(t, sw, testOwner)
				page = servedPage(get)
				if strategy == "best" {
					cases = append(cases, pageCase{tc.name, d.srv.Snapshot()})
				}
				_, res := post("/api/switch", `{"strategy":"`+strategy+`"}`)
				landed[tc.name][strategy], _ = switchLanding(t, res)
			})
		}
	}
	views := renderPage(t, page, cases)
	for _, tc := range strategyStates {
		v := views[tc.name]
		page := "none"
		if v.Best != nil {
			page = *v.Best
		}
		if best := landed[tc.name]["best"]; best != "" && page != best {
			t.Errorf("%s: Best switched to %s, the page's best candidate is %s", tc.name, best, page)
		}
		for _, n := range v.AtLimit {
			if landed[tc.name]["next-available"] == n {
				t.Errorf("%s: Next available landed on %s, which the page marks at limit (%v)", tc.name, n, v.AtLimit)
			}
		}
	}
}

var _ web.Facade = (*core.Switcher)(nil)
