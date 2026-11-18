package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// An account row with an alias offers Remove alias beside Change alias…,
// posting the alias route with no body, which removes it.
func TestAppJSRemoveAlias(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; alias menu test skipped")
	}
	path := filepath.Join(t.TempDir(), "app.js")
	if err := os.WriteFile(path, []byte(staticFile(t, "app.js")), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "testdata/alias.cjs", path).CombinedOutput(); err != nil {
		t.Fatalf("alias menu: %v\n%s", err, out)
	}
}
