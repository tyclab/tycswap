package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

func releaseFixture(t *testing.T) (*Store, string, *LegacyRelease) {
	t.Helper()
	s, dir := claimedSession(t)
	value, _, err := s.ReadProfileCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Creds.WriteBackup("1", "one@example.test", value); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAccountConfig("1", "one@example.test", `{"oauthAccount":{"emailAddress":"one@example.test","organizationUuid":""}}`); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.LegacyReleaseCandidate("1")
	if err != nil || candidate == nil {
		t.Fatalf("candidate: %v", err)
	}
	return s, dir, candidate
}

func TestLegacyReleaseRecoversAfterSourceCleared(t *testing.T) {
	s, dir, candidate := releaseFixture(t)
	marker := legacyReleaseMarker{Version: 1, Account: candidate.Owner.Account, Digest: releaseDigest(candidate.Credentials)}
	if err := groups.WriteJSON(legacyReleasePath(dir), marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, sessprofile.CredentialsFileName)); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock.With(func() error {
		retry, err := s.LegacyReleaseCandidate("1")
		if err != nil {
			return err
		}
		return s.ReleaseLegacyAccount(retry, func(string) error { return errors.New("must use the verified saved release") })
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := s.CredentialOwner("1")
	if err != nil || owner.Scope != "" {
		t.Fatalf("recovery left an owner: %+v, %v", owner, err)
	}
	if _, err := os.Stat(legacyReleasePath(dir)); !os.IsNotExist(err) {
		t.Fatal("recovery marker remained")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "history.jsonl")); err != nil || string(got) != "kept history\n" {
		t.Fatal("recovery lost history")
	}
}

func TestLegacyReleaseMissingCredentialNeedsMatchingSavedProof(t *testing.T) {
	for _, mutation := range []string{"missing", "version", "identity", "uuid", "digest", "backup"} {
		t.Run(mutation, func(t *testing.T) {
			s, dir, candidate := releaseFixture(t)
			marker := legacyReleaseMarker{Version: 1, Account: candidate.Owner.Account, Digest: releaseDigest(candidate.Credentials)}
			switch mutation {
			case "version":
				marker.Version++
			case "identity":
				marker.Account.OrgUUID = "other-org"
			case "uuid":
				marker.Account.UUID = "other-uuid"
			case "digest":
				marker.Digest = ""
			case "backup":
				if err := s.Creds.WriteBackup("1", "one@example.test", `{"claudeAiOauth":{"accessToken":"other","refreshToken":"other"}}`); err != nil {
					t.Fatal(err)
				}
			}
			if mutation != "missing" {
				if err := groups.WriteJSON(legacyReleasePath(dir), marker); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(filepath.Join(dir, sessprofile.CredentialsFileName)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LegacyReleaseCandidate("1"); err == nil {
				t.Fatal("missing source was released without proof of the saved backup")
			}
			registry, err := groups.LoadRegistry(s.BackupDir())
			if err != nil || registry.Claims["1"].Scope != "legacy:1" {
				t.Fatal("failed recovery dropped the claim")
			}
		})
	}
}

func TestLegacyReleaseKeepsSeatCredentials(t *testing.T) {
	s, dir, _ := releaseFixture(t)
	value := `{"claudeAiOauth":{"accessToken":"old-access","refreshToken":"old-refresh"},"mcpOAuth":{"test-server":{"accessToken":"seat-token"}}}`
	if err := os.WriteFile(filepath.Join(dir, sessprofile.CredentialsFileName), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock.With(func() error {
		candidate, err := s.LegacyReleaseCandidate("1")
		if err != nil {
			return err
		}
		return s.ReleaseLegacyAccount(candidate, func(string) error { return nil })
	}); err != nil {
		t.Fatal(err)
	}
	remaining, _, err := s.ReadProfileCredentials(dir)
	want, _ := ccfile.SeatWidePart(value)
	if err != nil || remaining != want || !ccfile.SeatWideOnly(remaining) {
		t.Fatal("the profile lost its seat credentials")
	}
	if owner, err := s.CredentialOwner("1"); err != nil || owner.Scope != "" {
		t.Fatal("seat-only profile still owns the login")
	}
}

type retainedReleaseKeychain struct{ keychain.KeychainClient }

func (retainedReleaseKeychain) Delete(string, string) error { return nil }

func TestLegacyReleaseKeepsClaimWhenKeychainRetainsCredential(t *testing.T) {
	s, dir, candidate := releaseFixture(t)
	kc := keychain.NewFake()
	kc.Seed(sessprofile.KeychainServiceName(dir), keychain.AccountName(), candidate.Credentials)
	s.Platform = platform.MacOS
	s.kc = retainedReleaseKeychain{kc}
	if err := s.Lock.With(func() error {
		return s.ReleaseLegacyAccount(candidate, func(string) error { return nil })
	}); err == nil {
		t.Fatal("uncleared Keychain credential was released")
	}
	registry, err := groups.LoadRegistry(s.BackupDir())
	if err != nil || registry.Claims["1"].Scope != "legacy:1" {
		t.Fatal("uncleared Keychain lost the claim")
	}
	if _, err := os.Stat(legacyReleasePath(dir)); err != nil {
		t.Fatal("failed cleanup lost recovery proof")
	}
}

func TestNewLegacyClaimDropsOldReleaseProof(t *testing.T) {
	s, dir, candidate := releaseFixture(t)
	marker := legacyReleaseMarker{Version: 1, Account: candidate.Owner.Account, Digest: releaseDigest(candidate.Credentials)}
	if err := groups.WriteJSON(legacyReleasePath(dir), marker); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock.With(func() error { return s.ClaimLegacyProfile("1", "one@example.test", dir) }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyReleasePath(dir)); !os.IsNotExist(err) {
		t.Fatal("a later session inherited old release proof")
	}
}

func TestLegacyReleaseRefusesInvalidSourceOrStoredConfig(t *testing.T) {
	for _, mismatch := range []string{"credential", "config", "kind"} {
		t.Run(mismatch, func(t *testing.T) {
			s, dir, _ := releaseFixture(t)
			switch mismatch {
			case "credential":
				if err := os.WriteFile(filepath.Join(dir, sessprofile.CredentialsFileName), []byte("not an OAuth login"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "config":
				if err := s.WriteAccountConfig("1", "one@example.test", `{"oauthAccount":{"emailAddress":"someone@example.test"}}`); err != nil {
					t.Fatal(err)
				}
			case "kind":
				writeSequenceRaw(t, s, `{"activeAccountNumber":null,"sequence":[1],"accounts":{"1":{"email":"one@example.test","organizationUuid":"","kind":"api_key"}}}`)
			}
			if _, err := s.LegacyReleaseCandidate("1"); err == nil {
				t.Fatal("invalid source or stored identity was accepted")
			}
			registry, err := groups.LoadRegistry(s.BackupDir())
			if err != nil || registry.Claims["1"].Scope != "legacy:1" {
				t.Fatal("refused handoff lost its claim")
			}
			if _, err := os.Stat(filepath.Join(dir, sessprofile.CredentialsFileName)); err != nil {
				t.Fatal("refused handoff lost its credentials")
			}
		})
	}
}
