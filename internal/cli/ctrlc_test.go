package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/clock"
)

// TestCtrlCReleasesClaudeCodeLocks: Ctrl-C exits from the SIGINT goroutine
// without running the deferred release of a lock a switch holds. Its cleanup
// releases the lock directory, so Claude Code does not wait it out as live
// until its staleness passes (DESIGN A55).
func TestCtrlCReleasesClaudeCodeLocks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".storage-write.lock")
	h, err := cclock.Acquire(dir, time.Second, clock.System{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()

	sigintCleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the lock directory outlived the Ctrl-C cleanup (stat err %v)", err)
	}
}
