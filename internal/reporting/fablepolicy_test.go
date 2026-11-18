// Tests for the default store's policy beside the Fable store (DESIGN A102).
package reporting

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

var incidentSlots = []string{"3", "4", "7", "8", "11"}

// fableIncident is the incident of 2026-11-15 (11 with 5h room) with Fable
// logins for logins, the one of live installed in the Fable store, and the
// sessions in onFable recorded on the Fable store.
func fableIncident(t *testing.T, sessions map[string]string, onFable []string, live string, logins ...string) (*store.Store, time.Time) {
	t.Helper()
	five := 10.0
	s, now := incident{live: "3", five11: &five, sessions: sessions}.store(t)
	for _, id := range onFable {
		if _, err := RecordDefaultSessionModel(s.BackupDir(), id, sessions[id], "fable", now); err != nil {
			t.Fatal(err)
		}
	}
	s.Platform = platform.Linux
	f, err := s.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetupFableDirectories(); err != nil {
		t.Fatal(err)
	}
	for _, num := range logins {
		if err := f.WriteAccountCredentials(num, "acct"+num+"@example.com", oauthCreds("fat"+num, "frt"+num, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if live != "" {
		putFableStore(t, s, oauthCreds("fat"+live, "frt"+live, 0))
	}
	return s, now
}

func putFableStore(t *testing.T, s *store.Store, creds string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
}

func reservedOf(r PolicyResult) string {
	var got []string
	for _, num := range incidentSlots {
		if r.Reserved != nil && r.Reserved(num) {
			got = append(got, num)
		}
	}
	return strings.Join(got, ",")
}

// rankFrom is the order a default tick on from would try its targets in.
func rankFrom(t *testing.T, s *store.Store, r PolicyResult, from string, now time.Time) []string {
	t.Helper()
	var candidates []string
	for _, num := range s.SwitchableAccountNumbers() {
		if num != from {
			candidates = append(candidates, num)
		}
	}
	return autoswitch.RankTargets(candidates, UsageByAccount(s), r.Settings, settings.ParseModelNames(r.Settings.Model), r.Reserved, now)
}

var fableAndOpus = map[string]string{"s-fable": "claude-fable-5-1", "s-opus": "claude-opus-5-5"}

// The 2026-11-15 shape with the Fable store on: the Fable session runs on the
// Fable store, so the default store counts no Fable window, and it leaves 3
// and 11 (Fable room, Fable logins) and the Fable store's account 8 to the
// Fable store; at 3's 5h bar it moves to 4 or 7, whose Fable week is spent.
func TestDefaultPolicyBesideTheFableStore(t *testing.T) {
	s, now := fableIncident(t, fableAndOpus, []string{"s-fable"}, "8", incidentSlots...)
	r := DefaultSessionPolicyResult(s, settings.Default(), now)
	if r.Settings.Model != nil || len(r.Unservable) != 0 || !reflect.DeepEqual(r.Sessions, []string{"opus"}) {
		t.Fatalf("model %q, unservable %v, sessions %v; want no Fable window and the Opus session only", deref(r.Settings.Model), r.Unservable, r.Sessions)
	}
	if got := reservedOf(r); got != "3,8,11" {
		t.Fatalf("reserved = %s, want 3,8,11", got)
	}
	if ranked := rankFrom(t, s, r, "3", now); len(ranked) == 0 || ranked[0] != "4" && ranked[0] != "7" {
		t.Fatalf("a default tick off 3 tries %v first, want 4 or 7", ranked)
	}
}

// A live session without a record keeps the reservation: the Fable store
// needs its accounts whatever the default store's sessions run.
func TestFableReservationSurvivesAnUnrecordedSession(t *testing.T) {
	s, now := fableIncident(t, map[string]string{"s-opus": "claude-opus-5-5", "s-unknown": ""}, nil, "8", incidentSlots...)
	if got := reservedOf(DefaultSessionPolicyResult(s, settings.Default(), now)); got != "3,8,11" {
		t.Fatalf("reserved = %s, want 3,8,11", got)
	}
}

// The Fable store's live account ranks after every account the default store
// may take, even with the most weekly room and a spent Fable week; an account
// with Fable room but no Fable login is the default store's (DESIGN A102).
func TestRankTargetsPutsTheFableLiveAccountLast(t *testing.T) {
	opus := map[string]string{"s-opus": "claude-opus-5-5"}
	s, now := fableIncident(t, opus, nil, "4", "3", "4")
	r := DefaultSessionPolicyResult(s, settings.Default(), now)
	if got := reservedOf(r); got != "3,4" {
		t.Fatalf("reserved = %s, want 3,4", got)
	}
	if ranked := rankFrom(t, s, r, "3", now); len(ranked) != 4 || ranked[3] != "4" {
		t.Fatalf("ranked %v, want the Fable store's account 4 last", ranked)
	}
}

// With the Fable store off (its logins kept), on with its live login dead, or
// without Fable logins the policy is A91's: the Fable session counts Fable
// and nothing is reserved.
func TestDefaultPolicyWithoutAUsableFableStoreIsA91(t *testing.T) {
	for name, tc := range map[string]struct {
		live   string
		logins []string
		dead   bool
	}{
		"store-off":       {"", incidentSlots, false},
		"store-unusable":  {"8", incidentSlots, true},
		"no-fable-logins": {"", nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			s, now := fableIncident(t, fableAndOpus, []string{"s-fable"}, tc.live, tc.logins...)
			if tc.dead {
				f, _ := s.ForFable()
				if err := autoswitch.Quarantine(f.StatePath(), s.Clk, tc.live, "acct"+tc.live+"@example.com", "invalid_grant", oauthCreds("fat"+tc.live, "frt"+tc.live, 0), func(autoswitch.Event) {}); err != nil {
					t.Fatal(err)
				}
				if st := s.FableStore(); !st.On || st.Usable {
					t.Fatalf("precondition: FableStore = %+v, want on and unusable", st)
				}
			}
			r := DefaultSessionPolicyResult(s, settings.Default(), now)
			if deref(r.Settings.Model) != "Fable" || r.Reserved != nil || !reflect.DeepEqual(r.Sessions, []string{"fable", "opus"}) {
				t.Fatalf("model %q, reserved %s, sessions %v; want A91's Fable counted, none reserved", deref(r.Settings.Model), reservedOf(r), r.Sessions)
			}
		})
	}
}

// The pool leaves out, in turn, a disabled account, a plan without Fable, an
// account without a Fable login and one whose Fable login is quarantined;
// the default store's quarantine leaves it in (DESIGN A102).
func TestFablePool(t *testing.T) {
	for name, tc := range map[string]struct {
		in      incident
		prepare func(*testing.T, *store.Store)
		want    string
	}{
		"all":                   {want: "3,4,7,8,11"},
		"disabled":              {in: incident{disabled: []string{"7"}}, want: "3,4,8,11"},
		"plan-without-fable":    {prepare: noFablePlan("4"), want: "3,7,8,11"},
		"no-fable-login":        {prepare: dropFableLogin("8"), want: "3,4,7,11"},
		"fable-quarantine":      {prepare: fableQuarantine("11"), want: "3,4,7,8"},
		"default-quarantine-in": {prepare: quarantine("11"), want: "3,4,7,8,11"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.in.live = "3"
			s, _ := tc.in.store(t)
			s.Platform = platform.Linux
			f, _ := s.ForFable()
			if err := f.SetupFableDirectories(); err != nil {
				t.Fatal(err)
			}
			for _, num := range incidentSlots {
				if err := f.WriteAccountCredentials(num, "acct"+num+"@example.com", oauthCreds("fat"+num, "frt"+num, 0)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			if got := strings.Join(FablePool(s), ","); got != tc.want {
				t.Fatalf("pool = %s, want %s", got, tc.want)
			}
		})
	}
}

func noFablePlan(num string) func(*testing.T, *store.Store) {
	return func(t *testing.T, s *store.Store) {
		data, err := s.SequenceForUpdate()
		if err != nil {
			t.Fatal(err)
		}
		data.Accounts[num] = []byte(`{"email": "acct` + num + `@example.com", "organizationUuid": "", "subscriptionType": "pro", "fableContinue": false}`)
		if err := s.WriteSequence(data); err != nil {
			t.Fatal(err)
		}
	}
}

func dropFableLogin(num string) func(*testing.T, *store.Store) {
	return func(t *testing.T, s *store.Store) {
		matches, _ := filepath.Glob(filepath.Join(paths.FableCredentialsDir(s.BackupDir()), ".creds-"+num+"-*"))
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func fableQuarantine(num string) func(*testing.T, *store.Store) {
	return func(t *testing.T, s *store.Store) {
		state := `{"schemaVersion": 1, "quarantine": {"` + num + `": {"email": "acct` + num + `@example.com", "reason": "invalid_grant"}}}`
		if err := os.WriteFile(filepath.Join(paths.FableScopeDir(s.BackupDir()), "autoswitch_state.json"), []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A Fable-store session's record names its store; a change of store is news
// like a change of family, and the default store's record has no store field.
func TestRecordCarriesTheStore(t *testing.T) {
	root := t.TempDir()
	t0 := time.Unix(100, 0)
	for i, step := range []struct {
		store string
		at    time.Duration
		news  bool
	}{{"fable", 0, true}, {"fable", 10 * time.Second, false}, {"", 20 * time.Second, true}, {"fable", 30 * time.Second, true}} {
		news, err := RecordDefaultSessionModel(root, "s1", "claude-fable-5-1", step.store, t0.Add(step.at))
		if err != nil || news != step.news {
			t.Fatalf("step %d: news %v, %v; want %v", i, news, err, step.news)
		}
		if family, store, ok := RecordedSession(root, "s1"); !ok || family != "fable" || store != step.store {
			t.Fatalf("step %d: recorded %q, %q, %v; want fable, %q", i, family, store, ok, step.store)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(root, "default_models.json"))
	if want := `{"schemaVersion":1,"sessions":{"s1":{"family":"fable","seenAt":130,"store":"fable"}}}`; string(raw) != want {
		t.Fatalf("default_models.json = %s, want %s", raw, want)
	}
}
