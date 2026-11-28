// Tests for AddFableLogin, remove and move when the default login, the roster
// or the Fable store changed under them, and for the locks they wait for
// (DESIGN A101).
package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
)

// The live default credential was rotated since its backup was written: it
// is still the account's default grant.
func TestAddFableLoginRefusesTheRotatedLiveDefaultLogin(t *testing.T) {
	s := fableLifecycleStore(t)
	rotated := planCreds("at-d1r", "rt-d1r", "max")
	seedLiveLogin(t, s, "a@example.com", "", "", "uuid-a", rotated)
	out := captureOut(t)
	before := everyFile(t, s)
	_, err := AddFableLogin(s, signIn(t, "a@example.com", rotated), nil)
	if want := "This is Account-1's default login, not a second sign-in; sign in again in the browser."; err == nil || err.Error() != want {
		t.Fatalf("AddFableLogin = %v, want %q", err, want)
	}
	sameFiles(t, "the refusal", before, everyFile(t, s))
	if out.Len() != 0 {
		t.Errorf("the refusal printed %q", out.String())
	}
}

func fableStoreFile(s *store.Store) string {
	return filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json")
}

func putFableStore(t *testing.T, s *store.Store, creds string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(fableStoreFile(s)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fableStoreFile(s), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
}

// unknownInTheStore puts a login only the profile endpoint knows in the Fable
// store; asking the endpoint about it runs hook and answers id.
func unknownInTheStore(t *testing.T, s *store.Store, id *oauth.Identity, hook func()) {
	t.Helper()
	putFableStore(t, s, planCreds("at-x", "rt-x", "max"))
	s.OAuth = &oauth.FakeClient{ProfileFn: func(_ context.Context, tok string) *oauth.Identity {
		if tok != "at-x" {
			return nil
		}
		hook()
		return id
	}}
}

var accountOne = &oauth.Identity{Email: "a@example.com"}

// The re-check under the store lock refuses on a box without a Fable login:
// the box stays without fable/ and credentials-fable/ (DESIGN A100).
func TestAddFableLoginRefusedUnderTheLockCreatesNoFableDirectory(t *testing.T) {
	s := fableLifecycleStore(t)
	captureOut(t)
	other := filelock.New(s.LockFile, 0)
	if ok, err := other.Acquire(0); !ok || err != nil {
		t.Fatalf("Acquire = %v, %v", ok, err)
	}
	t.Cleanup(func() { _ = other.Release() })
	done := make(chan error, 1)
	go func() {
		_, err := AddFableLogin(s, signIn(t, "b@example.com", planCreds("at-f2", "rt-f2", "max")), nil)
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	data := readSeq(t, s)
	delete(data.Accounts, "2")
	data.Sequence = []int{1, 3, 4}
	if err := s.WriteSequence(data); err != nil {
		t.Fatal(err)
	}
	_ = other.Release()
	if err, want := <-done, "Account-2 changed while the sign-in was stored; nothing stored."; err == nil || err.Error() != want {
		t.Fatalf("AddFableLogin = %v, want %q", err, want)
	}
	for _, dir := range []string{paths.FableCredentialsDir(s.BackupDir()), paths.FableScopeDir(s.BackupDir())} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("the refusal left %s", dir)
		}
	}
}

func TestAddFableLoginWaitsForTheStoreLock(t *testing.T) {
	s := fableLifecycleStore(t)
	captureOut(t)
	other := filelock.New(s.LockFile, 0)
	if ok, err := other.Acquire(0); !ok || err != nil {
		t.Fatalf("Acquire = %v, %v", ok, err)
	}
	t.Cleanup(func() { _ = other.Release() })
	asked := make(chan struct{})
	unknownInTheStore(t, s, nil, func() { close(asked) })
	done := make(chan error, 1)
	go func() {
		_, err := AddFableLogin(s, signIn(t, "b@example.com", planCreds("at-f2", "rt-f2", "max")), nil)
		done <- err
	}()
	<-asked
	stayed(t, func() bool { return !exists(fableFile(s, "2", "b@example.com")) }, "stored while another process held the store lock")
	_ = other.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := fableLoginOf(t, s, "2", "b@example.com"); got != planCreds("at-f2", "rt-f2", "max") {
		t.Fatalf("Fable login = %q", got)
	}
}

// Another tycswap switched the Fable store to Account-2 between the renewal's
// first read and its locks: the new login is stored, the store left on 2.
func TestAddFableLoginRenewalRechecksTheStoreUnderTheLocks(t *testing.T) {
	s := fableLifecycleStore(t)
	out := captureOut(t)
	two := planCreds("at-f2", "rt-f2", "max")
	if _, err := AddFableLogin(s, signIn(t, "b@example.com", two), nil); err != nil {
		t.Fatal(err)
	}
	unknownInTheStore(t, s, accountOne, func() { putFableStore(t, s, two) })
	out.Reset()
	fresh := planCreds("at-f1", "rt-f1", "max")
	if _, err := AddFableLogin(s, signIn(t, "a@example.com", fresh), nil); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(fableStoreFile(s)); string(raw) != two {
		t.Fatalf("store = %s, want Account-2's login left in place", raw)
	}
	if got := fableLoginOf(t, s, "1", "a@example.com"); got != fresh {
		t.Fatalf("Fable login = %q", got)
	}
	if got := out.String(); got != "Stored the Fable login for Account-1 (a@example.com).\n" {
		t.Errorf("output = %q", got)
	}
}

func TestAddFableLoginRenewalWaitsForClaudeCodesRefreshLock(t *testing.T) {
	s := fableLifecycleStore(t)
	captureOut(t)
	asked := make(chan struct{})
	unknownInTheStore(t, s, accountOne, func() { close(asked) })
	lock := paths.FableStoreDir(s.BackupDir()) + ".lock"
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })
	fresh := planCreds("at-f1", "rt-f1", "max")
	done := make(chan error, 1)
	go func() {
		_, err := AddFableLogin(s, signIn(t, "a@example.com", fresh), nil)
		done <- err
	}()
	<-asked
	stayed(t, func() bool {
		raw, _ := os.ReadFile(fableStoreFile(s))
		return string(raw) == planCreds("at-x", "rt-x", "max") && !exists(fableFile(s, "1", "a@example.com"))
	}, "renewed while Claude Code held the Fable store's credential-refresh lock")
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if live, _ := os.ReadFile(fableStoreFile(s)); !oauth.SameAccountBlock(string(live), fresh) {
		t.Fatalf("store = %s, want the new login", live)
	}
}

// An emptying that leaves the Fable store on the account refuses the remove
// before its Fable login is deleted.
func TestRemoveRefusesWhenTheFableStoreStillHoldsTheAccount(t *testing.T) {
	s := withFableLogins(t, "2")
	prev := EmptyFableStoreWithin
	EmptyFableStoreWithin = func(s *store.Store, fn func(empty func() error) error) error {
		return prev(s, func(func() error) error { return fn(func() error { return nil }) })
	}
	t.Cleanup(func() { EmptyFableStoreWithin = prev })
	err := RemoveAccount(s, "2", true)
	if want := "The Fable store took Account-2 again; nothing was removed. Retry the command."; err == nil || err.Error() != want {
		t.Fatalf("RemoveAccount = %v, want %q", err, want)
	}
	if !exists(fableFile(s, "2", "b@example.com")) {
		t.Error("the Fable login of the store's account was deleted")
	}
	if _, ok := recordAt(readSeq(t, s), "2"); !ok {
		t.Error("Account-2 left the roster")
	}
}

func TestMoveFailureLeavesNoFableLoginUnderTheTarget(t *testing.T) {
	s := withFableLogins(t, "1")
	before := everyFile(t, s)
	s.Creds = &fakeCreds{Store: s.Creds, failWrite: func(num, _, _ string) bool { return num == "5" }}
	if _, _, _, err := MoveAccount(s, "2", "5"); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("MoveAccount = %v, want the injected failure", err)
	}
	sameFiles(t, "the failed move", before, everyFile(t, s))
}

// stayed fails the test when ok turns false within a second.
func stayed(t *testing.T, ok func() bool, msg string) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if !ok() {
			t.Fatal(msg)
		}
	}
}
