package appicon

import (
	"bytes"
	"io/fs"
	"sync"
	"testing"
)

// renderings is Renderings, drawn once for the tests that compare with it.
var renderings = sync.OnceValue(Renderings)

// The embedded files are exactly what the drawing code renders now: a change
// to the mark without `go generate ./internal/appicon` fails here.
func TestRenderedFilesAreCurrent(t *testing.T) {
	want := renderings()
	entries, err := fs.ReadDir(renderedFS, "rendered")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name()] = true
		got, err := renderedFS.ReadFile("rendered/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		w, ok := want[e.Name()]
		if !ok {
			t.Errorf("rendered/%s is not one of the renderings; delete it and run go generate", e.Name())
			continue
		}
		if !bytes.Equal(got, w) {
			t.Errorf("rendered/%s is stale; run go generate ./internal/appicon", e.Name())
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("rendered/%s is missing; run go generate ./internal/appicon", name)
		}
	}
}

// Rendering twice gives the same bytes, so a regenerated file only changes
// when the drawing does.
func TestRenderingIsDeterministic(t *testing.T) {
	a, b := renderings(), Renderings()
	for name := range a {
		if !bytes.Equal(a[name], b[name]) {
			t.Errorf("%s differs between two renderings", name)
		}
	}
}

// What the trays ask for comes from the embedded files.
func TestTraySizesComeFromTheEmbeddedFiles(t *testing.T) {
	r := renderings()
	if !bytes.Equal(PNG(32, false), r["mark-32.png"]) || !bytes.Equal(PNG(128, false), r["mark-128.png"]) {
		t.Error("PNG did not serve the embedded mark")
	}
	if !bytes.Equal(PNGBadge(32), r["badge-32.png"]) {
		t.Error("PNGBadge did not serve the embedded badge")
	}
	if !bytes.Equal(PNGCounted(128, 12), r["count-128-9plus.png"]) || !bytes.Equal(PNGCounted(128, 0), r["count-128-1.png"]) {
		t.Error("PNGCounted did not serve the embedded counts")
	}
}
