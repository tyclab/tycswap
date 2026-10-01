package mappings

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// TestConcurrentSetKeepsEveryMapping: parallel Set calls on separate Store
// values (as separate commands would be) all persist.
func TestConcurrentSetKeepsEveryMapping(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	const n = 16
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := New(dir).Set(filepath.Join(dir, fmt.Sprintf("p%d", i)), fmt.Sprintf("u%d@example.com", i), ""); err != nil {
				t.Errorf("Set: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := len(New(dir).All()); got != n {
		t.Fatalf("%d mappings persisted, want %d", got, n)
	}
}
