//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/testutil"
)

// privateRootHome gives the test a store root at mode 0777 under a temp HOME.
func privateRootHome(t *testing.T) string {
	t.Helper()
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
	return root
}

// TestRunMakesAGroupWritableStorePrivate: a store root writable by group or
// other is made 0700 before the command runs, with nothing said.
func TestRunMakesAGroupWritableStorePrivate(t *testing.T) {
	root := privateRootHome(t)
	for _, argv := range [][]string{{"list"}, {"codex", "list"}, {"status"}} {
		if err := os.Chmod(root, 0o777); err != nil {
			t.Fatal(err)
		}
		code, _, errb := runCLI(t, argv, false, false)
		if code == 1 && strings.Contains(errb, "group or other") {
			t.Errorf("%v: refused: %q", argv, errb)
		}
		if strings.Contains(errb, "warning:") {
			t.Errorf("%v: warned although chmod worked: %q", argv, errb)
		}
		if fi, err := os.Stat(root); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%v: root left at %v, want 0700", argv, fi.Mode().Perm())
		}
	}
	if code, _, _ := runCLI(t, []string{"--help"}, false, false); code != 0 {
		t.Errorf("--help refused (exit %d)", code)
	}
}

// TestRunWarnsOnceWhenTheStoreModeCannotChange: when chmod leaves the mode
// as it was (a filesystem without POSIX modes), the command runs and stderr
// carries one warning line naming the quoted path.
func TestRunWarnsOnceWhenTheStoreModeCannotChange(t *testing.T) {
	root := privateRootHome(t)
	t.Cleanup(func() { paths.PrivateRootChmod = os.Chmod })
	paths.PrivateRootChmod = func(string, os.FileMode) error { return nil }
	code, _, errb := runCLI(t, []string{"list"}, false, false)
	if code == 1 && strings.Contains(errb, "group or other") {
		t.Fatalf("refused: %q", errb)
	}
	if n := strings.Count(errb, "warning:"); n != 1 || !strings.Contains(errb, "chmod 700 '"+root+"'") {
		t.Errorf("stderr = %q, want one warning line quoting the path", errb)
	}
	if fi, _ := os.Stat(root); fi.Mode().Perm() != 0o777 {
		t.Errorf("the stand-in chmod changed the mode to %v", fi.Mode().Perm())
	}
}
