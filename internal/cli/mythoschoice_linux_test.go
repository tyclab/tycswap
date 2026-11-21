package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/web"
)

func TestManualMythosChoiceMovesSessionsAndHoldsRotation(t *testing.T) {
	sw, from, to := mythosTickFixture(t, 20, 30, false)
	if _, err := settings.SetSetting(sw.BackupDir(), "autoswitch.cooldownSeconds", "1"); err != nil {
		t.Fatal(err)
	}
	moved := bindTickSession(t, sw, tickSessionA, "mythos", from)
	other := bindTickSession(t, sw, tickSessionB, "opus", sw.DefaultProfileDir())
	code, out, msg := runSub(t, "switch", "--store", "mythos", "3", "--json")
	if code != 0 {
		t.Fatalf("switch --store mythos 3: %d %s %s", code, out, msg)
	}
	doc := decodeJSON(t, out)
	if doc["store"] != "mythos" || doc["switched"] != true {
		t.Fatalf("switch result: %v", doc)
	}
	requireLinkTarget(t, moved, to)
	requireLinkTarget(t, other, sw.DefaultProfileDir())
	cache := filepath.Join(sw.BackupDir(), "cache", "usage.json")
	writeFile(t, cache, strings.ReplaceAll(readOrAbsent(t, cache), `"pct":30,`, `"pct":100,`))
	sessionStatus(t, moved, tickSessionA, "claude-mythos-5-2", false)
	runCodex(t, "", "auto", "--once", "--json")
	requireLinkTarget(t, moved, to)
	t.Setenv(secureStorageEnv, "")
	sw.Clk.(*clock.Fake).Advance(2 * time.Second)
	runCodex(t, "", "auto", "--once", "--json")
	requireLinkTarget(t, moved, from)
}

func TestManualMythosChoiceDefersOnlyRefreshingSessions(t *testing.T) {
	sw, from, to := mythosTickFixture(t, 20, 30, true)
	moved := bindTickSession(t, sw, tickSessionA, "mythos", from)
	lock := filepath.Join(from, ".oauth_refresh.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	code, out, msg := runSub(t, "switch", "--store", "mythos", "3", "--json")
	if !strings.Contains(out+msg, ".oauth_refresh.lock") {
		t.Fatalf("a busy login needs its own answer: %d %s %s", code, out, msg)
	}
	if got, err := store.SelectedMythosDigest(sw.BackupDir()); err != nil || got != filepath.Base(to) {
		t.Fatalf("manual selection during refresh: %q, %v", got, err)
	}
	requireLinkTarget(t, moved, from)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if code, _, msg := runSub(t, "switch", "--store", "mythos", "3", "--json"); code != 0 {
		t.Fatalf("retry after refresh: %d, %s", code, msg)
	}
	requireLinkTarget(t, moved, to)
}

func TestInvalidMythosChoiceCannotRemainSelected(t *testing.T) {
	for _, change := range []string{"unmarked", "disabled", "lost login"} {
		t.Run(change, func(t *testing.T) {
			sw, from, _ := mythosTickFixture(t, 20, 30, true)
			if err := store.SelectMythosLogin(sw.BackupDir(), from); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "unmarked":
				if err := sw.SetMythos("2", false); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if code, _, msg := runSub(t, "disable", "2"); code != 0 {
					t.Fatal(msg)
				}
			case "lost login":
				if err := os.Remove(filepath.Join(from, ".credentials.json")); err != nil {
					t.Fatal(err)
				}
				runCodex(t, "", "auto", "--once", "--json")
			}
			got, err := store.SelectedMythosDigest(sw.BackupDir())
			if err != nil || got == filepath.Base(from) {
				t.Fatalf("selection after %s: %q, %v", change, got, err)
			}
			if code, _, _ := runSub(t, "switch", "--store", "mythos", "2", "--json"); code != 1 {
				t.Fatalf("unavailable selection exited %d", code)
			}
		})
	}
}

func TestListNamesTheMythosLoginInUse(t *testing.T) {
	sw, from, _ := mythosTickFixture(t, 20, 30, false)
	bindTickSession(t, sw, tickSessionA, "mythos", from)
	code, out, err := runHuman(t, "list")
	if code != 0 {
		t.Fatal(err)
	}
	for _, c := range []struct{ email, tag string }{{fableEmail("2"), "(Mythos login: in use)"}, {fableEmail("3"), "(Mythos login)"}} {
		found := false
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, c.email) && strings.Contains(line, c.tag) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s on %s in %s", c.tag, c.email, out)
		}
	}
}

func TestMythosLoginReportsChoiceWindowsAndSessions(t *testing.T) {
	sw, from, _ := mythosTickFixture(t, 20, 30, false)
	bindTickSession(t, sw, tickSessionA, "mythos", from)
	bindTickSession(t, sw, tickSessionB, "opus", sw.DefaultProfileDir())
	for _, command := range []string{"list", "status"} {
		t.Run(command, func(t *testing.T) {
			code, out, msg := runSub(t, command, "--json")
			if code != 0 {
				t.Fatalf("%s: %d %s", command, code, msg)
			}
			doc := decodeJSON(t, out)
			m, _ := doc["mythos"].(map[string]any)
			if m["number"] != 2.0 || m["linkedSessions"] != 1.0 || !reflect.DeepEqual(m["countedWindows"], []any{"5h", "7d", "Fable"}) {
				t.Fatalf("%s Mythos login: %v", command, m)
			}
			if command == "list" {
				for _, row := range doc["accounts"].([]any) {
					a := row.(map[string]any)
					login, _ := a["mythosLogin"].(map[string]any)
					switch a["number"] {
					case 2.0:
						if login["live"] != true || login["selectable"] != false {
							t.Fatalf("selected: %v", a)
						}
					case 3.0:
						if login["status"] != "ok" || login["selectable"] != true {
							t.Fatalf("ready: %v", a)
						}
					case 4.0:
						if login["status"] != "sign in again" || login["selectable"] != false {
							t.Fatalf("missing login: %v", a)
						}
					}
				}
			}
			code, human, msg := runHuman(t, command)
			if code != 0 || !strings.Contains(human, "Mythos login:") || !strings.Contains(human, "counting 5h + 7d + Fable, 1 linked session") {
				t.Fatalf("human %s: %d %q %s", command, code, human, msg)
			}
		})
	}
}

func TestDashboardAndTrayShowTheMeteredMythosWindow(t *testing.T) {
	sw, from, _ := mythosTickFixture(t, 20, 30, false)
	bindTickSession(t, sw, tickSessionA, "mythos", from)
	_, _, get := serveDashboardPage(t, sw, testOwner)
	var state web.State
	if err := json.Unmarshal([]byte(get("/api/state")), &state); err != nil {
		t.Fatal(err)
	}
	if state.Mythos["number"] != 2.0 || state.Mythos["linkedSessions"] != 1.0 ||
		!reflect.DeepEqual(state.Mythos["countedWindows"], []any{"5h", "7d", "Fable"}) {
		t.Errorf("GET /api/state Mythos summary: %v", state.Mythos)
	}
	sh, _, _ := newTestShell(t)
	const want = "counting 5h + 7d + Fable"
	if got := sh.panel(state).MythosSummary; !strings.Contains(got, want+", 1 linked session") {
		t.Errorf("tray Mythos summary: %q", got)
	}
	views := renderPage(t, servedPage(get), []pageCase{{Name: "mythos", State: state}})
	if got := views["mythos"].MythosDetail; got != want+" · 1 linked session" {
		t.Errorf("dashboard Mythos summary: %q", got)
	}
}

func TestUnreadableMythosSelectionIsReported(t *testing.T) {
	sw, _, _ := mythosTickFixture(t, 20, 30, false)
	writeFile(t, filepath.Join(sw.BackupDir(), "mythos", "selected"), "")
	code, out, msg := runSub(t, "status", "--json")
	if code != 0 {
		t.Fatalf("status: %d %s", code, msg)
	}
	m, _ := decodeJSON(t, out)["mythos"].(map[string]any)
	if reason, _ := m["reason"].(string); !strings.Contains(reason, "unreadable") || m["linkedSessions"] != nil {
		t.Fatalf("unreadable selection reported as %v", m)
	}
}

func TestSoleReadyMythosLoginExplainsMissingChoice(t *testing.T) {
	sw, from, other := mythosTickFixture(t, 20, 30, false)
	bindTickSession(t, sw, tickSessionA, "mythos", from)
	credential := filepath.Join(other, ".credentials.json")
	if err := os.Remove(credential); err != nil {
		t.Fatal(err)
	}
	status := func() map[string]any {
		t.Helper()
		code, out, msg := runSub(t, "status", "--json")
		if code != 0 {
			t.Fatalf("status: %d %s", code, msg)
		}
		m, _ := decodeJSON(t, out)["mythos"].(map[string]any)
		return m
	}
	note, _ := status()["choiceNote"].(string)
	if !strings.Contains(strings.ToLower(note), "only ready mythos login") {
		t.Fatalf("single selected login lacks its explanation: %q", note)
	}
	// A directory is reliably unreadable as a credential, even for a privileged test user.
	if err := os.Mkdir(credential, 0700); err != nil {
		t.Fatal(err)
	}
	m := status()
	if m["choiceNote"] != nil || m["reason"] == nil {
		t.Fatalf("unreadable alternative was classified as absent: %v", m)
	}
}
