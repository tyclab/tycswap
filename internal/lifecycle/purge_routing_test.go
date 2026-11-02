package lifecycle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/store"
)

func TestPurgeRefusesManagedStartupBeforeNativeRegistry(t *testing.T) {
	s := newStore(t)
	seed(t, s, ip(1), switchable("1", "a@example.com"))
	registerPurgeSession(t, s)
	p := &countingPrompter{}
	withPrompter(t, p)
	if err := Purge(s); errKind(err) != "SessionError" {
		t.Fatalf("purge during startup = %v", err)
	}
	if p.calls != 0 {
		t.Fatal("prompted despite live managed startup")
	}
}

func TestPurgeRechecksSessionAfterConfirmation(t *testing.T) {
	for _, kind := range []string{"managed", "group", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			seed(t, s, ip(1), switchable("1", "a@example.com"))
			withPrompter(t, &racingPrompter{t: t, s: s, commit: func() {
				if kind == "managed" {
					registerPurgeSession(t, s)
					return
				}
				profile := groups.ProfileDir(s.SharedRoot(), groups.Opus)
				if kind == "legacy" {
					profile = s.SessionDir("1", "a@example.com")
				}
				writePurgeRegistry(t, profile, "sessions", "self.json", `{"pid":`+strconv.Itoa(os.Getpid())+`}`)
			}})
			if err := Purge(s); errKind(err) != "SessionError" {
				t.Fatalf("purge after session start = %v", err)
			}
			if got, _ := s.ReadAccountCredentials("1", "a@example.com"); got != switchable("1", "a@example.com").creds {
				t.Fatal("refused purge removed credentials")
			}
		})
	}
}

func TestPurgeRefusesGroupIDEAndUnknownState(t *testing.T) {
	for _, body := range []string{`{"pid":` + strconv.Itoa(os.Getpid()) + `}`, `not-json`, `{}`} {
		t.Run(body, func(t *testing.T) {
			s := newStore(t)
			seed(t, s, ip(1), switchable("1", "a@example.com"))
			writePurgeRegistry(t, groups.ProfileDir(s.SharedRoot(), groups.Fable), "ide", "self.lock", body)
			if err := Purge(s); errKind(err) != "SessionError" {
				t.Fatalf("purge with unsafe IDE state = %v", err)
			}
		})
	}
}

func TestPurgeKeepsLockInodesAndRemovesStaleRoutingState(t *testing.T) {
	s := newStore(t)
	seed(t, s, ip(1), switchable("1", "a@example.com"))
	if err := routingstate.With(s.SharedRoot(), func(state *routingstate.State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock.With(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var before []os.FileInfo
	for _, name := range []string{".lock", "routing.lock"} {
		info, err := os.Stat(filepath.Join(s.SharedRoot(), name))
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, info)
	}
	withPrompter(t, &racingPrompter{t: t, s: s, commit: func() {
		state := `{"version":1,"sessions":{"fable/dead":{"group":"fable","id":"dead","pid":2147483647,"model":"fable"}},"leases":{}}`
		if err := os.WriteFile(filepath.Join(s.SharedRoot(), "routing.json"), []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
	}})
	if err := Purge(s); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{".lock", "routing.lock"} {
		after, err := os.Stat(filepath.Join(s.SharedRoot(), name))
		if err != nil || !os.SameFile(before[i], after) {
			t.Fatalf("coordination file replaced: %s (%v)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.SharedRoot(), "routing.json")); !os.IsNotExist(err) {
		t.Fatalf("routing data survived purge: %v", err)
	}
}

func TestPurgeRevalidatesRosterAfterPrompt(t *testing.T) {
	s := newStore(t)
	seed(t, s, ip(1), switchable("1", "a@example.com"))
	withPrompter(t, &racingPrompter{t: t, s: s, commit: func() {
		if err := os.WriteFile(s.SequenceFile, []byte(`{"accounts":{"1":{"email":"../outside"}},"sequence":[1]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}})
	if err := Purge(s); errKind(err) != "ValidationError" {
		t.Fatalf("purge after roster changed = %v", err)
	}
}

func TestPurgeRemovesGroupKeychainEntries(t *testing.T) {
	kc := keychain.NewFake()
	s := newStoreOpts(t, store.Options{Keychain: kc})
	s.Platform = platform.MacOS
	seed(t, s, ip(1), switchable("1", "a@example.com"))
	profile := groups.ProfileDir(s.SharedRoot(), groups.Fable)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	service := sessprofile.KeychainServiceName(profile)
	kc.Seed(service, keychain.AccountName(), "test credential")
	answerYes(t)
	if err := Purge(s); err != nil {
		t.Fatal(err)
	}
	if kc.Exists(service, keychain.AccountName()) {
		t.Fatal("group credential survived purge")
	}
}

func registerPurgeSession(t *testing.T, s *store.Store) {
	t.Helper()
	if err := routingstate.With(s.SharedRoot(), func(state *routingstate.State) error {
		state.Sessions[routingstate.Key("fable", "starting")] = routingstate.Session{
			Group: "fable", ID: "starting", PID: os.Getpid(), Model: "fable", Launch: "pending",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func writePurgeRegistry(t *testing.T, profile, registry, name, body string) {
	t.Helper()
	path := filepath.Join(profile, registry)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, name), []byte(strings.TrimSpace(body)), 0o600); err != nil {
		t.Fatal(err)
	}
}
