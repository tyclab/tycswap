//go:build !windows

package filelock

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLockNeverFollowsOrTruncates: a symlink at the lock path is refused and
// its target left intact; an existing lock file keeps its bytes.
func TestLockNeverFollowsOrTruncates(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".lock")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if ok, err := New(link, 0).Acquire(0); ok || err == nil {
		t.Fatalf("Acquire through a symlink = %v, %v; want an error", ok, err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Fatalf("symlink target changed to %q", b)
	}

	plain := filepath.Join(dir, "plain.lock")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := New(plain, 0)
	if ok, err := l.Acquire(0); !ok || err != nil {
		t.Fatalf("Acquire = %v, %v", ok, err)
	}
	l.Release()
	if b, _ := os.ReadFile(plain); string(b) != "x" {
		t.Fatalf("lock file truncated to %q", b)
	}
	fresh := filepath.Join(dir, "fresh.lock")
	l = New(fresh, 0)
	if ok, _ := l.Acquire(0); !ok {
		t.Fatal("Acquire fresh")
	}
	l.Release()
	if fi, _ := os.Stat(fresh); fi.Mode().Perm() != 0o600 {
		t.Fatalf("new lock file mode %04o, want 0600", fi.Mode().Perm())
	}
}
