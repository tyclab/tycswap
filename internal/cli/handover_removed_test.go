package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/web"
)

// The handover after a limit is removed (DESIGN A95): every recovery verb
// exits 2 with one line, before the store is read, like tycswap run (A92).
func TestRecoveryCommandIsGone(t *testing.T) {
	for _, argv := range [][]string{
		{"recovery"}, {"recovery", "list"}, {"recovery", "prepare", "--help"},
		{"recovery", "reclaim", "--cwd", ".", "--confirm-no-background-tools"},
		{"recovery", "observe", "--provider", "codex"}, {"recovery", "dismiss"}, {"recovery", "plan"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			cleanHome(t)
			code, out, errStr := runSub(t, argv...)
			if code != 2 || out != "" {
				t.Fatalf("exit = %d, stdout %q; want 2 and nothing on stdout (stderr %q)", code, out, errStr)
			}
			want := "tycswap: error: tycswap recovery was removed: a session parked on a limit goes on when the login has room.\n"
			if errStr != want {
				t.Fatalf("stderr = %q, want %q", errStr, want)
			}
			if _, err := os.Stat(paths.GetBackupRoot()); !os.IsNotExist(err) {
				t.Fatalf("a removed command touched the store: %v", err)
			}
		})
	}
}

// A former group session's lifecycle hook still runs `recovery record`
// (A78); it fails open, so a prompt or a stop is never blocked.
func TestRecoveryRecordHookFailsOpen(t *testing.T) {
	cleanHome(t)
	var out, errb strings.Builder
	code := run("tycswap", []string{"recovery", "record", "--group", "fable", "--model", "fable"},
		ioStreams{in: strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"s"}`), out: &out, err: &errb}, false, false)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("exit = %d, stdout %q; want 0 and nothing on stdout", code, out.String())
	}
	if !strings.Contains(errb.String(), "recovery was removed") {
		t.Fatalf("stderr = %q", errb.String())
	}
	if _, err := os.Stat(paths.GetBackupRoot()); !os.IsNotExist(err) {
		t.Fatalf("the hook touched the store: %v", err)
	}
}

// The tray never offers a handover, also from an older server's state
// document that still carries one.
func TestTrayHasNoHandoverItem(t *testing.T) {
	var st web.State
	doc := `{"accounts":[],"recovery":{"incidents":[{"event":{"provider":"claude","session_id":"s","incident_id":"one","kind":"Stop","stopped":true},` +
		`"decision":{"action":"offer","reason":"limit","newly_offered":true},"offered":true,"dismissed":false}]}}`
	if err := json.Unmarshal([]byte(doc), &st); err != nil {
		t.Fatal(err)
	}
	shell, ft, _ := newTestShell(t)
	shell.update(st)
	for _, note := range ft.notesNow() {
		if strings.Contains(note, "Session at limit") {
			t.Fatalf("handover notification: %q", note)
		}
	}
	ft.mu.Lock()
	menu := fmt.Sprintf("%+v", ft.menu)
	ft.mu.Unlock()
	panel := fmt.Sprintf("%+v", shell.panel(st))
	for name, text := range map[string]string{"menu": menu, "panel": panel} {
		if strings.Contains(text, "recovery-open") || strings.Contains(text, "Session at limit") || strings.Contains(text, "handover") {
			t.Fatalf("%s offers a handover: %s", name, text)
		}
	}
}
