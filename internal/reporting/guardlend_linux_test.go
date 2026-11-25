//go:build linux

package reporting

import (
	"testing"

	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/store/ownertest"
	"github.com/tyclab/tycswap/internal/testutil"
)

type ownedRead struct {
	value string
	err   error
}

// readKeptCredential reads Account-num's owned credential through view.
func readKeptCredential(view *store.Store, num, email string, got chan<- ownedRead) {
	value, err := view.ReadOwnedCredentials(num, email)
	got <- ownedRead{value, err}
}

// The store a move's decision reads through takes the guard's hold of the
// store lock only while the guard lasts (DESIGN A118): inside, a former
// profile's credential is read at once; kept past MoveSessionLink, the same
// store takes the file again, so a holder elsewhere makes that read wait.
func TestAStoreKeptFromTheGuardTakesTheStoreLockAfterIt(t *testing.T) {
	f := ownerStore(t, ownertest.Options{Accounts: 6})
	s := f.Store
	root := s.BackupDir()
	link, err := CreateSessionLink(root, s.DefaultProfileDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := BindSessionLink(root, linkSessionID, link, "opus", "", s.Clk.Now()); err != nil {
		t.Fatal(err)
	}
	nested := testutil.NestedLocks(t)
	var kept *store.Store
	if err := MoveSessionLink(s, "test", linkSessionID, link, false, func(view *store.Store, _ *SessionLink) error {
		kept = view
		if value, err := view.ReadOwnedCredentials("2", f.Emails["2"]); err != nil || value != ownertest.Creds("2") {
			t.Errorf("inside the guard Account-2's credential reads %q, %v", value, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := nested(); len(n) != 0 {
		t.Fatalf("the read inside the guard asked for the store lock again:\n%s", n[0])
	}

	release := testutil.HoldLock(t, s.LockFile)
	got := make(chan ownedRead, 1)
	go readKeptCredential(kept, "2", f.Emails["2"], got)
	if !testutil.Parked(func() bool { return len(got) > 0 }, "reporting.readKeptCredential", "sleep") {
		r := <-got
		t.Fatalf("after the guard the kept store read %q, %v while another holder had the store lock", r.value, r.err)
	}
	release()
	if r := <-got; r.err != nil || r.value != ownertest.Creds("2") {
		t.Fatalf("after the holder released the store lock the kept store read %q, %v", r.value, r.err)
	}
}
