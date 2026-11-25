// Package locknest notices a goroutine that asks for a lock it already holds
// (DESIGN A118). filelock and cclock record each lock a goroutine takes and
// ask here before they wait for one. They refuse such a request at once: the
// holder is the goroutine that would wait, so the wait could only end in its
// timeout's answer. A test binary fails on it instead, naming both call
// sites, unless a test observes nestings.
package locknest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Site is a goroutine at the place it asked for a lock.
type Site struct {
	goroutine uint64
	pcs       []uintptr
}

// Here is the calling goroutine at its caller, the lock's Acquire.
func Here() Site {
	var pcs [48]uintptr
	n := runtime.Callers(2, pcs[:])
	return Site{goroutine: goroutineID(), pcs: slices.Clone(pcs[:n])}
}

// goroutineID is the calling goroutine's number from its stack header; 0,
// which nothing matches, when the header cannot be read.
func goroutineID() uint64 {
	var buf [64]byte
	head, ok := bytes.CutPrefix(buf[:runtime.Stack(buf[:], false)], []byte("goroutine "))
	if i := bytes.IndexByte(head, ' '); ok && i > 0 {
		id, _ := strconv.ParseUint(string(head[:i]), 10, 64)
		return id
	}
	return 0
}

// Nesting is a lock asked for by the goroutine that holds it.
type Nesting struct {
	Lock string
	// Held and Asked are the two call sites, innermost frame first.
	Held, Asked string
}

func (n Nesting) String() string {
	return fmt.Sprintf("%s asked for again by the goroutine that holds it\nheld since:\n%sasked again at:\n%s", n.Lock, n.Held, n.Asked)
}

type holding struct {
	lock any
	path string
	file os.FileInfo
	site Site
}

var (
	mu   sync.Mutex
	held []*holding
)

func clean(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// Hold records that site holds lock, the lock at path ("" for lock alone)
// whose file, when known, is file, until the returned func runs.
func Hold(lock any, path string, file os.FileInfo, site Site) (release func()) {
	h := &holding{lock: lock, path: clean(path), file: file, site: site}
	mu.Lock()
	held = append(held, h)
	mu.Unlock()
	return func() {
		mu.Lock()
		held = slices.DeleteFunc(held, func(o *holding) bool { return o == h })
		mu.Unlock()
	}
}

// Nested reports whether site's goroutine already holds lock, a lock at path
// or one on file; each of them may be left out (nil, ""). A nesting is
// reported before it is answered.
func Nested(lock any, path string, file os.FileInfo, site Site) bool {
	if site.goroutine == 0 {
		return false
	}
	path = clean(path)
	mu.Lock()
	i := slices.IndexFunc(held, func(h *holding) bool {
		return h.site.goroutine == site.goroutine && (lock != nil && h.lock == lock || path != "" && h.path == path ||
			file != nil && h.file != nil && os.SameFile(file, h.file))
	})
	var found *holding
	if i >= 0 {
		found = held[i]
	}
	mu.Unlock()
	if found == nil {
		return false
	}
	name := found.path
	if name == "" {
		name = path
	}
	report(Nesting{Lock: name, Held: frames(found.site.pcs), Asked: frames(site.pcs)})
	return true
}

func frames(pcs []uintptr) string {
	var b strings.Builder
	it := runtime.CallersFrames(pcs)
	for {
		f, more := it.Next()
		if f.Function != "" {
			fmt.Fprintf(&b, "\t%s\n\t\t%s:%d\n", f.Function, f.File, f.Line)
		}
		if !more {
			return b.String()
		}
	}
}

var observer atomic.Pointer[func(Nesting)]

// Observe is a test seam: fn is told of every nesting, in place of the test
// binary failing, until restore runs.
func Observe(fn func(Nesting)) (restore func()) {
	prev := observer.Swap(&fn)
	return func() { observer.Store(prev) }
}

func report(n Nesting) {
	if fn := observer.Load(); fn != nil {
		(*fn)(n)
		return
	}
	if testing.Testing() {
		// Code under test could recover a panic raised on this goroutine.
		go func() { panic("locknest: " + n.String()) }()
		select {}
	}
}
