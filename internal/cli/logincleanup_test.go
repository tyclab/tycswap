package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/lifecycle"
)

func TestConsoleLoginRefusedBeforeLaunching(t *testing.T) {
	for _, flag := range []string{"--console", "--console=true", "--console=false"} {
		t.Run(flag, func(t *testing.T) {
			f := newLoginFixture(t)
			before := f.liveCreds(t)
			code, _, errb := f.run(t, "add", "--login", "--", flag)
			if code != 1 || !strings.Contains(errb, "--console is not supported") {
				t.Fatalf("exit=%d stderr=%q", code, errb)
			}
			if _, err := os.Stat(f.log + ".args"); !os.IsNotExist(err) {
				t.Fatalf("login subprocess ran: %v", err)
			}
			if f.liveCreds(t) != before || len(f.sequence(t).Accounts) != 1 {
				t.Fatal("refused login changed the account")
			}
			assertNoScratch(t)
		})
	}
}

type failingLoginDelete struct {
	*keychain.Fake
	fail bool
}

func (k *failingLoginDelete) Delete(service, account string) error {
	if k.fail {
		return errors.New("simulated keychain failure with secret-like diagnostic")
	}
	return k.Fake.Delete(service, account)
}

func TestLoginCleanupFailureRetriedWithoutDeletingSharedKey(t *testing.T) {
	root := t.TempDir()
	kc := &failingLoginDelete{Fake: keychain.NewFake(), fail: true}
	dir, remove, err := makeLoginScratch(root, kc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kc.fail = false; _ = remove() })
	service := lifecycle.LoginKeychainService(dir)
	account := keychain.AccountName()
	if err := kc.Set(service, account, "scratch-fixture"); err != nil {
		t.Fatal(err)
	}
	if err := kc.Set("Claude Code", account, "shared-fixture"); err != nil {
		t.Fatal(err)
	}
	if err := remove(); err == nil || !strings.Contains(err.Error(), "retained for retry") || strings.Contains(err.Error(), "secret-like") {
		t.Fatalf("cleanup error: %v", err)
	}
	if !kc.Exists(service, account) {
		t.Fatal("failed deletion unexpectedly removed the key")
	}
	if _, err := os.Stat(filepath.Join(dir, loginCleanupMarker)); err != nil {
		t.Fatal(err)
	}
	// Another login may be active: it has no cleanup marker and must survive.
	active, err := os.MkdirTemp(root, "login.")
	if err != nil {
		t.Fatal(err)
	}
	kc.fail = false
	if err := retryLoginCleanups(root, kc); err != nil {
		t.Fatal(err)
	}
	if kc.Exists(service, account) {
		t.Fatal("scratch key survived retry")
	}
	if !kc.Exists("Claude Code", account) {
		t.Fatal("shared Console key was deleted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch directory survived: %v", err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active login was removed: %v", err)
	}
	if err := remove(); err != nil {
		t.Fatalf("cleanup was not idempotent: %v", err)
	}
}

func TestLoginCleanupClosureRetriesTransientFailure(t *testing.T) {
	kc := &failingLoginDelete{Fake: keychain.NewFake(), fail: true}
	dir, remove, err := makeLoginScratch(t.TempDir(), kc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kc.fail = false; _ = remove() })
	if err := remove(); err == nil {
		t.Fatal("failed deletion reported success")
	}
	kc.fail = false
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("retry left the scratch directory")
	}
}

func TestSavedLoginReportsIncompleteCleanup(t *testing.T) {
	f := newLoginFixture(t)
	kc := &failingLoginDelete{Fake: keychain.NewFake(), fail: true}
	useLoginKeychain(t, kc)
	code, out, errb := f.run(t, "add", "--login")
	if code != 0 || !strings.Contains(out, "Added Account 2") || !strings.Contains(errb, "cleanup incomplete") {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, errb)
	}
	if len(f.sequence(t).Accounts) != 2 {
		t.Fatal("cleanup failure undid the saved account")
	}
	kc.fail = false
	lifecycle.RunCleanups()
	assertNoScratch(t)
}
