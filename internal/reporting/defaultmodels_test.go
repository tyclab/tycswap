package reporting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/usage"
)

func liveDefaultSession(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := groups.WriteJSON(filepath.Join(dir, "sessions", strconv.Itoa(os.Getpid())+".json"), map[string]any{"pid": os.Getpid(), "sessionId": id}); err != nil {
		t.Fatal(err)
	}
}

// The default login counts a model's limits only while one of its live
// sessions runs that model, as a group does (DESIGN A76).
func TestDefaultSessionSettingsFollowLiveSessions(t *testing.T) {
	strp := func(v string) *string { return &v }
	for name, tc := range map[string]struct {
		live, model string
		set         *string
		want        string
	}{
		"fable-session":      {"s-fable", "claude-fable-5-1", nil, "Fable"},
		"fable-1m":           {"s-fable", "claude-fable-5-1[1m]", nil, "Fable"},
		"opus-session":       {"s-opus", "claude-opus-5-5", nil, ""},
		"recorded-but-ended": {"s-other", "claude-fable-5-1", nil, ""},
		// A Model on the base decides nothing: the policy derives it (A97).
		"base-model-ignored": {"s-opus", "claude-opus-5-5", strp("all"), ""},
		"base-model-fable":   {"s-fable", "claude-fable-5-1", strp("Opus"), "Fable"},
	} {
		t.Run(name, func(t *testing.T) {
			clk := testutil.FixedClock(t, fixedNow)
			s := seedAtLimitStore(t, clk)
			now := clock.Seconds(clk)
			writeUsageRows(t, s, map[string]any{"1": map[string]any{
				"email": "alice@example.com", "organizationUuid": "", "fetchedAt": now - 60,
				"lastGood": map[string]any{"seven_day": map[string]any{"pct": 20.0},
					"scoped": []any{map[string]any{"name": "Fable", "pct": 30.0}}},
			}})
			liveDefaultSession(t, s.DefaultProfileDir(), tc.live)
			recorded := "s-fable"
			if tc.live == "s-opus" {
				recorded = "s-opus"
			}
			if _, err := RecordDefaultSessionModel(s.BackupDir(), recorded, tc.model, "", clk.Now()); err != nil {
				t.Fatal(err)
			}
			base := settings.Default()
			base.Model = tc.set
			got, _ := DefaultSessionPolicy(s, base)
			if gotModel := deref(got.Model); gotModel != tc.want {
				t.Fatalf("model = %q, want %q", gotModel, tc.want)
			}
		})
	}
}

// One write a minute per session at most: the status line runs every few
// seconds, while a model change is written at once.
func TestRecordDefaultSessionModelBoundsItsWrites(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC)
	path := filepath.Join(root, defaultModelsFile)
	mtime := func() time.Time {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.ModTime()
	}
	if _, err := RecordDefaultSessionModel(root, "s1", "claude-opus-5-5", "", t0); err != nil {
		t.Fatal(err)
	}
	first := mtime()
	time.Sleep(20 * time.Millisecond)
	if _, err := RecordDefaultSessionModel(root, "s1", "claude-opus-5-5", "", t0.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !mtime().Equal(first) {
		t.Fatal("an unchanged model was rewritten within a minute")
	}
	if _, err := RecordDefaultSessionModel(root, "s1", "claude-fable-5-1", "", t0.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if mtime().Equal(first) {
		t.Fatal("a model change was not written")
	}
	if _, err := RecordDefaultSessionModel(root, "s1", "not a model", "", t0.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := readDefaultSessionModels(root)["s1"].Family; got != "fable" {
		t.Fatalf("family = %q after an unknown model, want fable", got)
	}
}

// A session's first record and a change of family are news, which brings a
// tick forward; the minute's rewrite of an unchanged model is not (DESIGN A77).
func TestRecordDefaultSessionModelReportsNews(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC)
	for _, step := range []struct {
		session, model string
		at             time.Duration
		want           bool
	}{
		{"s1", "claude-opus-5-5", 0, true},
		{"s1", "claude-opus-5-5", 10 * time.Second, false},
		{"s1", "claude-opus-5-5", 2 * time.Minute, false},
		{"s2", "claude-opus-5-5", 2 * time.Minute, true},
		{"s1", "claude-fable-5-1", 3 * time.Minute, true},
		{"s1", "not a model", 4 * time.Minute, false},
		{"", "claude-opus-5-5", 5 * time.Minute, false},
	} {
		got, err := RecordDefaultSessionModel(root, step.session, step.model, "", t0.Add(step.at))
		if err != nil {
			t.Fatal(err)
		}
		if got != step.want {
			t.Fatalf("%s %s at %v: news = %v, want %v", step.session, step.model, step.at, got, step.want)
		}
	}
}

// liveSessionAs writes a default-profile session record for a live process.
func liveSessionAs(t *testing.T, dir string, pid int, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := groups.WriteJSON(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"), map[string]any{"pid": pid, "sessionId": id}); err != nil {
		t.Fatal(err)
	}
}

// The default login leaves Fable-capable accounts with Fable room to Fable
// work only while every live session has a recorded model and none runs
// Fable (DESIGN A77, A91).
func TestDefaultSessionPolicyReservesFableForFableSessions(t *testing.T) {
	strp := func(v string) *string { return &v }
	type session struct{ id, model string }
	for name, tc := range map[string]struct {
		live     []session
		recorded []session
		set      *string
		reserve  []string
	}{
		"opus-sessions":      {live: []session{{"a", "claude-opus-5-5"}, {"b", "claude-sonnet-5-5"}}, reserve: []string{"1", "3"}},
		"one-fable-session":  {live: []session{{"a", "claude-opus-5-5"}, {"b", "claude-fable-5-1"}}},
		"unrecorded-session": {live: []session{{"a", "claude-opus-5-5"}, {"b", ""}}},
		"no-live-session":    {recorded: []session{{"gone", "claude-opus-5-5"}}},
		"base-model-ignored": {live: []session{{"a", "claude-opus-5-5"}, {"b", "claude-sonnet-5-5"}}, set: strp("Opus"), reserve: []string{"1", "3"}},
	} {
		t.Run(name, func(t *testing.T) {
			clk := testutil.FixedClock(t, fixedNow)
			s := newStore(t, clk, nil)
			writeSequenceRaw(t, s, `{
  "activeAccountNumber": 1, "lastUpdated": "x", "sequence": [1, 2, 3, 4, 5],
  "accounts": {
    "1": {"email": "ultra@example.com", "subscriptionType": "ultra"},
    "2": {"email": "pro@example.com", "subscriptionType": "pro"},
    "3": {"email": "team@example.com", "subscriptionType": "team", "fableContinue": true},
    "4": {"email": "max@example.com", "subscriptionType": "max", "fableContinue": false},
    "5": {"email": "unknown@example.com", "subscriptionType": "team"}
  }
}`)
			rows := map[string]any{}
			for num, email := range map[string]string{"1": "ultra", "2": "pro", "3": "team", "4": "max", "5": "unknown"} {
				rows[num] = map[string]any{"email": email + "@example.com", "organizationUuid": "", "fetchedAt": clock.Seconds(clk) - 60,
					"lastGood": map[string]any{"seven_day": map[string]any{"pct": 20.0}, "scoped": []any{map[string]any{"name": "Fable", "pct": 10.0}}}}
			}
			writeUsageRows(t, s, rows)
			pids := []int{os.Getpid(), os.Getppid()}
			if pids[1] <= 1 {
				t.Skip("no second live process to stand for a session")
			}
			for i, sess := range tc.live {
				liveSessionAs(t, s.DefaultProfileDir(), pids[i], sess.id)
			}
			for _, sess := range append(append([]session{}, tc.live...), tc.recorded...) {
				if sess.model == "" {
					continue
				}
				if _, err := RecordDefaultSessionModel(s.BackupDir(), sess.id, sess.model, "", clk.Now()); err != nil {
					t.Fatal(err)
				}
			}
			base := settings.Default()
			base.Model = tc.set
			_, reserved := DefaultSessionPolicy(s, base)
			var got []string
			for _, num := range []string{"1", "2", "3", "4", "5"} {
				if reserved != nil && reserved(num) {
					got = append(got, num)
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.reserve, ",") {
				t.Fatalf("reserved = %v, want %v", got, tc.reserve)
			}
		})
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// incident is the cached usage of 2026-11-15 23:53 (DESIGN A91): 3 and 11
// have Fable room but are at their 5h limits, 4, 7 and 8 have room on 5h
// and 7d but their Fable week is spent. live is the default login's account;
// fiveHourReset, when set, moves 11's 5h reset into the past.
type incident struct {
	live          string
	disabled      []string
	fiveHourReset time.Duration
	sessions      map[string]string // session id -> model, live for this process and its parent
	five11        *float64          // 11's 5h utilization instead of 100
	row11         map[string]any    // usage-row fields of 11 over the fresh success
}

func incidentWindow(pct float64, reset time.Time) map[string]any {
	return map[string]any{"pct": pct, "resets_at": reset.UTC().Format(time.RFC3339)}
}

func (in incident) store(t *testing.T) (*store.Store, time.Time) {
	t.Helper()
	clk := testutil.FixedClock(t, fixedNow)
	now := clk.Now()
	s := newStore(t, clk, nil)
	type figures struct{ five, week, fable float64 }
	accounts := map[string]figures{"3": {100, 27, 28}, "4": {5, 78, 100}, "7": {4, 88, 100}, "8": {-1, 93, 100}, "11": {100, 80, 78}}
	if in.five11 != nil {
		accounts["11"] = figures{*in.five11, 80, 78}
	}
	disabled := map[string]bool{}
	for _, num := range in.disabled {
		disabled[num] = true
	}
	records := map[string]any{}
	rows := map[string]any{}
	for num, f := range accounts {
		email := "acct" + num + "@example.com"
		records[num] = map[string]any{"email": email, "organizationUuid": "", "organizationName": "", "subscriptionType": "ultra", "disabled": disabled[num]}
		creds := oauthCreds("at"+num, "rt"+num, 0)
		writeBackup(t, s, num, email, creds, `{"oauthAccount":{"emailAddress":"`+email+`"}}`)
		if num == in.live {
			writeLiveConfig(t, s, email, "")
			writeActiveCreds(t, s, creds)
		}
		fiveReset := now.Add(2 * time.Hour)
		if num == "11" && in.fiveHourReset != 0 {
			fiveReset = now.Add(-in.fiveHourReset)
		}
		last := map[string]any{
			"seven_day": incidentWindow(f.week, now.Add(3*24*time.Hour)),
			"scoped":    []any{map[string]any{"name": "Fable", "pct": f.fable, "resets_at": now.Add(4 * 24 * time.Hour).UTC().Format(time.RFC3339)}},
		}
		if f.five >= 0 {
			last["five_hour"] = incidentWindow(f.five, fiveReset)
		}
		row := map[string]any{"email": email, "organizationUuid": "", "fetchedAt": clock.Seconds(clk) - 60, "lastGood": last}
		if num == "11" {
			for k, v := range in.row11 {
				row[k] = v
			}
		}
		rows[num] = row
	}
	seq, err := json.Marshal(map[string]any{"activeAccountNumber": 3, "lastUpdated": "x", "sequence": []int{3, 4, 7, 8, 11}, "accounts": records})
	if err != nil {
		t.Fatal(err)
	}
	writeSequenceRaw(t, s, string(seq))
	writeUsageRows(t, s, rows)
	pids := []int{os.Getpid(), os.Getppid()}
	i := 0
	for id, model := range in.sessions {
		if i >= len(pids) || pids[i] <= 1 {
			t.Skip("no live process left to stand for a session")
		}
		liveSessionAs(t, s.DefaultProfileDir(), pids[i], id)
		i++
		if _, err := RecordDefaultSessionModel(s.BackupDir(), id, model, "", now); err != nil {
			t.Fatal(err)
		}
	}
	return s, now
}

var fableSession = map[string]string{"s-fable": "claude-fable-5-1"}

func TestFableCountingCaptionUsesTheSameNamesAsTheEngine(t *testing.T) {
	five := 10.0
	s, _ := incident{live: "11", sessions: fableSession, five11: &five}.store(t)
	row := usageRow(t, s, "4")
	row["lastGood"].(map[string]any)["scoped"].([]any)[0].(map[string]any)["name"] = "fable"
	writeUsageRows(t, s, map[string]any{"4": row, "11": usageRow(t, s, "11")})
	count := CountedForSwitch(s)
	if strings.Join(count.Models, ",") != "Fable" || count.Caption != "counting 5h + 7d + Fable (live Fable sessions)" {
		t.Fatalf("counted models %v, caption %q", count.Models, count.Caption)
	}
}

// A live Fable session no usable account can serve blocks nobody: every
// account with Fable room is at its 5h limit (DESIGN A91).
func TestPolicyDropsUnservableModel(t *testing.T) {
	s, now := incident{live: "3", sessions: fableSession}.store(t)
	r := DefaultSessionPolicyResult(s, settings.Default(), now)
	if r.Settings.Model != nil || strings.Join(r.Unservable, ",") != "Fable" {
		t.Fatalf("model = %q, unservable = %v; want none counted and Fable unservable", deref(r.Settings.Model), r.Unservable)
	}
}

// A cached 5h window at 100 % whose reset has passed is room (ENG-7): 11
// serves Fable again, so Fable counts, while 11's last reading is one the
// engine still trusts; an older one serves only after 11's next fetch
// (DESIGN A91).
func TestPolicyKeepsModelAfterReset(t *testing.T) {
	now := float64(testutil.FixedClock(t, fixedNow).Now().Unix())
	for name, tc := range map[string]struct {
		age     float64
		counted bool
	}{
		"trusted":   {age: 60, counted: true},
		"untrusted": {age: usage.StaleOKS + 60},
	} {
		t.Run(name, func(t *testing.T) {
			s, at := incident{live: "3", sessions: fableSession, fiveHourReset: time.Minute, row11: map[string]any{"fetchedAt": now - tc.age}}.store(t)
			r := DefaultSessionPolicyResult(s, settings.Default(), at)
			if counted := deref(r.Settings.Model) == "Fable" && len(r.Unservable) == 0; counted != tc.counted {
				t.Fatalf("model = %q, unservable = %v; want Fable counted %v", deref(r.Settings.Model), r.Unservable, tc.counted)
			}
		})
	}
}

// Only accounts a tick may use serve a model: a disabled, quarantined or
// legacy-held account does not, the live account does even when disabled.
func TestPolicyPoolExcludesDisabledAndHeld(t *testing.T) {
	for name, tc := range map[string]struct {
		in      incident
		prepare func(t *testing.T, s *store.Store)
		counted bool
	}{
		"enabled":             {in: incident{live: "3"}, counted: true},
		"disabled":            {in: incident{live: "3", disabled: []string{"11"}}},
		"live-and-disabled":   {in: incident{live: "11", disabled: []string{"11"}}, counted: true},
		"quarantined":         {in: incident{live: "3"}, prepare: quarantine("11")},
		"unreadable-state":    {in: incident{live: "3"}, prepare: brokenState, counted: true},
		"held-by-live-legacy": {in: incident{live: "3"}, prepare: legacyHold("11")},
	} {
		t.Run(name, func(t *testing.T) {
			tc.in.sessions, tc.in.fiveHourReset = fableSession, time.Minute
			s, now := tc.in.store(t)
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			r := DefaultSessionPolicyResult(s, settings.Default(), now)
			if counted := deref(r.Settings.Model) == "Fable" && len(r.Unservable) == 0; counted != tc.counted {
				t.Fatalf("model = %q, unservable = %v; want Fable counted %v", deref(r.Settings.Model), r.Unservable, tc.counted)
			}
		})
	}
}

// An account serves a model only below the bars the engine moves at, not at
// 100 %: at 5h 92 with the bar at 90 the engine would move off 11, so Fable,
// which only 11 can still run, is dropped; at 85 it is counted (DESIGN A91).
func TestPolicyServesBelowTheEngineBars(t *testing.T) {
	for name, tc := range map[string]struct {
		five    float64
		counted bool
	}{
		"over-the-5h-bar":  {five: 92},
		"under-the-5h-bar": {five: 85, counted: true},
	} {
		t.Run(name, func(t *testing.T) {
			s, now := incident{live: "3", sessions: fableSession, five11: &tc.five}.store(t)
			base := settings.Default()
			base.FiveHourThreshold = 90
			r := DefaultSessionPolicyResult(s, base, now)
			if counted := deref(r.Settings.Model) == "Fable"; counted != tc.counted || counted == (len(r.Unservable) > 0) {
				t.Fatalf("model = %q, unservable = %v; want Fable counted %v", deref(r.Settings.Model), r.Unservable, tc.counted)
			}
		})
	}
	for _, bar := range []struct {
		name string
		set  func(*settings.AutoSwitchSettings)
	}{
		{"7d-bar", func(b *settings.AutoSwitchSettings) { b.SevenDayThreshold = 80 }},
		{"model-bar", func(b *settings.AutoSwitchSettings) { b.ModelThreshold = 78 }},
	} {
		t.Run(bar.name, func(t *testing.T) {
			s, now := incident{live: "3", sessions: fableSession, fiveHourReset: time.Minute}.store(t)
			base := settings.Default()
			bar.set(&base)
			if r := DefaultSessionPolicyResult(s, base, now); r.Settings.Model != nil {
				t.Fatalf("11 at its %s still serves Fable: model %q", bar.name, deref(r.Settings.Model))
			}
		})
	}
}

// Only a reading the engine would still decide on serves (DESIGN A91): a
// failing fetch keeps 11 serving while the engine still trusts its last
// reading (usage.TrustMaxAgeS), not past that; a sentinel (no credential) or
// an old reading never serves, however good its numbers look.
func TestPolicyIgnoresUnknownOrOldUsage(t *testing.T) {
	now := float64(testutil.FixedClock(t, fixedNow).Now().Unix())
	failing := func(age float64) map[string]any {
		return map[string]any{"fetchedAt": now - age, "consecutiveFailures": 2, "lastError": "http-429", "lastAttemptAt": now - 30}
	}
	for name, tc := range map[string]struct {
		row     map[string]any
		prepare func(*testing.T, *store.Store)
		counted bool
	}{
		"failing-trusted":    {row: failing(1800), counted: true},
		"failing-past-trust": {row: failing(usage.TrustMaxAgeS + 60)},
		"no-credential":      {prepare: noAccessToken("11")},
		"old":                {row: map[string]any{"fetchedAt": now - 2*3600}},
	} {
		t.Run(name, func(t *testing.T) {
			s, at := incident{live: "3", sessions: fableSession, fiveHourReset: time.Minute, row11: tc.row}.store(t)
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			r := DefaultSessionPolicyResult(s, settings.Default(), at)
			if counted := deref(r.Settings.Model) == "Fable" && len(r.Unservable) == 0; counted != tc.counted || !counted && strings.Join(r.Unservable, ",") != "Fable" {
				t.Fatalf("model = %q, unservable = %v; want Fable counted %v", deref(r.Settings.Model), r.Unservable, tc.counted)
			}
		})
	}
}

// noAccessToken leaves num's backup without an access token, so its usage
// reads as the no-credentials sentinel.
func noAccessToken(num string) func(*testing.T, *store.Store) {
	return func(t *testing.T, s *store.Store) {
		email := "acct" + num + "@example.com"
		writeBackup(t, s, num, email, oauthCreds("", "rt"+num, 0), `{"oauthAccount":{"emailAddress":"`+email+`"}}`)
	}
}

func quarantine(num string) func(*testing.T, *store.Store) {
	return func(t *testing.T, s *store.Store) {
		state := `{"schemaVersion": 1, "quarantine": {"` + num + `": {"email": "acct` + num + `@example.com", "reason": "invalid_grant"}}}`
		if err := os.WriteFile(filepath.Join(s.BackupDir(), "autoswitch_state.json"), []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func brokenState(t *testing.T, s *store.Store) {
	if err := os.WriteFile(filepath.Join(s.BackupDir(), "autoswitch_state.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func legacyHold(num string) func(*testing.T, *store.Store) {
	return func(t *testing.T, s *store.Store) {
		makeLiveSession(t, s, num, "acct"+num+"@example.com")
		data, err := s.ReadSequence()
		if err != nil || s.RotationEligible(data, num) {
			t.Fatalf("Account-%s is still rotation-eligible under a live legacy profile (%v)", num, err)
		}
	}
}

// Without a live Fable session only accounts that can run Fable and sit
// under the model bar on their Fable window are left to Fable (DESIGN A91):
// a spent Fable week, or one at the bar, reserves nothing.
func TestReservationNeedsFableRoom(t *testing.T) {
	s, now := incident{live: "3", sessions: map[string]string{"s-opus": "claude-opus-5-5"}}.store(t)
	rows := map[string]any{}
	for num, fable := range map[string]float64{"3": 96, "4": 100, "7": 100, "8": 100, "11": 78} {
		email := "acct" + num + "@example.com"
		rows[num] = map[string]any{"email": email, "organizationUuid": "", "fetchedAt": float64(now.Unix()) - 60, "lastGood": map[string]any{
			"five_hour": incidentWindow(10, now.Add(time.Hour)), "seven_day": incidentWindow(50, now.Add(72*time.Hour)),
			"scoped": []any{map[string]any{"name": "Fable", "pct": fable, "resets_at": now.Add(96 * time.Hour).UTC().Format(time.RFC3339)}},
		}}
	}
	writeUsageRows(t, s, rows)
	base := settings.Default()
	base.ModelThreshold = 95
	r := DefaultSessionPolicyResult(s, base, now)
	var got []string
	for _, num := range []string{"3", "4", "7", "8", "11"} {
		if r.Reserved != nil && r.Reserved(num) {
			got = append(got, num)
		}
	}
	if strings.Join(got, ",") != "11" {
		t.Fatalf("reserved = %v, want [11]", got)
	}
}
