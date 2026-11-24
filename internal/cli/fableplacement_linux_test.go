// Tests for the Fable placement rule on linked sessions (DESIGN A112, A109).
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/reporting"
)

// bindFableSession links a session of this process as Fable on target, the
// Fable store or the default login, as its SessionStart hook binds it.
func bindFableSession(t *testing.T, sw *core.Switcher, id, target string) string {
	t.Helper()
	link, err := reporting.CreateSessionLink(sw.BackupDir(), target)
	if err != nil {
		t.Fatal(err)
	}
	scope := ""
	if target == fableStoreDir() {
		scope = "fable"
	}
	if err := reporting.BindSessionLink(sw.BackupDir(), id, link, "claude-fable-5-2", scope, sw.Clk.Now()); err != nil {
		t.Fatal(err)
	}
	return link
}

// A native Fable start, and the resume of a session whose saved link names
// the Fable store, link the default login while no Fable login has room.
func TestFableStartWithoutFableRoomLinksTheDefaultLogin(t *testing.T) {
	for name, args := range map[string][]string{
		"fresh start": {"start", "--", "claude", "--model", "fable"},
		"resume":      {"start", "--", "claude", "--resume", placementSession},
	} {
		t.Run(name, func(t *testing.T) {
			sw, rec := fableStartLogin(t, poolAtFableLimit, "1", "2")
			if name == "resume" {
				bindFableSession(t, sw, placementSession, fableStoreDir())
			}
			startResolve = func(string) (string, error) { return "/fixture/claude", nil }
			code, _, stderr := runCLI(t, args, false, false)
			if code != 23 || !strings.Contains(stderr, "Fable store: no Fable login has room") {
				t.Fatalf("start: code=%d stderr=%q", code, stderr)
			}
			assertNativeStore(t, rec.env, "")
			if !strings.Contains(storeLive(t, sw), "at-f2") {
				t.Fatalf("the Fable store moved: %s", storeLive(t, sw))
			}
		})
	}
}

// The status line moves a linked Fable session off the Fable store while no
// Fable login has room and the default login's account has, and back once the
// Fable store's account has room again, as before.
func TestStatusLinePlacesAFableSessionByFableRoom(t *testing.T) {
	t.Run("off the Fable store", func(t *testing.T) {
		sw, _ := fableStartLogin(t, poolAtFableLimit, "1", "2")
		link := bindFableSession(t, sw, placementSession, fableStoreDir())
		if line := sessionStatus(t, link, placementSession, "claude-fable-5-2", false); line != "" {
			t.Fatalf("status line %q", line)
		}
		requireLinkTarget(t, link, sw.DefaultProfileDir())
		requireMoveLog(t, sw, placementSession, "status line", "Fable store (Account-2)", "default login (Account-1)")
	})
	t.Run("back once the Fable store's account has room", func(t *testing.T) {
		sw, _ := fableStartLogin(t, poolAtFableLimit, "1", "2")
		link := bindFableSession(t, sw, placementSession, sw.DefaultProfileDir())
		sessionStatus(t, link, placementSession, "claude-fable-5-2", false)
		requireLinkTarget(t, link, sw.DefaultProfileDir())
		setFablePct(t, sw, "2", 10)
		sessionStatus(t, link, placementSession, "claude-fable-5-2", false)
		requireLinkTarget(t, link, fableStoreDir())
		requireMoveLog(t, sw, placementSession, "status line", "default login (Account-1)", "Fable store (Account-2)")
	})
}

// A held choice is not ruled: a session on the Fable store whose record holds
// fable, as tycswap use fable leaves it before Claude reports the model,
// stays there through status-line observations and a tick, with no move or
// deferral line, while no Fable login has room.
func TestHeldFableChoiceIsNotPlacedByFableRoom(t *testing.T) {
	sw, _ := fableStartLogin(t, poolAtFableLimit, "1", "2")
	link, err := reporting.CreateSessionLink(sw.BackupDir(), fableStoreDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := reporting.BindSessionLink(sw.BackupDir(), placementSession, link, "claude-opus-5-5", "fable", sw.Clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := reporting.UpdateSessionLink(sw.BackupDir(), placementSession, link, sw.Clk.Now(), func(rec *reporting.SessionLink) error {
		rec.Wanted = "fable"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ model, line string }{
		{"claude-opus-5-5", "Fable login ready: /model fable\n"},
		{"claude-fable-5-2", ""},
	} {
		if line := sessionStatus(t, link, placementSession, step.model, false); line != step.line {
			t.Fatalf("status line for %s: %q, want %q", step.model, line, step.line)
		}
		requireLinkTarget(t, link, fableStoreDir())
	}
	runCodex(t, "", "auto", "--once", "--json")
	requireLinkTarget(t, link, fableStoreDir())
	if lines := sessionMoveLogLines(t, sw, placementSession); len(lines) != 0 {
		t.Fatalf("log: %q", lines)
	}
}

// After the Fable store's tick an idle linked Fable session gets the same
// placement through the move guard; a dry run and auto-switch off move
// nothing, and a busy refresh lock defers the move to a later tick.
func TestTickPlacesLinkedFableSessionsByFableRoom(t *testing.T) {
	t.Run("moved", func(t *testing.T) {
		sw, _ := fableStartLogin(t, poolAtFableLimit, "1", "2")
		link := bindFableSession(t, sw, tickSessionA, fableStoreDir())
		runCodex(t, "", "auto", "--once", "--json", "--dry-run")
		requireLinkTarget(t, link, fableStoreDir())
		runCodex(t, "", "auto", "--once", "--json")
		requireLinkTarget(t, link, sw.DefaultProfileDir())
		requireMoveLog(t, sw, tickSessionA, "tick", "Fable store (Account-2)", "default login (Account-1)")
	})
	t.Run("auto-switch off", func(t *testing.T) {
		fx := poolAtFableLimit
		fx.off = true
		sw, _ := fableStartLogin(t, fx, "1", "2")
		link := bindFableSession(t, sw, tickSessionA, fableStoreDir())
		runCodex(t, "", "auto", "--once", "--json")
		requireLinkTarget(t, link, fableStoreDir())
		if lines := sessionMoveLogLines(t, sw, tickSessionA); len(lines) != 0 {
			t.Fatalf("log: %q", lines)
		}
	})
	t.Run("busy refresh lock", func(t *testing.T) {
		sw, _ := fableStartLogin(t, poolAtFableLimit, "1", "2")
		link := bindFableSession(t, sw, tickSessionA, fableStoreDir())
		lock := filepath.Join(fableStoreDir(), ".oauth_refresh.lock")
		if err := os.Mkdir(lock, 0o700); err != nil {
			t.Fatal(err)
		}
		runCodex(t, "", "auto", "--once", "--json")
		requireLinkTarget(t, link, fableStoreDir())
		saved, ok, err := reporting.LookupSessionLink(sw.BackupDir(), tickSessionA)
		if err != nil || !ok || !strings.Contains(saved.LastMoveDeferral, ".oauth_refresh.lock") {
			t.Fatalf("deferral %q (%v, %v)", saved.LastMoveDeferral, ok, err)
		}
		if err := os.Remove(lock); err != nil {
			t.Fatalf("the client's lock is gone: %v", err)
		}
		runCodex(t, "", "auto", "--once", "--json")
		requireLinkTarget(t, link, sw.DefaultProfileDir())
		lines := sessionMoveLogLines(t, sw, tickSessionA)
		if len(lines) != 2 || !strings.Contains(lines[0], "deferred by tick") || !strings.Contains(lines[1], "moved from Fable store (Account-2) to default login (Account-1)") {
			t.Fatalf("log: %q", lines)
		}
	})
}
