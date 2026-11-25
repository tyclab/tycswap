package filelock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/locknest"
)

// holdOuter and askAgain are the two call sites a nesting names.
func holdOuter(t *testing.T, path string) *FileLock {
	t.Helper()
	l := New(path, 0)
	if ok, err := l.Acquire(0); !ok || err != nil {
		t.Fatalf("Acquire = (%v, %v)", ok, err)
	}
	return l
}

func askAgain(l *FileLock) (bool, error) { return l.Acquire(time.Hour) }

func observeNestings(t *testing.T) *[]locknest.Nesting {
	var seen []locknest.Nesting
	t.Cleanup(locknest.Observe(func(n locknest.Nesting) { seen = append(seen, n) }))
	return &seen
}

// A goroutine asking for a lock file it holds, through the same lock, another
// lock on the path or the file under another path, is refused at once, as
// its timeout would refuse it: the holder is the goroutine that would wait.
// The nesting names both call sites (DESIGN A118).
func TestNestedAcquisitionFailsAtOnce(t *testing.T) {
	for _, variant := range []string{"the same lock", "another lock on the path", "the file under another path"} {
		t.Run(variant, func(t *testing.T) {
			seen := observeNestings(t)
			dir := t.TempDir()
			path := filepath.Join(dir, ".lock")
			outer := holdOuter(t, path)
			defer outer.Release()
			inner := outer
			switch variant {
			case "another lock on the path":
				inner = New(path, 0)
			case "the file under another path":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(dir, alias); err != nil {
					t.Skip(err)
				}
				inner = New(filepath.Join(alias, ".lock"), 0)
			}
			if ok, err := askAgain(inner); ok || err != nil {
				t.Fatalf("nested Acquire = (%v, %v), want (false, nil)", ok, err)
			}
			if len(*seen) != 1 {
				t.Fatalf("%d nestings reported, want 1", len(*seen))
			}
			if n := (*seen)[0]; !strings.Contains(n.Held, "filelock.holdOuter") || !strings.Contains(n.Asked, "filelock.askAgain") {
				t.Fatalf("nesting %s", n)
			}
			if !outer.Held() || inner != outer && inner.Held() {
				t.Fatal("the refusal changed who holds the lock")
			}
			outer.Release()
			if ok, err := inner.Acquire(0); !ok || err != nil {
				t.Fatalf("Acquire after the release = (%v, %v)", ok, err)
			}
			inner.Release()
		})
	}
}

// Two goroutines contending for one file, through one lock or one each, are
// no nesting: the second waits for the first and then gets it.
func TestContendingGoroutinesStillWait(t *testing.T) {
	for _, shared := range []bool{true, false} {
		seen := observeNestings(t)
		path := filepath.Join(t.TempDir(), ".lock")
		first := holdOuter(t, path)
		second := first
		if !shared {
			second = New(path, 0)
		}
		asking, got := make(chan struct{}), make(chan bool)
		go func() {
			close(asking)
			ok, err := second.Acquire(time.Hour)
			got <- ok && err == nil
		}()
		<-asking
		select {
		case ok := <-got:
			t.Fatalf("shared %v: the second goroutine's Acquire returned %v while the first held the lock", shared, ok)
		case <-time.After(3 * pollInterval):
		}
		first.Release()
		if !<-got {
			t.Fatalf("shared %v: the second goroutine did not get the lock once the first released it", shared)
		}
		second.Release()
		if len(*seen) != 0 {
			t.Fatalf("shared %v: contention reported as nesting: %v", shared, *seen)
		}
	}
}
