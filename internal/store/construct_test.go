// Tests for the parity-critical construction order (spec 07§5.6, DESIGN
// Appendix): legacy-dir migration is the one fallible step and runs before any
// path/logging/dir setup; registry migrations never abort; a no-op run must not
// materialize the backup dir.
package store

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

// TestNew_NoOpDoesNotMaterializeBackupDir: constructing against a fresh $HOME
// must not create the backup directory (lazy logging, no _setup_directories in
// __init__ — spec 07§5.5). Materializing it would trip the migration collision
// check on a later run.
func TestNew_NoOpDoesNotMaterializeBackupDir(t *testing.T) {
	testutil.IsolateHome(t)

	s, err := New(Options{Clock: testutil.FixedClock(t, "2026-07-17T09:00:00Z"), Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := os.Stat(s.BackupDir()); !os.IsNotExist(err) {
		t.Errorf("backup dir was materialized by a no-op construction: stat err=%v", err)
	}
}

// TestNew_NeverTouchesOldStores: construction neither moves nor reads the
// stores this fork came from (DESIGN A23): both old roots keep their data and
// the store resolves to tycswap's own root, with no notice printed.
func TestNew_NeverTouchesOldStores(t *testing.T) {
	home := testutil.IsolateHome(t)

	olds := []string{filepath.Join(home, ".claude-swap-backup"), filepath.Join(home, ".local", "share", "claude-swap")}
	for _, d := range olds {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "sequence.json"), []byte(`{"sequence":[1]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var stderr bytes.Buffer
	s, err := New(Options{Clock: testutil.FixedClock(t, "2026-07-17T09:00:00Z"), Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &stderr})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if strings.Contains(s.BackupDir(), "claude-swap") {
		t.Errorf("BackupDir = %q, want tycswap's own root", s.BackupDir())
	}
	for _, d := range olds {
		if b, err := os.ReadFile(filepath.Join(d, "sequence.json")); err != nil || string(b) != `{"sequence":[1]}` {
			t.Errorf("old store %s changed: %q, %v", d, b, err)
		}
	}
	if _, err := os.Stat(s.BackupDir()); !os.IsNotExist(err) {
		t.Errorf("construction materialized %s", s.BackupDir())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", stderr.String())
	}
}

// TestNew_FixtureHomeConstructs: constructing against the materialized
// Python-fixture home wires all paths and reads the four-account table.
func TestNew_FixtureHomeConstructs(t *testing.T) {
	s, fh := newFixtureStore(t)
	if s.BackupDir() != fh.BackupRoot {
		t.Errorf("BackupDir()=%q want %q", s.BackupDir(), fh.BackupRoot)
	}
	if s.SequenceFile != filepath.Join(fh.BackupRoot, "sequence.json") {
		t.Errorf("SequenceFile=%q", s.SequenceFile)
	}
	data, err := s.ReadSequence()
	if err != nil || data == nil {
		t.Fatalf("ReadSequence: %v", err)
	}
	if len(data.Accounts) != 4 {
		t.Errorf("accounts=%d want 4", len(data.Accounts))
	}
	if data.ActiveAccountNumber == nil || *data.ActiveAccountNumber != 1 {
		t.Errorf("activeAccountNumber=%v want 1", data.ActiveAccountNumber)
	}
}
