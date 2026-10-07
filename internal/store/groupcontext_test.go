package store

import (
	"bytes"
	"errors"
	"github.com/tyclab/tycswap/internal/platform"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
)

func TestGroupContextIgnoresInheritedManagedProfileAndRetainsExplicitDefault(t *testing.T) {
	s := freshStore(t)
	custom := filepath.Join(t.TempDir(), "default")
	for _, profile := range []string{groups.ProfileDir(s.SharedRoot(), groups.Fable), s.SessionDir("1", "fixture@example.test")} {
		t.Setenv("CLAUDE_CONFIG_DIR", profile)
		t.Setenv("TYCSWAP_DEFAULT_PROFILE_DIR", "")
		plain, err := New(Options{Stderr: &bytes.Buffer{}, Keychain: keychain.NewFake()})
		if err != nil {
			t.Fatal(err)
		}
		if plain.DefaultProfileDir() != filepath.Join(s.Home, ".claude") {
			t.Fatal("a managed profile was treated as the default credential owner")
		}
		t.Setenv("TYCSWAP_DEFAULT_PROFILE_DIR", custom)
		retained, err := New(Options{Stderr: &bytes.Buffer{}, Keychain: keychain.NewFake()})
		if err != nil {
			t.Fatal(err)
		}
		if retained.DefaultProfileDir() != custom || retained.DefaultConfigPath() != filepath.Join(custom, ".claude.json") {
			t.Fatal("the explicit custom default context was lost")
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", custom)
	t.Setenv("TYCSWAP_DEFAULT_PROFILE_DIR", "/ignored/stale-context")
	plain, err := New(Options{Stderr: &bytes.Buffer{}, Keychain: keychain.NewFake()})
	if err != nil {
		t.Fatal(err)
	}
	if plain.DefaultProfileDir() != custom {
		t.Fatal("intentional unmanaged profile was ignored")
	}
	explicit, err := New(Options{Stderr: &bytes.Buffer{}, Keychain: keychain.NewFake(), DefaultProfileDir: filepath.Join(custom, "explicit")})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.DefaultProfileDir() != filepath.Join(custom, "explicit") {
		t.Fatal("explicit constructor injection was ignored")
	}
}

func TestReadOwnedCredentialsFailsForCorruptClaimsAndIdentityDrift(t *testing.T) {
	s := freshStore(t)
	writeSequenceRaw(t, s, `{"activeAccountNumber":null,"sequence":[1],"accounts":{"1":{"email":"one@example.test","organizationUuid":""}}}`)
	if err := s.WriteAccountCredentials("1", "one@example.test", `{"claudeAiOauth":{"accessToken":"synthetic-one","refreshToken":"refresh-one"}}`); err != nil {
		t.Fatal(err)
	}
	dir := s.SessionDir("1", "one@example.test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"one@example.test","organizationUuid":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"synthetic-one","refreshToken":"refresh-one"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock.With(func() error { return s.ClaimLegacyProfile("1", "one@example.test", dir) }); err != nil {
		t.Fatal(err)
	}
	if value, err := s.ReadOwnedCredentials("1", "one@example.test"); err != nil || value == "" {
		t.Fatal("reserved legacy profile was not readable")
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"other@example.test","organizationUuid":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := s.ReadOwnedCredentials("1", "one@example.test"); err == nil || value != "" {
		t.Fatal("drifted profile supplied a foreign credential")
	}
	if err := os.WriteFile(groups.ClaimsPath(s.SharedRoot()), []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := s.ReadOwnedCredentials("1", "one@example.test"); err == nil || value != "" {
		t.Fatal("corrupt claims silently reverted to backup credentials")
	}
}

func TestStoppedLegacyDeletionReleasesItsReservation(t *testing.T) {
	s := freshStore(t)
	writeSequenceRaw(t, s, `{"activeAccountNumber":null,"sequence":[1],"accounts":{"1":{"email":"one@example.test","organizationUuid":""}}}`)
	if err := s.WriteAccountCredentials("1", "one@example.test", `{"claudeAiOauth":{"accessToken":"synthetic-one","refreshToken":"refresh-one"}}`); err != nil {
		t.Fatal(err)
	}
	dir := s.SessionDir("1", "one@example.test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"one@example.test","organizationUuid":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"synthetic-one","refreshToken":"refresh-one"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock.With(func() error { return s.ClaimLegacyProfile("1", "one@example.test", dir) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock.With(func() error { return s.DeleteAccountFiles("1", "one@example.test") }); err != nil {
		t.Fatal(err)
	}
	registry, err := groups.LoadRegistry(s.SharedRoot())
	if err != nil || len(registry.Claims) != 0 {
		t.Fatal("normal deletion left a legacy reservation")
	}
}

func TestUnclaimedLegacyPartialRegistryStillBlocksDestruction(t *testing.T) {
	s := freshStore(t)
	dir := s.SessionDir("1", "legacy@example.test")
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", "self.json"), []byte(`{"pid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccountFiles("1", "legacy@example.test"); err == nil {
		t.Fatal("an old unclaimed partial process registry allowed destructive profile removal")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("guard deleted the uncertain legacy profile")
	}
}

type failingProfileKeychain struct{ keychain.KeychainClient }

func (f failingProfileKeychain) Get(string, string) (string, bool, error) {
	return "", false, errors.New("synthetic Keychain read failure")
}

func TestMacKeychainReadFailureCannotTransferPlaintextShadow(t *testing.T) {
	s := freshStore(t)
	writeSequenceRaw(t, s, `{"activeAccountNumber":null,"sequence":[1],"accounts":{"1":{"email":"one@example.test","organizationUuid":""}}}`)
	dir := s.SessionDir("1", "one@example.test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shadow := `{"claudeAiOauth":{"accessToken":"stale-shadow","refreshToken":"stale-refresh"}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(shadow), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"one@example.test","organizationUuid":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Platform = platform.MacOS
	s.kc = failingProfileKeychain{KeychainClient: keychain.NewFake()}
	if value, _, err := s.ReadProfileCredentials(dir); err == nil || value != "" {
		t.Fatal("Keychain failure returned an old plaintext credential")
	}
	group, err := s.ForGroup(groups.Fable)
	if err != nil {
		t.Fatal(err)
	}
	group = group.WithGroupHandoff()
	if _, err := group.BeginGroupSwitch(groups.Account{Number: "1", Email: "one@example.test"}, shadow); err == nil {
		t.Fatal("ownership transfer proceeded after Keychain failure")
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, ".credentials.json")); string(raw) != shadow {
		t.Fatal("failed handoff modified the shadow")
	}
}

func TestRelativeDefaultCaptureIsStableAndUnpinnedConfigKeepsHomeAsymmetry(t *testing.T) {
	s := freshStore(t)
	if !s.DefaultProfileUnpinned() || s.DefaultConfigPath() != filepath.Join(s.Home, ".claude.json") {
		t.Fatal("default config asymmetry was lost")
	}
	custom, err := New(Options{Stderr: &bytes.Buffer{}, Keychain: keychain.NewFake(), DefaultProfileDir: "relative-profile"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(custom.DefaultProfileDir()) || custom.DefaultProfileUnpinned() || custom.DefaultConfigPath() != filepath.Join(custom.DefaultProfileDir(), ".claude.json") {
		t.Fatal("relative custom profile was not captured as a stable explicit profile")
	}
}
