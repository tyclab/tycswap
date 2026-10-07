package clock

import (
	"sync"
	"time"
)

// Clock is a wall clock.
type Clock interface {
	Now() time.Time
}

// Sleeper blocks or schedules using the clock's time source.
type Sleeper interface {
	Sleep(d time.Duration)
	After(d time.Duration) <-chan time.Time
}

// Seconds returns the clock's current time as fractional Unix seconds, matching
// Python's time.time() used by the usage store and autoswitch clock() seam.
func Seconds(c Clock) float64 {
	return float64(c.Now().UnixNano()) / 1e9
}

// System is the real clock/sleeper backed by the OS.
type System struct{}

func (System) Now() time.Time { return time.Now() }

// Sleep blocks for d.
func (System) Sleep(d time.Duration) { time.Sleep(d) }

func (System) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Fake is a controllable clock/sleeper for tests. Its Sleep and After advance
// the fake time immediately (deterministic); consumers needing true blocking
// semantics (e.g. the autoswitch loop select) should inject their own sleeper.
type Fake struct {
	mu    sync.Mutex
	t     time.Time
	Slept []time.Duration
}

func NewFake(t time.Time) *Fake { return &Fake{t: t} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = t
}

func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// Sleep records d and advances the fake clock by d without blocking.
func (f *Fake) Sleep(d time.Duration) {
	f.mu.Lock()
	f.Slept = append(f.Slept, d)
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// After advances the fake clock by d and returns a channel already holding the
// resulting time.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	f.t = f.t.Add(d)
	now := f.t
	f.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- now
	return ch
}
