package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

func TestPickerResumeRecoversTheSessionsMythosLogin(t *testing.T) {
	const id = "40124a8b-9862-4a4d-a541-339b87cdbe96"
	if dir := os.Getenv("TYCSWAP_TEST_PREVIOUS_MYTHOS"); dir != "" {
		root := paths.GetBackupRoot()
		link, err := reporting.CreateSessionLink(root, dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := reporting.BindSessionLink(root, id, link, "mythos", "mythos/"+filepath.Base(dir), time.Now()); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, reason := range []string{"eligible", "at limit", "disabled", "allocation declined", "disabled during approval"} {
		t.Run(reason, func(t *testing.T) {
			spent := 60.0
			if reason == "at limit" {
				spent = 100
			}
			sw, saved, selected := mythosTickFixture(t, spent, 5, true)
			root := sw.BackupDir()
			if err := store.SeedMythosSelection(root, selected); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestPickerResumeRecoversTheSessionsMythosLogin$")
			child.Env = append(os.Environ(), "TYCSWAP_TEST_PREVIOUS_MYTHOS="+saved)
			if out, err := child.CombinedOutput(); err != nil {
				t.Fatalf("previous session: %s %v", out, err)
			}
			if reason == "disabled" {
				if err := sw.SetAccountDisabled("2", true); err != nil {
					t.Fatal(err)
				}
			}
			ask := reason == "allocation declined" || reason == "disabled during approval"
			if ask {
				setRecord(t, sw, "2", store.ModelsKey, []string{"opus"})
			}
			oldResolve, oldExec, oldAsk := startResolve, startExec, askPlacement
			t.Cleanup(func() { startResolve, startExec, askPlacement = oldResolve, oldExec, oldAsk })
			var link string
			startResolve = func(string) (string, error) { return "/fixture/claude", nil }
			startExec = func(_ string, _ []string, env []string) (int, error) {
				link = assertNativeStore(t, env, selected)
				return 23, nil
			}

			if code, _, msg := runCodex(t, "", "start", "--", "claude", "--model", "mythos", "--resume"); code != 23 {
				t.Fatalf("picker: %d %s", code, msg)
			}
			t.Setenv(secureStorageEnv, link)
			hook := func() string {
				code, out, msg := runCodex(t, `{"source":"resume","session_id":"`+id+`","model":"mythos"}`, "start", "--started")
				if code != 0 || out != "" || msg != "" {
					t.Errorf("resume hook: %d %q %q", code, out, msg)
				}
				return out
			}
			if ask {
				b := installAllocationBroker(t)
				b.decide(t, hook, func(q, _ string) bool {
					if !strings.Contains(q, "opus") {
						t.Fatalf("unexpected question %q", q)
					}
					if reason == "disabled during approval" {
						if err := sw.SetAccountDisabled("2", true); err != nil {
							t.Fatal(err)
						}
						return true
					}
					return false
				})
			} else {
				hook()
			}
			want := selected
			if reason == "eligible" {
				want = saved
			}
			requireLinkTarget(t, link, want)
			if current, err := store.SelectedMythosDigest(root); err != nil || current != filepath.Base(selected) {
				t.Fatalf("resuming switched the global selection: %q %v", current, err)
			}
		})
	}
}
