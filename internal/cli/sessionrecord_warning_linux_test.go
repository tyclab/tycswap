package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
)

func TestBrokenBindingWarnsOnceAcrossCommands(t *testing.T) {
	const id = "43e1087b-ea00-402d-8df1-78d6ccd04780"
	if os.Getenv("TYCSWAP_TEST_BINDING_WARNING") == "1" {
		os.Args = []string{"tycswap", "use", "default", "--session", id}
		if code := Main(); code != 1 {
			t.Fatalf("damaged binding exit %d", code)
		}
		return
	}
	cleanHome(t)
	root := paths.GetBackupRoot()
	if err := os.MkdirAll(filepath.Join(root, "links"), 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "default_models.json"), `{"sessions":{"`+id+`":{"family":"opus","link":"bad"}}}`)
	for i := 0; i < 3; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBrokenBindingWarnsOnceAcrossCommands$")
		cmd.Env = append(os.Environ(), "TYCSWAP_TEST_BINDING_WARNING=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command: %s %v", out, err)
		}
	}
	raw := readOrAbsent(t, filepath.Join(root, "tycswap.log"))
	count := 0
	for line := range strings.SplitSeq(raw, "\n") {
		if strings.Contains(line, " - WARNING - ") && strings.Contains(line, id) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("warnings naming the damaged session = %d, want 1; log %q", count, raw)
	}
}
