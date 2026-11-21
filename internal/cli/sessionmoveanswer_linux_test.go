package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

func TestSessionMoveLogAllocationReply(t *testing.T) {
	sw, launch := fableStartLogin(t, fableTickFixture{def: "1", off: true})
	link := linkedTestStart(t, launch, placementSession, "opus")
	setRecord(t, sw, "2", "mythos", true, store.ModelsKey, []string{"opus"})
	target := store.MythosLoginDir(sw.BackupDir(), fableEmail("2"), "")
	writeFile(t, filepath.Join(target, ".credentials.json"), dryCreds("independent", "fixture-refresh"))
	b := installAllocationBroker(t)
	b.decide(t, func() string { return sessionStatus(t, link, placementSession, "claude-mythos-5-2", false) }, func(string, string) bool { return false })
	requireLinkTarget(t, link, sw.DefaultProfileDir())
	requireReasons := func() {
		t.Helper()
		lines := sessionMoveLogLines(t, sw, placementSession)
		if len(lines) != 2 || !strings.Contains(strings.ToLower(lines[0]), "waiting") || !strings.Contains(strings.ToLower(lines[1]), "allocation decision deferred") {
			t.Fatalf("the detached answer changed waiting to declined; got %q", lines)
		}
	}
	requireReasons()
	sessionStatus(t, link, placementSession, "claude-mythos-5-2", false)
	requireReasons()
}

func TestSessionMoveLogSeparateCommandsShareARefusal(t *testing.T) {
	sw, from, to := mythosTickFixture(t, 100, 30, false)
	link := bindTickSession(t, sw, placementSession, "mythos", from)
	lock := filepath.Join(from, ".oauth_refresh.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := switchMythos(sw, "3"); err == nil {
			t.Fatal("missing guarded refusal")
		}
		runCodex(t, "", "auto", "--once")
	}
	requireLinkTarget(t, link, from)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if _, err := switchMythos(sw, "3"); err != nil {
		t.Fatal(err)
	}
	requireLinkTarget(t, link, to)
	lines := sessionMoveLogLines(t, sw, placementSession)
	if len(lines) != 2 || !strings.Contains(lines[0], "refresh") || !strings.Contains(lines[0], "switch --store mythos") || !strings.Contains(lines[1], "Mythos login (Account-3)") {
		t.Fatalf("want one common guard reason and one commit: %q", lines)
	}
}
