package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
)

func TestSessionHintEndsWhenRequestedModelIsObserved(t *testing.T) {
	for _, tc := range []struct{ family, model, hint string }{
		{"mythos", "claude-mythos-5-2", "Mythos login ready: /model claude-mythos-5-2\n"},
		{"fable", "claude-fable-5-2", "Fable login ready: /model fable\n"},
	} {
		t.Run(tc.family, func(t *testing.T) {
			sw, launch := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
			setRecord(t, sw, "3", "mythos", true)
			mythos := store.MythosLoginDir(sw.BackupDir(), fableEmail("3"), "")
			writeFile(t, filepath.Join(mythos, ".credentials.json"), dryCreds("independent", "fixture-refresh"))
			link := linkedTestStart(t, launch, placementSession, "opus")
			if code, _, stderr := runCLI(t, []string{"use", tc.family, "--session", placementSession}, false, false); code != 0 {
				t.Fatal(stderr)
			}
			if line := sessionStatus(t, link, placementSession, "claude-opus-5-5", false); line != tc.hint {
				t.Fatalf("waiting for %s: got %q, want %q", tc.family, line, tc.hint)
			}
			for i := 1; i <= 2; i++ {
				if line := sessionStatus(t, link, placementSession, tc.model, false); line != "" {
					t.Errorf("accepted %s, status %d still prints %q", tc.family, i, line)
				}
			}
		})
	}
}

func TestNativeStartAndHookLeaveUnmanagedHomeAlone(t *testing.T) {
	for _, tc := range []string{"no store", "empty roster"} {
		t.Run(tc, func(t *testing.T) {
			startTestLaunch(t)
			root := paths.GetBackupRoot()
			if err := os.MkdirAll(paths.GetClaudeConfigHome(), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc == "empty roster" {
				writeFile(t, filepath.Join(root, "sequence.json"), `{"accounts":{},"sequence":[]}`)
			}
			before := os.Environ()
			var child []string
			startExec = func(_ string, _ []string, env []string) (int, error) {
				child = env
				return 0, nil
			}
			if code, _, stderr := runCLI(t, []string{"start"}, false, false); code != 0 || stderr != "" {
				t.Fatalf("native launch = %d, %q", code, stderr)
			}
			if !reflect.DeepEqual(before, child) {
				t.Errorf("native launch changed the environment: link=%q family=%q", envValue(child, secureStorageEnv), envValue(child, startFamilyEnv))
			}
			for _, key := range []string{secureStorageEnv, startFamilyEnv, startHoldEnv} {
				for _, entry := range child {
					if value, ok := strings.CutPrefix(entry, key+"="); ok {
						t.Setenv(key, value)
					}
				}
			}
			code, out, stderr := runCodex(t, `{"source":"startup","session_id":"`+placementSession+`","model":"opus"}`, "start", "--started")
			if code != 0 || out != "" || stderr != "" {
				t.Fatalf("SessionStart = %d, %q, %q", code, out, stderr)
			}
			entries, err := os.ReadDir(root)
			if tc == "no store" && !os.IsNotExist(err) || tc == "empty roster" && (err != nil || len(entries) != 1 || entries[0].Name() != "sequence.json") {
				t.Errorf("store after launch and hook: entries=%v error=%v", entries, err)
			}
		})
	}
}
