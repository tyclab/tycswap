package reporting

import (
	"context"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

// TestInactiveRefreshRechecksUnderTheLock: the inactive-slot refresh decides
// again under the store lock. Only an unchanged lineage is refreshed and
// written back; a backup whose lineage moved on while the fetch waited is used
// as stored; a slot that became the live account, or whose lock is held, is
// not refreshed at all.
func TestInactiveRefreshRechecksUnderTheLock(t *testing.T) {
	realNowMS := time.Now().UnixMilli()
	held := oauthCreds("old-access", "rt-held", realNowMS-3600_000)
	newer := oauthCreds("newer-access", "rt-newer", realNowMS+3600_000)
	rotated := oauthCreds("rotated-access", "rt-rotated", realNowMS+3600_000)

	setup := func(t *testing.T, active int) (*store.Store, *int) {
		calls := new(int)
		oc := &oauth.FakeClient{RefreshFn: func(_ context.Context, _ string) oauth.RefreshOutcome {
			*calls++
			return oauth.RefreshOutcome{Credentials: rotated}
		}}
		s := newStore(t, testutil.FixedClock(t, fixedNow), oc)
		writeSequenceRaw(t, s, `{"activeAccountNumber": 1, "sequence": [1, 2], "accounts": {`+
			`"1": {"email": "a@example.com", "organizationUuid": ""}, "2": {"email": "b@example.com", "organizationUuid": ""}}}`)
		if active == 2 {
			writeLiveConfig(t, s, "b@example.com", "")
		}
		if err := s.Creds.WriteBackup("2", "b@example.com", held); err != nil {
			t.Fatal(err)
		}
		return s, calls
	}
	backup := func(s *store.Store) string { b, _ := s.ReadAccountCredentials("2", "b@example.com"); return b }

	t.Run("unchanged lineage is refreshed and persisted", func(t *testing.T) {
		s, calls := setup(t, 1)
		out := inactiveRefresh(s, "2", "b@example.com")(context.Background(), held)
		if *calls != 1 || out.Credentials != rotated || backup(s) != rotated {
			t.Fatalf("calls=%d out=%q backup=%q", *calls, out.Credentials, backup(s))
		}
	})
	t.Run("a lineage that moved on is used, not refreshed", func(t *testing.T) {
		s, calls := setup(t, 1)
		// A switch-back or another process wrote a newer generation after this
		// fetch read the backup.
		if err := s.Creds.WriteBackup("2", "b@example.com", newer); err != nil {
			t.Fatal(err)
		}
		out := inactiveRefresh(s, "2", "b@example.com")(context.Background(), held)
		if *calls != 0 || out.Credentials != newer || backup(s) != newer {
			t.Fatalf("calls=%d out=%q backup=%q", *calls, out.Credentials, backup(s))
		}
	})
	t.Run("a slot that became the live account is not refreshed", func(t *testing.T) {
		s, calls := setup(t, 2)
		out := inactiveRefresh(s, "2", "b@example.com")(context.Background(), held)
		if *calls != 0 || out.Credentials != "" || backup(s) != held {
			t.Fatalf("calls=%d out=%q", *calls, out.Credentials)
		}
	})
	t.Run("a held lock means no refresh", func(t *testing.T) {
		s, calls := setup(t, 1)
		s.Lock = newShortLock(s)
		holder := newShortLock(s)
		if ok, _ := holder.Acquire(time.Second); !ok {
			t.Fatal("could not take the lock")
		}
		defer holder.Release()
		out := inactiveRefresh(s, "2", "b@example.com")(context.Background(), held)
		if *calls != 0 || out.Credentials != "" {
			t.Fatalf("refreshed without the lock: calls=%d", *calls)
		}
	})
}

func newShortLock(s *store.Store) *filelock.FileLock {
	return filelock.New(s.LockFile, 20*time.Millisecond)
}
