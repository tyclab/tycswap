package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	t.Run("fable-best", func(t *testing.T) {
		useSwitcher(t, countingStore(t, "claude-fable-5-1", strategyStates[0].usage))
		if _, out, _ := runHuman(t, "switch", "--strategy", "best"); !strings.Contains(out, "Counting 5h + 7d + Fable (live Fable sessions) · account order: soonest-reset\n") {
			t.Fatalf("stdout %q, want the caption and the account order on one line", out)
		}
	})
	t.Run("no-session", func(t *testing.T) {
		useSwitcher(t, countingStore(t, "", strategyStates[0].usage))
		_, out, _ := runHuman(t, "switch", "--strategy", "best")
		if strings.Contains(out, "Counting") || !strings.Contains(out, "Account order: soonest-reset\n") {
			t.Fatalf("stdout %q, want the account order and no counted set while only 5h and 7d count", out)
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

// rankRow is one account's cached usage for the ranking states: 5h, 7d and
// Fable percentages, the hours until its 7d and Fable windows renew, and
// unknown for an account with no reading at all.
type rankRow struct {
	five, week, fable, renews float64
	unknown                   bool
}

// rankingStore is countingStore with a renewal time of its own per account.
func rankingStore(t *testing.T, model string, rows map[string]rankRow) *core.Switcher {
	t.Helper()
	flat := map[string][3]float64{}
	for num, r := range rows {
		flat[num] = [3]float64{r.five, r.week, r.fable}
	}
	sw := countingStore(t, model, flat)
	now := time.Now()
	accounts := map[string]any{}
	for num, r := range rows {
		if r.unknown {
			continue
		}
		at := func(h float64) string {
			return now.Add(time.Duration(h * float64(time.Hour))).UTC().Format(time.RFC3339)
		}
		email := map[string]string{"1": "alice@example.com", "2": "bob@example.com", "5": "carol@example.com"}[num]
		accounts[num] = map[string]any{"email": email, "organizationUuid": "", "fetchedAt": float64(now.Unix()) - 30, "lastGood": map[string]any{
			"five_hour": map[string]any{"pct": r.five, "resets_at": at(3)},
			"seven_day": map[string]any{"pct": r.week, "resets_at": at(r.renews)},
			"scoped":    []any{map[string]any{"name": "Fable", "pct": r.fable, "resets_at": at(r.renews)}},
		}}
	}
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 2, "accounts": accounts})
	if err := os.WriteFile(filepath.Join(sw.BackupDir(), "cache", "usage.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return sw
}

// rankingStates: accounts 1 (live), 2 and 5 under both account orders. want
// is where Best lands per order ("" stays: already-best), page the page's
// best candidate when it differs, engine whether a scheduled tick moves (the
// live account at its 5-hour limit).
var rankingStates = []struct {
	name, model string
	rows        map[string]rankRow
	want, page  map[string]string
	engine      bool
}{
	{"renewal-against-room", "", map[string]rankRow{"1": {100, 30, 0, 72, false}, "2": {10, 20, 0, 120, false}, "5": {10, 40, 0, 24, false}},
		map[string]string{"best": "2", "soonest-reset": "5"}, nil, true},
	{"over-a-bar-not-at-limit", "", map[string]rankRow{"1": {100, 30, 0, 72, false}, "2": {90, 10, 0, 72, false}, "5": {10, 40, 0, 72, false}},
		map[string]string{"best": "2", "soonest-reset": "5"}, nil, true},
	{"reserved", "claude-opus-5-5", map[string]rankRow{"1": {100, 30, 100, 72, false}, "2": {10, 10, 30, 72, false}, "5": {10, 40, 100, 72, false}},
		map[string]string{"best": "5", "soonest-reset": "5"}, nil, true},
	{"fable-counted", "claude-fable-5-1", map[string]rankRow{"1": {100, 30, 20, 72, false}, "2": {10, 10, 100, 120, false}, "5": {10, 40, 10, 24, false}},
		map[string]string{"best": "5", "soonest-reset": "5"}, nil, true},
	{"fable-unservable", "claude-fable-5-1", map[string]rankRow{"1": {100, 30, 100, 72, false}, "2": {10, 20, 100, 120, false}, "5": {10, 40, 100, 24, false}},
		map[string]string{"best": "2", "soonest-reset": "5"}, nil, true},
	{"unknown-usage", "", map[string]rankRow{"1": {100, 30, 0, 72, false}, "2": {0, 0, 0, 0, true}, "5": {10, 40, 0, 72, false}},
		map[string]string{"best": "5", "soonest-reset": "5"}, nil, true},
	{"live-account-first", "", map[string]rankRow{"1": {10, 10, 0, 24, false}, "2": {10, 30, 0, 72, false}, "5": {10, 40, 0, 120, false}},
		map[string]string{"best": "", "soonest-reset": ""}, map[string]string{"best": "2", "soonest-reset": "2"}, false},
}

// For one state, under either account order, the account a switch by hand to
// the best account takes is the page's best candidate and, where a scheduled
// tick moves, the engine's target; one that ranks the live account first
// stays. Next available never lands on an account the page marks at limit
// (DESIGN A98, A99).
func TestSwitchNowAgreesWithThePage(t *testing.T) {
	var cases []pageCase
	var page pageFiles
	type result struct{ best, engine, next string }
	results := map[string]*result{}
	for _, tc := range rankingStates {
		for _, order := range []string{"best", "soonest-reset"} {
			name := tc.name + "/" + order
			results[name] = &result{}
			settingsBody := `{"schemaVersion":1,"autoswitch":{"enabled":true,"strategy":"` + order + `"}}`
			t.Run(name, func(t *testing.T) {
				sw := rankingStore(t, tc.model, tc.rows)
				writeSettings(t, sw, settingsBody)
				d, post, get := serveDashboardPage(t, sw, testOwner)
				page = servedPage(get)
				cases = append(cases, pageCase{name, d.srv.Snapshot()})
				useCodexSource(t, &fakeCodexSource{})
				_, out, _ := runCodex(t, "", "auto", "--once", "--dry-run", "--json")
				for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
					if ev := decodeJSON(t, line); ev["event"] == "switch" {
						to, _ := ev["to"].(map[string]any)
						results[name].engine = fmt.Sprint(to["number"])
					}
				}
				_, res := post("/api/switch", `{"strategy":"best"}`)
				results[name].best, _ = switchLanding(t, res)
			})
			t.Run(name+"/next-available", func(t *testing.T) {
				sw := rankingStore(t, tc.model, tc.rows)
				writeSettings(t, sw, settingsBody)
				_, post := serveDashboard(t, sw, testOwner)
				_, res := post("/api/switch", `{"strategy":"next-available"}`)
				results[name].next, _ = switchLanding(t, res)
			})
		}
	}
	views := renderPage(t, page, cases)
	for _, tc := range rankingStates {
		for _, order := range []string{"best", "soonest-reset"} {
			name := tc.name + "/" + order
			r, v := results[name], views[name]
			pageBest := "none"
			if v.Best != nil {
				pageBest = *v.Best
			}
			wantPage := tc.want[order]
			if p, ok := tc.page[order]; ok {
				wantPage = p
			}
			if r.best != tc.want[order] || pageBest != wantPage {
				t.Errorf("%s: Best landed on %q, the page's best candidate is %s; want %q and %s", name, r.best, pageBest, tc.want[order], wantPage)
			}
			if tc.engine && r.engine != tc.want[order] || !tc.engine && r.engine != "" {
				t.Errorf("%s: the engine moved to %q, want %q (moves: %v)", name, r.engine, tc.want[order], tc.engine)
			}
			for _, n := range v.AtLimit {
				if r.next == n {
					t.Errorf("%s: Next available landed on %s, which the page marks at limit (%v)", name, n, v.AtLimit)
				}
			}
		}
	}
}

var _ web.Facade = (*core.Switcher)(nil)
