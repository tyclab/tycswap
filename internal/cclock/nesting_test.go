package cclock_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/locknest"
)

func takeOuter(t *testing.T, dir string) *cclock.Handle {
	t.Helper()
	h, err := cclock.Acquire(dir, time.Second, sys)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func askAgain(dir string) (*cclock.Handle, error) { return cclock.Acquire(dir, time.Hour, sys) }

// A goroutine asking for a Claude Code lock it holds is refused at once with
// the timeout's error, the lock left held; the nesting names both call sites
// (DESIGN A118). Another goroutine's request still waits for the holder.
func TestNestedAcquisitionFailsAtOnce(t *testing.T) {
	var seen []locknest.Nesting
	t.Cleanup(locknest.Observe(func(n locknest.Nesting) { seen = append(seen, n) }))
	dir := filepath.Join(t.TempDir(), "x.lock")
	outer := takeOuter(t, dir)
	h, err := askAgain(dir)
	if err == nil {
		h.Release()
		t.Fatal("the nested Acquire took the lock")
	}
	if got := cerr.TypeName(err); got != "ClaudeCodeLockTimeout" {
		t.Fatalf("nested Acquire error %v (%s), want the timeout's", err, got)
	}
	if len(seen) != 1 || !strings.Contains(seen[0].Held, "cclock_test.takeOuter") || !strings.Contains(seen[0].Asked, "cclock_test.askAgain") {
		t.Fatalf("nestings %v", seen)
	}
	if !exists(dir) {
		t.Fatal("the refusal released the held lock")
	}

	got := make(chan error)
	go func() {
		h, err := cclock.Acquire(dir, time.Hour, sys)
		if err == nil {
			h.Release()
		}
		got <- err
	}()
	time.Sleep(time.Second)
	outer.Release()
	if err := <-got; err != nil || len(seen) != 1 {
		t.Fatalf("another goroutine's Acquire = %v with %d nestings, want it to wait and succeed", err, len(seen))
	}
}
