// Tests for Config.ActivePath and Config.FileOnly (DESIGN A100): the Fable
// store's credential file is the only active credential such a store touches.
package credstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/testutil"
)

const (
	fableLoginA = `{"claudeAiOauth":{"accessToken":"at-a","refreshToken":"rt-a"}}`
	fableLoginB = `{"claudeAiOauth":{"accessToken":"at-b","refreshToken":"rt-b"}}`
	fableMCP    = `{"mcpOAuth":{"srv":{"accessToken":"m"}}}`
)

func fableStore(t *testing.T, plat platform.Platform, kc *fakeKC) (*FileKeychainStore, string) {
	t.Helper()
	active := filepath.Join(t.TempDir(), "store", ".credentials.json")
	cfg := Config{Platform: plat, CredentialsDir: t.TempDir(), ActivePath: active, FileOnly: true}
	return New(cfg, kc, clock.System{}, logging.New(t.TempDir(), false)), active
}

func TestActivePathIsTheOnlyActiveFile(t *testing.T) {
	home := testutil.IsolateHome(t)
	defaultCreds := filepath.Join(home, ".claude", ".credentials.json")
	globalConfig := filepath.Join(home, ".claude.json")
	s, active := fableStore(t, platform.Linux, newFakeKC())

	if got, _, err := s.ReadActive(); err != nil || got != "" {
		t.Fatalf("ReadActive on an empty store = %q, %v", got, err)
	}
	writeFile(t, mustMkdir(t, active), fableMCP)
	if got, _, _ := s.ReadActive(); got != "" {
		t.Fatalf("seat-wide keys only read as %q, want no login", got)
	}
	if err := s.WriteActiveAccount(fableLoginA); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(active)
	if string(raw) != `{"claudeAiOauth":{"accessToken":"at-a","refreshToken":"rt-a"},"mcpOAuth":{"srv":{"accessToken":"m"}}}` {
		t.Fatalf("active file = %s, want the login with the store's own mcpOAuth", raw)
	}
	if got, _, _ := s.ReadActive(); got != string(raw) {
		t.Fatalf("ReadActive = %q, want the active file", got)
	}
	if got := s.ReadLiveOAuth(); got != string(raw) {
		t.Fatalf("ReadLiveOAuth = %q", got)
	}
	if err := s.WriteActive(fableLoginB); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.ReadActive(); got != fableLoginB {
		t.Fatalf("ReadActive after WriteActive = %q", got)
	}
	if err := s.WriteActive("sk-ant-api03-key"); err == nil {
		t.Fatal("WriteActive took an API key on a file-only store")
	}
	writeFile(t, active, `{"claudeAiOauth":{"accessToken":"a"},"mcpOAuth":{"srv":{}}}`)
	if err := s.ClearActive(); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(active); string(raw) != `{"mcpOAuth":{"srv":{}}}` {
		t.Fatalf("after ClearActive the file = %s, want the seat-wide keys", raw)
	}
	for _, p := range []string{defaultCreds, globalConfig} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after the Fable store's writes (%v)", p, err)
		}
	}
}

func TestActivePathLeavesTheDefaultFileUnchanged(t *testing.T) {
	fh := testutil.BuildFixtureHome(t)
	before, err := os.ReadFile(fh.CredentialsFile)
	if err != nil {
		t.Fatal(err)
	}
	cfgBefore, _ := os.ReadFile(fh.GlobalConfig)
	s, _ := fableStore(t, platform.Linux, newFakeKC())
	if got, _, _ := s.ReadActive(); got != "" {
		t.Fatalf("ReadActive read %q; the default file must not be read", got)
	}
	if err := s.WriteActiveAccount(fableLoginA); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearActive(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(fh.CredentialsFile); string(after) != string(before) {
		t.Fatal("the default credentials file changed")
	}
	if after, _ := os.ReadFile(fh.GlobalConfig); string(after) != string(cfgBefore) {
		t.Fatal("~/.claude.json changed")
	}
}

func TestFileOnlyNeverCallsTheKeychain(t *testing.T) {
	testutil.IsolateHome(t)
	kc := newFakeKC()
	s, active := fableStore(t, platform.MacOS, kc)
	if err := s.WriteActiveAccount(fableLoginA); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.ReadActive(); got != fableLoginA {
		t.Fatalf("ReadActive = %q", got)
	}
	_ = s.ReadLiveOAuth()
	if err := s.WriteBackup("1", "a@example.com", fableLoginA); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteBackup("1", "a@example.com", fableLoginB); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadBackup("1", "a@example.com"); got != fableLoginB {
		t.Fatalf("ReadBackup = %q", got)
	}
	if got, _ := s.ReadPrev("1", "a@example.com"); got != fableLoginA {
		t.Fatalf("ReadPrev = %q", got)
	}
	_ = s.DeleteBackup("1", "a@example.com")
	if err := s.ClearActive(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(active); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active file after ClearActive: %v", err)
	}
	if kc.getCalls+kc.setCalls+kc.delCalls != 0 {
		t.Fatalf("Keychain calls get=%d set=%d delete=%d, want none", kc.getCalls, kc.setCalls, kc.delCalls)
	}
	if b := s.LastActiveBackend(); b != "file" {
		t.Fatalf("LastActiveBackend = %q, want file", b)
	}
}

func mustMkdir(t *testing.T, file string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	return file
}
