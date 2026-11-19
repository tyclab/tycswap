package reporting

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

// markMythos sets the Mythos mark on accounts nums in the roster as written.
func markMythos(nums ...string) func(*testing.T, *store.Store) {
	return func(t *testing.T, s *store.Store) {
		t.Helper()
		data, err := s.ReadSequence()
		if err != nil || data == nil {
			t.Fatalf("roster: %v", err)
		}
		for _, num := range nums {
			var rec map[string]any
			if err := json.Unmarshal(data.Accounts[num], &rec); err != nil {
				t.Fatal(err)
			}
			rec["mythos"] = true
			data.Accounts[num], _ = json.Marshal(rec)
		}
		if err := s.WriteSequence(data); err != nil {
			t.Fatal(err)
		}
	}
}

var mythosSession = map[string]string{"s-mythos": "claude-mythos-5-2"}

// Mythos is a family: its session is recorded, and a session that changes
// from Fable to Mythos is news and recorded as Mythos (DESIGN A106).
func TestRecordDefaultSessionModelRecordsMythos(t *testing.T) {
	if got := ModelFamily("claude-mythos-5-2"); got != "mythos" {
		t.Fatalf("family = %q", got)
	}
	root := t.TempDir()
	t0 := time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC)
	for _, step := range []struct {
		model string
		at    time.Duration
		news  bool
	}{
		{"claude-fable-5-2", 0, true},
		{"claude-mythos-5-2", 10 * time.Second, true},
		{"claude-mythos-5-2[1m]", 20 * time.Second, false},
	} {
		news, err := RecordDefaultSessionModel(root, "s1", step.model, "", t0.Add(step.at))
		if err != nil || news != step.news {
			t.Fatalf("%s: news = %v (%v), want %v", step.model, news, err, step.news)
		}
	}
	if family, _, ok := RecordedSession(root, "s1"); !ok || family != "mythos" {
		t.Fatalf("recorded %q %v", family, ok)
	}
}

// While a default-store session runs Mythos the policy restricts the login to
// the marked accounts; with no marked account in the pool it restricts
// nothing and names Mythos unservable (DESIGN A106).
func TestPolicyRestrictsTheLoginToMythosAccounts(t *testing.T) {
	for name, tc := range map[string]struct {
		in         incident
		prepare    func(*testing.T, *store.Store)
		serves     string
		unservable string
	}{
		"marked":            {in: incident{live: "3", sessions: mythosSession}, prepare: markMythos("8", "11"), serves: "8,11"},
		"none-marked":       {in: incident{live: "3", sessions: mythosSession}, unservable: "Mythos"},
		"marked-disabled":   {in: incident{live: "3", sessions: mythosSession, disabled: []string{"8"}}, prepare: markMythos("8"), unservable: "Mythos"},
		"marked-live":       {in: incident{live: "8", sessions: mythosSession, disabled: []string{"8"}}, prepare: markMythos("8"), serves: "8"},
		"no-mythos-session": {in: incident{live: "3", sessions: fableSession}, prepare: markMythos("8"), unservable: "Fable"},
	} {
		t.Run(name, func(t *testing.T) {
			s, now := tc.in.store(t)
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			r := DefaultSessionPolicyResult(s, settings.Default(), now)
			if got := strings.Join(r.ServesIn(s), ","); got != tc.serves || (r.Serves == nil) != (tc.serves == "") {
				t.Fatalf("serves %q (nil %v), want %q", got, r.Serves == nil, tc.serves)
			}
			if got := strings.Join(r.Unservable, ","); got != tc.unservable {
				t.Fatalf("unservable %q, want %q", got, tc.unservable)
			}
		})
	}
}

// Beside a Mythos session the reservation and the counted models are the
// ones without it: Fable sessions still count Fable, and Mythos alone
// reserves the accounts with Fable room (A77, A91).
func TestPolicyMythosKeepsTheOtherRules(t *testing.T) {
	five := 10.0
	both := map[string]string{"s-mythos": "claude-mythos-5-2", "s-fable": "claude-fable-5-2"}
	s, now := incident{live: "3", sessions: both, five11: &five}.store(t)
	markMythos("11")(t, s)
	r := DefaultSessionPolicyResult(s, settings.Default(), now)
	if deref(r.Settings.Model) != "Fable" || strings.Join(r.Sessions, ",") != "fable,mythos" || r.Reserved != nil {
		t.Fatalf("model %q sessions %v reserved %v", deref(r.Settings.Model), r.Sessions, r.Reserved != nil)
	}
	s, now = incident{live: "3", sessions: mythosSession, five11: &five}.store(t)
	markMythos("11")(t, s)
	r = DefaultSessionPolicyResult(s, settings.Default(), now)
	if r.Settings.Model != nil || reservedOf(r) == "" {
		t.Fatalf("model %q reserved %q", deref(r.Settings.Model), reservedOf(r))
	}
}

// The counting caption says in one line which accounts a live Mythos session
// leaves the login, or that none is marked (DESIGN A106).
func TestCountingCaptionNamesMythos(t *testing.T) {
	for name, tc := range map[string]struct {
		sessions, unservable, mythos []string
		want                         string
	}{
		"served":     {[]string{"mythos"}, nil, []string{"2", "5"}, "counting 5h + 7d; live Mythos session: only accounts 2, 5; no live Fable session"},
		"not-served": {[]string{"mythos"}, []string{"Mythos"}, nil, "counting 5h + 7d; Mythos not served: no account marked; no live Fable session"},
		"both":       {[]string{"fable", "mythos"}, []string{"Fable", "Mythos"}, nil, "counting 5h + 7d; Fable not counted: no usable account can serve it; Mythos not served: no account marked"},
		"without":    {[]string{"opus"}, nil, nil, "counting 5h + 7d; no live Fable session"},
	} {
		if got := CountingCaption(false, nil, tc.sessions, tc.unservable, nil, tc.mythos); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// The mark shows as "(Mythos)" in list, as mythos: true in its JSON rows only
// when set, and in the account snapshot (DESIGN A106).
func TestListAndSnapshotShowTheMythosMark(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	s := newStore(t, clk, nil)
	writeLiveConfig(t, s, "alice@example.com", "")
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 1, "lastUpdated": "x", "sequence": [1, 2],
  "accounts": {
    "1": {"email": "alice@example.com", "organizationUuid": "", "organizationName": "", "mythos": true},
    "2": {"email": "bob@example.com", "organizationUuid": "", "organizationName": "", "startLogin": true, "mythos": "true"}
  }
}`)
	writeActiveCreds(t, s, oauthCreds("at1", "rt1", 0))
	writeBackup(t, s, "2", "bob@example.com", oauthCreds("at2", "rt2", 0), `{"oauthAccount":{"emailAddress":"bob@example.com"}}`)
	infos := BuildAccountsInfo(s)
	var buf bytes.Buffer
	renderAccounts(&buf, s, infos, CollectUsageEntries(s, infos, nil), false)
	out := buf.String()
	if !strings.Contains(out, "1: alice@example.com [personal] (active) (Mythos)") || strings.Count(out, "(Mythos)") != 1 {
		t.Fatalf("list:\n%s", out)
	}
	got, err := ListAccounts(s, false, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := got.(map[string]any)["accounts"].([]any)
	if rows[0].(map[string]any)["mythos"] != true {
		t.Fatalf("row 1 %v", rows[0])
	}
	if _, present := rows[1].(map[string]any)["mythos"]; present {
		t.Fatalf("row 2 %v", rows[1])
	}
	for _, a := range Snapshot(s, nil).Accounts {
		if a.Mythos != (a.Number == "1") {
			t.Fatalf("snapshot %s Mythos=%v", a.Number, a.Mythos)
		}
	}
}
