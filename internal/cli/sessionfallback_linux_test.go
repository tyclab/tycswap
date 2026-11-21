package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

func TestFreshMythosLinkFailureUsesOriginalRanking(t *testing.T) {
	for _, same := range []bool{false, true} {
		name := "different selected login"
		if same {
			name = "same selected login needs an exception"
		}
		t.Run(name, func(t *testing.T) {
			sw, first, second := mythosTickFixture(t, 10, 65, true)
			root := sw.BackupDir()
			prevResolve, prevExec, prevAsk := startResolve, startExec, askPlacement
			t.Cleanup(func() { startResolve, startExec, askPlacement = prevResolve, prevExec, prevAsk })
			startResolve = func(name string) (string, error) { return filepath.Join("/fixture", name), nil }
			var launched string
			startExec = func(_ string, _ []string, env []string) (int, error) {
				launched = envValue(env, secureStorageEnv)
				return 23, nil
			}
			launch := func(command string) string {
				t.Helper()
				code, _, notice := runCodex(t, "", "start", "--", command, "--model", "mythos")
				if code != 23 {
					t.Fatalf("%s start exited %d: %s", command, code, notice)
				}
				return notice
			}
			launch("command")
			original := launched
			selected := first
			if original == first {
				selected = second
			} else if original != second {
				t.Fatalf("unexpected direct login %s", original)
			}
			questions := 0
			if same {
				selected = original
				for _, num := range []string{"2", "3"} {
					setRecord(t, sw, num, store.ModelsKey, []string{"opus"})
				}
				askPlacement = func(ioStreams, string, string, bool) (bool, error) { questions++; return true, nil }
			}
			if err := store.SeedMythosSelection(root, selected); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(root, "links"), "not a directory")
			msg := launch("claude")
			if strings.Count(msg, "\n") != 1 || !strings.Contains(strings.ToLower(msg), "link") {
				t.Fatalf("link failure notice %q", msg)
			}
			if launched != original {
				t.Fatalf("link failure used %s; original direct start used %s", launched, original)
			}
			if same && questions != 1 {
				t.Fatalf("allocation asked %d times; expected one decision", questions)
			}
		})
	}
}
