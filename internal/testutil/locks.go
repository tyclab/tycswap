package testutil

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/locknest"
)

// HoldLock takes the lock file at path on a goroutine of its own, as another
// process would hold it, until release runs or the test ends. A test that
// stands in for another holder holds the lock here, not on its own
// goroutine, whose later request for it would be a nesting (DESIGN A118).
func HoldLock(t testing.TB, path string) (release func()) {
	t.Helper()
	took, stop, done := make(chan error, 1), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		l := filelock.New(path, 0)
		ok, err := l.Acquire(0)
		if err == nil && !ok {
			err = errors.New("the lock is busy")
		}
		took <- err
		if err == nil {
			<-stop
			_ = l.Release()
		}
	}()
	if err := <-took; err != nil {
		t.Fatalf("holding %s: %v", path, err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
	t.Cleanup(release)
	return release
}

// NestedLocks collects, until the test ends, every lock a goroutine asked
// for again while it held it (locknest, DESIGN A118).
func NestedLocks(t testing.TB) func() []locknest.Nesting {
	t.Helper()
	var mu sync.Mutex
	var seen []locknest.Nesting
	t.Cleanup(locknest.Observe(func(n locknest.Nesting) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, n)
	}))
	return func() []locknest.Nesting {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

// Parked reports true once a goroutine running frame is parked with wait
// reason state (a goroutine dump's "sleep", "sync.Mutex.Lock"), false once
// done reports true first. It polls the goroutine dump: a test waits on what
// a goroutine is doing, not on a duration.
func Parked(done func() bool, frame, state string) bool {
	buf := make([]byte, 1<<16)
	for !done() {
		n := runtime.Stack(buf, true)
		if n == len(buf) {
			buf = make([]byte, 2*len(buf))
			continue
		}
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if head, _, _ := strings.Cut(g, "\n"); strings.Contains(head, "["+state) && strings.Contains(g, frame) {
				return true
			}
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
