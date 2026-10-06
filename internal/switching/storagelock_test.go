package switching

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/testutil"
)

// TestSwitchWaitsForClaudeCodesLocks: while Claude Code holds its
// storage-write lock or its credential-refresh lock (where
// CLAUDE_SECURESTORAGE_CONFIG_DIR puts them, when set), the switch writes nothing, also when the
// lock is older than the 10s other locks are stale after but younger than
// Claude Code's own staleness for it (15s, 60s). Once the lock is free the
// switch lands (DESIGN A55).
func TestSwitchWaitsForClaudeCodesLocks(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  func(t *testing.T, home string) string
		age  time.Duration
	}{
		{"storage-write lock, 12s old", func(_ *testing.T, home string) string {
			return filepath.Join(home, ".claude", ".storage-write.lock")
		}, 12 * time.Second},
		{"storage-write lock in CLAUDE_SECURESTORAGE_CONFIG_DIR", func(t *testing.T, _ string) string {
			ks := t.TempDir()
			testutil.Setenv(t, "CLAUDE_SECURESTORAGE_CONFIG_DIR", ks)
			return filepath.Join(ks, ".storage-write.lock")
		}, 0},
		{"credential-refresh lock in CLAUDE_SECURESTORAGE_CONFIG_DIR", func(t *testing.T, _ string) string {
			ks := filepath.Join(t.TempDir(), "ks")
			testutil.Setenv(t, "CLAUDE_SECURESTORAGE_CONFIG_DIR", ks)
			return ks + ".lock"
		}, 0},
		{"credential-refresh lock, 30s old", func(_ *testing.T, home string) string {
			return filepath.Join(home, ".claude.lock")
		}, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ca, cb := twoAccountStore(t)
			lockDir := tc.dir(t, s.Home)
			if err := os.Mkdir(lockDir, 0o700); err != nil {
				t.Fatal(err)
			}
			// Dated by the store's clock: a holder that renewed it age ago.
			if then := s.Clk.Now().Add(-tc.age); os.Chtimes(lockDir, then, then) != nil {
				t.Fatal("could not date the lock")
			}
			done := make(chan error, 1)
			go func() { _, err := SwitchTo(s, "2", true, false); done <- err }()

			select {
			case err := <-done:
				t.Fatalf("the switch finished while Claude Code held its lock (err %v)", err)
			case <-time.After(time.Second):
			}
			if got := readActiveCreds(t, s); got != ca {
				t.Fatalf("the switch wrote the credential store under Claude Code's lock:\n%s", got)
			}

			if err := os.Remove(lockDir); err != nil {
				t.Fatalf("Claude Code's lock was taken over (remove: %v)", err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("SwitchTo: %v", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("the switch did not finish once the lock was free")
			}
			if got := readActiveCreds(t, s); got != cb {
				t.Errorf("live credential = %s, want b's", got)
			}
		})
	}
}
