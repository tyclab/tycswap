package cli

import (
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/tray"
)

func mythosActorFixture(t *testing.T) (*core.Switcher, string) {
	t.Helper()
	sw, _ := (fableTickFixture{def: "1", off: true}).build(t)
	from, _ := addMythosTestLogins(t, sw)
	if err := store.SeedMythosSelection(sw.BackupDir(), from); err != nil {
		t.Fatal(err)
	}
	for _, num := range []string{"2", "3"} {
		writeMythosTestUsage(t, sw, num, 53, 20, "", "")
	}
	return sw, from
}

func checkMythosSelectionLog(t *testing.T, sw *core.Switcher, actor string) {
	t.Helper()
	var selections []string
	for _, msg := range logLines(t, sw) {
		if strings.Contains(msg, "Mythos login") && !strings.HasPrefix(msg, "Session ") {
			selections = append(selections, msg)
		}
	}
	want := regexp.MustCompile("^Switched the Mythos login to account 3 by " + actor + "$")
	if len(selections) != 1 || !want.MatchString(selections[0]) {
		t.Errorf("selection INFO lines = %q, want one matching %s", selections, want)
	}
}

func TestMythosSelectionActorFromCommandAndMenu(t *testing.T) {
	t.Run("command", func(t *testing.T) {
		sw, _ := mythosActorFixture(t)
		for range 2 {
			if code, _, msg := runSub(t, "switch", "--store", "mythos", fableEmail("3")); code != 0 {
				t.Fatal(msg)
			}
		}
		checkMythosSelectionLog(t, sw, regexp.QuoteMeta(wantCLIActor(t)))
	})
	t.Run("menu", func(t *testing.T) {
		sw, _ := mythosActorFixture(t)
		t.Setenv(childRoleEnv, "session-placement-cli")
		menuScript(t, "claude #3", "Use for Mythos")
		menuManage(ioStreams{out: io.Discard, err: io.Discard})
		selected, err := store.SelectedMythosDigest(sw.BackupDir())
		if err != nil || selected != filepath.Base(store.MythosLoginDir(sw.BackupDir(), fableEmail("3"), "")) {
			t.Fatalf("menu did not select Account-3: %v", err)
		}
		checkMythosSelectionLog(t, sw, "menu")
	})
}

func TestDashboardMythosSelectionAndSessionMoveActors(t *testing.T) {
	sw, from := mythosActorFixture(t)
	link := bindTickSession(t, sw, tickSessionA, "mythos", from)
	_, post := serveDashboard(t, sw, testOwner)
	for range 2 {
		if status, _ := post("/api/sessions/"+tickSessionA+"/use", `{"store":"default"}`); status != 200 {
			t.Fatalf("move returned %d", status)
		}
		if status, _ := post("/api/switch/claude:"+fableEmail("3")+"?store=mythos", ""); status != 200 {
			t.Fatalf("select returned %d", status)
		}
	}
	requireLinkTarget(t, link, sw.DefaultProfileDir())
	checkMythosSelectionLog(t, sw, `dashboard\(cookie,127\.0\.0\.1:[0-9]+\)`)
	requireMoveLog(t, sw, tickSessionA, "by dashboard(cookie,127.0.0.1:", "Mythos login (Account-2)", "default login (Account-1)")
}

func TestTrayMythosSelectionAndSessionMoveActors(t *testing.T) {
	for _, surface := range []string{"local", "remote"} {
		t.Run(surface, func(t *testing.T) {
			appTestHome(t)
			sw, from := mythosActorFixture(t)
			link := bindTickSession(t, sw, tickSessionA, "mythos", from)
			ft := &blockingTray{done: make(chan struct{})}
			var opts atomic.Pointer[tray.Options]
			prev := newTray
			newTray = func(_ tray.Icon, o tray.Options) (tray.Tray, error) { opts.Store(&o); return ft, nil }
			t.Cleanup(func() { newTray = prev })
			stop := stopViaNotifyContext(t)
			var errb syncBuffer
			done := runApp([]string{"--port", "0", "--no-update-check"}, &errb)
			t.Cleanup(func() {
				stop()
				ft.Quit()
				if code := waitExit(t, done, &errb); code != 0 {
					t.Errorf("app exited %d", code)
				}
			})
			waitUntil(t, "tray menu", func() bool { _, ok := ft.item("open"); return ok })
			if surface == "local" {
				for range 2 {
					opts.Load().OnClick("use:" + tickSessionA + ":default")
					opts.Load().OnClick("mythos:claude:3")
				}
			} else {
				rc, _, err := runningDashboard()
				if err != nil || rc == nil {
					t.Fatalf("remote connection: %v", err)
				}
				for range 2 {
					if err := rc.UseSession(tickSessionA, "default"); err != nil {
						t.Fatal(err)
					}
					if _, err := rc.SwitchMythos("claude:3"); err != nil {
						t.Fatal(err)
					}
				}
			}
			requireLinkTarget(t, link, sw.DefaultProfileDir())
			checkMythosSelectionLog(t, sw, `tray(\(bearer,127\.0\.0\.1:[0-9]+\))?`)
			requireMoveLog(t, sw, tickSessionA, "by tray", "Mythos login (Account-2)", "default login (Account-1)")
		})
	}
}
