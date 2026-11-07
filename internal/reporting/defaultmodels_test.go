package reporting

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/testutil"
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
		"explicit-setting":   {"s-opus", "claude-opus-5-5", strp("all"), "all"},
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
			if _, err := RecordDefaultSessionModel(s.BackupDir(), recorded, tc.model, clk.Now()); err != nil {
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
	if _, err := RecordDefaultSessionModel(root, "s1", "claude-opus-5-5", t0); err != nil {
		t.Fatal(err)
	}
	first := mtime()
	time.Sleep(20 * time.Millisecond)
	if _, err := RecordDefaultSessionModel(root, "s1", "claude-opus-5-5", t0.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !mtime().Equal(first) {
		t.Fatal("an unchanged model was rewritten within a minute")
	}
	if _, err := RecordDefaultSessionModel(root, "s1", "claude-fable-5-1", t0.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if mtime().Equal(first) {
		t.Fatal("a model change was not written")
	}
	if _, err := RecordDefaultSessionModel(root, "s1", "not a model", t0.Add(30*time.Second)); err != nil {
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
		got, err := RecordDefaultSessionModel(root, step.session, step.model, t0.Add(step.at))
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

// The default login leaves Fable-capable accounts to Fable work only while
// every live session has a recorded model and none runs Fable (DESIGN A77).
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
		"explicit-setting":   {live: []session{{"a", "claude-opus-5-5"}}, set: strp("Opus")},
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
				if _, err := RecordDefaultSessionModel(s.BackupDir(), sess.id, sess.model, clk.Now()); err != nil {
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
