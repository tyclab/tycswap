//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/testutil"
)

// TestRunRefusesAGroupWritableStore: every command but help refuses a store
// root writable by group or other, naming the path.
func TestRunRefusesAGroupWritableStore(t *testing.T) {
	home := t.TempDir()
	testutil.Setenv(t, "HOME", home)
	xdg := filepath.Join(home, "data")
	testutil.Setenv(t, "XDG_DATA_HOME", xdg)
	testutil.Unsetenv(t, "CLAUDE_CONFIG_DIR")
	root := filepath.Join(xdg, "tycswap")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"list"}, {"codex", "list"}, {"status"}} {
		code, _, errb := runCLI(t, argv, false, false)
		if code != 1 || !strings.Contains(errb, root) || !strings.Contains(errb, "group or other") {
			t.Errorf("%v: exit %d stderr %q", argv, code, errb)
		}
	}
	if code, _, _ := runCLI(t, []string{"--help"}, false, false); code != 0 {
		t.Errorf("--help refused (exit %d)", code)
	}
}
