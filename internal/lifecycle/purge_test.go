package lifecycle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/storenames"
)

// TestPurgeConfirmed removes the entire backup directory after a "y".
func TestPurgeConfirmed(t *testing.T) {
	s := newStore(t)
	seed(t, s, ip(1), switchable("1", "a@example.com"))
	answerYes(t)
	if err := Purge(s); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := os.Stat(s.BackupDir()); !os.IsNotExist(err) {
		t.Errorf("backup dir still exists: %v", err)
	}
}

// TestPurgeCancelled: "n" leaves everything.
func TestPurgeCancelled(t *testing.T) {
	s := newStore(t)
	seed(t, s, ip(1), switchable("1", "a@example.com"))
	withPrompter(t, &fakePrompter{prompts: []promptResp{{val: "n", ok: true}}})
	if err := Purge(s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.BackupDir()); err != nil {
		t.Errorf("backup dir removed despite cancel: %v", err)
	}
}

// TestPurgeSweepsLegacyNone: purge unlinks the legacy account-None credential
// alias (spec 01§13) and reports it before removing the tree.
func TestPurgeSweepsLegacyNone(t *testing.T) {
	s := newStore(t)
	seed(t, s, ip(3), acct{num: "3", email: "key@example.com", creds: "x", config: "y"})
	noneFile := filepath.Join(s.CredentialsDir, storenames.CredsFile("None", "key@example.com"))
	if err := os.WriteFile(noneFile, []byte("c3RhbGU="), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureOut(t)
	answerYes(t)
	if err := Purge(s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Credential file: "+storenames.CredsFile("None", "key@example.com")) {
		t.Errorf("legacy account-None sweep not reported:\n%s", out.String())
	}
}

// TestPurgeRefusesTraversalEmail: a roster email a file name cannot carry
// is refused, naming the slot, before any path is built from it: nothing
// inside the store is removed, and a file where the raw name would resolve,
// above the store, is never touched.
func TestPurgeRefusesTraversalEmail(t *testing.T) {
	s := newStore(t)
	seed(t, s, ip(1), switchable("1", "a@example.com"))
	const evil = "a/../../../x"
	outside := filepath.Clean(filepath.Join(s.CredentialsDir, storenames.CredsFile("1", evil)))
	if strings.HasPrefix(outside, s.BackupDir()+string(filepath.Separator)) {
		t.Fatalf("%s does not leave the store", outside)
	}
	if err := os.WriteFile(outside, []byte("not tycswap's"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := `{"accounts":{"1":{"email":"a@example.com"},"2":{"email":"` + evil + `"}},"sequence":[1,2]}`
	if err := os.WriteFile(s.SequenceFile, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureOut(t)
	answerYes(t)
	err := Purge(s)
	if errKind(err) != "ValidationError" || !strings.Contains(err.Error(), "Slot 2 ") {
		t.Fatalf("Purge = %v, want a ValidationError naming slot 2", err)
	}
	if strings.Contains(out.String(), "Are you sure") {
		t.Error("the user was prompted before the roster was refused")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "not tycswap's" {
		t.Errorf("the file outside the store was touched: %q, %v", b, err)
	}
	if _, err := os.Stat(s.BackupDir()); err != nil {
		t.Errorf("the store was removed despite the refusal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.CredentialsDir, storenames.CredsFile("1", "a@example.com"))); err != nil {
		t.Errorf("slot 1's credential file was removed: %v", err)
	}
}

// TestPurgeRefusesLiveSession: a live session-mode instance blocks purge.
func TestPurgeRefusesLiveSession(t *testing.T) {
	s := newStore(t)
	seed(t, s, ip(1), switchable("1", "a@example.com"))
	sessionsDir := filepath.Join(s.SessionDir("1", "a@example.com"), "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"pid": ` + strconv.Itoa(os.Getpid()) + `}`)
	if err := os.WriteFile(filepath.Join(sessionsDir, "self.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if errKind(Purge(s)) != "SessionError" {
		t.Fatal("want SessionError while a live session is running")
	}
}

// TestPurgeRemovesKeychainBackupsAndTheirPrev: on macOS, purge deletes each
// slot's Keychain backup and its retained .prev generation, for the slot's
// name and the legacy account-None alias, through the store's Keychain
// client, and reports every one; an item of another service stays.
func TestPurgeRemovesKeychainBackupsAndTheirPrev(t *testing.T) {
	kc := keychain.NewFake()
	s := newStoreOpts(t, store.Options{Keychain: kc})
	s.Platform = platform.MacOS
	seed(t, s, ip(1), switchable("1", "a@example.com"))
	const email = "a@example.com"
	ours := []string{
		storenames.KeychainAccount("1", email), storenames.KeychainAccountPrev("1", email),
		storenames.KeychainAccount("None", email), storenames.KeychainAccountPrev("None", email),
	}
	for _, name := range ours {
		kc.Seed(keychain.BackupService, name, "secret "+name)
	}
	kc.Seed("other-tool", "account-1-"+email, "not ours")

	out := captureOut(t)
	answerYes(t)
	if err := Purge(s); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	for _, name := range ours {
		if kc.Exists(keychain.BackupService, name) {
			t.Errorf("Keychain item %s survived purge", name)
		}
		if !strings.Contains(out.String(), "Credential: "+name) {
			t.Errorf("removal of %s not reported:\n%s", name, out.String())
		}
	}
	if !kc.Exists("other-tool", "account-1-"+email) {
		t.Error("purge deleted another service's item")
	}
}
