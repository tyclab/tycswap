// A Keychain call the deadline cut off, as on a Mac under heavy load, is a busy
// Keychain and not an unusable one (DESIGN A69): it starts no file-mode
// cooldown, so the next read still asks the Keychain, and an active-credential
// write that timed out fails rather than leave the live login in a plaintext
// file that Claude Code reads only after the Keychain item.
package credstore

import (
	"os"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/testutil"
)

const (
	liveAlice     = `{"claudeAiOauth":{"accessToken":"alice-access","refreshToken":"alice-refresh","expiresAt":4102444800000}}`
	storedBob     = `{"claudeAiOauth":{"accessToken":"bob-access","refreshToken":"bob-refresh","expiresAt":4102444800000}}`
	storedBobNext = `{"claudeAiOauth":{"accessToken":"bob-access-2","refreshToken":"bob-refresh-2","expiresAt":4102444800000}}`
	bobItem       = "account-2-bob@e.com"
	bobPrevItem   = bobItem + ".prev"
)

// busyKC times out every call while busy is set, every write of busySet's
// items and every delete of busyDelete's. deletes counts Delete calls.
type busyKC struct {
	*fakeKC
	busy                bool
	busySet, busyDelete string
	deletes             int
}

func timedOut() error {
	return &keychain.KeychainError{Msg: "security timed out after 15s", Timeout: true}
}

func (k *busyKC) Get(service, account string) (string, bool, error) {
	if k.busy {
		return "", false, timedOut()
	}
	return k.fakeKC.Get(service, account)
}

func (k *busyKC) Set(service, account, password string) error {
	if k.busy || service == k.busySet {
		return timedOut()
	}
	return k.fakeKC.Set(service, account, password)
}

func (k *busyKC) Delete(service, account string) error {
	k.deletes++
	if k.busy || service == k.busyDelete {
		return timedOut()
	}
	return k.fakeKC.Delete(service, account)
}

// busyStore is a macOS store over a busyKC whose OAuth item holds liveAlice,
// with no credentials file beside it.
func busyStore(t *testing.T) (*FileKeychainStore, *busyKC, testutil.FixtureHome) {
	t.Helper()
	fh := testutil.BuildFixtureHome(t)
	if err := os.Remove(fh.CredentialsFile); err != nil {
		t.Fatal(err)
	}
	kc := &busyKC{fakeKC: newFakeKC()}
	kc.put(claudeCodeKeychainService, keychain.AccountName(), liveAlice)
	s := newStore(t, platform.MacOS, t.TempDir(), kc, testutil.FixedClock(t, "2026-10-07T21:00:00Z"))
	return s, kc, fh
}

// TestTimedOutBackupReadKeepsKeychain_macOS: a backup read that timed out (the
// app reads every slot's backup every few seconds) leaves the active read and
// the next backup write on the Keychain. Before A69 the active read skipped
// the Keychain for a minute and reported "keychain unavailable", and the
// backup write went to an .enc file.
func TestTimedOutBackupReadKeepsKeychain_macOS(t *testing.T) {
	s, kc, _ := busyStore(t)
	kc.put(securityService, bobItem, storedBob)

	kc.busy = true
	if v, _ := s.ReadBackup("2", "bob@e.com"); v != "" {
		t.Fatalf("ReadBackup while busy = %q, want \"\"", v)
	}
	kc.busy = false

	v, unavailable, err := s.ReadActive()
	if err != nil || unavailable || v != liveAlice {
		t.Fatalf("ReadActive after a timed-out backup read = (%q, %v, %v), want the Keychain login", v, unavailable, err)
	}
	if err := s.WriteBackup("3", "carol@e.com", storedBob); err != nil {
		t.Fatal(err)
	}
	if got, _ := kc.peek(securityService, "account-3-carol@e.com"); got != storedBob {
		t.Errorf("backup Keychain item = %q, want the backup", got)
	}
	if _, err := os.Stat(s.backupEncPath("3", "carol@e.com")); err == nil {
		t.Error("backup went to an .enc file after a timed-out read")
	}
}

// TestTimedOutReadsReportUnavailable_macOS: a Keychain that times out on every
// attempt still reports the active login as unavailable, not as absent.
func TestTimedOutReadsReportUnavailable_macOS(t *testing.T) {
	s, kc, _ := busyStore(t)
	kc.busy = true
	v, unavailable, err := s.ReadActive()
	if err != nil || !unavailable || v != "" {
		t.Fatalf("ReadActive while busy = (%q, %v, %v), want (\"\", true, nil)", v, unavailable, err)
	}
}

// TestTimedOutActiveWriteFails_macOS: a switch's write of an OAuth login that
// timed out fails with the timeout. It writes no credentials file, does not
// clear the Keychain item, and leaves the next write on the Keychain. Before
// A69 it fell back to the file and pinned file mode for the process.
func TestTimedOutActiveWriteFails_macOS(t *testing.T) {
	s, kc, fh := busyStore(t)
	kc.busy = true
	err := s.WriteActiveAccount(storedBob)
	if !keychain.IsTimeout(err) {
		t.Fatalf("WriteActiveAccount while busy = %v, want the timeout", err)
	}
	if got := readFileOrNone(t, fh.CredentialsFile); got != "" {
		t.Errorf("credentials file = %q after a timed-out write, want none", got)
	}
	if got, _ := kc.peek(claudeCodeKeychainService, keychain.AccountName()); got != liveAlice {
		t.Errorf("OAuth Keychain item = %q after a timed-out write, want the old login", got)
	}

	kc.busy = false
	if err := s.WriteActiveAccount(storedBob); err != nil {
		t.Fatal(err)
	}
	if got := s.LastActiveBackend(); got != "keychain" {
		t.Errorf("backend after the Keychain answered = %q, want keychain", got)
	}
	if got, _ := kc.peek(claudeCodeKeychainService, keychain.AccountName()); !strings.Contains(got, "bob-access") {
		t.Errorf("OAuth Keychain item = %q, want the new login", got)
	}
}

// TestTimedOutManagedWriteFails_macOS: the same for a managed API key. It is
// not put into primaryApiKey, where the "Claude Code" item would shadow it.
func TestTimedOutManagedWriteFails_macOS(t *testing.T) {
	s, kc, _ := busyStore(t)
	kc.busy = true
	if err := s.WriteActiveAccount(seatKey); !keychain.IsTimeout(err) {
		t.Fatalf("WriteActiveAccount(key) while busy = %v, want the timeout", err)
	}
	cfg, err := ccfile.ReadGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := cfg["primaryApiKey"]; ok {
		t.Errorf("primaryApiKey = %v after a timed-out write, want none", k)
	}
	assertKeychainItem(t, kc.fakeKC, managedKeychainService, "")
	if got, _ := kc.peek(claudeCodeKeychainService, keychain.AccountName()); got != liveAlice {
		t.Errorf("OAuth Keychain item = %q after a timed-out write, want the old login", got)
	}
}

// TestTimedOutOAuthClearFailsManagedWrite_macOS: a switch onto an API key
// whose Keychain stores the key but times out taking the OAuth login off
// "Claude Code-credentials", the item Claude Code reads first, fails and puts
// the managed item back. The login stays, in the item and its shadow file,
// whether the write of the seat's MCP logins in its place or the delete of
// the item timed out, and when both would.
func TestTimedOutOAuthClearFailsManagedWrite_macOS(t *testing.T) {
	for _, tc := range []struct {
		name, live  string
		set, delete bool
	}{
		{"MCP logins write-back", loginBesideMCP, true, false},
		{"item delete", liveAlice, false, true},
		{"both, MCP logins", loginBesideMCP, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, kc, fh := busyStore(t)
			kc.put(claudeCodeKeychainService, keychain.AccountName(), tc.live)
			writeFile(t, fh.CredentialsFile, tc.live)
			if tc.set {
				kc.busySet = claudeCodeKeychainService
			}
			if tc.delete {
				kc.busyDelete = claudeCodeKeychainService
			}

			if err := s.WriteActiveAccount(seatKey); !keychain.IsTimeout(err) {
				t.Fatalf("WriteActiveAccount(key) = %v, want the timeout", err)
			}
			if got, _ := kc.peek(claudeCodeKeychainService, keychain.AccountName()); got != tc.live {
				t.Errorf("OAuth Keychain item = %q, want the old login", got)
			}
			if got := readFileOrNone(t, fh.CredentialsFile); got != tc.live {
				t.Errorf("credentials file = %q, want the old login", got)
			}
			assertKeychainItem(t, kc.fakeKC, managedKeychainService, "")
			if got := s.LastActiveBackend(); got != "" {
				t.Errorf("backend = %q after a failed write, want none recorded", got)
			}
		})
	}
}

// TestTimedOutBackupWriteSkipsKeychainDelete_macOS: a backup write whose
// Keychain write timed out goes to the .enc, which reads serve first, and
// spends no second deadline on deleting the item.
func TestTimedOutBackupWriteSkipsKeychainDelete_macOS(t *testing.T) {
	kc := &busyKC{fakeKC: newFakeKC(), busySet: securityService}
	s := newStore(t, platform.MacOS, t.TempDir(), kc, nil)
	if err := s.WriteBackup("2", "bob@e.com", storedBob); err != nil {
		t.Fatal(err)
	}
	if v, err := s.ReadBackup("2", "bob@e.com"); v != storedBob || err != nil {
		t.Errorf("ReadBackup = (%q, %v), want the backup from its .enc", v, err)
	}
	if kc.deletes != 0 {
		t.Errorf("Keychain deletes = %d after a timed-out write, want 0", kc.deletes)
	}
}

// TestTimedOutClearActiveFails_macOS: taking the login off fails with the
// timeout when the item's delete timed out, and the login stays.
func TestTimedOutClearActiveFails_macOS(t *testing.T) {
	s, kc, _ := busyStore(t)
	kc.busyDelete = claudeCodeKeychainService
	if err := s.ClearActive(); !keychain.IsTimeout(err) {
		t.Fatalf("ClearActive = %v, want the timeout", err)
	}
	if got, _ := kc.peek(claudeCodeKeychainService, keychain.AccountName()); got != liveAlice {
		t.Errorf("OAuth Keychain item = %q, want the login still there", got)
	}
}
