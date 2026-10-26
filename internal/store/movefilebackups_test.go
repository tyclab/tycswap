// The app's start-up move of file-mode backups into the Keychain (DESIGN
// A69) runs under the FileLock, so a Keychain that times out ends it rather
// than hold the lock a deadline per slot.
package store

import (
	"testing"

	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
)

// failingMove fails every slot's move with err and counts the calls.
type failingMove struct {
	credstore.Store
	err   error
	calls int
}

func (f *failingMove) MoveFileBackupToKeychain(num, email string) (bool, error) {
	f.calls++
	return false, f.err
}

func TestMoveFileBackupsStopsAtFirstTimeout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		calls       int
		wantTimeout bool
	}{
		{"a timeout ends the run", &keychain.KeychainError{Msg: "security timed out after 15s", Timeout: true}, 1, true},
		{"another failure skips the slot", &keychain.KeychainError{Msg: "security failed (rc=51)"}, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := freshStore(t)
			s.Platform = platform.MacOS
			writeSequenceRaw(t, s, `{
  "activeAccountNumber": 1,
  "lastUpdated": "2026-07-17T09:00:00Z",
  "sequence": [1, 2, 3],
  "accounts": {
    "1": {"email": "a@example.com", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "t"},
    "2": {"email": "b@example.com", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "t"},
    "3": {"email": "c@example.com", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "t"}
  }
}`)
			f := &failingMove{Store: s.Creds, err: tc.err}
			s.Creds = f

			moved, err := s.MoveFileBackupsToKeychain()
			if moved != 0 || keychain.IsTimeout(err) != tc.wantTimeout || (!tc.wantTimeout && err != nil) {
				t.Errorf("MoveFileBackupsToKeychain = (%d, %v), want the timeout: %v", moved, err, tc.wantTimeout)
			}
			if f.calls != tc.calls {
				t.Errorf("slots tried = %d, want %d", f.calls, tc.calls)
			}
		})
	}
}
