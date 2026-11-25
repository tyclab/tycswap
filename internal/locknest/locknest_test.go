package locknest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// takeOuter and askAgain are the two call sites a nesting names.
func takeOuter(path string) func() { return Hold(nil, path, nil, Here()) }

func askAgain(path string) bool { return Nested(nil, path, nil, Here()) }

// observe collects the nestings reported until the test ends.
func observe(t *testing.T) *[]Nesting {
	var seen []Nesting
	t.Cleanup(Observe(func(n Nesting) { seen = append(seen, n) }))
	return &seen
}

// A goroutine asking for a lock it holds is a nesting that names where it
// took the lock and where it asked again; asking for another lock, or after
// the release, is none.
func TestNestingNamesBothSites(t *testing.T) {
	seen := observe(t)
	path := filepath.Join(t.TempDir(), ".lock")
	release := takeOuter(path)
	if askAgain(path + ".other") {
		t.Fatal("another lock is a nesting")
	}
	if !askAgain(path) || len(*seen) != 1 {
		t.Fatalf("the held lock asked again: %d nestings reported, want 1", len(*seen))
	}
	n := (*seen)[0]
	if n.Lock != path || !strings.Contains(n.Held, "locknest.takeOuter") || !strings.Contains(n.Asked, "locknest.askAgain") {
		t.Fatalf("nesting %s", n)
	}
	release()
	if askAgain(path) {
		t.Fatal("a released lock is a nesting")
	}
}

// Another goroutine asking for a lock this one holds waits as before: no
// nesting.
func TestAnotherGoroutineIsNoNesting(t *testing.T) {
	seen := observe(t)
	path := filepath.Join(t.TempDir(), ".lock")
	defer takeOuter(path)()
	asked := make(chan bool)
	go func() { asked <- askAgain(path) }()
	if <-asked || len(*seen) != 0 {
		t.Fatalf("another goroutine's request was a nesting: %v", *seen)
	}
}

// Without an observer a test binary fails on a nesting, naming both sites:
// the standing check every package's tests get (DESIGN A118).
func TestNestingFailsTheTestBinary(t *testing.T) {
	if path := os.Getenv("LOCKNEST_NESTING_CHILD"); path != "" {
		defer takeOuter(path)()
		askAgain(path)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNestingFailsTheTestBinary$", "-test.count=1")
	cmd.Env = append(os.Environ(), "LOCKNEST_NESTING_CHILD="+filepath.Join(t.TempDir(), ".lock"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a test binary passed a nesting:\n%s", out)
	}
	for _, want := range []string{"asked for again by the goroutine that holds it", "locknest.takeOuter", "locknest.askAgain"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the failure does not name %q:\n%s", want, out)
		}
	}
}
