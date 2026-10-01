package cclock

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
)

// TestBreakStaleNeverRemovesAFreshLock: the second of two waiters that both
// judged a lock stale arrives after the first broke it and retook a fresh one.
// Its break must leave the fresh lock in place; a lock still stale after the
// rename is removed, and no aside directory is left behind either way.
func TestBreakStaleNeverRemovesAFreshLock(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, ".credentials.json.lock")
	clk := clock.System{}

	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	breakStale(lock, clk) // fresh mtime: the first waiter's new lock
	if fi, err := os.Stat(lock); err != nil || !fi.IsDir() {
		t.Fatalf("a fresh lock was removed: %v", err)
	}

	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	breakStale(lock, clk)
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("stale lock not removed: %v", err)
	}
	breakStale(lock, clk) // already gone: a no-op
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}
