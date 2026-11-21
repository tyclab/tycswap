// Tests for the at-limit fold-in (DESIGN A15): the pure atLimitFor computation
// (models unset / named / "all"; nil headroom = unknown → not at-limit; the
// exactly-100 boundary; multiple simultaneous limiting windows), the additive
// JSON key omission-when-false on list rows and the status active object, the
// human "at limit" marker, and the AccountSnapshot fields.
package reporting

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

// writeSettings writes a raw settings.json under the backup root (full control
// over which autoswitch keys are present).
func writeSettings(t *testing.T, s *store.Store, body string) {
	t.Helper()
	if err := os.WriteFile(settings.SettingsPath(s.BackupDir()), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAtLimitFor_Table(t *testing.T) {
	cases := []struct {
		name         string
		decision     any
		models       []string
		wantAtLimit  bool
		wantLimiting []string
	}{
		{
			name:        "models_unset_7d_at_100",
			decision:    map[string]any{"five_hour": map[string]any{"pct": 10.0}, "seven_day": map[string]any{"pct": 100.0}},
			wantAtLimit: true, wantLimiting: []string{"7d"},
		},
		{
			name:        "models_unset_under_limit",
			decision:    map[string]any{"five_hour": map[string]any{"pct": 10.0}, "seven_day": map[string]any{"pct": 99.0}},
			wantAtLimit: false,
		},
		{
			name:        "exactly_100_boundary_binds",
			decision:    map[string]any{"five_hour": map[string]any{"pct": 100.0}, "seven_day": map[string]any{"pct": 20.0}},
			wantAtLimit: true, wantLimiting: []string{"5h"},
		},
		{
			// The named weekly window is exhausted while 5h/7d have room:
			// unlisted → not at-limit; listed → at-limit on that window alone.
			name:        "named_model_unlisted_not_at_limit",
			decision:    map[string]any{"five_hour": map[string]any{"pct": 10.0}, "seven_day": map[string]any{"pct": 20.0}, "scoped": []any{map[string]any{"name": "Fable 5", "pct": 100.0}}},
			models:      nil,
			wantAtLimit: false,
		},
		{
			name:        "named_model_listed_at_limit",
			decision:    map[string]any{"five_hour": map[string]any{"pct": 10.0}, "seven_day": map[string]any{"pct": 20.0}, "scoped": []any{map[string]any{"name": "Fable 5", "pct": 100.0}}},
			models:      []string{"Fable 5"},
			wantAtLimit: true, wantLimiting: []string{"Fable 5"},
		},
		{
			name:        "all_sentinel_folds_scoped",
			decision:    map[string]any{"five_hour": map[string]any{"pct": 10.0}, "seven_day": map[string]any{"pct": 20.0}, "scoped": []any{map[string]any{"name": "Fable 5", "pct": 100.0}}},
			models:      []string{"all"},
			wantAtLimit: true, wantLimiting: []string{"Fable 5"},
		},
		{
			name:        "multiple_simultaneous_limiting_windows",
			decision:    map[string]any{"five_hour": map[string]any{"pct": 100.0}, "seven_day": map[string]any{"pct": 100.0}, "scoped": []any{map[string]any{"name": "Fable 5", "pct": 100.0}}},
			models:      []string{"all"},
			wantAtLimit: true, wantLimiting: []string{"5h", "7d", "Fable 5"},
		},
		{
			// Only windows >= 100 are limiting even when headroom binds elsewhere.
			name:        "only_over_windows_listed",
			decision:    map[string]any{"five_hour": map[string]any{"pct": 50.0}, "seven_day": map[string]any{"pct": 100.0}},
			wantAtLimit: true, wantLimiting: []string{"7d"},
		},
		{
			// Spend-only usage → no gating window → headroom nil → unknown, NOT
			// at-limit even though spend is exhausted.
			name:        "nil_headroom_unknown_not_at_limit",
			decision:    map[string]any{"spend": map[string]any{"used": 5000.0, "limit": 5000.0, "pct": 100.0, "currency": "USD"}},
			wantAtLimit: false,
		},
		{
			name:        "sentinel_string_never_at_limit",
			decision:    jsonout.UsageTokenExpired,
			wantAtLimit: false,
		},
		{
			name:        "nil_decision_never_at_limit",
			decision:    nil,
			wantAtLimit: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			atLimit, limiting := atLimitFor(tc.decision, tc.models, time.Time{})
			if atLimit != tc.wantAtLimit {
				t.Fatalf("atLimit = %v, want %v", atLimit, tc.wantAtLimit)
			}
			if !equalStrings(limiting, tc.wantLimiting) {
				t.Errorf("limitingWindows = %v, want %v", limiting, tc.wantLimiting)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// seedTwoAccounts writes two credentialed managed accounts (1 at-limit on 7d, 2
// under limit) plus fresh served usage. Returns the store.
func seedAtLimitStore(t *testing.T, clk clock.Clock) *store.Store {
	t.Helper()
	now := clock.Seconds(clk)
	s := newStore(t, clk, nil) // no client → served from store, never fetched
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 1, "lastUpdated": "x", "sequence": [1, 2],
  "accounts": {
    "1": {"email": "alice@example.com", "organizationUuid": "", "organizationName": ""},
    "2": {"email": "bob@example.com", "organizationUuid": "", "organizationName": ""}
  }
}`)
	writeLiveConfig(t, s, "alice@example.com", "")
	writeActiveCreds(t, s, oauthCreds("at1", "rt1", 0))
	writeBackup(t, s, "2", "bob@example.com", oauthCreds("at2", "rt2", 0), `{"oauthAccount":{"emailAddress":"bob@example.com"}}`)
	writeUsageRows(t, s, map[string]any{
		"1": map[string]any{
			"email": "alice@example.com", "organizationUuid": "",
			"lastGood":  map[string]any{"five_hour": map[string]any{"pct": 10.0}, "seven_day": map[string]any{"pct": 100.0}},
			"fetchedAt": now - 5,
		},
		"2": map[string]any{
			"email": "bob@example.com", "organizationUuid": "",
			"lastGood":  map[string]any{"five_hour": map[string]any{"pct": 10.0}, "seven_day": map[string]any{"pct": 40.0}},
			"fetchedAt": now - 5,
		},
	})
	return s
}

func TestListPayload_AtLimitAdditiveAndOmitted(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	s := seedAtLimitStore(t, clk)

	got, err := ListAccounts(s, false, true, nil)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	rows := got.(map[string]any)["accounts"].([]any)
	row1 := rows[0].(map[string]any)
	row2 := rows[1].(map[string]any)

	// At-limit row carries both additive keys; usageStatus is untouched.
	if row1["usageStatus"] != "ok" {
		t.Errorf("row1 usageStatus = %v, want ok (unchanged)", row1["usageStatus"])
	}
	if row1["atLimit"] != true {
		t.Errorf("row1 atLimit = %v, want true", row1["atLimit"])
	}
	if !jsonEqual(t, row1["limitingWindows"], []any{"7d"}) {
		t.Errorf("row1 limitingWindows = %v, want [7d]", row1["limitingWindows"])
	}

	// Under-limit row OMITS both keys entirely.
	if _, ok := row2["atLimit"]; ok {
		t.Errorf("row2 must omit atLimit, got %v", row2["atLimit"])
	}
	if _, ok := row2["limitingWindows"]; ok {
		t.Errorf("row2 must omit limitingWindows, got %v", row2["limitingWindows"])
	}
}

func TestStatusPayload_AtLimitAdditive(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	s := seedAtLimitStore(t, clk) // account 1 (alice) is active and at-limit

	got := buildStatusPayload(s)
	active := got["active"].(map[string]any)
	if active["usageStatus"] != "ok" {
		t.Errorf("usageStatus = %v, want ok (unchanged)", active["usageStatus"])
	}
	if active["atLimit"] != true {
		t.Errorf("active atLimit = %v, want true", active["atLimit"])
	}
	if !jsonEqual(t, active["limitingWindows"], []any{"7d"}) {
		t.Errorf("active limitingWindows = %v, want [7d]", active["limitingWindows"])
	}
}

func TestStatusPayload_AtLimitOmittedWhenUnderLimit(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	now := clock.Seconds(clk)
	s := newStore(t, clk, nil)
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 1, "lastUpdated": "x", "sequence": [1],
  "accounts": {"1": {"email": "alice@example.com", "organizationUuid": "", "organizationName": ""}}
}`)
	writeLiveConfig(t, s, "alice@example.com", "")
	writeActiveCreds(t, s, oauthCreds("at1", "rt1", 0))
	writeUsageRows(t, s, map[string]any{
		"1": map[string]any{
			"email": "alice@example.com", "organizationUuid": "",
			"lastGood":  map[string]any{"seven_day": map[string]any{"pct": 40.0}},
			"fetchedAt": now - 5,
		},
	})
	active := buildStatusPayload(s)["active"].(map[string]any)
	if _, ok := active["atLimit"]; ok {
		t.Errorf("under-limit active must omit atLimit, got %v", active["atLimit"])
	}
	if _, ok := active["limitingWindows"]; ok {
		t.Errorf("under-limit active must omit limitingWindows, got %v", active["limitingWindows"])
	}
}

func TestListPayload_AtLimitIgnoresTheRetiredModelKey(t *testing.T) {
	// A scoped weekly window at 100% reads as at-limit only while the
	// default-session policy counts it (TestAtLimitUsesDerivedPolicy); a file
	// that still holds autoswitch.model counts nothing (DESIGN A97).
	newScopedStore := func(t *testing.T, clk clock.Clock) *store.Store {
		now := clock.Seconds(clk)
		s := newStore(t, clk, nil)
		writeSequenceRaw(t, s, `{
  "activeAccountNumber": null, "lastUpdated": "x", "sequence": [1],
  "accounts": {"1": {"email": "alice@example.com", "organizationUuid": "", "organizationName": ""}}
}`)
		writeBackup(t, s, "1", "alice@example.com", oauthCreds("at1", "rt1", 0), `{"oauthAccount":{"emailAddress":"alice@example.com"}}`)
		writeUsageRows(t, s, map[string]any{
			"1": map[string]any{
				"email": "alice@example.com", "organizationUuid": "",
				"lastGood":  map[string]any{"five_hour": map[string]any{"pct": 10.0}, "seven_day": map[string]any{"pct": 20.0}, "scoped": []any{map[string]any{"name": "Fable 5", "pct": 100.0}}},
				"fetchedAt": now - 5,
			},
		})
		return s
	}
	rowOf := func(t *testing.T, s *store.Store) map[string]any {
		got, err := ListAccounts(s, false, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got.(map[string]any)["accounts"].([]any)[0].(map[string]any)
	}

	t.Run("no_model_setting_not_at_limit", func(t *testing.T) {
		clk := testutil.FixedClock(t, fixedNow)
		row := rowOf(t, newScopedStore(t, clk))
		if _, ok := row["atLimit"]; ok {
			t.Errorf("scoped window must not gate without a configured model, got %v", row["atLimit"])
		}
	})
	for _, stored := range []string{"Fable 5", "all"} {
		t.Run("retired_model_key_"+stored, func(t *testing.T) {
			clk := testutil.FixedClock(t, fixedNow)
			s := newScopedStore(t, clk)
			writeSettings(t, s, `{"autoswitch": {"model": "`+stored+`"}}`)
			if row := rowOf(t, s); row["atLimit"] != nil {
				t.Errorf("a stored autoswitch.model %q gated the row: atLimit %v on %v", stored, row["atLimit"], row["limitingWindows"])
			}
		})
	}
}

func TestRenderAccounts_AtLimitMarker(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	s := seedAtLimitStore(t, clk)
	infos := BuildAccountsInfo(s)
	entries := CollectUsageEntries(s, infos, nil)
	var buf bytes.Buffer
	renderAccounts(&buf, s, infos, entries, false)
	out := buf.String()

	// Alice (account 1) is at-limit on 7d; the marker rides her identity line.
	line1 := lineContaining(out, "alice@example.com")
	if !strings.Contains(line1, "at limit: 7d") {
		t.Errorf("alice row missing at-limit marker:\n%s", line1)
	}
	// Bob (account 2) is under limit → no marker on his line.
	line2 := lineContaining(out, "bob@example.com")
	if strings.Contains(line2, "at limit") {
		t.Errorf("bob row must not show at-limit marker:\n%s", line2)
	}
}

func TestRenderStatus_AtLimitMarker(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	s := seedAtLimitStore(t, clk) // alice active + at-limit
	var buf bytes.Buffer
	renderStatus(&buf, s)
	out := buf.String()
	if !strings.Contains(out, "at limit: 7d") {
		t.Errorf("status missing at-limit marker:\n%s", out)
	}
}

func TestSnapshot_AtLimitFields(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	s := seedAtLimitStore(t, clk)
	snap := Snapshot(s, nil)
	byNum := map[string]AccountSnapshot{}
	for _, a := range snap.Accounts {
		byNum[a.Number] = a
	}
	if a := byNum["1"]; !a.AtLimit || !equalStrings(a.LimitingWindows, []string{"7d"}) {
		t.Errorf("account 1 snapshot AtLimit=%v LimitingWindows=%v, want true [7d]", a.AtLimit, a.LimitingWindows)
	}
	if a := byNum["2"]; a.AtLimit {
		t.Errorf("account 2 snapshot must not be at-limit: %+v", a.LimitingWindows)
	}
}

// lineContaining returns the first line of out that contains sub, or "".
func lineContaining(out, sub string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}

// `list` marks an account at its limit on
// the windows the default-session policy counts: Fable while some usable
// account can serve it, not while none can (DESIGN A91).
func TestAtLimitUsesDerivedPolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		reset time.Duration
		want4 []string
	}{
		"fable-servable":   {reset: time.Minute, want4: []string{"Fable"}},
		"fable-unservable": {},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := incident{live: "3", sessions: fableSession, fiveHourReset: tc.reset}.store(t)
			got, err := ListAccounts(s, false, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			rows := map[string]map[string]any{}
			for _, raw := range got.(map[string]any)["accounts"].([]any) {
				row := raw.(map[string]any)
				rows[fmt.Sprint(row["number"])] = row
			}
			if tc.want4 == nil {
				if _, ok := rows["4"]["atLimit"]; ok {
					t.Fatalf("Account-4 marked at limit on %v; Fable is not counted", rows["4"]["limitingWindows"])
				}
			} else if rows["4"]["atLimit"] != true || !jsonEqual(t, rows["4"]["limitingWindows"], tc.want4) {
				t.Fatalf("Account-4 atLimit = %v on %v, want %v", rows["4"]["atLimit"], rows["4"]["limitingWindows"], tc.want4)
			}
			var out bytes.Buffer
			renderAccounts(&out, s, BuildAccountsInfo(s), CollectUsageEntries(s, BuildAccountsInfo(s), map[string]bool{}), false)
			if marked := strings.Contains(lineContaining(out.String(), "acct4@example.com"), "at limit: Fable"); marked != (tc.want4 != nil) {
				t.Fatalf("human list marks Account-4 at its Fable limit = %v, want %v:\n%s", marked, tc.want4 != nil, out.String())
			}
		})
	}
}

func TestSharedMythosMarksTheWindowThatMetersEachAccount(t *testing.T) {
	five := 10.0
	s, now := incident{live: "7", sessions: mythosSession, five11: &five}.store(t)
	markMythos("4", "7", "11")(t, s)
	row4 := usageRow(t, s, "4")
	usage4 := row4["lastGood"].(map[string]any)
	usage4["scoped"] = append(usage4["scoped"].([]any), map[string]any{"name": "Mythos", "pct": 10.0})
	// 4 and 7 have spent Fable. Only 4 has its own Mythos window; 11 still has Fable room.
	writeUsageRows(t, s, map[string]any{"4": row4, "7": usageRow(t, s, "7"), "11": usageRow(t, s, "11")})
	payload, err := ListAccounts(s, false, true, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	rows := listRows(t, payload.(map[string]any))
	if rows["4"]["atLimit"] != nil || rows["7"]["atLimit"] != true || !jsonEqual(t, rows["7"]["limitingWindows"], []string{"Fable"}) {
		t.Errorf("list: 4 limited by %v, 7 limited by %v", rows["4"]["limitingWindows"], rows["7"]["limitingWindows"])
	}
	active := buildStatusPayload(s)["active"].(map[string]any)
	if active["atLimit"] != true || !jsonEqual(t, active["limitingWindows"], []string{"Fable"}) {
		t.Errorf("status: active account limited by %v", active["limitingWindows"])
	}
	values := map[string]any{}
	for _, a := range Snapshot(s, map[string]bool{}).Accounts {
		values[a.Number] = a.Usage.DecisionValue()
		if a.AtLimit != (a.Number == "7") {
			t.Errorf("snapshot: account %s atLimit = %v on %v", a.Number, a.AtLimit, a.LimitingWindows)
		}
	}
	counted := CountedForSwitch(s)
	ranking := autoswitch.RankTargets([]string{"4", "7", "11"}, values, counted.Settings, counted.Models, counted.Reserved, now)
	if !equalStrings(ranking, []string{"4", "11"}) {
		t.Errorf("ranked %v, want 4 (Mythos room) then 11 (Fable room, reserved)", ranking)
	}
}

// Guard: one list or status command runs the default-session policy once,
// whichever output it prints (its process scan and account view are not
// free).
func TestListAndStatusRunThePolicyOnce(t *testing.T) {
	s, _ := incident{live: "3", sessions: fableSession}.store(t)
	runs := 0
	prev := runDefaultPolicy
	runDefaultPolicy = func(s *store.Store, base settings.AutoSwitchSettings, now time.Time) PolicyResult {
		runs++
		return prev(s, base, now)
	}
	t.Cleanup(func() { runDefaultPolicy = prev })
	for name, run := range map[string]func() error{
		"list --json":   func() error { _, err := ListAccounts(s, false, true, nil); return err },
		"list":          func() error { _, err := ListAccounts(s, false, false, nil); return err },
		"status --json": func() error { _, err := Status(s, true); return err },
		"status":        func() error { _, err := Status(s, false); return err },
	} {
		runs = 0
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if runs != 1 {
			t.Errorf("%s ran the policy %d times, want once", name, runs)
		}
	}
}

// The at-limit marks read a cached window whose reset has passed as renewed,
// as the engine does: 11's 5h window at 100 % reset a minute ago, 3's resets
// in two hours. list (JSON and human), status and the account snapshot agree
// (DESIGN A91).
func TestAtLimitReadsRenewedWindows(t *testing.T) {
	for _, live := range []string{"3", "11"} {
		t.Run("live-"+live, func(t *testing.T) {
			s, _ := incident{live: live, fiveHourReset: time.Minute}.store(t)
			got, err := ListAccounts(s, false, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			rows := map[string]map[string]any{}
			for _, raw := range got.(map[string]any)["accounts"].([]any) {
				row := raw.(map[string]any)
				rows[fmt.Sprint(row["number"])] = row
			}
			if _, ok := rows["11"]["atLimit"]; ok || rows["3"]["atLimit"] != true {
				t.Fatalf("list --json atLimit: 11 = %v on %v, 3 = %v; want 11 renewed, 3 at its 5h limit", rows["11"]["atLimit"], rows["11"]["limitingWindows"], rows["3"]["atLimit"])
			}
			var out bytes.Buffer
			renderAccounts(&out, s, BuildAccountsInfo(s), CollectUsageEntries(s, BuildAccountsInfo(s), map[string]bool{}), false)
			if strings.Contains(lineContaining(out.String(), "acct11@example.com"), "at limit") || !strings.Contains(lineContaining(out.String(), "acct3@example.com"), "at limit: 5h") {
				t.Fatalf("human list marks:\n%s", out.String())
			}
			for _, a := range Snapshot(s, map[string]bool{}).Accounts {
				if want := a.Number == "3"; a.AtLimit != want {
					t.Fatalf("snapshot Account-%s AtLimit = %v on %v, want %v", a.Number, a.AtLimit, a.LimitingWindows, want)
				}
			}
			active := buildStatusPayload(s)["active"].(map[string]any)
			if _, at := active["atLimit"]; at != (live == "3") {
				t.Fatalf("status of live %s: atLimit = %v, want %v", live, active["atLimit"], live == "3")
			}
		})
	}
}
