package store_test

import (
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/store/ownertest"
	"github.com/tyclab/tycswap/internal/testutil"
)

// A read of an owned account's credential takes the store lock, so the owner
// and the credential it reads belong together (DESIGN A118): while another
// holder has the lock the read of a former profile's and the Opus group's
// account waits and gives up; once it is released the read gives the
// owner's credential.
func TestReadOwnedCredentialsTakesTheStoreLock(t *testing.T) {
	f := viewFixture(t)
	s := f.Store
	s.Lock = filelock.New(s.LockFile, 100*time.Millisecond)
	release := testutil.HoldLock(t, s.LockFile)
	for _, num := range []string{"2", "5"} {
		if value, err := s.ReadOwnedCredentials(num, f.Emails[num]); cerr.TypeName(err) != "LockError" || value != "" {
			t.Errorf("Account-%s while another holder has the store lock: %q, %v; want the lock refused", num, value, err)
		}
	}
	release()
	for _, num := range []string{"2", "5"} {
		if value, err := s.ReadOwnedCredentials(num, f.Emails[num]); err != nil || value != ownertest.Creds(num) {
			t.Errorf("Account-%s with the store lock free: %q, %v; want its owner's credential", num, value, err)
		}
	}
}
