// Tests for the Fable store's scope, state, resolver, refresh and harvest
// (DESIGN A100).
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

const fableRoster = `{
  "activeAccountNumber": 1,
  "lastUpdated": "2026-07-17T09:00:00Z",
  "sequence": [1, 2, 3],
  "accounts": {
    "1": {"email": "one@example.com", "uuid": "u-1", "organizationUuid": "org-1", "organizationName": "One", "added": "t"},
    "2": {"email": "two@example.com", "uuid": "u-2", "organizationUuid": "org-2", "organizationName": "Two", "added": "t"},
    "3": {"email": "three@example.com", "uuid": "u-3", "organizationUuid": "org-3", "organizationName": "Three", "added": "t"}
  }
}`

var fableEmails = map[string]string{"1": "one@example.com", "2": "two@example.com", "3": "three@example.com"}

func fableCreds(access, refresh string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","expiresAt":4102444800000,"subscriptionType":"max"}}`
}

const storeMCP = `"mcpOAuth":{"srv":{"accessToken":"m"}}`

// fableFixture: three roster accounts with default backups; slot 1's default
// login is live in ~/.claude.
func fableFixture(t *testing.T) *Store {
	t.Helper()
	s := fableFreshStore(t)
	writeSequenceRaw(t, s, fableRoster)
	for num, email := range fableEmails {
		if err := s.Creds.WriteBackup(num, email, fableCreds("at-d"+num, "rt-d"+num)); err != nil {
			t.Fatal(err)
		}
		writeBackupConfig(t, s, num, email, `{"oauthAccount":{}}`)
	}
	writeLiveCreds(t, s, fableCreds("at-d1", "rt-d1"))
	writeLiveIdentity(t, s, "one@example.com", "org-1")
	return s
}

// fableFreshStore is freshStore off macOS, where the Fable store is refused.
func fableFreshStore(t *testing.T) *Store {
	t.Helper()
	s := freshStore(t)
	s.Platform = platform.Linux
	return s
}

func mustFable(t *testing.T, s *Store) *Store {
	t.Helper()
	f, err := s.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func putFableLogin(t *testing.T, s *Store, num, creds string) {
	t.Helper()
	if err := mustFable(t, s).WriteAccountCredentials(num, fableEmails[num], creds); err != nil {
		t.Fatal(err)
	}
}

func putFableLive(t *testing.T, s *Store, content string) {
	t.Helper()
	p := filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func withMCP(creds string) string { return strings.TrimSuffix(creds, "}") + "," + storeMCP + "}" }

// snapshot is every file under dir, path → bytes.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			raw, _ := os.ReadFile(p)
			out[p] = string(raw)
		}
		return nil
	})
	return out
}

func sameSnapshot(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func fingerprint(creds string) string { return *oauth.CredentialFingerprint(creds) }

// A2 -------------------------------------------------------------------------

func TestFableScopePaths(t *testing.T) {
	s := fableFreshStore(t)
	root := s.BackupDir()
	f := mustFable(t, s)
	if !f.IsFable() || s.IsFable() {
		t.Fatalf("IsFable: fable %v, default %v", f.IsFable(), s.IsFable())
	}
	for _, c := range []struct{ name, got, want string }{
		{"FableScopeDir", paths.FableScopeDir(root), filepath.Join(root, "fable")},
		{"FableStoreDir", paths.FableStoreDir(root), filepath.Join(root, "fable", "store")},
		{"FableCredentialsDir", paths.FableCredentialsDir(root), filepath.Join(root, "credentials-fable")},
		{"CredentialsDir", f.CredentialsDir, filepath.Join(root, "credentials-fable")},
		{"StatePath", f.StatePath(), filepath.Join(root, "fable", "autoswitch_state.json")},
		{"ProfileDir", f.ProfileDir(), filepath.Join(root, "fable", "store")},
		{"chain record", f.liveLineagePath(), filepath.Join(root, "fable", "live_lineage.json")},
		{"default StatePath", s.StatePath(), filepath.Join(root, "autoswitch_state.json")},
		{"default chain record", s.liveLineagePath(), filepath.Join(root, "live_lineage.json")},
	} {
		if c.got != c.want {
			t.Errorf("%s = %s, want %s", c.name, c.got, c.want)
		}
	}
	if err := f.SetupFableDirectories(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{paths.FableScopeDir(root), paths.FableStoreDir(root), paths.FableCredentialsDir(root)} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("%s: %v", dir, err)
		}
		if mode := info.Mode().Perm(); !platform.IsWindows() && mode != 0o700 {
			t.Errorf("%s mode %o, want 0700", dir, mode)
		}
	}
}

func TestFableBackupsRoundTripBesideDefault(t *testing.T) {
	s := fableFixture(t)
	before := snapshot(t, s.CredentialsDir)
	f := mustFable(t, s)
	first, second := fableCreds("at-f1", "rt-f1"), fableCreds("at-f1b", "rt-f1b")
	for _, c := range []string{first, second} {
		if err := f.WriteAccountCredentials("1", fableEmails["1"], c); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := f.ReadAccountCredentials("1", fableEmails["1"]); got != second {
		t.Fatalf("Fable login = %q, want %q", got, second)
	}
	if got, _ := f.Creds.ReadPrev("1", fableEmails["1"]); got != first {
		t.Fatalf(".prev = %q, want the first generation", got)
	}
	if _, err := os.Stat(filepath.Join(s.BackupDir(), "credentials-fable", ".creds-1-one@example.com.enc")); err != nil {
		t.Fatalf("Fable login file: %v", err)
	}
	if after := snapshot(t, s.CredentialsDir); !sameSnapshot(before, after) {
		t.Fatal("credentials/ changed by a Fable login write")
	}
	if got, _ := s.ReadAccountCredentials("1", fableEmails["1"]); got != fableCreds("at-d1", "rt-d1") {
		t.Fatalf("default backup = %q", got)
	}
}

func TestForFableRefusedOnMacOS(t *testing.T) {
	s := freshStore(t)
	s.Platform = platform.MacOS
	_, err := s.ForFable()
	if !errors.Is(err, ErrFableUnsupported) {
		t.Fatalf("ForFable on macOS: %v", err)
	}
	if want := "The Fable store needs a file-based credential store; macOS keeps Claude Code's login in the Keychain"; err.Error() != want {
		t.Fatalf("message %q", err.Error())
	}
	if st := s.FableStore(); st.On || s.HasFableLogins() {
		t.Fatalf("FableStore on macOS = %+v", st)
	}
}

func TestFableReadsCreateNoDirectory(t *testing.T) {
	testutil.IsolateHome(t)
	s, err := New(Options{Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &bytes.Buffer{}, OAuth: &oauth.FakeClient{}})
	if err != nil {
		t.Fatal(err)
	}
	s.Platform = platform.Linux
	f := mustFable(t, s)
	_ = s.FableStore()
	_ = f.FableStore()
	_ = s.HasFableLogins()
	_ = s.HasFableLogin("1")
	_, _ = f.ReadAccountCredentials("1", "one@example.com")
	_ = s.ResolveFableLiveSlot(nil)
	_ = f.CurrentAccountNumber()
	_ = f.HasLiveLogin()
	for _, dir := range []string{paths.FableScopeDir(s.BackupDir()), paths.FableCredentialsDir(s.BackupDir())} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after reads (%v)", dir, err)
		}
	}
}

// A3 -------------------------------------------------------------------------

func TestFableStoreState(t *testing.T) {
	live := fableCreds("at-f2", "rt-f2")
	quarantine := func(fp string) string {
		return `{"quarantine":{"2":{"email":"two@example.com","reason":"invalid_grant","at":"t","refreshTokenFingerprint":"` + fp + `"}}}`
	}
	for _, tc := range []struct {
		name       string
		logins     bool
		store      string
		state      string
		want       FableState
		hasLogins  bool
		reasonPart string
	}{
		{name: "no files", want: FableState{}, reasonPart: "no Fable login"},
		{name: "logins, empty store", logins: true, hasLogins: true, want: FableState{}, reasonPart: "empty"},
		{name: "seat-wide keys only", logins: true, hasLogins: true, store: `{` + storeMCP + `}`, want: FableState{}, reasonPart: "empty"},
		{name: "a login", logins: true, hasLogins: true, store: withMCP(live),
			want: FableState{On: true, Usable: true, Live: LiveSlot{Slot: "2", Source: SourceBackup}}},
		{name: "dead entry, its fingerprint", logins: true, hasLogins: true, store: withMCP(live), state: quarantine(fingerprint(live)),
			want: FableState{On: true, Live: LiveSlot{Slot: "2", Source: SourceBackup}}, reasonPart: "Account-2's Fable login: sign in again"},
		{name: "dead entry, stale fingerprint", logins: true, hasLogins: true, store: withMCP(live), state: quarantine(fingerprint(fableCreds("x", "rt-old"))),
			want: FableState{On: true, Usable: true, Live: LiveSlot{Slot: "2", Source: SourceBackup}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fableFixture(t)
			if tc.logins {
				putFableLogin(t, s, "1", fableCreds("at-f1", "rt-f1"))
				putFableLogin(t, s, "2", live)
			}
			if tc.store != "" {
				putFableLive(t, s, tc.store)
			}
			if tc.state != "" {
				if err := os.WriteFile(mustFable(t, s).StatePath(), []byte(tc.state), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := s.FableStore()
			reason := got.Reason
			got.Reason = ""
			if got != tc.want {
				t.Fatalf("FableStore = %+v, want %+v", got, tc.want)
			}
			if !strings.Contains(reason, tc.reasonPart) || (tc.reasonPart == "") != (reason == "") {
				t.Fatalf("Reason = %q, want %q", reason, tc.reasonPart)
			}
			if s.HasFableLogins() != tc.hasLogins {
				t.Fatalf("HasFableLogins = %v", !tc.hasLogins)
			}
			if s.HasFableLogin("2") != tc.logins || s.HasFableLogin("3") {
				t.Fatalf("HasFableLogin 2 = %v, 3 = %v", s.HasFableLogin("2"), s.HasFableLogin("3"))
			}
		})
	}
}

// A4 -------------------------------------------------------------------------

func writeFableChain(t *testing.T, s *Store, creds, slot string, id oauth.Identity) {
	t.Helper()
	f := mustFable(t, s)
	if err := f.writeLiveLineage(fingerprint(creds), slot, &id); err != nil {
		t.Fatal(err)
	}
}

func resolveFable(t *testing.T, s *Store) LiveSlot {
	t.Helper()
	data, err := s.ReadSequence()
	if err != nil {
		t.Fatal(err)
	}
	return s.ResolveFableLiveSlot(data)
}

func TestResolveFableLiveSlotSources(t *testing.T) {
	two := oauth.Identity{UUID: "u-2", Email: "two@example.com", OrgUUID: "org-2"}
	unknownLive := fableCreds("at-new", "rt-new")
	t.Run("backup", func(t *testing.T) {
		s := fableFixture(t)
		putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
		putFableLive(t, s, withMCP(fableCreds("at-f2-rotated", "rt-f2")))
		if got := resolveFable(t, s); got != (LiveSlot{Slot: "2", Source: SourceBackup}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("chain", func(t *testing.T) {
		s := fableFixture(t)
		putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
		putFableLive(t, s, unknownLive)
		writeFableChain(t, s, unknownLive, "2", two)
		got := resolveFable(t, s)
		if got.Slot != "2" || got.Source != SourceChain || got.Identity == nil || *got.Identity != two {
			t.Fatalf("got %+v", got)
		}
		if _, err := os.Stat(filepath.Join(s.BackupDir(), "live_lineage.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the default chain record was written")
		}
	})
	t.Run("profile", func(t *testing.T) {
		s := fableFixture(t)
		s.OAuth = &oauth.FakeClient{ProfileFn: func(_ context.Context, tok string) *oauth.Identity {
			if tok == "at-new" {
				return &two
			}
			return nil
		}}
		putFableLive(t, s, unknownLive)
		got := resolveFable(t, s.WithoutLineageWrites())
		if got.Slot != "2" || got.Source != SourceProfile {
			t.Fatalf("got %+v", got)
		}
		chain := filepath.Join(s.BackupDir(), "fable", "live_lineage.json")
		if _, err := os.Stat(chain); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a WithoutLineageWrites view wrote the Fable chain record")
		}
		if got := resolveFable(t, s); got.Slot != "2" || got.Source != SourceProfile {
			t.Fatalf("got %+v", got)
		}
		var rec liveLineage
		raw, err := os.ReadFile(chain)
		if err != nil || json.Unmarshal(raw, &rec) != nil || rec.Slot != "2" || rec.Fingerprint != fingerprint(unknownLive) {
			t.Fatalf("Fable chain record = %s (%v)", raw, err)
		}
		if _, err := os.Stat(filepath.Join(s.BackupDir(), "live_lineage.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the default chain record was written")
		}
	})
}

func TestResolveFableLiveSlotIgnoresTheConfig(t *testing.T) {
	s := fableFixture(t)
	putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
	putFableLive(t, s, fableCreds("at-f2", "rt-f2"))
	writeLiveIdentity(t, s, "three@example.com", "org-3")
	if got := resolveFable(t, s); got != (LiveSlot{Slot: "2", Source: SourceBackup}) {
		t.Fatalf("got %+v", got)
	}
	putFableLive(t, s, fableCreds("at-x", "rt-x"))
	if got := resolveFable(t, s); got.Slot != "" {
		t.Fatalf("an unknown credential resolved to %+v; the config must not decide", got)
	}
}

func TestResolveFableLiveSlotCopiedLoginIsNoEvidence(t *testing.T) {
	s := fableFixture(t)
	same := fableCreds("at-f", "rt-same")
	putFableLogin(t, s, "1", same)
	putFableLogin(t, s, "2", same)
	putFableLive(t, s, same)
	if got := resolveFable(t, s); got.Slot != "" {
		t.Fatalf("got %+v, want unknown", got)
	}
}

func TestResolveFableLiveSlotUnknownCredential(t *testing.T) {
	s := fableFixture(t)
	putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
	putFableLive(t, s, fableCreds("at-x", "rt-x"))
	if got := resolveFable(t, s); got != (LiveSlot{}) {
		t.Fatalf("got %+v", got)
	}
}

// ResolveOwnerSlot names the last install for a login nothing else names,
// while the roster holds the chain record's identity in its slot;
// ResolveFableLiveSlot and FableStore keep their answer.
func TestResolveOwnerSlotTakesTheFableStoresLastInstall(t *testing.T) {
	s := fableFixture(t)
	two := oauth.Identity{UUID: "u-2", Email: "two@example.com", OrgUUID: "org-2"}
	putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
	writeFableChain(t, s, fableCreds("at-f2", "rt-f2"), "2", two)
	putFableLive(t, s, fableCreds("at-f2-rotated", "rt-f2-rotated"))
	data, _ := s.ReadSequence()
	f := mustFable(t, s)
	if got := f.ResolveOwnerSlot(data); got != (LiveSlot{Slot: "2", Source: SourceLastInstall}) {
		t.Fatalf("owner %+v, want the last install", got)
	}
	if got := s.ResolveFableLiveSlot(data); got != (LiveSlot{}) {
		t.Fatalf("ResolveFableLiveSlot = %+v, want no slot", got)
	}
	if got := s.FableStore().Live; got != (LiveSlot{}) {
		t.Fatalf("FableStore().Live = %+v, want no slot", got)
	}
	writeSequenceRaw(t, s, strings.Replace(fableRoster, `"uuid": "u-2", "organizationUuid": "org-2"`, `"uuid": "u-9", "organizationUuid": "org-9"`, 1))
	data, _ = s.ReadSequence()
	if got := f.ResolveOwnerSlot(data); got.Slot != "" {
		t.Fatalf("owner %+v after slot 2 changed identity, want none", got)
	}
}

func TestResolveFableLiveSlotAsksTheProfileOnce(t *testing.T) {
	s := fableFixture(t)
	calls := 0
	s.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity { calls++; return nil }}
	putFableLive(t, s, fableCreds("at-x", "rt-x"))
	for i := 0; i < 3; i++ {
		_ = resolveFable(t, s)
		_ = mustFable(t, s).CurrentAccountNumber()
	}
	if calls != 1 {
		t.Fatalf("profile asked %d times, want 1", calls)
	}
}

// A5 -------------------------------------------------------------------------

func TestRefreshFableBackupGuarded(t *testing.T) {
	held := fableCreds("at-f2", "rt-f2")
	fresh := fableCreds("at-f2b", "rt-f2b")
	t.Run("writes only the Fable login", func(t *testing.T) {
		s := fableFixture(t)
		putFableLogin(t, s, "2", held)
		before := snapshot(t, s.CredentialsDir)
		var sent string
		c := &oauth.FakeClient{RefreshFn: func(_ context.Context, creds string) oauth.RefreshOutcome {
			sent = creds
			if !s.Lock.Held() {
				t.Error("the store lock is not held during the refresh")
			}
			return oauth.RefreshOutcome{Credentials: fresh}
		}}
		out := s.RefreshFableBackupGuarded(context.Background(), c, "2", fableEmails["2"], held)
		if out.Credentials != fresh || sent != held {
			t.Fatalf("outcome %+v, sent %q", out, sent)
		}
		f := mustFable(t, s)
		if got, _ := f.ReadAccountCredentials("2", fableEmails["2"]); got != fresh {
			t.Fatalf("Fable login = %q", got)
		}
		if got, _ := f.Creds.ReadPrev("2", fableEmails["2"]); got != held {
			t.Fatalf(".prev = %q", got)
		}
		if after := snapshot(t, s.CredentialsDir); !sameSnapshot(before, after) {
			t.Fatal("the default backups changed")
		}
	})
	t.Run("a moved lineage is not refreshed", func(t *testing.T) {
		s := fableFixture(t)
		moved := fableCreds("at-f2c", "rt-f2c")
		putFableLogin(t, s, "2", moved)
		c := &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
			t.Fatal("refreshed a moved lineage")
			return oauth.RefreshOutcome{}
		}}
		out := s.RefreshFableBackupGuarded(context.Background(), c, "2", fableEmails["2"], held)
		if out.Credentials != moved {
			t.Fatalf("outcome %+v, want the stored login", out)
		}
	})
	t.Run("the live Fable login is Claude Code's", func(t *testing.T) {
		s := fableFixture(t)
		putFableLogin(t, s, "2", held)
		putFableLive(t, s, withMCP(held))
		c := &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
			t.Fatal("refreshed the live login's lineage")
			return oauth.RefreshOutcome{}
		}}
		if out := s.RefreshFableBackupGuarded(context.Background(), c, "2", fableEmails["2"], held); out.Error != RefreshDeclined {
			t.Fatalf("outcome %+v", out)
		}
	})
	t.Run("invalid_grant comes back", func(t *testing.T) {
		s := fableFixture(t)
		putFableLogin(t, s, "2", held)
		c := &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
			return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
		}}
		if out := s.RefreshFableBackupGuarded(context.Background(), c, "2", fableEmails["2"], held); out.Error != oauth.ErrInvalidGrant {
			t.Fatalf("outcome %+v", out)
		}
		if got, _ := mustFable(t, s).ReadAccountCredentials("2", fableEmails["2"]); got != held {
			t.Fatalf("Fable login = %q", got)
		}
	})
}

func TestHarvestFableLive(t *testing.T) {
	two := oauth.Identity{UUID: "u-2", Email: "two@example.com", OrgUUID: "org-2"}
	rotated := fableCreds("at-r", "rt-rotated")
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, s *Store)
		view    func(s *Store) *Store
		want    bool
	}{
		{"by chain", func(t *testing.T, s *Store) { writeFableChain(t, s, rotated, "2", two) }, nil, true},
		{"by profile", func(t *testing.T, s *Store) {
			s.OAuth = &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity { return &two }}
		}, nil, true},
		{"unknown", func(*testing.T, *Store) {}, nil, false},
		{"dry view", func(t *testing.T, s *Store) { writeFableChain(t, s, rotated, "2", two) }, (*Store).WithoutLineageWrites, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fableFixture(t)
			putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
			putFableLive(t, s, withMCP(rotated))
			tc.prepare(t, s)
			view := s
			if tc.view != nil {
				view = tc.view(s)
			}
			before := snapshot(t, s.CredentialsDir)
			if got := view.HarvestFableLive(); got != tc.want {
				t.Fatalf("HarvestFableLive = %v, want %v", got, tc.want)
			}
			stored, _ := mustFable(t, s).ReadAccountCredentials("2", fableEmails["2"])
			if harvested := fingerprintsEqual(stored, rotated) && !strings.Contains(stored, "mcpOAuth"); harvested != tc.want {
				t.Fatalf("Fable login = %q", stored)
			}
			if raw, _ := os.ReadFile(filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json")); string(raw) != withMCP(rotated) {
				t.Fatal("the harvest wrote the live credential")
			}
			if after := snapshot(t, s.CredentialsDir); !sameSnapshot(before, after) {
				t.Fatal("the default backups changed")
			}
			for _, lock := range []string{filepath.Join(s.BackupDir(), "fable", "store.lock"), filepath.Join(paths.FableStoreDir(s.BackupDir()), ".storage-write.lock")} {
				if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s left behind", lock)
				}
			}
		})
	}
	t.Run("same fingerprint", func(t *testing.T) {
		s := fableFixture(t)
		putFableLogin(t, s, "2", rotated)
		putFableLive(t, s, withMCP(rotated))
		writeFableChain(t, s, rotated, "2", two)
		if s.HarvestFableLive() {
			t.Fatal("harvested a login already stored")
		}
	})
}

// A10 ------------------------------------------------------------------------

func TestDefaultResolverBesideTheFableStore(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fable   bool
		warning bool
	}{
		{"config names the Fable store's live account", true, false},
		{"no Fable store", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fableFixture(t)
			writeLiveIdentity(t, s, "two@example.com", "org-2")
			if tc.fable {
				putFableLogin(t, s, "2", fableCreds("at-f2", "rt-f2"))
				putFableLive(t, s, withMCP(fableCreds("at-f2", "rt-f2")))
			}
			got := resolveLive(t, s)
			if want := (LiveSlot{Slot: "1", ConfigSlot: "2", ByLineage: true, Source: SourceBackup}); got != want {
				t.Fatalf("ResolveLiveSlot = %+v, want %+v", got, want)
			}
			raw, _ := os.ReadFile(filepath.Join(s.BackupDir(), "tycswap.log"))
			if warned := strings.Contains(string(raw), "WARNING") && strings.Contains(string(raw), "live config names slot 2"); warned != tc.warning {
				t.Fatalf("WARNING logged = %v, want %v:\n%s", warned, tc.warning, raw)
			}
		})
	}
}

// The default login resolved from a Fable scope (ownership, retirement) uses
// the default backups and the default chain record.
func TestDefaultLiveSlotFromTheFableScope(t *testing.T) {
	s := fableFixture(t)
	putFableLogin(t, s, "2", fableCreds("at-d1", "rt-d1"))
	f := mustFable(t, s)
	data, _ := s.ReadSequence()
	if got := f.defaultLiveSlot(data, fableCreds("at-d1", "rt-d1")); got.Slot != "1" || got.Source != SourceBackup {
		t.Fatalf("defaultLiveSlot on the Fable scope = %+v, want slot 1 by backup", got)
	}
}
