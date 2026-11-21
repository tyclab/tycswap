package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/logging"
)

func sessionMoveLogLines(t *testing.T, sw *core.Switcher, id string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")), "\n") {
		rec, ok := logging.ParseLine(line)
		if ok && rec.Level == logging.LevelInfo && strings.Contains(rec.Message, id[:8]) {
			if strings.Contains(rec.Message, id) {
				t.Errorf("expected an abbreviated session ID: %s", rec.Message)
			}
			lines = append(lines, rec.Message)
		}
	}
	return lines
}

func requireMoveLog(t *testing.T, sw *core.Switcher, id, actor, from, to string) {
	t.Helper()
	lines := sessionMoveLogLines(t, sw, id)
	if len(lines) != 1 {
		t.Fatalf("committed move from %s to %s by %s produced %d session INFO lines: %q", from, to, actor, len(lines), lines)
	}
	left, right, ok := strings.Cut(lines[0], from)
	if !ok || !strings.Contains(right, to) || !strings.Contains(lines[0], actor) || strings.Contains(left, to) {
		t.Fatalf("missing actor or wrong login identities/direction: %q", lines[0])
	}
}

func TestSessionMoveLogActors(t *testing.T) {
	for _, actor := range []string{"use", "status line", "hook"} {
		t.Run(actor, func(t *testing.T) {
			sw, launch := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
			link := linkedTestStart(t, launch, placementSession, "opus")
			for range 2 {
				switch actor {
				case "use":
					if code, _, msg := runCLI(t, []string{"use", "fable", "--session", placementSession}, false, false); code != 0 {
						t.Fatal(msg)
					}
				case "status line":
					sessionStatus(t, link, placementSession, "claude-fable-5-2", true)
				case "hook":
					t.Setenv(secureStorageEnv, link)
					t.Setenv(startHoldEnv, "")
					raw := fmt.Sprintf(`{"source":"resume","session_id":%q,"model":"claude-fable-5-2"}`, placementSession)
					if code, out, msg := runCodex(t, raw, "start", "--started"); code != 0 || out != "" || msg != "" {
						t.Fatalf("hook: %d %s %s", code, out, msg)
					}
				}
			}
			requireLinkTarget(t, link, fableStoreDir())
			requireMoveLog(t, sw, placementSession, actor, "default login (Account-1)", "Fable store (Account-2)")
		})
	}
	for _, actor := range []string{"tick", "switch --store mythos"} {
		t.Run(actor, func(t *testing.T) {
			sw, from, to := mythosTickFixture(t, 100, 30, false)
			link := bindTickSession(t, sw, placementSession, "mythos", from)
			if code, _, msg := runCodex(t, "", "auto", "--once", "--dry-run"); code != 0 {
				t.Fatal(msg)
			}
			for range 2 {
				if actor == "tick" {
					runCodex(t, "", "auto", "--once")
				} else if _, err := switchMythos(sw, "3"); err != nil {
					t.Fatal(err)
				}
			}
			requireLinkTarget(t, link, to)
			requireMoveLog(t, sw, placementSession, actor, "Mythos login (Account-2)", "Mythos login (Account-3)")
		})
	}
}

func TestSessionMoveLogDeduplicatesChangedRefusals(t *testing.T) {
	sw, launch := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	link := linkedTestStart(t, launch, placementSession, "opus")
	lock := filepath.Join(sw.DefaultProfileDir(), ".oauth_refresh.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		old := time.Now().Add(-time.Duration(i+1) * time.Hour)
		if err := os.Chtimes(lock, old, old); err != nil {
			t.Fatal(err)
		}
		if msg := sessionStatus(t, link, placementSession, "claude-fable-5-2", false); !strings.Contains(msg, ".oauth_refresh.lock") {
			t.Fatal(msg)
		}
	}
	for range 2 {
		if msg := sessionStatus(t, link, placementSession, "claude-mythos-5-2", false); !strings.Contains(strings.ToLower(msg), "mythos") {
			t.Fatal(msg)
		}
	}
	sessionStatus(t, link, placementSession, "claude-fable-5-2", false)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	sessionStatus(t, link, placementSession, "claude-fable-5-2", true)
	requireLinkTarget(t, link, fableStoreDir())
	lines := sessionMoveLogLines(t, sw, placementSession)
	if len(lines) != 4 {
		t.Fatalf("want three changes of refusal reason followed by one commit; got %q", lines)
	}
	for i, word := range []string{"refresh", "mythos", "refresh"} {
		line := strings.ToLower(lines[i])
		if !strings.Contains(line, "defer") || !strings.Contains(line, word) || !strings.Contains(line, "status line") {
			t.Fatalf("defer %d: %q", i, lines[i])
		}
	}
	if !strings.Contains(lines[3], "Fable store (Account-2)") || !strings.Contains(lines[3], "status line") {
		t.Fatal(lines[3])
	}
}
