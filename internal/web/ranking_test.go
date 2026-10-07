package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAppJSHierarchicalRanking(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; dashboard ranking test skipped")
	}
	src, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.js")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "testdata/ranking.cjs", path).CombinedOutput()
	if err != nil {
		t.Fatalf("dashboard ranking: %v\n%s", err, out)
	}
}
