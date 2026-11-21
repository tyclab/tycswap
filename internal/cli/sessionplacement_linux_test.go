package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
)

const placementSession = "9421a2ec-67ef-4ff6-9e22-dad93a8d0b87"

func checkLinkTarget(t *testing.T, link, target string) {
	t.Helper()
	got, err := filepath.EvalSymlinks(link)
	if err != nil || got != target {
		t.Fatalf("resolve %q = %q, %v; want %q", link, got, err, target)
	}
}

func sessionStatus(t *testing.T, link, session, model string, observe bool) string {
	t.Helper()
	t.Setenv(secureStorageEnv, link)
	raw := fmt.Sprintf(`{"session_id":%q,"model":{"id":%q}}`, session, model)
	if observe {
		_, _, errb := runCodex(t, raw, "auto", "--observe")
		if errb != "" {
			t.Fatal(errb)
		}
		return ""
	}
	code, line, errb := runCodex(t, raw, "start", "--check")
	if code != 0 || errb != "" || strings.Count(line, "\n") > 1 {
		t.Fatalf("status check = %d, %q, %q", code, line, errb)
	}
	return line
}

func TestSessionUseHoldsForModelAndOnlyMovesItsSession(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	setRecord(t, sw, "3", "mythos", true)
	mythos := store.MythosLoginDir(sw.BackupDir(), fableEmail("3"), "")
	writeFile(t, filepath.Join(mythos, ".credentials.json"), dryCreds("independent", "fixture-refresh"))
	link := linkedTestStart(t, rec, placementSession, "opus")
	other := linkedTestStart(t, rec, "704ac64c-eed0-43b4-b091-65045b486cc2", "opus")
	def := sw.DefaultProfileDir()
	before := defaultLive(t, sw)
	t.Setenv(secureStorageEnv, link)
	if code, _, errb := runCLI(t, []string{"use", "mythos"}, false, false); code != 0 {
		t.Fatalf("use in session = %d: %s", code, errb)
	}
	checkLinkTarget(t, link, mythos)
	if line := sessionStatus(t, link, placementSession, "claude-opus-5-5", false); line != "Mythos login ready: /model claude-mythos-5-2\n" {
		t.Fatalf("pending Mythos model: %q", line)
	}
	sessionStatus(t, link, placementSession, "claude-opus-5-5", true)
	checkLinkTarget(t, link, mythos)
	sessionStatus(t, link, placementSession, "claude-mythos-5-2", true)
	sessionStatus(t, link, placementSession, "", false)
	checkLinkTarget(t, link, mythos)
	sessionStatus(t, link, placementSession, "claude-fable-5-2", true)
	checkLinkTarget(t, link, paths.FableStoreDir(sw.BackupDir()))
	sessionStatus(t, link, placementSession, "claude-sonnet-5-5", false)
	checkLinkTarget(t, link, def)
	if code, _, errb := runCLI(t, []string{"use", "fable", "--session", placementSession}, false, false); code != 0 {
		t.Fatalf("explicit session: %d %s", code, errb)
	}
	checkLinkTarget(t, link, paths.FableStoreDir(sw.BackupDir()))
	if code, _, errb := runCLI(t, []string{"use", "default", "--session", placementSession}, false, false); code != 0 {
		t.Fatalf("use default: %d %s", code, errb)
	}
	checkLinkTarget(t, link, def)
	checkLinkTarget(t, other, def)
	if defaultLive(t, sw) != before {
		t.Fatal("session placement replaced the shared login")
	}
}

func TestSessionPlacementUnavailable(t *testing.T) {
	for _, reason := range []string{"no login", "no room", "disabled"} {
		t.Run(reason, func(t *testing.T) {
			fx := fableTickFixture{def: "1", off: true, usage: map[string][3]float64{"2": {20, 100, 30}}}
			sw, rec := fableStartLogin(t, fx)
			link := linkedTestStart(t, rec, placementSession, "opus")
			if reason != "no login" {
				setRecord(t, sw, "2", "mythos", true, "disabled", reason == "disabled")
				dir := store.MythosLoginDir(sw.BackupDir(), fableEmail("2"), "")
				writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("independent", "fixture-refresh"))
			}
			if code, _, errb := runCLI(t, []string{"use", "mythos", "--session", placementSession}, false, false); code != 1 || errb == "" {
				t.Fatalf("unavailable use: %d %q", code, errb)
			}
			if line := sessionStatus(t, link, placementSession, "claude-mythos-5-2", false); !strings.Contains(strings.ToLower(line), "mythos") {
				t.Fatalf("no explanation for placement failure: %q", line)
			}
			checkLinkTarget(t, link, sw.DefaultProfileDir())
		})
	}
}

func TestSessionMoveAsksOnceAndExplicitUseBypassesRefusal(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", off: true})
	link := linkedTestStart(t, rec, placementSession, "opus")
	setRecord(t, sw, "2", "mythos", true, store.ModelsKey, []string{"opus"})
	dir := store.MythosLoginDir(sw.BackupDir(), fableEmail("2"), "")
	writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("independent", "fixture-refresh"))
	b := installAllocationBroker(t)
	line := b.decide(t, func() string { return sessionStatus(t, link, placementSession, "claude-mythos-5-2", false) }, func(question, detail string) bool {
		if !strings.Contains(detail, placementSession) || !strings.Contains(question, fableEmail("2")) {
			t.Fatalf("allocation question %q, %q", question, detail)
		}
		return false
	})
	if line == "" || sessionStatus(t, link, placementSession, "claude-mythos-5-2", false) == "" {
		t.Fatal("missing placement status")
	}
	checkLinkTarget(t, link, sw.DefaultProfileDir())
	if code, _, errb := runCLI(t, []string{"use", "mythos", "--session", placementSession}, false, false); code != 0 {
		t.Fatalf("explicit use: %d %q", code, errb)
	}
	checkLinkTarget(t, link, dir)
	if asked := len(b.requests()); asked != 1 {
		t.Fatalf("allocation questions = %d, want 1", asked)
	}
}

func TestUseRejectsMissingOrUnreadableSessionRecords(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", off: true})
	link := linkedTestStart(t, rec, placementSession, "opus")
	p := filepath.Join(sw.BackupDir(), "default_models.json")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", `{}`, `{"sessions":null}`, `{"sessions":{}`, `{"sessions":{"` + placementSession + `":{"family":"opus"}}}`} {
		if raw != "" {
			writeFile(t, p, raw)
		}
		t.Setenv(secureStorageEnv, link)
		if code, _, errb := runCLI(t, []string{"use", "default"}, false, false); code != 1 || errb == "" {
			t.Fatalf("record %q: code=%d stderr=%q", raw, code, errb)
		}
		checkLinkTarget(t, link, sw.DefaultProfileDir())
	}
}

func TestSessionApprovalRechecksPlacement(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprint("account changed ", change), func(t *testing.T) {
			sw, rec := fableStartLogin(t, fableTickFixture{def: "1", off: true})
			link := linkedTestStart(t, rec, placementSession, "opus")
			setRecord(t, sw, "2", "mythos", true, store.ModelsKey, []string{"opus"})
			dir := store.MythosLoginDir(sw.BackupDir(), fableEmail("2"), "")
			writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("independent", "fixture-refresh"))
			b := installAllocationBroker(t)
			line := b.decide(t, func() string { return sessionStatus(t, link, placementSession, "claude-mythos-5-2", false) }, func(_, _ string) bool {
				if change {
					setRecord(t, sw, "2", "mythos", false)
				}
				return true
			})
			if change {
				checkLinkTarget(t, link, sw.DefaultProfileDir())
				if line == "" {
					t.Fatal("changed placement had no explanation")
				}
			} else {
				checkLinkTarget(t, link, dir)
			}
			if asks := len(b.requests()); asks != 1 {
				t.Fatalf("questions = %d", asks)
			}
		})
	}
}

func TestUnlinkedLegacySessionKeepsItsLoginWhileLinkedSessionsRun(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	sibling := linkedTestStart(t, rec, placementSession, "opus")
	const legacy = "5442dd68-51f6-4dac-80b4-d1232ada8411"
	for _, observe := range []bool{true, false} {
		line := sessionStatus(t, paths.FableStoreDir(sw.BackupDir()), legacy, "claude-opus-5-5", observe)
		if !observe && !strings.Contains(line, "Opus session on the Fable store") {
			t.Fatalf("legacy warning = %q", line)
		}
	}
	if code, _, notice := runCLI(t, []string{"use", "default", "--session", legacy}, false, false); code != 1 || notice == "" {
		t.Fatalf("unlinked use = %d, %q", code, notice)
	}
	entries, err := os.ReadDir(filepath.Dir(sibling))
	if err != nil || len(entries) != 1 {
		t.Fatalf("legacy session acquired a link: %v, %v", entries, err)
	}
	checkLinkTarget(t, sibling, sw.DefaultProfileDir())
}

func TestSessionUseRefusesWhileRefreshIsBusy(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	link := linkedTestStart(t, rec, placementSession, "opus")
	lock := filepath.Join(link, ".oauth_refresh.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	code, _, msg := runCLI(t, []string{"use", "fable", "--session", placementSession}, false, false)
	if code != 1 || !strings.Contains(msg, ".oauth_refresh.lock") || !strings.Contains(msg, "2h") {
		t.Fatalf("busy refresh = %d, %q", code, msg)
	}
	checkLinkTarget(t, link, sw.DefaultProfileDir())
	if err := os.Remove(lock); err != nil {
		t.Fatalf("lost client's lock: %v", err)
	}
	if code, _, msg := runCLI(t, []string{"use", "fable", "--session", placementSession}, false, false); code != 0 {
		t.Fatalf("retry = %d, %q", code, msg)
	}
	checkLinkTarget(t, link, paths.FableStoreDir(sw.BackupDir()))
}
