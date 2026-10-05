package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAppJS_ModelControls(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	for _, name := range []string{"app.js", "index.html"} {
		src, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(node, "testdata/modelcontrols.cjs", filepath.Join(dir, "app.js"), filepath.Join(dir, "index.html")).CombinedOutput()
	if err != nil {
		t.Fatalf("model controls: %v\n%s", err, out)
	}
}
