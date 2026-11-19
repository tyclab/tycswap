// Tests for a Fable switch when another process changed the Fable store
// between its first read and its locks (DESIGN A100, the switch's step 3).
package switching

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/storenames"
)

// changedBeforeTheLocks: the store holds a login only the profile endpoint
// names (account 1); while the endpoint answers, the store changes to next.
// The endpoint must not be asked under the locks.
func changedBeforeTheLocks(t *testing.T, next string) (s, f *store.Store, calls *int) {
	t.Helper()
	s, f = fableSwitchStore(t)
	setFableLive(t, s, planCreds("at-x", "rt-x", "max"))
	calls = new(int)
	s.OAuth = &oauth.FakeClient{ProfileFn: func(_ context.Context, tok string) *oauth.Identity {
		*calls++
		if s.Lock.Held() {
			t.Errorf("the profile endpoint was asked for %s under the store lock", tok)
		}
		if tok == "at-x" {
			setFableLive(t, s, next)
			return &oauth.Identity{UUID: "u-1", Email: fableEmails["1"], OrgUUID: "org-1"}
		}
		return nil
	}}
	return s, f, calls
}

func TestSwitchFableResolvesALoginChangedBeforeTheLocks(t *testing.T) {
	three := planCreds("at-f3", "rt-f3", "pro")
	s, f, calls := changedBeforeTheLocks(t, three)
	res, err := SwitchFable(s, "2", FableSwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := refOf(t, res["from"]); n != 3 {
		t.Fatalf("from %v, want account 3, whose login the store held under the locks", res["from"])
	}
	if got, _ := f.ReadAccountCredentials("1", fableEmails["1"]); got != planCreds("at-f1", "rt-f1", "max") {
		t.Fatalf("1's Fable login = %s, want it unchanged", got)
	}
	if got, _ := f.ReadAccountCredentials("3", fableEmails["3"]); got != three {
		t.Fatalf("3's Fable login = %s", got)
	}
	if *calls != 1 {
		t.Fatalf("profile asked %d times, want once, before the locks", *calls)
	}
}

func TestSwitchFableStashesAnUnknownLoginChangedBeforeTheLocks(t *testing.T) {
	s, f, calls := changedBeforeTheLocks(t, planCreds("at-y", "rt-y", "max"))
	res, err := SwitchFable(s, "2", FableSwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res["from"] != nil && res["from"].(map[string]any) != nil {
		t.Fatalf("from %v, want none", res["from"])
	}
	if rows, _ := f.Creds.ListUnclaimed(); len(rows) != 1 {
		t.Fatalf("unclaimed %v, want the unknown login", rows)
	}
	if got, _ := f.ReadAccountCredentials("1", fableEmails["1"]); got != planCreds("at-f1", "rt-f1", "max") {
		t.Fatalf("1's Fable login = %s, want it unchanged", got)
	}
	if *calls != 1 {
		t.Fatalf("profile asked %d times, want once, before the locks", *calls)
	}
}

func TestEmptyFableWaitsForClaudeCodesRefreshLock(t *testing.T) {
	s, f := fableSwitchStore(t)
	live := planCreds("at-f1", "rt-f1", "max")
	setFableLive(t, s, live)
	lock := paths.FableStoreDir(s.BackupDir()) + ".lock"
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })
	done := make(chan error, 1)
	go func() {
		_, err := EmptyFable(f)
		done <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); !f.Lock.Held(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("EmptyFable never took the store lock")
		}
	}
	time.Sleep(300 * time.Millisecond)
	if raw, _ := os.ReadFile(fableStorePath(s)); string(raw) != live {
		t.Fatal("emptied while Claude Code held the Fable store's credential-refresh lock")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fableStorePath(s)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store file after EmptyFable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.BackupDir(), "fable", "store.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the credential-refresh lock was left behind")
	}
}

// swapWhileWaiting runs act while another tycswap holds the store lock, and
// commits `swap a b` meanwhile, as lifecycle's swap leaves the store: the two
// roster records, their Fable logins and the chain record of the Fable
// store's account change places. act has read and resolved by then.
func swapWhileWaiting(t *testing.T, s, f *store.Store, a, b string, act func() error) error {
	t.Helper()
	if ok, err := s.Lock.Acquire(0); err != nil || !ok {
		t.Fatalf("store lock: %v %v", ok, err)
	}
	done := make(chan error, 1)
	go func() { done <- act() }()
	time.Sleep(500 * time.Millisecond)
	data := mustSeq(t, s)
	data.Accounts[a], data.Accounts[b] = data.Accounts[b], data.Accounts[a]
	writeSeq(t, s, data)
	loginA, _ := f.ReadAccountCredentials(a, fableEmails[a])
	loginB, _ := f.ReadAccountCredentials(b, fableEmails[b])
	for _, w := range []struct{ num, email, creds string }{{b, fableEmails[a], loginA}, {a, fableEmails[b], loginB}} {
		if err := f.WriteAccountCredentials(w.num, w.email, w.creds); err != nil {
			t.Fatal(err)
		}
	}
	dir := paths.FableCredentialsDir(s.BackupDir())
	for _, old := range []string{storenames.CredsFile(a, fableEmails[a]), storenames.CredsFile(b, fableEmails[b])} {
		if err := os.Remove(filepath.Join(dir, old)); err != nil {
			t.Fatal(err)
		}
	}
	if live, _ := os.ReadFile(fableStorePath(s)); len(live) > 0 {
		if ls := s.FableStore().Live.Slot; ls == "" {
			t.Fatal("the swap left the store's account unknown")
		}
	}
	if err := s.Lock.Release(); err != nil {
		t.Fatal(err)
	}
	return <-done
}

// The target's slot changes hands while the switch waits for the store lock:
// it switches nothing and writes no login into another account's file.
func TestSwitchFableRefusesATargetRenumberedWhileItWaited(t *testing.T) {
	s, f := fableSwitchStore(t)
	if _, err := SwitchFable(f, "1", FableSwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	one, _ := f.ReadAccountCredentials("1", fableEmails["1"])
	two, _ := f.ReadAccountCredentials("2", fableEmails["2"])
	live, _ := os.ReadFile(fableStorePath(s))
	err := swapWhileWaiting(t, s, f, "1", "2", func() error {
		_, err := SwitchFable(f, "2", FableSwitchOptions{})
		return err
	})
	if want := "Account-2 changed while the switch waited for the store lock; nothing switched. Retry the command."; err == nil || err.Error() != want {
		t.Errorf("SwitchFable = %v, want %q", err, want)
	}
	if got, _ := f.ReadAccountCredentials("1", fableEmails["2"]); got != two {
		t.Errorf("slot 1's new owner's Fable login = %s, want %s", got, two)
	}
	if got, _ := f.ReadAccountCredentials("2", fableEmails["1"]); got != one {
		t.Errorf("slot 2's new owner's Fable login = %s, want %s", got, one)
	}
	if raw, _ := os.ReadFile(fableStorePath(s)); string(raw) != string(live) {
		t.Errorf("the store changed: %s", raw)
	}
}

// The outgoing account's slot changes hands while the switch waits: its live
// login goes into its own Fable login under its new number.
func TestSwitchFableBacksUpAnOutgoingAccountRenumberedWhileItWaited(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(f *store.Store) (map[string]any, error)
	}{
		{"switch", func(f *store.Store) (map[string]any, error) { return SwitchFable(f, "2", FableSwitchOptions{}) }},
		{"empty", EmptyFable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := fableSwitchStore(t)
			if _, err := SwitchFable(f, "1", FableSwitchOptions{}); err != nil {
				t.Fatal(err)
			}
			three, _ := f.ReadAccountCredentials("3", fableEmails["3"])
			var res map[string]any
			err := swapWhileWaiting(t, s, f, "1", "3", func() error {
				var err error
				res, err = tc.act(f)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := f.ReadAccountCredentials("1", fableEmails["3"]); got != three {
				t.Errorf("slot 1's new owner's Fable login = %s, want %s", got, three)
			}
			if n, e := refOf(t, res["from"]); n != 3 || e != fableEmails["1"] {
				t.Errorf("from %v, want Account-3 (%s)", res["from"], fableEmails["1"])
			}
		})
	}
}
