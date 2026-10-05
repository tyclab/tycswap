package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestProbeRunsACmdShimFromPathsCmdExeParses: the session probe runs npm's
// claude.cmd from paths with characters cmd.exe acts on: Program Files (x86),
// where a 32-bit Node.js puts it, and user profiles with an ampersand, with
// and without a space.
func TestProbeRunsACmdShimFromPathsCmdExeParses(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, dir string }{
		{"parentheses", `Program Files (x86)\nodejs`},
		{"ampersand and space", `Ann & Bob\AppData\Roaming\npm`},
		{"ampersand", `Ann&Bob\AppData\Roaming\npm`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tc.dir)
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
		})
	}
}
