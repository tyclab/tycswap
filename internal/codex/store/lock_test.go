// lock_test.go — the store's own locking of sequence.json read-modify-writes,
// the refusal to overwrite an unreadable registry, and the macOS file
// fallback for snapshots too large for `security -i`'s stdin line.

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"git.dpemmons.com/dpemmons/cswap/internal/codex/authfile"
	"git.dpemmons.com/dpemmons/cswap/internal/filelock"
	"git.dpemmons.com/dpemmons/cswap/internal/keychain"
	"git.dpemmons.com/dpemmons/cswap/internal/platform"
)

// A mutation waits for another holder of the flock (a separate descriptor,
// as another cswap process would have) and completes once it is released.
func TestAMutationWaitsForAnotherHolderOfTheLock(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")

	other := filelock.New(filepath.Join(f.root, ".lock"), 0)
	if ok, err := other.Acquire(time.Second); err != nil || !ok {
		t.Fatalf("Acquire = (%v, %v)", ok, err)
	}
	done := make(chan error, 1)
	go func() { done <- s.SetAlias(keyA, "work") }()
	select {
	case err := <-done:
		t.Fatalf("SetAlias finished (%v) while another holder had the lock", err)
	case <-time.After(400 * time.Millisecond):
	}
	if err := other.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetAlias = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetAlias still blocked after the lock was released")
	}
	if got := f.open().SlotForKey(keyA).Alias; got != "work" {
		t.Fatalf("alias = %q", got)
	}
}

// A caller holding Lock() (the switch path) can still mutate: the store does
// not try to take the non-reentrant flock a second time.
func TestAHolderOfLockCanStillMutate(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	l := s.Lock()
	if ok, err := l.Acquire(time.Second); err != nil || !ok {
		t.Fatalf("Acquire = (%v, %v)", ok, err)
	}
	start := time.Now()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	if err := s.SetActive(keyA); err != nil {
		t.Fatal(err)
	}
	// A second Store over the same root sees the same in-process marker.
	if err := f.open().SetAlias(keyA, "work"); err != nil {
		t.Fatal(err)
	}
	if err := s.WithLock(func() error { return s.SetDisabled(keyA, true) }); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("mutations under Lock() took %s (waited on their own lock)", d)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	_ = l.Release() // idempotent, and must not drive the marker negative
	sl := f.open().SlotForKey(keyA)
	if sl == nil || sl.Alias != "work" || !sl.Disabled || f.open().ActiveKey() != keyA {
		t.Fatalf("slot = %+v, active = %q", sl, f.open().ActiveKey())
	}

	// Released: a mutation takes the flock again, so an outside holder blocks it.
	other := filelock.New(filepath.Join(f.root, ".lock"), 0)
	if ok, err := other.Acquire(time.Second); err != nil || !ok {
		t.Fatalf("Acquire = (%v, %v)", ok, err)
	}
	done := make(chan error, 1)
	go func() { done <- s.SetAlias(keyA, "home") }()
	select {
	case <-done:
		t.Fatal("mutation skipped the lock after Release")
	case <-time.After(300 * time.Millisecond):
	}
	_ = other.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestACorruptSequenceIsNotOverwrittenByMutations(t *testing.T) {
	const torn = `{"accounts": {"1": {"account_key": "k1"`
	f := newFixture(t)
	writeRaw(t, f.seqPath(), torn)
	s := f.open()
	if got := s.Slots(); len(got) != 0 {
		t.Fatalf("listing read of a torn file = %+v, want empty", got)
	}
	checks := map[string]error{
		"SetAlias":         s.SetAlias("k1", "work"),
		"SetWorkspaceName": s.SetWorkspaceName("k1", "ws"),
		"SetActive":        s.SetActive("k1"),
		"Renumber":         s.Renumber(map[string]string{"k1": "2"}),
	}
	_, checks["UpsertSlot"] = s.UpsertSlot(keyA, Upsert{Email: "a@example.com"})
	_, checks["RemoveSlot"] = s.RemoveSlot("k1")
	for name, err := range checks {
		if !errors.Is(err, ErrCorruptRegistry) {
			t.Errorf("%s = %v, want ErrCorruptRegistry", name, err)
		}
	}
	if b, _ := os.ReadFile(f.seqPath()); string(b) != torn {
		t.Fatalf("sequence.json rewritten to %q", b)
	}
}

// Concurrent upserts for different keys, through separate Store values over
// one root, all survive with distinct numbers.
func TestConcurrentUpsertsForDifferentKeysAllSurvive(t *testing.T) {
	f := newFixture(t)
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.open().UpsertSlot(fmt.Sprintf("user-%d::acct-%d", i, i), Upsert{Email: "x@example.com"})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	slots := f.open().Slots()
	if len(slots) != n {
		t.Fatalf("%d slots survived, want %d: %+v", len(slots), n, slots)
	}
	seen := map[string]bool{}
	for _, sl := range slots {
		if seen[sl.Number] {
			t.Fatalf("number %s assigned twice", sl.Number)
		}
		seen[sl.Number] = true
	}
}

func TestValidAliasMatchesTheSwitcherRule(t *testing.T) {
	for in, want := range map[string]string{" Work ": "work", "a.b_c-d": "a.b_c-d"} {
		if got, ok := ValidAlias(in); !ok || got != want {
			t.Errorf("ValidAlias(%q) = (%q, %v), want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "  ", "2", "007", "-x", "a b", "é"} {
		if got, ok := ValidAlias(in); ok {
			t.Errorf("ValidAlias(%q) = %q, want rejected", in, got)
		}
	}
}

// ---- macOS snapshot file fallback ----------------------------------------

func macFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.plat = platform.MacOS
	return f
}

// bigPayload is an auth.json whose blob is too large for security -i.
func bigPayload() map[string]any {
	p := authJSON("a@example.com")
	p["pad"] = strings.Repeat("x", keychain.SecurityStdinLineLimit)
	return p
}

func TestAnOversizedSnapshotGoesToAPrivateFileOnMacOS(t *testing.T) {
	f := macFixture(t)
	s := f.open()
	if err := s.WriteSnapshot(keyA, bigPayload()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.credDir(), authfile.FileKey(keyA)+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("fallback file missing: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("fallback file mode = %o, want 600", info.Mode().Perm())
	}
	if f.kc.Exists(KeychainService, authfile.FileKey(keyA)) {
		t.Fatal("oversized snapshot also went to the keychain")
	}
	if got := s.ReadSnapshot(keyA); got == nil || got["pad"] == nil {
		t.Fatalf("ReadSnapshot = %v", got)
	}
	if err := s.DeleteSnapshot(keyA); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fallback file survived DeleteSnapshot: %v", err)
	}
	if s.ReadSnapshot(keyA) != nil {
		t.Fatal("snapshot still readable after delete")
	}
}

func TestASmallSnapshotStillGoesToTheKeychainAndReplacesAFallbackFile(t *testing.T) {
	f := macFixture(t)
	s := f.open()
	path := filepath.Join(f.credDir(), authfile.FileKey(keyA)+".json")
	if err := s.WriteSnapshot(keyA, bigPayload()); err != nil {
		t.Fatal(err)
	}
	small := authJSON("a@example.com")
	if err := s.WriteSnapshot(keyA, small); err != nil {
		t.Fatal(err)
	}
	if !f.kc.Exists(KeychainService, authfile.FileKey(keyA)) {
		t.Fatal("small snapshot not in the keychain")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("superseded fallback file survived: %v", err)
	}
	if got := s.ReadSnapshot(keyA); got == nil || got["pad"] != nil {
		t.Fatalf("ReadSnapshot = %v, want the small payload", got)
	}
	// Growing again moves it back to the file and drops the keychain item.
	if err := s.WriteSnapshot(keyA, bigPayload()); err != nil {
		t.Fatal(err)
	}
	if f.kc.Exists(KeychainService, authfile.FileKey(keyA)) {
		t.Fatal("stale keychain item left beside the fallback file")
	}
	if got := s.ReadSnapshot(keyA); got == nil || got["pad"] == nil {
		t.Fatalf("ReadSnapshot = %v, want the big payload", got)
	}
	if err := s.WriteSnapshot(keyA, small); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSnapshot(keyA); err != nil {
		t.Fatal(err)
	}
	if f.kc.Exists(KeychainService, authfile.FileKey(keyA)) || s.ReadSnapshot(keyA) != nil {
		t.Fatal("keychain snapshot survived DeleteSnapshot")
	}
}
