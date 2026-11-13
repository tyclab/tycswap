package store

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

type LegacyRelease struct {
	Owner       groups.Owner
	Credentials string
}

type legacyReleaseMarker struct {
	Version int            `json:"version"`
	Account groups.Account `json:"account"`
	Digest  string         `json:"digest"`
}

func legacyReleasePath(dir string) string { return filepath.Join(dir, ".tycswap-release.json") }

func releaseDigest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(oauth.AccountOnly(value))))
}

// LegacyReleaseCandidate is read under the store lock, before any network verification.
func (s *Store) LegacyReleaseCandidate(number string) (*LegacyRelease, error) {
	owner, err := s.CredentialOwner(number)
	if err != nil || !strings.HasPrefix(owner.Scope, "legacy:") {
		return nil, err
	}
	if len(owner.PIDs) > 0 {
		return nil, legacyBusy(number, owner.PIDs)
	}
	if owner.Scope != "legacy:"+number {
		return nil, cerr.Session("Account-%s is held by another account's session profile; reconcile its identity before switching", number)
	}
	if s.AccountKindFor(number) == "api_key" {
		return nil, cerr.Session("Account-%s's session profile cannot release an OAuth login into an API-key slot", number)
	}
	if err := s.verifyLegacyHandoff(&owner); err != nil {
		return nil, err
	}
	value, _, err := s.ReadProfileCredentials(owner.ProfileDir)
	if err != nil {
		return nil, err
	}
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return nil, err
	}
	if claim, ok := registry.Claims[number]; ok && (claim.Uncertain || claim.Account.Number != number || !claim.Account.SameIdentity(owner.Account) || claim.Account.UUID != "" && claim.Account.UUID != owner.Account.UUID) {
		return nil, cerr.Session("Account-%s's session claim is uncertain; reconcile its identity before switching", number)
	}
	if err := s.verifyLegacyBackupConfig(owner); err != nil {
		return nil, err
	}
	if ccfile.SeatWideOnly(value) {
		value = ""
	}
	if value == "" {
		if err := s.verifiedLegacyReleaseMarker(owner); err != nil {
			return nil, err
		}
	} else if oauth.ExtractAccessToken(value) == "" {
		return nil, cerr.Session("Account-%s's session credential is unreadable; its login and claim remain held", number)
	} else if owner.Uncertain {
		return nil, cerr.Session("Account-%s's session ownership is uncertain; reconcile its identity before switching", number)
	}
	return &LegacyRelease{Owner: owner, Credentials: value}, nil
}

func (s *Store) verifyLegacyBackupConfig(owner groups.Owner) error {
	config, err := s.ReadAccountConfig(owner.Account.Number, owner.Account.Email)
	if err != nil {
		return err
	}
	var data struct {
		Account struct {
			Email string `json:"emailAddress"`
			Org   string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal([]byte(config), &data) != nil || data.Account.Email != owner.Account.Email || data.Account.Org != owner.Account.OrgUUID {
		return cerr.Session("Account-%s's stored config disagrees with its session identity; reconcile the account before switching", owner.Account.Number)
	}
	return nil
}

func legacyBusy(number string, pids []int) error {
	return cerr.Session("Account-%s has a running session profile (PID %s); exit its Claude sessions, then retry tycswap switch %s. Its login and history will be preserved.", number, joinPIDs(pids), number)
}

func (s *Store) verifiedLegacyReleaseMarker(owner groups.Owner) error {
	var marker legacyReleaseMarker
	if err := groups.ReadJSON(legacyReleasePath(owner.ProfileDir), &marker); err != nil {
		return err
	}
	backup, err := s.ReadAccountCredentials(owner.Account.Number, owner.Account.Email)
	if err != nil {
		return err
	}
	if marker.Version != 1 || marker.Account != owner.Account || marker.Digest == "" || oauth.ExtractAccessToken(backup) == "" || marker.Digest != releaseDigest(backup) {
		return cerr.Session("Account-%s's session credential is missing without a verified saved release; reconcile that profile before switching", owner.Account.Number)
	}
	return nil
}

// ReleaseLegacyAccount needs the store and source credential/config/storage locks.
// The marker proves the saved backup if cleanup stops after removing the source.
func (s *Store) ReleaseLegacyAccount(expected *LegacyRelease, verify func(string) error) error {
	current, err := s.LegacyReleaseCandidate(expected.Owner.Account.Number)
	if err != nil {
		return err
	}
	if current == nil || current.Owner.ProfileDir != expected.Owner.ProfileDir || current.Owner.Account != expected.Owner.Account || current.Credentials != expected.Credentials {
		return cerr.Session("session ownership changed while preparing the switch; retry tycswap switch %s", expected.Owner.Account.Number)
	}
	owner := current.Owner
	if current.Credentials != "" {
		if err := verify(current.Credentials); err != nil {
			return err
		}
		marker := legacyReleaseMarker{Version: 1, Account: owner.Account, Digest: releaseDigest(current.Credentials)}
		if err := groups.WriteJSON(legacyReleasePath(owner.ProfileDir), marker); err != nil {
			return err
		}
		if err := s.Creds.WriteBackup(owner.Account.Number, owner.Account.Email, oauth.AccountOnly(current.Credentials)); err != nil {
			return err
		}
	}
	if err := s.clearLegacyLogin(owner.ProfileDir); err != nil {
		return err
	}
	if value, _, err := s.ReadProfileCredentials(owner.ProfileDir); err != nil {
		return err
	} else if value != "" && !ccfile.SeatWideOnly(value) {
		return cerr.Session("Account-%s's session credential could not be cleared; its claim remains held", owner.Account.Number)
	}
	if err := sessprofile.ClearStaleMarker(owner.ProfileDir); err != nil {
		return err
	}
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return err
	}
	delete(registry.Claims, owner.Account.Number)
	if err := registry.Save(s.backupDir); err != nil {
		return err
	}
	if err := os.Remove(legacyReleasePath(owner.ProfileDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if s.Log != nil {
		s.Log.Infof("Released idle session profile for account %s", owner.Account.Number)
	}
	return nil
}

func (s *Store) clearLegacyLogin(dir string) error {
	value, _, err := s.ReadProfileCredentials(dir)
	if err != nil {
		return err
	}
	remainder, keep := ccfile.SeatWidePart(value)
	if s.Platform == platform.MacOS {
		if keep {
			err = s.kc.Set(sessprofile.KeychainServiceName(dir), keychain.AccountName(), remainder)
		} else {
			err = s.kc.Delete(sessprofile.KeychainServiceName(dir), keychain.AccountName())
		}
		if err != nil {
			return err
		}
	}
	path := filepath.Join(dir, sessprofile.CredentialsFileName)
	if keep {
		return atomicfile.Write(path, []byte(remainder), atomicfile.Opts{})
	}
	return removeGroupFile(path)
}
