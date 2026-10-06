package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

// transfer.Import reads the roster through this adapter from inside its own
// write-pass lock: a roster that still needs the org backfill is migrated
// under that lock, not by taking the non-reentrant store lock again.
func TestImportRosterReadMigratesUnderTheImportLock(t *testing.T) {
	cleanHome(t)
	sw, err := core.New(store.Options{Clock: testutil.FixedClock(t, "2026-07-17T09:00:00Z"), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Store.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sw.Store.SequenceFile, []byte(`{"sequence":[1],"accounts":{"1":{"email":"a@example.com"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sw.Store.Lock = filelock.New(sw.Store.LockFile, 100*time.Millisecond)
	importLock := filelock.New(sw.Store.LockFile, time.Second)
	if ok, err := importLock.Acquire(time.Second); err != nil || !ok {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	defer importLock.Release()

	data, err := transferAdapter{sw}.MigratedSequenceForUpdate()
	if err != nil {
		t.Fatalf("MigratedSequenceForUpdate under the import's lock: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(data.Accounts["1"], &rec); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec["organizationUuid"]; !ok {
		t.Errorf("record 1 = %v, want the org backfill applied", rec)
	}
}
