package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
	"github.com/tyclab/tycswap/internal/web"
)

func writeMythosTestUsage(t *testing.T, sw *core.Switcher, num string, week, fable float64, weekReset, fableReset string) {
	t.Helper()
	window := func(pct float64, reset string) map[string]any {
		w := map[string]any{"pct": pct}
		if reset != "" {
			w["resets_at"] = reset
		}
		return w
	}
	scoped := window(fable, fableReset)
	scoped["name"] = "Fable"
	reading := map[string]any{"seven_day": window(week, weekReset), "scoped": []any{scoped}}
	if err := sw.Usage.Record(map[string]usage.FetchRecord{num: {Usage: reading}}, map[string]usage.Identity{num: {Email: fableEmail(num)}}); err != nil {
		t.Fatal(err)
	}
}

func TestMythosLimitFeedbackAcrossSurfaces(t *testing.T) {
	late := time.Date(2026, 11, 28, 9, 59, 0, 0, time.Local).UTC().Format(time.RFC3339)
	early := time.Date(2026, 11, 26, 8, 15, 0, 0, time.Local).UTC().Format(time.RFC3339)
	for _, tc := range []struct {
		name                  string
		week, fable           float64
		weekReset, fableReset string
		windows               []string
		returnAt, detail      string
	}{
		{"Fable spent", 53, 100, early, late, []string{"Fable"}, late, "at limit: Fable until Nov 28 09:59"},
		{"both spent, week resets last", 100, 100, late, early, []string{"7d", "Fable"}, late, "at limit: 7d, Fable until Nov 28 09:59"},
		{"one reset unknown", 100, 100, "", early, []string{"7d", "Fable"}, "", "at limit: 7d, Fable"},
		{"room", 53, 99, early, late, nil, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, _ := (fableTickFixture{def: "1", off: true}).build(t)
			selected, _ := addMythosTestLogins(t, sw)
			if err := store.SelectMythosLogin(sw.BackupDir(), selected); err != nil {
				t.Fatal(err)
			}
			writeMythosTestUsage(t, sw, "2", tc.week, tc.fable, tc.weekReset, tc.fableReset)
			checkJSON := func(source string, m map[string]any) {
				t.Helper()
				for _, k := range []string{"atLimit", "limitingWindows", "resetsAt"} {
					if _, ok := m[k]; !ok {
						t.Errorf("%s: missing %s in %v", source, k, m)
					}
				}
				if tc.returnAt == "" && m["resetsAt"] != nil {
					t.Errorf("%s: resetsAt = %v, want null", source, m["resetsAt"])
				}
				var summary struct {
					AtLimit         bool
					LimitingWindows []string
					ResetsAt        string
				}
				raw, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &summary); err != nil {
					t.Fatal(err)
				}
				if summary.AtLimit != (len(tc.windows) > 0) || !slices.Equal(summary.LimitingWindows, tc.windows) || summary.ResetsAt != tc.returnAt {
					t.Errorf("%s: atLimit=%v limitingWindows=%v resetsAt=%q", source, summary.AtLimit, summary.LimitingWindows, summary.ResetsAt)
				}
			}
			parts := []string{"counting 7d + Fable"}
			if tc.detail != "" {
				parts = append(parts, tc.detail)
			}
			parts = append(parts, "0 linked sessions")
			want := "Mythos login: Account-2 (" + fableEmail("2") + "), " + strings.Join(parts, ", ")
			for _, cmd := range []string{"list", "status"} {
				code, out, msg := runSub(t, cmd, "--json")
				if code != 0 {
					t.Fatalf("%s --json: %s", cmd, msg)
				}
				checkJSON(cmd, decodeJSON(t, out)["mythos"].(map[string]any))
				code, out, msg = runHuman(t, cmd)
				if code != 0 || !slices.Contains(strings.Split(out, "\n"), want) {
					t.Errorf("%s: missing %q in %s %s", cmd, want, out, msg)
				}
			}
			_, _, get := serveDashboardPage(t, sw, testOwner)
			var state web.State
			if err := json.Unmarshal([]byte(get("/api/state")), &state); err != nil {
				t.Fatal(err)
			}
			checkJSON("/api/state", state.Mythos)
			sh, _, _ := newTestShell(t)
			if got := sh.panel(state).MythosSummary; got != want {
				t.Errorf("tray: got %q, want %q", got, want)
			}
			view := renderPage(t, servedPage(get), []pageCase{{Name: "mythos", State: state}})["mythos"]
			separator := ", "
			if tc.detail == "" {
				separator = " · "
			}
			if wantDetail := strings.Join(parts, separator); view.MythosDetail != wantDetail {
				t.Errorf("dashboard: got %q, want %q", view.MythosDetail, wantDetail)
			}
		})
	}
}
