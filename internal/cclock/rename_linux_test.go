//go:build linux

package cclock

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRenameNoReplaceKeepsAnExistingLock: putting a lock directory back must
// fail when the name is taken, where rename(2) would silently replace the
// empty directory another waiter just made; a free name is taken.
func TestRenameNoReplaceKeepsAnExistingLock(t *testing.T) {
	dir := t.TempDir()
	aside, lock := filepath.Join(dir, "lock.stale-1-1"), filepath.Join(dir, "lock")
	for _, d := range []string{aside, lock} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(lock, "owner"), []byte("other waiter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(aside, lock); err == nil {
		t.Fatal("renamed over an existing lock")
	}
	if _, err := os.Stat(aside); err != nil {
		t.Errorf("the aside directory is gone: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(lock, "owner")); err != nil || string(b) != "other waiter" {
		t.Errorf("the existing lock was replaced: %q, %v", b, err)
	}

	if err := os.RemoveAll(lock); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(aside, lock); err != nil {
		t.Fatalf("rename to a free name: %v", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("lock not put back: %v", err)
	}
	if _, err := os.Stat(aside); !os.IsNotExist(err) {
		t.Errorf("aside still present: %v", err)
	}
}

// TestRenameIfAbsentKeepsAnExistingLock: the checked rename refuses a taken
// name too, and reports it as fs.ErrExist.
func TestRenameIfAbsentKeepsAnExistingLock(t *testing.T) {
	dir := t.TempDir()
	aside, lock := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for _, d := range []string{aside, lock} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := renameIfAbsent(aside, lock); !os.IsExist(err) {
		t.Fatalf("err = %v, want exists", err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := renameIfAbsent(aside, lock); err != nil {
		t.Fatalf("free name: %v", err)
	}
}
