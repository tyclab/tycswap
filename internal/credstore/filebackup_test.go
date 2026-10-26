// A busy Keychain is no empty slot, and what a spell in file mode left in
// plaintext goes back into the Keychain (DESIGN A69).
package credstore

import (
	"os"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
)

// macStore is a macOS store over kc in a fresh credentials dir.
func macStore(t *testing.T, kc keychain.KeychainClient) *FileKeychainStore {
	t.Helper()
	return newStore(t, platform.MacOS, t.TempDir(), kc, nil)
}

func mustReadBackup(t *testing.T, s *FileKeychainStore, want string) {
	t.Helper()
	if got, err := s.ReadBackup("2", "bob@e.com"); got != want || err != nil {
		t.Fatalf("ReadBackup = (%q, %v), want %q", got, err, want)
	}
}

// TestTimedOutBackupReadFails_macOS: a backup read the deadline killed fails,
// so a switch or a move cannot take the slot for empty.
func TestTimedOutBackupReadFails_macOS(t *testing.T) {
	kc := &busyKC{fakeKC: newFakeKC()}
	kc.put(securityService, bobItem, storedBob)
	s := macStore(t, kc)

	kc.busy = true
	v, err := s.ReadBackup("2", "bob@e.com")
	if v != "" || !keychain.IsTimeout(err) {
		t.Fatalf("ReadBackup while busy = (%q, %v), want the timeout", v, err)
	}
	kc.busy = false
	mustReadBackup(t, s, storedBob)
}

// TestOtherReadErrorStillReadsEmpty_macOS: any other Keychain failure keeps
// the old contract, a logged "" and no error.
func TestOtherReadErrorStillReadsEmpty_macOS(t *testing.T) {
	kc := newFakeKC()
	kc.failGet = true
	s := macStore(t, kc)
	if v, err := s.ReadBackup("2", "bob@e.com"); v != "" || err != nil {
		t.Fatalf("ReadBackup on a failing Keychain = (%q, %v), want (\"\", nil)", v, err)
	}
}

// TestKeychainPrevWriteDropsStaleEncPrev_macOS: a generation retained in the
// Keychain removes the .enc.prev an earlier file-mode write left, which reads
// would otherwise prefer.
func TestKeychainPrevWriteDropsStaleEncPrev_macOS(t *testing.T) {
	kc := newFakeKC()
	kc.put(securityService, bobItem, storedBob)
	s := macStore(t, kc)
	if err := s.atomicB64Write(s.prevBackupPath("2", "bob@e.com"), "stale"); err != nil {
		t.Fatal(err)
	}

	if err := s.WriteBackup("2", "bob@e.com", storedBobNext); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.prevBackupPath("2", "bob@e.com")); !os.IsNotExist(err) {
		t.Errorf(".enc.prev still there (%v)", err)
	}
	if v, _ := s.ReadPrev("2", "bob@e.com"); v != storedBob {
		t.Errorf("ReadPrev = %q, want the generation just retained", v)
	}
}

// TestMoveFileBackupToKeychain_macOS: an .enc backup and its .enc.prev move
// into the Keychain and the files go; the slot reads the same before and after.
func TestMoveFileBackupToKeychain_macOS(t *testing.T) {
	kc := newFakeKC()
	s := macStore(t, kc)
	if err := s.writeBackupEnc("2", "bob@e.com", storedBob); err != nil {
		t.Fatal(err)
	}
	if err := s.atomicB64Write(s.prevBackupPath("2", "bob@e.com"), "older"); err != nil {
		t.Fatal(err)
	}

	moved, err := s.MoveFileBackupToKeychain("2", "bob@e.com")
	if err != nil || !moved {
		t.Fatalf("MoveFileBackupToKeychain = (%v, %v), want (true, nil)", moved, err)
	}
	if got, _ := kc.peek(securityService, bobItem); got != storedBob {
		t.Errorf("Keychain backup = %q", got)
	}
	if got, _ := kc.peek(securityService, bobPrevItem); got != "older" {
		t.Errorf("Keychain .prev = %q", got)
	}
	for _, p := range []string{s.backupEncPath("2", "bob@e.com"), s.prevBackupPath("2", "bob@e.com")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there (%v)", p, err)
		}
	}
	mustReadBackup(t, s, storedBob)

	if moved, err := s.MoveFileBackupToKeychain("2", "bob@e.com"); moved || err != nil {
		t.Errorf("second move = (%v, %v), want nothing to do", moved, err)
	}
}

// TestMoveKeepsEncPrevBesideKeychainPrev_macOS: with a .prev in the Keychain
// nothing says which generation is newer, so the file stays.
func TestMoveKeepsEncPrevBesideKeychainPrev_macOS(t *testing.T) {
	kc := newFakeKC()
	kc.put(securityService, bobItem, storedBob)
	kc.put(securityService, bobPrevItem, "keychain-prev")
	s := macStore(t, kc)
	if err := s.atomicB64Write(s.prevBackupPath("2", "bob@e.com"), "file-prev"); err != nil {
		t.Fatal(err)
	}

	if moved, err := s.MoveFileBackupToKeychain("2", "bob@e.com"); moved || err != nil {
		t.Fatalf("MoveFileBackupToKeychain = (%v, %v), want (false, nil)", moved, err)
	}
	if _, err := os.Stat(s.prevBackupPath("2", "bob@e.com")); err != nil {
		t.Errorf(".enc.prev removed beside a Keychain .prev: %v", err)
	}
	if got, _ := kc.peek(securityService, bobPrevItem); got != "keychain-prev" {
		t.Errorf("Keychain .prev = %q, want it untouched", got)
	}
}

// TestMoveKeepsTooLargeBackupInFile_macOS: a backup too large for security's
// stdin is in its .enc on purpose and stays there.
func TestMoveKeepsTooLargeBackupInFile_macOS(t *testing.T) {
	kc := newFakeKC()
	s := macStore(t, kc)
	big := `{"claudeAiOauth":{"accessToken":"` + strings.Repeat("a", keychain.SecurityStdinLineLimit) + `"}}`
	if err := s.writeBackupEnc("2", "bob@e.com", big); err != nil {
		t.Fatal(err)
	}
	if moved, err := s.MoveFileBackupToKeychain("2", "bob@e.com"); moved || err != nil {
		t.Fatalf("MoveFileBackupToKeychain = (%v, %v), want (false, nil)", moved, err)
	}
	if _, found := kc.peek(securityService, bobItem); found {
		t.Error("a too-large backup reached the Keychain")
	}
	mustReadBackup(t, s, big)
}

// TestMoveFailsOnBusyKeychain_macOS: a write the deadline killed leaves the
// file in place.
func TestMoveFailsOnBusyKeychain_macOS(t *testing.T) {
	kc := &busyKC{fakeKC: newFakeKC()}
	s := macStore(t, kc)
	if err := s.writeBackupEnc("2", "bob@e.com", storedBob); err != nil {
		t.Fatal(err)
	}
	kc.busy = true
	if moved, err := s.MoveFileBackupToKeychain("2", "bob@e.com"); moved || !keychain.IsTimeout(err) {
		t.Fatalf("MoveFileBackupToKeychain while busy = (%v, %v), want the timeout", moved, err)
	}
	if _, err := os.Stat(s.backupEncPath("2", "bob@e.com")); err != nil {
		t.Errorf(".enc gone after a failed move: %v", err)
	}
}

// TestMoveDoesNothingOffMacOS: elsewhere the .enc file is the backup.
func TestMoveDoesNothingOffMacOS(t *testing.T) {
	kc := newFakeKC()
	s := newStore(t, platform.Linux, t.TempDir(), kc, nil)
	if err := s.writeBackupEnc("2", "bob@e.com", storedBob); err != nil {
		t.Fatal(err)
	}
	if moved, err := s.MoveFileBackupToKeychain("2", "bob@e.com"); moved || err != nil {
		t.Fatalf("MoveFileBackupToKeychain off macOS = (%v, %v), want (false, nil)", moved, err)
	}
	if _, err := os.Stat(s.backupEncPath("2", "bob@e.com")); err != nil {
		t.Errorf(".enc gone off macOS: %v", err)
	}
}
