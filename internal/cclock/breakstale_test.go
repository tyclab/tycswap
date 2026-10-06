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
	breakStale(lock, StalenessS, clk) // fresh mtime: the first waiter's new lock
	if fi, err := os.Stat(lock); err != nil || !fi.IsDir() {
		t.Fatalf("a fresh lock was removed: %v", err)
	}

	// 30s old is fresh by the credentials lock's own 60s staleness.
	date := func(age time.Duration) {
		if then := time.Now().Add(-age); os.Chtimes(lock, then, then) != nil {
			t.Fatal("could not date the lock")
		}
	}
	date(30 * time.Second)
	breakStale(lock, CredentialsStalenessS, clk)
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("a lock fresh by its own staleness was removed: %v", err)
	}

	// 12s old is fresh by storage-write's 15s, 16s old is not.
	date(12 * time.Second)
	breakStale(lock, StorageWriteStalenessS, clk)
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("a storage-write lock 12s old was removed: %v", err)
	}
	date(16 * time.Second)
	breakStale(lock, StorageWriteStalenessS, clk)
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("a storage-write lock 16s old was kept: %v", err)
	}
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}

	date(time.Minute)
	breakStale(lock, StalenessS, clk)
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("stale lock not removed: %v", err)
	}
	breakStale(lock, StalenessS, clk) // already gone: a no-op
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}
