// Tests for what the Fable scope never touches and for the Fable store's
// re-checks and locks around a change made by another process (DESIGN A100).
package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
)

var fableTwo = oauth.Identity{UUID: "u-2", Email: "two@example.com", OrgUUID: "org-2"}

func TestFableLoginWriteInvalidatesNoSessionProfile(t *testing.T) {
	s := fableFixture(t)
	dir := s.SessionDir("1", fableEmails["1"])
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte(fableCreds("at-d1", "rt-d1")), 0o600); err != nil {
		t.Fatal(err)
	}
	putFableLogin(t, s, "1", fableCreds("at-f1", "rt-f1"))
	if _, err := os.Stat(creds); err != nil {
		t.Fatalf("a Fable login write invalidated the session profile: %v", err)
	}
	if err := s.WriteAccountCredentials("1", fableEmails["1"], fableCreds("at-d1b", "rt-d1b")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(creds); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a default login write left the session profile's credential (%v)", err)
	}
}

func TestFableScopeNamesNoCurrentAccount(t *testing.T) {
	s := fableFixture(t)
	if email, _, ok := s.GetCurrentAccount(); !ok || email != "one@example.com" {
		t.Fatalf("default GetCurrentAccount = %q, %v", email, ok)
	}
	if email, org, ok := mustFable(t, s).GetCurrentAccount(); ok || email != "" || org != "" {
		t.Fatalf("Fable GetCurrentAccount = %q, %q, %v; want none", email, org, ok)
	}
}

func TestResolveFableLiveSlotChainNeedsTheRostersSlot(t *testing.T) {
	s := fableFixture(t)
	live := fableCreds("at-new", "rt-new")
	putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
	putFableLive(t, s, live)
	writeFableChain(t, s, live, "3", fableTwo)
	if got := resolveFable(t, s); got != (LiveSlot{}) {
		t.Fatalf("got %+v; the record names slot 3, the roster has its identity in 2", got)
	}
}

func TestRefreshFableLiveGuarded(t *testing.T) {
	held := withMCP(fableCreds("at-f2", "rt-f2"))
	fresh := fableCreds("at-f2b", "rt-f2b")
	refuse := func(t *testing.T) *oauth.FakeClient {
		return &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
			t.Error("refreshed")
			return oauth.RefreshOutcome{}
		}}
	}
	t.Run("refreshes under the locks", func(t *testing.T) {
		s := fableFixture(t)
		putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
		putFableLive(t, s, held)
		before := snapshot(t, s.CredentialsDir)
		c := &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
			if !s.Lock.Held() {
				t.Error("the store lock is not held during the refresh")
			}
			for _, l := range fableLockDirs(s) {
				if info, err := os.Stat(l); err != nil || !info.IsDir() {
					t.Errorf("%s not held during the refresh: %v", l, err)
				}
			}
			return oauth.RefreshOutcome{Credentials: fresh}
		}}
		if out := s.RefreshFableLiveGuarded(context.Background(), c, "2", fableEmails["2"], held); out.Credentials != fresh {
			t.Fatalf("outcome %+v", out)
		}
		if raw, _ := os.ReadFile(fableStoreFile(s)); !fingerprintsEqual(string(raw), fresh) || !strings.Contains(string(raw), storeMCP) {
			t.Fatalf("store = %s, want the refreshed login with the store's mcpOAuth", raw)
		}
		if got, _ := mustFable(t, s).ReadAccountCredentials("2", fableEmails["2"]); got != fresh {
			t.Fatalf("Fable login = %q", got)
		}
		if after := snapshot(t, s.CredentialsDir); !sameSnapshot(before, after) {
			t.Fatal("the default backups changed")
		}
	})
	t.Run("a login rotated since held comes back", func(t *testing.T) {
		s := fableFixture(t)
		rotated := withMCP(fableCreds("at-f2r", "rt-f2r"))
		putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
		putFableLive(t, s, rotated)
		writeFableChain(t, s, rotated, "2", fableTwo)
		if out := s.RefreshFableLiveGuarded(context.Background(), refuse(t), "2", fableEmails["2"], held); out.Credentials != rotated {
			t.Fatalf("outcome %+v, want the store's login as it is", out)
		}
		if raw, _ := os.ReadFile(fableStoreFile(s)); string(raw) != rotated {
			t.Fatalf("store = %s", raw)
		}
	})
	t.Run("another account's login is not refreshed", func(t *testing.T) {
		s := fableFixture(t)
		one := fableCreds("at-f1", "rt-f1")
		putFableLogin(t, s, "1", one)
		putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
		putFableLive(t, s, withMCP(one))
		if out := s.RefreshFableLiveGuarded(context.Background(), refuse(t), "2", fableEmails["2"], withMCP(one)); out.Error != RefreshDeclined {
			t.Fatalf("outcome %+v", out)
		}
		if got, _ := mustFable(t, s).ReadAccountCredentials("2", fableEmails["2"]); got != fableCreds("at-f2", "rt-f2") {
			t.Fatalf("2's Fable login = %q", got)
		}
	})
}

func TestHarvestFableLiveRechecksTheLiveLogin(t *testing.T) {
	s := fableFixture(t)
	stored := fableCreds("at-f2", "rt-f2")
	putFableLogin(t, s, "2", stored)
	putFableLive(t, s, withMCP(fableCreds("at-r", "rt-rotated")))
	s.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity {
		putFableLive(t, s, withMCP(fableCreds("at-r2", "rt-rotated2")))
		return &fableTwo
	}}
	if s.HarvestFableLive() {
		t.Fatal("harvested a login that changed before the locks")
	}
	if got, _ := mustFable(t, s).ReadAccountCredentials("2", fableEmails["2"]); got != stored {
		t.Fatalf("Fable login = %q, want it unchanged", got)
	}
}

func TestHarvestFableLiveWaitsForClaudeCodesRefreshLock(t *testing.T) {
	s := fableFixture(t)
	stored, rotated := fableCreds("at-f2", "rt-f2"), fableCreds("at-r", "rt-rotated")
	putFableLogin(t, s, "2", stored)
	putFableLive(t, s, withMCP(rotated))
	release := holdFableRefreshLock(t, s)
	resolved := make(chan struct{})
	s.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity {
		close(resolved)
		return &fableTwo
	}}
	done := make(chan bool, 1)
	go func() { done <- s.HarvestFableLive() }()
	<-resolved
	time.Sleep(300 * time.Millisecond)
	if got, _ := mustFable(t, s).ReadAccountCredentials("2", fableEmails["2"]); got != stored {
		t.Fatal("harvested while Claude Code held the Fable store's credential-refresh lock")
	}
	release()
	if !<-done {
		t.Fatal("no harvest once the lock was free")
	}
	if got, _ := mustFable(t, s).ReadAccountCredentials("2", fableEmails["2"]); !fingerprintsEqual(got, rotated) {
		t.Fatalf("Fable login = %q", got)
	}
}

func TestFableLocksTakeTheRefreshLockFirst(t *testing.T) {
	s := fableFixture(t)
	if err := mustFable(t, s).SetupFableDirectories(); err != nil {
		t.Fatal(err)
	}
	release := holdFableRefreshLock(t, s)
	storage := fableLockDirs(s)[1]
	done := make(chan error, 1)
	go func() {
		done <- s.WithFableLocks(func() error {
			if _, err := os.Stat(storage); err != nil {
				return err
			}
			return nil
		})
	}()
	for deadline := time.Now().Add(5 * time.Second); !s.Lock.Held(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the store lock was never taken")
		}
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(storage); err == nil {
		t.Fatal("the storage-write lock was taken while Claude Code held the credential-refresh lock")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func fableStoreFile(s *Store) string {
	return filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json")
}

// fableLockDirs is Claude Code's credential-refresh and storage-write locks
// of the Fable store.
func fableLockDirs(s *Store) []string {
	store := paths.FableStoreDir(s.BackupDir())
	return []string{store + ".lock", filepath.Join(store, ".storage-write.lock")}
}

// holdFableRefreshLock holds the Fable store's credential-refresh lock as a
// refreshing Claude Code would, until release.
func holdFableRefreshLock(t *testing.T, s *Store) (release func()) {
	t.Helper()
	dir := fableLockDirs(s)[0]
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	release = func() {
		if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}
	}
	t.Cleanup(release)
	return release
}
