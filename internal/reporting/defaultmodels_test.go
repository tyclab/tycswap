package reporting

import (
	"os"
	"path/filepath"
	"strconv"
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
			if err := RecordDefaultSessionModel(s.BackupDir(), recorded, tc.model, clk.Now()); err != nil {
				t.Fatal(err)
			}
			base := settings.Default()
			base.Model = tc.set
			got := DefaultSessionSettings(s, base)
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
	if err := RecordDefaultSessionModel(root, "s1", "claude-opus-5-5", t0); err != nil {
		t.Fatal(err)
	}
	first := mtime()
	time.Sleep(20 * time.Millisecond)
	if err := RecordDefaultSessionModel(root, "s1", "claude-opus-5-5", t0.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !mtime().Equal(first) {
		t.Fatal("an unchanged model was rewritten within a minute")
	}
	if err := RecordDefaultSessionModel(root, "s1", "claude-fable-5-1", t0.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if mtime().Equal(first) {
		t.Fatal("a model change was not written")
	}
	if err := RecordDefaultSessionModel(root, "s1", "not a model", t0.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := readDefaultSessionModels(root)["s1"].Family; got != "fable" {
		t.Fatalf("family = %q after an unknown model, want fable", got)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
