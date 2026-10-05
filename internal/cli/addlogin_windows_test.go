package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/testutil"
)

// TestAddLoginRunsTheNpmShim: npm installs claude on Windows as claude.cmd,
// which cmd.exe runs, beside a sh script named claude, under the user's
// profile, whose name may hold a space. add --login finds the .cmd on PATH
// and runs it, its passthrough arguments intact through cmd.exe's parse of the
// command line, one with a space included.
func TestAddLoginRunsTheNpmShim(t *testing.T) {
	for _, tc := range []struct{ name, dir string }{
		{"plain path", "npm"},
		{"path with a space", `First Last\AppData\Roaming\npm`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoginFixture(t)
			// The fake claude leaves PATH, so only the shim reaches it.
			fake := filepath.Join(t.TempDir(), "node.exe")
			if err := os.Rename(filepath.Join(f.bin, "claude.exe"), fake); err != nil {
				t.Fatal(err)
			}
			npm := filepath.Join(t.TempDir(), tc.dir)
			writeTestFile(t, filepath.Join(npm, "claude.cmd"), "@\""+fake+"\" %*\r\n", 0o755)
			writeTestFile(t, filepath.Join(npm, "claude"), "#!/bin/sh\nexec node \"$basedir/cli.js\" \"$@\"\n", 0o755)
			testutil.Setenv(t, "PATH", npm+string(os.PathListSeparator)+os.Getenv("PATH"))

			code, out, errb := f.run(t, "add", "--login", "--", "--email", "b@example.com", "--sso", "two words")
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, errb)
			}
			if !strings.Contains(out, "Added Account 2: b@example.com [personal] (from login)") {
				t.Errorf("output = %q", out)
			}
			if got, want := f.loginArgs(t), []string{"--claudeai", "--email", "b@example.com", "--sso", "two words"}; !slices.Equal(got, want) {
				t.Errorf("claude auth login got passthrough %q, want %q", got, want)
			}
			if got := f.storedCreds(t, "2", "b@example.com"); !strings.Contains(got, "sk-ant-oat01-test-token-2") {
				t.Errorf("stored credential for slot 2 = %q", got)
			}
			assertNoScratch(t)
		})
	}
}
