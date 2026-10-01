// store_test.go — the Codex slot registry and snapshot store. Ports claude-swap
// PR #252 tests/test_codex_store.py, plus the store behaviour
// tests/test_codex_switcher.py leans on (renumbering for swap/move, the active
// marker, snapshot round trips) and golden checks of the sequence.json bytes.
//
// Every store is rooted in t.TempDir() via Options.Root and gets a
// keychain.Fake, so no test can reach the real backup root or Keychain.

package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/testutil"
)

var (
	keyA = authfile.AccountKey("user-a", "acct-a")
	keyB = authfile.AccountKey("user-b", "acct-b")
	keyC = authfile.AccountKey("user-c", "acct-c")
)

const t0 = "2026-01-02T03:04:05Z"

type fixture struct {
	root string
	kc   *keychain.Fake
	clk  *clock.Fake
	plat platform.Platform
}

// newFixture returns a file-backed (Linux) store environment: the Python's
// file_store fixture, which forces the on-disk path on any host.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{
		root: filepath.Join(t.TempDir(), "codex"),
		kc:   keychain.NewFake(),
		clk:  testutil.FixedClock(t, t0),
		plat: platform.Linux,
	}
}

// open is `CodexStore()`: a fresh Store over the same root, proving state
// lives on disk and not in the value.
func (f *fixture) open() *Store {
	return New(Options{Root: f.root, Keychain: f.kc, Clock: f.clk, Platform: f.plat})
}

func (f *fixture) seqPath() string { return filepath.Join(f.root, "sequence.json") }
func (f *fixture) credDir() string { return filepath.Join(f.root, "credentials") }

func mustUpsert(t *testing.T, s *Store, key, email, plan string) Slot {
	t.Helper()
	sl, err := s.UpsertSlot(key, Upsert{Email: email, Plan: plan})
	if err != nil {
		t.Fatalf("UpsertSlot(%s): %v", key, err)
	}
	return sl
}

func numbers(s *Store) []string {
	out := []string{}
	for _, sl := range s.Slots() {
		out = append(out, sl.Number)
	}
	return out
}

func authJSON(email string) map[string]any {
	return map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      "header.payload.sig",
			"access_token":  "at-" + email,
			"refresh_token": "rt-" + email,
			"account_id":    "acct-a",
		},
		"last_refresh": "2026-01-01T00:00:00Z",
	}
}

func writeRaw(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readSeq(t *testing.T, f *fixture) map[string]any {
	t.Helper()
	b, err := os.ReadFile(f.seqPath())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("sequence.json is not valid JSON: %v\n%s", err, b)
	}
	return m
}

// ---- slot registry ------------------------------------------------------

func TestFreshStoreHasNoSlots(t *testing.T) {
	s := newFixture(t).open()
	if got := s.Slots(); len(got) != 0 {
		t.Fatalf("Slots = %v, want none", got)
	}
	if got := s.ActiveNumber(); got != "" {
		t.Fatalf("ActiveNumber = %q, want empty", got)
	}
	if got := s.ActiveKey(); got != "" {
		t.Fatalf("ActiveKey = %q, want empty", got)
	}
}

func TestAddingASlotAssignsTheFirstFreeNumber(t *testing.T) {
	s := newFixture(t).open()
	sl := mustUpsert(t, s, keyA, "a@example.com", "pro")
	if sl.Number != "1" {
		t.Fatalf("number = %q, want 1", sl.Number)
	}
	if got := s.Slots()[0]; got.AccountKey != keyA || got.Added != t0 || got.AuthMode != "chatgpt" {
		t.Fatalf("slot = %+v", got)
	}
}

func TestASecondSlotGetsTheNextNumber(t *testing.T) {
	s := newFixture(t).open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	if n := mustUpsert(t, s, keyB, "b@example.com", "plus").Number; n != "2" {
		t.Fatalf("number = %q, want 2", n)
	}
}

func TestUpsertingTheSameKeyUpdatesRatherThanDuplicating(t *testing.T) {
	s := newFixture(t).open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	again := mustUpsert(t, s, keyA, "renamed@example.com", "business")
	if again.Number != "1" {
		t.Fatalf("number = %q, want 1", again.Number)
	}
	slots := s.Slots()
	if len(slots) != 1 || slots[0].Email != "renamed@example.com" || slots[0].Plan != "business" {
		t.Fatalf("slots = %+v", slots)
	}
}

func TestUpsertFieldRefreshRules(t *testing.T) {
	cases := []struct {
		name string
		u    Upsert
		want Slot
	}{
		{"empty fields keep what is stored", Upsert{},
			Slot{Email: "a@example.com", Plan: "pro", WorkspaceName: "Alpha", AuthMode: "chatgpt"}},
		{"non-empty fields refresh", Upsert{Email: "n@example.com", Plan: "team", WorkspaceName: "Beta", AuthMode: "apikey"},
			Slot{Email: "n@example.com", Plan: "team", WorkspaceName: "Beta", AuthMode: "apikey"}},
		{"empty workspace never blanks a learned one", Upsert{Email: "n@example.com", Plan: "team"},
			Slot{Email: "n@example.com", Plan: "team", WorkspaceName: "Alpha", AuthMode: "chatgpt"}},
		{"authMode always refreshes, defaulting to chatgpt", Upsert{Plan: "pro"},
			Slot{Email: "a@example.com", Plan: "pro", WorkspaceName: "Alpha", AuthMode: "chatgpt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			s := f.open()
			if _, err := s.UpsertSlot(keyA, Upsert{Email: "a@example.com", Plan: "pro", WorkspaceName: "Alpha", AuthMode: "apikey"}); err != nil {
				t.Fatal(err)
			}
			if err := s.SetAlias(keyA, "work"); err != nil {
				t.Fatal(err)
			}
			f.clk.Advance(time.Hour) // added must not move on an update
			got, err := s.UpsertSlot(keyA, tc.u)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			want.Number, want.AccountKey, want.Alias, want.Added = "1", keyA, "work", t0
			if got != want {
				t.Fatalf("returned %+v\nwant     %+v", got, want)
			}
			if stored := f.open().Slots()[0]; stored != want {
				t.Fatalf("stored %+v\nwant   %+v", stored, want)
			}
		})
	}
}

func TestSlotsSurviveAReload(t *testing.T) {
	f := newFixture(t)
	mustUpsert(t, f.open(), keyA, "a@example.com", "pro")
	if got := f.open().Slots(); len(got) != 1 || got[0].AccountKey != keyA {
		t.Fatalf("reloaded slots = %+v", got)
	}
}

// Renumbering on delete would silently repoint every alias, mapping and
// muscle-memorised number the user has. Slots keep their numbers; the gap stays.
func TestRemovingASlotLeavesTheOthersNumberedAsTheyWere(t *testing.T) {
	s := newFixture(t).open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	mustUpsert(t, s, keyB, "b@example.com", "plus")
	if ok, err := s.RemoveSlot(keyA); !ok || err != nil {
		t.Fatalf("RemoveSlot = %v, %v", ok, err)
	}
	if got := numbers(s); !reflect.DeepEqual(got, []string{"2"}) {
		t.Fatalf("numbers = %v, want [2]", got)
	}
}

func TestAFreedNumberIsReusedByTheNextAdd(t *testing.T) {
	s := newFixture(t).open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	mustUpsert(t, s, keyB, "b@example.com", "plus")
	if _, err := s.RemoveSlot(keyA); err != nil {
		t.Fatal(err)
	}
	if n := mustUpsert(t, s, keyC, "c@example.com", "pro").Number; n != "1" {
		t.Fatalf("number = %q, want 1", n)
	}
}

func TestRemovingAnUnknownKeyReportsThatNothingHappened(t *testing.T) {
	f := newFixture(t)
	ok, err := f.open().RemoveSlot("nope")
	if ok || err != nil {
		t.Fatalf("RemoveSlot = %v, %v; want false, nil", ok, err)
	}
	if _, err := os.Stat(f.seqPath()); !os.IsNotExist(err) {
		t.Fatalf("a no-op remove wrote sequence.json: %v", err)
	}
}

func TestActiveNumberIsRecordedAndReadBack(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	mustUpsert(t, s, keyB, "b@example.com", "plus")
	if err := s.SetActive(keyB); err != nil {
		t.Fatal(err)
	}
	if got := f.open().ActiveNumber(); got != "2" {
		t.Fatalf("ActiveNumber = %q, want 2", got)
	}
	if got := f.open().ActiveKey(); got != keyB {
		t.Fatalf("ActiveKey = %q", got)
	}
}

func TestActiveNumberIsEmptyForAKeyWithoutASlot(t *testing.T) {
	s := newFixture(t).open()
	if err := s.SetActive(keyA); err != nil {
		t.Fatal(err)
	}
	if got := s.ActiveNumber(); got != "" {
		t.Fatalf("ActiveNumber = %q, want empty", got)
	}
}

func TestSetActiveEmptyClearsToNull(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	if err := s.SetActive(keyA); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive(""); err != nil {
		t.Fatal(err)
	}
	m := readSeq(t, f)
	if v, has := m["activeAccountKey"]; !has || v != nil {
		t.Fatalf("activeAccountKey = %#v (present %v), want null", v, has)
	}
	if got := f.open().ActiveKey(); got != "" {
		t.Fatalf("ActiveKey = %q", got)
	}
}

func TestRemovingTheActiveSlotClearsTheActiveMarker(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	if err := s.SetActive(keyA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveSlot(keyA); err != nil {
		t.Fatal(err)
	}
	if got := f.open().ActiveKey(); got != "" {
		t.Fatalf("ActiveKey = %q, want empty", got)
	}
}

func TestRemovingAnotherSlotKeepsTheActiveMarker(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	mustUpsert(t, s, keyB, "b@example.com", "plus")
	if err := s.SetActive(keyA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveSlot(keyB); err != nil {
		t.Fatal(err)
	}
	if got := f.open().ActiveKey(); got != keyA {
		t.Fatalf("ActiveKey = %q, want %q", got, keyA)
	}
}

func TestMetadataSettersRoundTrip(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "business")

	if err := s.SetAlias(keyA, "work"); err != nil {
		t.Fatal(err)
	}
	if got := f.open().Slots()[0].Alias; got != "work" {
		t.Fatalf("alias = %q", got)
	}
	if err := s.SetAlias(keyA, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.open().Slots()[0].Alias; got != "" {
		t.Fatalf("cleared alias = %q", got)
	}

	if err := s.SetDisabled(keyA, true); err != nil {
		t.Fatal(err)
	}
	if !f.open().Slots()[0].Disabled {
		t.Fatal("disabled did not round-trip")
	}
	if err := s.SetDisabled(keyA, false); err != nil {
		t.Fatal(err)
	}
	if f.open().Slots()[0].Disabled {
		t.Fatal("re-enabled did not round-trip")
	}

	if err := s.SetWorkspaceName(keyA, "Workspace Alpha"); err != nil {
		t.Fatal(err)
	}
	if got := f.open().Slots()[0].DisplayLabel(); got != "a@example.com [Workspace Alpha]" {
		t.Fatalf("DisplayLabel = %q", got)
	}
}

func TestSettersOnAnUnknownKeyAreSilentNoOps(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	for name, fn := range map[string]func() error{
		"alias":     func() error { return s.SetAlias("nope", "x") },
		"disabled":  func() error { return s.SetDisabled("nope", true) },
		"workspace": func() error { return s.SetWorkspaceName("nope", "x") },
	} {
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(f.seqPath()); !os.IsNotExist(err) {
		t.Fatalf("a no-op setter wrote sequence.json: %v", err)
	}
}

func TestASlotWithoutAWorkspaceDisplaysAsPersonal(t *testing.T) {
	s := newFixture(t).open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	if got := s.Slots()[0].DisplayLabel(); got != "a@example.com [personal]" {
		t.Fatalf("DisplayLabel = %q", got)
	}
}

func TestSlotForKey(t *testing.T) {
	s := newFixture(t).open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	mustUpsert(t, s, keyB, "b@example.com", "plus")
	if sl := s.SlotForKey(keyB); sl == nil || sl.Number != "2" || sl.Email != "b@example.com" {
		t.Fatalf("SlotForKey(B) = %+v", sl)
	}
	if sl := s.SlotForKey("nope"); sl != nil {
		t.Fatalf("SlotForKey(nope) = %+v, want nil", sl)
	}
}

// ---- renumbering (swap / move) ------------------------------------------

func TestRenumberSwapsTwoSlotsInOneWrite(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	mustUpsert(t, s, keyB, "b@example.com", "plus")
	if err := s.SetAlias(keyA, "work"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSnapshot(keyA, authJSON("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Renumber(map[string]string{keyA: "2", keyB: "1"}); err != nil {
		t.Fatal(err)
	}
	r := f.open()
	if a := r.SlotForKey(keyA); a == nil || a.Number != "2" || a.Alias != "work" || a.Email != "a@example.com" {
		t.Fatalf("A after swap = %+v", a)
	}
	if b := r.SlotForKey(keyB); b == nil || b.Number != "1" {
		t.Fatalf("B after swap = %+v", b)
	}
	// Snapshots are keyed by account_key: the swap never touched the secret.
	if got := r.ReadSnapshot(keyA); !reflect.DeepEqual(got, roundTrip(t, authJSON("a"))) {
		t.Fatalf("snapshot after swap = %v", got)
	}
}

func TestRenumberMovesToAFreeNumber(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	mustUpsert(t, s, keyB, "b@example.com", "plus")
	if err := s.Renumber(map[string]string{keyA: "7"}); err != nil {
		t.Fatal(err)
	}
	if got := numbers(f.open()); !reflect.DeepEqual(got, []string{"2", "7"}) {
		t.Fatalf("numbers = %v", got)
	}
	// The vacated 1 is the next add's number.
	if n := mustUpsert(t, s, keyC, "c@example.com", "pro").Number; n != "1" {
		t.Fatalf("next add = %q, want 1", n)
	}
}

func TestRenumberRejectsBadMappingsWithoutWriting(t *testing.T) {
	cases := []struct {
		name    string
		mapping map[string]string
		unknown bool
	}{
		{"unknown account key", map[string]string{keyA: "2", "nope": "1"}, true},
		{"non-numeric target", map[string]string{keyA: "x"}, false},
		{"empty target", map[string]string{keyA: ""}, false},
		{"two keys one number", map[string]string{keyA: "3", keyB: "03"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			s := f.open()
			mustUpsert(t, s, keyA, "a@example.com", "pro")
			mustUpsert(t, s, keyB, "b@example.com", "plus")
			before, _ := os.ReadFile(f.seqPath())
			f.clk.Advance(time.Minute)
			err := s.Renumber(tc.mapping)
			if err == nil {
				t.Fatal("Renumber accepted a bad mapping")
			}
			if got := errors.Is(err, ErrUnknownAccount); got != tc.unknown {
				t.Fatalf("errors.Is(ErrUnknownAccount) = %v, want %v (%v)", got, tc.unknown, err)
			}
			after, _ := os.ReadFile(f.seqPath())
			if string(before) != string(after) {
				t.Fatalf("sequence.json changed on a rejected renumber")
			}
		})
	}
}

func TestRenumberWithAnEmptyMappingIsANoOp(t *testing.T) {
	f := newFixture(t)
	if err := f.open().Renumber(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.seqPath()); !os.IsNotExist(err) {
		t.Fatalf("empty renumber wrote sequence.json: %v", err)
	}
}

// ---- snapshot store -----------------------------------------------------

// roundTrip normalises a payload through JSON (numbers → float64 etc.).
func roundTrip(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSnapshotRoundTripsThroughTheCredentialStore(t *testing.T) {
	for _, plat := range []platform.Platform{platform.Linux, platform.MacOS} {
		t.Run(plat.String(), func(t *testing.T) {
			f := newFixture(t)
			f.plat = plat
			s := f.open()
			payload := authJSON("a@example.com")
			if err := s.WriteSnapshot(keyA, payload); err != nil {
				t.Fatal(err)
			}
			if got := f.open().ReadSnapshot(keyA); !reflect.DeepEqual(got, roundTrip(t, payload)) {
				t.Fatalf("ReadSnapshot = %v", got)
			}
		})
	}
}

// Keying by slot would turn `codex swap`/`codex move` into a data migration.
// The key is the identity, and it never renumbers.
func TestSnapshotIsKeyedByAccountKeyNotSlotNumber(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	if err := s.WriteSnapshot(keyA, authJSON("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.credDir(), authfile.FileKey(keyA)+".json")); err != nil {
		t.Fatalf("snapshot not at credentials/<filekey>.json: %v", err)
	}
}

func TestMacOSSnapshotsGoToTheKeychain(t *testing.T) {
	f := newFixture(t)
	f.plat = platform.MacOS
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	if err := s.WriteSnapshot(keyA, authJSON("a")); err != nil {
		t.Fatal(err)
	}
	if !f.kc.Exists(KeychainService, authfile.FileKey(keyA)) {
		t.Fatalf("no Keychain item (%s, %s)", KeychainService, authfile.FileKey(keyA))
	}
	if files, _ := filepath.Glob(filepath.Join(f.credDir(), "*.json")); len(files) != 0 {
		t.Fatalf("macOS snapshot leaked to disk: %v", files)
	}
	// Removing the slot deletes the Keychain item too.
	if _, err := s.RemoveSlot(keyA); err != nil {
		t.Fatal(err)
	}
	if f.kc.Exists(KeychainService, authfile.FileKey(keyA)) {
		t.Fatal("RemoveSlot left the Keychain item behind")
	}
	if got := s.ReadSnapshot(keyA); got != nil {
		t.Fatalf("ReadSnapshot after remove = %v", got)
	}
}

// failingKeychain reports every call as a Keychain failure.
type failingKeychain struct{}

func (failingKeychain) Get(string, string) (string, bool, error) {
	return "", false, &keychain.KeychainError{Msg: "boom"}
}
func (failingKeychain) Set(string, string, string) error {
	return &keychain.KeychainError{Msg: "boom"}
}
func (failingKeychain) Delete(string, string) error { return &keychain.KeychainError{Msg: "boom"} }
func (failingKeychain) Exists(string, string) bool  { return false }

func TestMacOSKeychainFailures(t *testing.T) {
	s := New(Options{Root: t.TempDir(), Keychain: failingKeychain{}, Platform: platform.MacOS})
	if err := s.WriteSnapshot(keyA, authJSON("a")); !keychain.IsUnusable(err) {
		t.Fatalf("WriteSnapshot err = %v, want a Keychain error", err)
	}
	if got := s.ReadSnapshot(keyA); got != nil {
		t.Fatalf("ReadSnapshot = %v, want nil on a Keychain failure", got)
	}
	if err := s.DeleteSnapshot(keyA); !keychain.IsUnusable(err) {
		t.Fatalf("DeleteSnapshot err = %v, want a Keychain error", err)
	}
}

func TestOnDiskSnapshotsArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	f := newFixture(t)
	if err := f.open().WriteSnapshot(keyA, authJSON("a")); err != nil {
		t.Fatal(err)
	}
	assertMode(t, f.credDir(), 0o700)
	assertMode(t, filepath.Join(f.credDir(), authfile.FileKey(keyA)+".json"), 0o600)
}

func TestTheSequenceFileAndItsDirectoryArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	f := newFixture(t)
	if err := os.MkdirAll(f.root, 0o755); err != nil { // a pre-existing loose root is tightened
		t.Fatal(err)
	}
	mustUpsert(t, f.open(), keyA, "a@example.com", "pro")
	assertMode(t, f.seqPath(), 0o600)
	assertMode(t, f.root, 0o700)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func TestDeletingASlotDeletesItsSnapshot(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	if err := s.WriteSnapshot(keyA, authJSON("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveSlot(keyA); err != nil {
		t.Fatal(err)
	}
	if got := s.ReadSnapshot(keyA); got != nil {
		t.Fatalf("ReadSnapshot after remove = %v", got)
	}
}

func TestDeletingAMissingSnapshotIsNotAnError(t *testing.T) {
	if err := newFixture(t).open().DeleteSnapshot(keyA); err != nil {
		t.Fatal(err)
	}
}

func TestUnreadableSnapshotsReadAsMissing(t *testing.T) {
	cases := []struct{ name, content string }{
		{"torn", "{ torn"},
		{"empty", ""},
		{"array", "[1, 2]"},
		{"string", `"x"`},
		{"null", "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			writeRaw(t, filepath.Join(f.credDir(), authfile.FileKey(keyA)+".json"), tc.content)
			if got := f.open().ReadSnapshot(keyA); got != nil {
				t.Fatalf("ReadSnapshot = %v, want nil", got)
			}
			f.plat = platform.MacOS
			if err := f.kc.Set(KeychainService, authfile.FileKey(keyA), tc.content); err != nil {
				t.Fatal(err)
			}
			if got := f.open().ReadSnapshot(keyA); got != nil {
				t.Fatalf("Keychain ReadSnapshot = %v, want nil", got)
			}
		})
	}
}

func TestReadingAMissingSnapshotReturnsNil(t *testing.T) {
	if got := newFixture(t).open().ReadSnapshot("nope"); got != nil {
		t.Fatalf("ReadSnapshot = %v", got)
	}
}

// ---- sequence.json on disk ----------------------------------------------

func TestSequenceFileIsValidJSONOnDisk(t *testing.T) {
	f := newFixture(t)
	mustUpsert(t, f.open(), keyA, "a@example.com", "pro")
	m := readSeq(t, f)
	row := m["accounts"].(map[string]any)["1"].(map[string]any)
	if row["account_key"] != keyA {
		t.Fatalf("accounts[1].account_key = %v", row["account_key"])
	}
}

// The exact bytes a fresh store writes, matching Python's
// json.dumps(data, indent=2) of the same dict.
func TestSequenceFileGoldenBytes(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	f.clk.Advance(time.Second)
	if err := s.SetActive(keyA); err != nil {
		t.Fatal(err)
	}
	want := `{
  "accounts": {
    "1": {
      "account_key": "user-a::acct-a",
      "email": "a@example.com",
      "plan": "pro",
      "workspaceName": "",
      "alias": "",
      "added": "2026-01-02T03:04:05Z",
      "disabled": false,
      "authMode": "chatgpt"
    }
  },
  "activeAccountKey": "user-a::acct-a",
  "lastUpdated": "2026-01-02T03:04:06Z"
}`
	got, err := os.ReadFile(f.seqPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("sequence.json =\n%s\nwant\n%s", got, want)
	}
}

// A Python-written file keeps its key order and the keys this port does not
// model through a Go read-modify-write; only lastUpdated and the edit move.
func TestPythonWrittenFileRoundTripsPreservingLayout(t *testing.T) {
	f := newFixture(t)
	writeRaw(t, f.seqPath(), `{
  "lastUpdated": "2025-01-01T00:00:00Z",
  "activeAccountKey": null,
  "futureTopLevel": [1, 2],
  "accounts": {
    "2": {"account_key": "user-b::acct-b", "email": "b@example.com", "plan": "plus", "workspaceName": "", "alias": "", "added": "2025-01-01T00:00:00Z", "disabled": false, "authMode": "chatgpt", "extra": {"k": "<&>"}},
    "1": {"account_key": "user-a::acct-a", "email": "a@example.com", "plan": "pro", "workspaceName": "", "alias": "", "added": "2025-01-01T00:00:00Z", "disabled": false, "authMode": "chatgpt"}
  }
}`)
	s := f.open()
	if got := numbers(s); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("numbers = %v", got)
	}
	if err := s.SetAlias(keyB, "home"); err != nil {
		t.Fatal(err)
	}
	want := `{
  "lastUpdated": "2026-01-02T03:04:05Z",
  "activeAccountKey": null,
  "futureTopLevel": [
    1,
    2
  ],
  "accounts": {
    "2": {
      "account_key": "user-b::acct-b",
      "email": "b@example.com",
      "plan": "plus",
      "workspaceName": "",
      "alias": "home",
      "added": "2025-01-01T00:00:00Z",
      "disabled": false,
      "authMode": "chatgpt",
      "extra": {
        "k": "<&>"
      }
    },
    "1": {
      "account_key": "user-a::acct-a",
      "email": "a@example.com",
      "plan": "pro",
      "workspaceName": "",
      "alias": "",
      "added": "2025-01-01T00:00:00Z",
      "disabled": false,
      "authMode": "chatgpt"
    }
  }
}`
	got, _ := os.ReadFile(f.seqPath())
	if string(got) != want {
		t.Fatalf("sequence.json =\n%s\nwant\n%s", got, want)
	}
}

func TestLastUpdatedIsStampedOnEveryWrite(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	steps := []struct {
		name string
		fn   func() error
	}{
		{"upsert", func() error { _, err := s.UpsertSlot(keyA, Upsert{Email: "a@example.com"}); return err }},
		{"active", func() error { return s.SetActive(keyA) }},
		{"alias", func() error { return s.SetAlias(keyA, "w") }},
		{"disabled", func() error { return s.SetDisabled(keyA, true) }},
		{"workspace", func() error { return s.SetWorkspaceName(keyA, "W") }},
		{"renumber", func() error { return s.Renumber(map[string]string{keyA: "4"}) }},
		{"remove", func() error { _, err := s.RemoveSlot(keyA); return err }},
	}
	for _, st := range steps {
		f.clk.Advance(time.Minute)
		if err := st.fn(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		want := f.clk.Now().UTC().Format("2006-01-02T15:04:05Z")
		if got := readSeq(t, f)["lastUpdated"]; got != want {
			t.Fatalf("%s: lastUpdated = %v, want %s", st.name, got, want)
		}
	}
}

// A torn write must degrade to "no accounts", never to an error that makes
// every tycswap command unusable.
func TestUnusableSequenceFilesDegradeToNoAccounts(t *testing.T) {
	cases := []struct{ name, content string }{
		{"not json", "{ not json"},
		{"json array", "[1, 2, 3]"},
		{"json null", "null"},
		{"json string", `"x"`},
		{"empty", ""},
		{"accounts not an object", `{"accounts": [1], "activeAccountKey": null}`},
		{"trailing garbage", `{"accounts": {}} {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			writeRaw(t, f.seqPath(), tc.content)
			s := f.open()
			if got := s.Slots(); len(got) != 0 {
				t.Fatalf("Slots = %+v", got)
			}
			if got := s.ActiveKey(); got != "" {
				t.Fatalf("ActiveKey = %q", got)
			}
			// An empty file, or an object whose accounts member is not an
			// object, is still writable over (the Python's in-place reset).
			// Anything that does not parse as one JSON object is a registry
			// this port cannot read, and a mutation refuses rather than
			// replacing it with an empty one.
			writable := tc.name == "empty" || tc.name == "accounts not an object"
			if !writable {
				if _, err := s.UpsertSlot(keyA, Upsert{Email: "a@example.com"}); !errors.Is(err, ErrCorruptRegistry) {
					t.Fatalf("UpsertSlot over %s = %v, want ErrCorruptRegistry", tc.name, err)
				}
				if b, _ := os.ReadFile(f.seqPath()); string(b) != tc.content {
					t.Fatalf("sequence.json rewritten to %q", b)
				}
				return
			}
			if n := mustUpsert(t, s, keyA, "a@example.com", "pro").Number; n != "1" {
				t.Fatalf("number = %q", n)
			}
			if got := f.open().Slots(); len(got) != 1 {
				t.Fatalf("after rewrite Slots = %+v", got)
			}
		})
	}
}

func TestSlotsSkipMalformedRowsAndSortNumerically(t *testing.T) {
	f := newFixture(t)
	writeRaw(t, f.seqPath(), `{
  "accounts": {
    "10": {"account_key": "k10"},
    "x": {"account_key": "kx"},
    "2": {"account_key": "k2", "email": null, "disabled": 1, "authMode": ""},
    "3": "not a row",
    "1": {"account_key": "k1", "disabled": "yes", "authMode": "apikey"}
  },
  "activeAccountKey": 5
}`)
	s := f.open()
	got := s.Slots()
	if want := []string{"1", "2", "10"}; !reflect.DeepEqual(numbers(s), want) {
		t.Fatalf("numbers = %v, want %v", numbers(s), want)
	}
	if !got[0].Disabled || got[0].AuthMode != "apikey" {
		t.Fatalf("slot 1 = %+v", got[0])
	}
	if !got[1].Disabled || got[1].Email != "" || got[1].AuthMode != "chatgpt" {
		t.Fatalf("slot 2 = %+v", got[1])
	}
	if s.ActiveKey() != "" {
		t.Fatalf("non-string activeAccountKey read as %q", s.ActiveKey())
	}
	// Every digit key counts as taken, even the non-row "3": next is 4.
	if n := mustUpsert(t, s, keyA, "a@example.com", "pro").Number; n != "4" {
		t.Fatalf("next number = %q, want 4", n)
	}
}

func TestDisabledTruthiness(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"true", true}, {"false", false}, {"null", false}, {"0", false}, {"1", true},
		{`""`, false}, {`"no"`, true}, {"[]", false}, {"[0]", true}, {"{}", false},
	}
	for _, tc := range cases {
		if got := truthy(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("truthy(%s) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestWritingLeavesNoTempFileBehind(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	mustUpsert(t, s, keyA, "a@example.com", "pro")
	if err := s.WriteSnapshot(keyA, authJSON("a")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{f.root, f.credDir()} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".tmp" {
				t.Fatalf("temp file left behind: %s", filepath.Join(dir, e.Name()))
			}
		}
	}
}

// ---- construction and paths ---------------------------------------------

func TestNewDefaultsAndPaths(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("default root resolution is exercised via XDG_DATA_HOME on Linux")
	}
	xdg := t.TempDir()
	testutil.Setenv(t, "HOME", t.TempDir())
	testutil.Setenv(t, "XDG_DATA_HOME", xdg)
	testutil.Unsetenv(t, "WSL_DISTRO_NAME")
	s := New(Options{})
	if want := filepath.Join(xdg, "tycswap", "codex"); s.Root() != want || s.Root() != authfile.StoreRoot() {
		t.Fatalf("Root = %q, want %q", s.Root(), want)
	}
	if s.platform != platform.Linux {
		t.Fatalf("platform = %v, want detected linux", s.platform)
	}
	if _, ok := s.kc.(keychain.Security); !ok {
		t.Fatalf("keychain = %T, want keychain.Security", s.kc)
	}
	if _, ok := s.clk.(clock.System); !ok {
		t.Fatalf("clock = %T, want clock.System", s.clk)
	}
}

func TestPathsFollowRoot(t *testing.T) {
	root := t.TempDir()
	s := New(Options{Root: root, Keychain: keychain.NewFake(), Platform: platform.Linux})
	if got, want := s.CacheDir(), filepath.Join(root, "cache"); got != want {
		t.Fatalf("CacheDir = %q, want %q", got, want)
	}
	if got, want := s.sequencePath(), filepath.Join(root, "sequence.json"); got != want {
		t.Fatalf("sequencePath = %q, want %q", got, want)
	}
	if got, want := s.credentialsDir(), filepath.Join(root, "credentials"); got != want {
		t.Fatalf("credentialsDir = %q, want %q", got, want)
	}
	err := s.Lock().With(func() error {
		_, err := os.Stat(filepath.Join(root, ".lock"))
		return err
	})
	if err != nil {
		t.Fatalf("Lock not on Root/.lock: %v", err)
	}
}

func TestMacOSIsHonouredOnlyWithAnInjectedKeychain(t *testing.T) {
	root := t.TempDir()
	if s := New(Options{Root: root, Keychain: keychain.NewFake(), Platform: platform.MacOS}); !s.useKeychain() {
		t.Fatal("explicit MacOS with a fake keychain was not honoured")
	}
	if s := New(Options{Root: root}); s.platform != platform.Detect() {
		t.Fatalf("zero Platform without a keychain = %v, want detected %v", s.platform, platform.Detect())
	}
}
