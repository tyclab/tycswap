package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exercise the browser's actual tab routing against the embedded markup. A
// Guide contents link must reveal and scroll its section, including on reload.
func TestAppJS_Navigation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; navigation test skipped")
	}
	dir := t.TempDir()
	for _, name := range []string{"app.js", "index.html"} {
		src, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(node, "testdata/navigation.cjs", filepath.Join(dir, "app.js"), filepath.Join(dir, "index.html")).CombinedOutput()
	if err != nil {
		t.Fatalf("navigation failed: %v\n%s", err, out)
	}
}
