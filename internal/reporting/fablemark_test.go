// The account snapshot reads no stored login for the Fable mark (DESIGN
// A107): it feeds every refresh of the dashboard, the tray and the TUI.
package reporting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/storenames"
	"github.com/tyclab/tycswap/internal/testutil"
)

// snapshotReadsOnMain is how many times Snapshot read each account's stored
// login on origin/main 5969cfa, measured with this test there.
const snapshotReadsOnMain = 4

// A corrupt backup logs one warning per read, so the log counts the reads.
func TestSnapshotReadsNoStoredLoginForFable(t *testing.T) {
	s := newStore(t, testutil.FixedClock(t, fixedNow), nil)
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": null,
  "lastUpdated": "2026-07-17T08:00:00Z",
  "sequence": [1, 2, 3],
  "accounts": {
    "1": {"email": "max@example.com", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "x"},
    "2": {"email": "org@example.com", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "x"},
    "3": {"email": "marked@example.com", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "x", "fableContinue": true}
  }
}`)
	for num, email := range map[string]string{"1": "max@example.com", "2": "org@example.com", "3": "marked@example.com"} {
		writeBackup(t, s, num, email, oauthCreds("at"+num, "rt"+num, 0), `{"oauthAccount":{}}`)
		if err := os.WriteFile(filepath.Join(s.CredentialsDir, storenames.CredsFile(num, email)), []byte("!not base64!"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reads := func() int {
		raw, _ := os.ReadFile(filepath.Join(s.BackupDir(), "tycswap.log"))
		return strings.Count(string(raw), "Failed to read credentials file")
	}
	before := reads()
	Snapshot(s, nil)
	if got := reads() - before; got != 3*snapshotReadsOnMain {
		t.Fatalf("Snapshot read the stored logins %d times, want %d (%d per account, as on main)", got, 3*snapshotReadsOnMain, snapshotReadsOnMain)
	}
}
