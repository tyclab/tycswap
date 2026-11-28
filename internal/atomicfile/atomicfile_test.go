package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWriteCreatesParentAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	path := filepath.Join(dir, "f.txt")
	if err := Write(path, []byte("hello"), Opts{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q", got)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %o, want 600", fi.Mode().Perm())
		}
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1 (no leftover temp)", len(entries))
	}
}

func TestWriteJSONIndentTwoSpaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "obj.json")
	if err := WriteJSON(path, map[string]int{"a": 1}, Opts{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "{\n  \"a\": 1\n}"
	if string(got) != want {
		t.Errorf("WriteJSON = %q, want %q", got, want)
	}
}

// TestWriteJSONNoHTMLEscape asserts that <, >, & survive as literal ASCII,
// matching Python's json.dumps (which does not HTML-escape), rather than being
// rewritten to </>/& by json.MarshalIndent's default escaping.
// The realistic case is a mappings.json key that is a filesystem path
// containing such characters, e.g. `tycswap map 2 /home/me/a&b/proj`.
func TestWriteJSONNoHTMLEscape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.json")
	if err := WriteJSON(path, map[string]string{"2": "/home/me/a&b<c>d/proj"}, Opts{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "{\n  \"2\": \"/home/me/a&b<c>d/proj\"\n}"
	if string(got) != want {
		t.Errorf("WriteJSON = %q, want %q", got, want)
	}
}

func TestWriteJSONValidatedNoHTMLEscape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seq.json")
	if err := WriteJSONValidated(path, map[string]string{"path": "a&b<c>d"}, Opts{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "{\n  \"path\": \"a&b<c>d\"\n}"
	if string(got) != want {
		t.Errorf("WriteJSONValidated = %q, want %q", got, want)
	}
}

func TestWriteJSONValidatedSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seq.json")
	payload := map[string]any{"sequence": []int{1, 2, 3}, "activeAccountNumber": nil}
	if err := WriteJSONValidated(path, payload, Opts{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("written file not valid JSON: %v", err)
	}
}

// unmarshalable forces json.MarshalIndent to fail so we can assert temp cleanup.
type unmarshalable struct{}

func (unmarshalable) MarshalJSON() ([]byte, error) { return nil, os.ErrInvalid }

func TestWriteJSONValidatedMarshalErrorLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seq.json")
	err := WriteJSONValidated(path, unmarshalable{}, Opts{})
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if fileExists(path) {
		t.Error("target should not exist after marshal failure")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("leftover temp files: %v", entries)
	}
}

func TestWriteErrorUnlinksTemp(t *testing.T) {
	// Rename fails when the destination directory does not exist and cannot be
	// created because a parent path component is a regular file.
	base := t.TempDir()
	fileAsParent := filepath.Join(base, "afile")
	if err := os.WriteFile(fileAsParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// path whose parent is a file → MkdirAll fails, so Write errors before temp.
	path := filepath.Join(fileAsParent, "child", "f.txt")
	if err := Write(path, []byte("data"), Opts{}); err == nil {
		t.Fatal("expected error writing under a file-as-directory")
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 1 { // only "afile"
		t.Errorf("leftover artifacts under base: %v", entries)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// A128: old temp files go at any depth; fresh ones, other names and what lies
// behind a symlink stay.
func TestSweepStaleRemovesOldTempFiles(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	file := func(p string, mtime time.Time) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldTop := file(filepath.Join(root, ".atomic-1.tmp"), old)
	nested := file(filepath.Join(root, "cache", "deep", ".atomic-2.tmp"), old)
	fresh := file(filepath.Join(root, ".atomic-3.tmp"), now)
	other := file(filepath.Join(root, ".tycswap-migrate-4.tmp"), old)
	behind := file(filepath.Join(outside, ".atomic-5.tmp"), old)
	want := map[string]bool{oldTop: false, nested: false, fresh: true, other: true, behind: true}
	links, fileLink := filepath.Join(root, "links"), filepath.Join(root, ".atomic-6.tmp")
	if os.Symlink(outside, links) == nil && os.Symlink(behind, fileLink) == nil {
		want[links], want[fileLink] = true, true
	} else {
		t.Log("no symlinks here: only the plain files are checked")
	}
	if got := SweepStale(root, time.Hour, now); got != 2 {
		t.Fatalf("SweepStale = %d, want 2", got)
	}
	for p, kept := range want {
		if _, err := os.Lstat(p); (err == nil) != kept {
			t.Errorf("%s kept = %v, want %v", p, err == nil, kept)
		}
	}
}

// A data root that is itself a symlink is still swept.
func TestSweepStaleThroughASymlinkedRoot(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, ".atomic-1.tmp")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(stale, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	if got := SweepStale(link, time.Hour, now); got != 1 {
		t.Fatalf("SweepStale through a symlinked root = %d, want 1", got)
	}
}
