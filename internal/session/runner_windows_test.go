package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestProbeRunsACmdShimFromProgramFilesX86: the session probe runs npm's
// claude.cmd from a path with a space and parentheses, as a Node.js installed
// under Program Files (x86) puts it.
func TestProbeRunsACmdShimFromProgramFilesX86(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "Program Files (x86)", "nodejs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "claude.cmd")
	if err := os.WriteFile(shim, []byte("@\""+exe+"\" %*\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, rc, err := osRunner{}.Probe([]string{shim, "auth", "status", "--json"}, append(os.Environ(), childRoleEnv+"=hello"), 10*time.Second)
	if err != nil || rc != 0 || stdout != "hello" {
		t.Fatalf("Probe = (%q, %d, %v); want (\"hello\", 0, nil)", stdout, rc, err)
	}
}
