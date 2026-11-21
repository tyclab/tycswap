//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

const (
	tickSessionA = "11111111-2222-4333-8444-000000000001"
	tickSessionB = "11111111-2222-4333-8444-000000000002"
	tickSessionC = "11111111-2222-4333-8444-000000000003"
)

func mythosTickFixture(t *testing.T, spent, room float64, off bool) (*core.Switcher, string, string) {
	t.Helper()
	sw, _ := (fableTickFixture{def: "1", off: off, usage: map[string][3]float64{
		"1": {5, 15, 25}, "2": {5, 15, spent}, "3": {5, 15, room}, "4": {1, 1, 1},
	}}).build(t)
	from, to := addMythosTestLogins(t, sw)
	return sw, from, to
}

func addMythosTestLogins(t *testing.T, sw *core.Switcher) (string, string) {
	t.Helper()
	root := sw.BackupDir()
	for _, num := range []string{"2", "3", "4"} {
		if err := sw.SetMythos(num, true); err != nil {
			t.Fatal(err)
		}
		if num != "4" {
			dir := store.MythosLoginDir(root, fableEmail(num), "")
			writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("mythos-"+num, "refresh-"+num))
		}
	}
	return store.MythosLoginDir(root, fableEmail("2"), ""), store.MythosLoginDir(root, fableEmail("3"), "")
}

func bindTickSession(t *testing.T, sw *core.Switcher, id, family, target string) string {
	t.Helper()
	link, err := reporting.CreateSessionLink(sw.BackupDir(), target)
	if err != nil {
		t.Fatal(err)
	}
	scope := ""
	if family == "mythos" {
		scope = "mythos/" + filepath.Base(target)
	}
	if err := reporting.BindSessionLink(sw.BackupDir(), id, link, family, scope, sw.Clk.Now()); err != nil {
		t.Fatal(err)
	}
	return link
}

func requireLinkTarget(t *testing.T, link, want string) {
	t.Helper()
	if target, err := os.Readlink(link); err != nil || target != want {
		t.Fatalf("session link %s points to %s, want %s (%v)", link, target, want, err)
	}
}

func TestTickMovesOnlyMythosSessionsAndRetainsTheSelection(t *testing.T) {
	sw, from, to := mythosTickFixture(t, 100, 30, false)
	cache := filepath.Join(sw.BackupDir(), "cache", "usage.json")
	reset := strings.ReplaceAll(readOrAbsent(t, cache), `"pct":100,`, `"pct":1,`)
	if reset == readOrAbsent(t, cache) {
		t.Fatal("fixture has no exhausted window")
	}
	first := bindTickSession(t, sw, tickSessionA, "mythos", from)
	second := bindTickSession(t, sw, tickSessionB, "mythos", from)
	other := bindTickSession(t, sw, tickSessionC, "opus", sw.DefaultProfileDir())
	runCodex(t, "", "auto", "--once", "--dry-run", "--json")
	requireLinkTarget(t, first, from)
	requireLinkTarget(t, second, from)
	code, out, errText := runCodex(t, "", "auto", "--once", "--json")
	moved := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		event := decodeJSON(t, line)
		if event["event"] == "switch" && event["trigger"] == "mythos-at-limit" {
			if id, ok := event["sessionId"].(string); ok {
				moved[id] = true
			}
		}
	}
	if code != 0 || len(moved) != 2 || !moved[tickSessionA] || !moved[tickSessionB] {
		t.Fatalf("expected both linked sessions to move for mythos-at-limit, got exit %d: %s %s", code, out, errText)
	}
	requireLinkTarget(t, first, to)
	requireLinkTarget(t, second, to)
	requireLinkTarget(t, other, sw.DefaultProfileDir())
	writeFile(t, cache, reset)
	next, err := locateMythosLogin(sw.BackupDir(), "mythos", "", "")
	if err != nil || next.dir != to {
		t.Fatalf("new start selected %s, want retained %s (%v)", next.dir, to, err)
	}
}

func TestTickWithoutAnotherReadyMythosLoginReportsQuota(t *testing.T) {
	sw, from, other := mythosTickFixture(t, 100, 30, false)
	if err := os.Remove(filepath.Join(other, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	link := bindTickSession(t, sw, tickSessionA, "mythos", from)
	code, out, msg := runCodex(t, "", "auto", "--once", "--json")
	requireLinkTarget(t, link, from)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		event := decodeJSON(t, line)
		if code == 3 && event["event"] == "no-switch" && event["reason"] == "mythos-session" &&
			event["detail"] == "no available Mythos login has quota for mythos" {
			return
		}
	}
	t.Fatalf("expected a blocked tick with a Mythos quota event, got exit %d: %s %s", code, out, msg)
}

func TestTickLeavesMythosSessionsWhenRotationCannotProceed(t *testing.T) {
	for _, tc := range []struct {
		name              string
		source, other     float64
		off, allocateOpus bool
	}{
		{"source has room", 99, 2, false, false},
		{"no ready alternative has room", 100, 100, false, false},
		{"auto off", 100, 2, true, false},
		{"requires an allocation exception", 100, 2, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, from, _ := mythosTickFixture(t, tc.source, tc.other, tc.off)
			if tc.allocateOpus {
				if code, _, msg := runCodex(t, "", "models", "3", "opus"); code != 0 {
					t.Fatal(msg)
				}
				before := askPlacement
				t.Cleanup(func() { askPlacement = before })
				askPlacement = func(ioStreams, string, string, bool) (bool, error) {
					t.Fatal("the tick waited for a placement decision")
					return false, nil
				}
			}
			link := bindTickSession(t, sw, tickSessionA, "mythos", from)
			runCodex(t, "", "auto", "--once", "--json")
			requireLinkTarget(t, link, from)
		})
	}
}

func TestTickSelectsMythosWithoutLiveSessionsAndRetriesOldLinks(t *testing.T) {
	sw, from, to := mythosTickFixture(t, 100, 30, false)
	root := sw.BackupDir()
	if err := store.SeedMythosSelection(root, from); err != nil {
		t.Fatal(err)
	}
	runCodex(t, "", "auto", "--once", "--dry-run")
	if got, err := store.SelectedMythosDigest(root); err != nil || got != filepath.Base(from) {
		t.Fatalf("dry run changed selection: %q, %v", got, err)
	}
	cache := filepath.Join(root, "cache", "usage.json")
	reset := strings.ReplaceAll(readOrAbsent(t, cache), `"pct":100,`, `"pct":1,`)
	runCodex(t, "", "auto", "--once")
	if got, err := store.SelectedMythosDigest(root); err != nil || got != filepath.Base(to) {
		t.Fatalf("exhausted selection with no sessions: %q, %v", got, err)
	}
	// A link that missed the previous move must catch up, even after its old limit resets.
	writeFile(t, cache, reset)
	link := bindTickSession(t, sw, tickSessionA, "mythos", from)
	runCodex(t, "", "auto", "--once")
	requireLinkTarget(t, link, to)
}
