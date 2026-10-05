package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddLoginRunsTheNpmShim: npm installs claude on Windows as claude.cmd,
// which cmd.exe runs, beside a sh script named claude. add --login finds the
// .cmd on PATH and runs it, its passthrough arguments intact.
func TestAddLoginRunsTheNpmShim(t *testing.T) {
	f := newLoginFixture(t)
	// The fake claude leaves PATH, so only the shim reaches it.
	fake := filepath.Join(t.TempDir(), "node.exe")
	if err := os.Rename(filepath.Join(f.bin, "claude.exe"), fake); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(f.bin, "claude.cmd"), "@\""+fake+"\" %*\r\n", 0o755)
	writeTestFile(t, filepath.Join(f.bin, "claude"), "#!/bin/sh\nexec node \"$basedir/cli.js\" \"$@\"\n", 0o755)

	code, out, errb := f.run(t, "add", "--login", "--", "--email", "b@example.com", "--sso")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if !strings.Contains(out, "Added Account 2: b@example.com [personal] (from login)") {
		t.Errorf("output = %q", out)
	}
	if got := strings.Join(f.loginArgs(t), " "); got != "--claudeai --email b@example.com --sso" {
		t.Errorf("claude auth login got passthrough %q", got)
	}
	if got := f.storedCreds(t, "2", "b@example.com"); !strings.Contains(got, "sk-ant-oat01-test-token-2") {
		t.Errorf("stored credential for slot 2 = %q", got)
	}
	assertNoScratch(t)
}
