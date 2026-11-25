package switching

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/store/ownertest"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

// One read saw account 3's former profile idle and marked stale; then a
// session starts in it. The release decides on the running session, not on
// the drop of a stale idle login (DESIGN A116).
func TestReleaseIdleLegacyReadsAfreshInOneRead(t *testing.T) {
	testutil.IsolateHome(t)
	s, err := store.New(store.Options{Clock: testutil.FixedClock(t, "2026-11-25T09:00:00Z"), Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	f := ownertest.Build(t, s, ownertest.Options{Accounts: 6})
	dir := f.Legacy["3"]
	ownertest.EndSessions(t, dir)
	sessprofile.MarkStale(dir)
	view := s.OneRead()
	if _, err := view.CredentialOwner("3"); err != nil {
		t.Fatal(err)
	}
	ownertest.LiveSession(t, dir)
	if err := ReleaseIdleLegacy(view, "3"); err == nil || !strings.Contains(err.Error(), "Account-3 has a running session profile") {
		t.Fatalf("ReleaseIdleLegacy(3) = %v, want the running session", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, ccfile.CredentialsFileName)); err != nil || string(got) != ownertest.Creds("3") {
		t.Fatalf("the running profile's login changed: %q, %v", got, err)
	}
}
