package reporting

import (
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/filelock"
)

// TestStatusAndListReportABusyStoreLock: a roster that still needs the org
// backfill waits for the store lock. When another tycswap holds it past the
// budget, status and list report the lock error instead of reading the roster
// as absent: "(not managed)" for the live account, or no accounts (DESIGN A57).
func TestStatusAndListReportABusyStoreLock(t *testing.T) {
	s := newStore(t, nil, nil)
	writeSequenceRaw(t, s, `{"activeAccountNumber":1,"lastUpdated":"x","sequence":[1],"accounts":{"1":{"email":"a@example.com"}}}`)
	writeLiveConfig(t, s, "a@example.com", "")
	holder := filelock.New(s.LockFile, time.Second)
	if ok, err := holder.Acquire(time.Second); err != nil || !ok {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	defer holder.Release()
	s.Lock = filelock.New(s.LockFile, 100*time.Millisecond)

	for _, jsonOut := range []bool{true, false} {
		if _, err := Status(s, jsonOut); cerr.TypeName(err) != string(cerr.KindLock) {
			t.Errorf("Status(json=%v) = %v, want the lock error", jsonOut, err)
		}
		if _, err := ListAccounts(s, false, jsonOut, nil); cerr.TypeName(err) != string(cerr.KindLock) {
			t.Errorf("ListAccounts(json=%v) = %v, want the lock error", jsonOut, err)
		}
	}
}
