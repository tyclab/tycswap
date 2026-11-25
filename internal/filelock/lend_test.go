package filelock

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// parked reports true once a goroutine running frame is parked with wait
// reason state in a goroutine dump, false once done reports true first.
func parked(done func() bool, frame, state string) bool {
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

// takenElsewhere reports whether another lock on path, asked on a goroutine
// of its own as another process would ask, gets the file within timeout.
func takenElsewhere(path string, timeout time.Duration) bool {
	got := make(chan bool)
	go func() {
		l := New(path, timeout)
		ok, err := l.Acquire(timeout)
		if ok {
			l.Release()
		}
		got <- ok && err == nil
	}()
	return <-got
}

// askLent is a goroutine asking for the lent lock; it reports whether it got it.
func askLent(lent *FileLock, timeout time.Duration, got chan<- bool) {
	ok, err := lent.Acquire(timeout)
	got <- ok && err == nil
}

// While a lock is lent, the lent lock is taken at once on the lender's hold,
// and the file stays held against every other lock (DESIGN A118).
func TestALentLockTakesTheLendersHold(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	lender := holdOuter(t, path)
	defer lender.Release()
	ran := false
	err := lender.Lend(func(lent *FileLock) error {
		ran = true
		if ok, err := lent.Acquire(time.Hour); !ok || err != nil {
			t.Fatalf("lent Acquire = (%v, %v), want it taken on the lender's hold", ok, err)
		}
		if !lent.Held() {
			t.Error("the lent lock is not held after its Acquire")
		}
		if takenElsewhere(path, 200*time.Millisecond) {
			t.Error("another lock took the file while the lent lock was held")
		}
		return lent.Release()
	})
	if err != nil || !ran {
		t.Fatalf("Lend = %v, ran %v", err, ran)
	}
}

// A lock nobody holds cannot be lent: Lend refuses and runs nothing.
func TestOnlyAHeldLockCanBeLent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	l := New(path, 0)
	for _, when := range []string{"never acquired", "released"} {
		if when == "released" {
			if ok, err := l.Acquire(0); !ok || err != nil {
				t.Fatalf("Acquire = (%v, %v)", ok, err)
			}
			l.Release()
		}
		ran := false
		if err := l.Lend(func(*FileLock) error { ran = true; return nil }); err == nil || ran {
			t.Errorf("%s: Lend = %v, ran %v; want a refusal", when, err, ran)
		}
	}
}

// Once Lend has returned, the lock it lent takes the file as any lock does:
// it waits for another holder of the file, and while it holds the file no
// other lock gets it.
func TestALentLockTakesTheFileOnceTheLendingEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	lender := holdOuter(t, path)
	var lent *FileLock
	if err := lender.Lend(func(l *FileLock) error { lent = l; return nil }); err != nil {
		t.Fatal(err)
	}
	lender.Release()
	other := holdElsewhere(t, path)
	if ok, _ := lent.Acquire(200 * time.Millisecond); ok {
		lent.Release()
		t.Fatal("the formerly lent lock was taken while another holder had the file")
	}
	other.Release()
	if ok, err := lent.Acquire(time.Second); !ok || err != nil {
		t.Fatalf("the formerly lent lock with the file free: (%v, %v)", ok, err)
	}
	defer lent.Release()
	if takenElsewhere(path, 200*time.Millisecond) {
		t.Fatal("another lock took the file while the formerly lent lock held it")
	}
}

// The lent lock has one holder at a time: a second goroutine's Acquire waits
// until the first holder releases it.
func TestALentLockHasOneHolderAtATime(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	lender := holdOuter(t, path)
	defer lender.Release()
	if err := lender.Lend(func(lent *FileLock) error {
		if ok, err := lent.Acquire(0); !ok || err != nil {
			t.Fatalf("first lent Acquire = (%v, %v)", ok, err)
		}
		got := make(chan bool, 1)
		go askLent(lent, time.Hour, got)
		if !parked(func() bool { return len(got) > 0 }, "filelock.askLent", "sync.Mutex.Lock") {
			t.Fatalf("a second holder's Acquire returned %v while the first held the lent lock", <-got)
		}
		lent.Release()
		if !<-got {
			t.Fatal("the second holder did not get the lent lock once the first released it")
		}
		return lent.Release()
	}); err != nil {
		t.Fatal(err)
	}
}

// Lend does not return while a goroutine still holds the lent lock; once it
// releases, Lend returns and the lending has ended: a goroutine that was
// waiting for the lent lock meanwhile goes to the file, which the lender
// still holds, and is refused within its timeout.
func TestLendWaitsForAHolderOfTheLentLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	lender := holdOuter(t, path)
	defer lender.Release()
	lentCh, holding, release := make(chan *FileLock, 1), make(chan struct{}), make(chan struct{})
	lendDone := make(chan error, 1)
	go func() {
		lendDone <- lender.Lend(func(lent *FileLock) error {
			lentCh <- lent
			go func() {
				if ok, _ := lent.Acquire(0); ok {
					close(holding)
					<-release
					lent.Release()
				}
			}()
			<-holding
			return nil
		})
	}()
	lent := <-lentCh
	if !parked(func() bool { return len(lendDone) > 0 }, "filelock.(*FileLock).Lend", "sync.Mutex.Lock") {
		t.Fatalf("Lend returned (%v) while a goroutine held the lent lock", <-lendDone)
	}
	late := make(chan bool, 1)
	go askLent(lent, 200*time.Millisecond, late)
	if !parked(func() bool { return len(late) > 0 }, "filelock.askLent", "sync.Mutex.Lock") {
		t.Fatalf("a late Acquire of the lent lock returned %v while another goroutine held it", <-late)
	}
	close(release)
	if err := <-lendDone; err != nil {
		t.Fatal(err)
	}
	if <-late {
		t.Fatal("a goroutine waiting for the lent lock as the lending ended got it without the file")
	}
}
