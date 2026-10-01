package reporting

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/testutil"
)

// TestRunningInstancesHeadingNeedsAGroup: a live IDE lock with no (or empty)
// workspaceFolders adds no group, so no "Running instances:" heading prints.
func TestRunningInstancesHeadingNeedsAGroup(t *testing.T) {
	for _, body := range []string{
		`{"pid": PID, "ideName": "VS Code"}`,
		`{"pid": PID, "ideName": "VS Code", "workspaceFolders": []}`,
	} {
		home := t.TempDir()
		testutil.Setenv(t, "HOME", home)
		testutil.Unsetenv(t, "CLAUDE_CONFIG_DIR")
		ideDir := filepath.Join(home, ".claude", "ide")
		if err := os.MkdirAll(ideDir, 0o700); err != nil {
			t.Fatal(err)
		}
		lock := strings.Replace(body, "PID", strconv.Itoa(os.Getpid()), 1)
		if err := os.WriteFile(filepath.Join(ideDir, "1234.lock"), []byte(lock), 0o600); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		renderRunningInstances(&buf)
		if strings.Contains(buf.String(), "Running instances") {
			t.Errorf("%s: heading printed with nothing under it:\n%q", body, buf.String())
		}
	}
}
