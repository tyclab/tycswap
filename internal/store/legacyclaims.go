package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

func (s *Store) EnsureSessionAccountAvailable(number, profile string) error {
	owner, err := s.CredentialOwner(number)
	if err != nil {
		return err
	}
	if owner.Scope == "" || owner.Scope == "legacy:"+number && owner.ProfileDir == profile && !owner.Uncertain {
		return nil
	}
	return fmt.Errorf("Account-%s is held by %s%s; this legacy profile cannot copy its rotating credential", number, owner.Scope, uncertainSuffix(owner.Uncertain))
}

func (s *Store) ClaimLegacyProfile(number, email, profile string) error {
	if profile != s.SessionDir(number, email) {
		return fmt.Errorf("legacy profile path disagrees with its account")
	}
	if err := s.EnsureSessionAccountAvailable(number, profile); err != nil {
		return err
	}
	data, err := s.classifiedRoster()
	if err != nil {
		return err
	}
	account, err := s.accountFrom(data, number)
	if err != nil {
		return err
	}
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return err
	}
	registry.Claims[number] = groups.Owner{Scope: "legacy:" + number, ProfileDir: profile, Account: account}
	return registry.Save(s.backupDir)
}

func (s *Store) snapshotLegacyHandoff(journal *groups.Journal) error {
	owner, err := s.CredentialOwner(journal.Target.Number)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(owner.Scope, "legacy:") {
		return nil
	}
	if !s.groupHandoff || owner.Uncertain || len(owner.PIDs) > 0 {
		return fmt.Errorf("the legacy profile still owns this credential; exit it before opting into a group")
	}
	value, ok, err := s.ReadProfileCredentials(owner.ProfileDir)
	if err != nil {
		return err
	}
	if !ok || value == "" {
		return fmt.Errorf("the legacy credential cannot be preserved before group handoff")
	}
	journal.SourceOwner = &owner
	journal.SourceCredential = groups.Snapshot{Exists: true, Text: value}
	return nil
}

func (s *Store) ClearLegacyForGroup(journal *groups.Journal) error {
	if journal.SourceOwner == nil {
		return nil
	}
	owner := journal.SourceOwner
	if err := s.verifyLegacyHandoff(owner); err != nil {
		return err
	}
	value, ok, err := s.ReadProfileCredentials(owner.ProfileDir)
	if err != nil {
		return err
	}
	if ok && value != "" && fingerprintOf(value) != fingerprintOf(journal.SourceCredential.Text) {
		return fmt.Errorf("the legacy credential changed; account claims remain held")
	}
	if s.Platform == platform.MacOS {
		if err := s.kc.Delete(sessprofile.KeychainServiceName(owner.ProfileDir), keychain.AccountName()); err != nil {
			return err
		}
	}
	err = os.Remove(filepath.Join(owner.ProfileDir, sessprofile.CredentialsFileName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Store) restoreLegacyHandoff(journal *groups.Journal) error {
	if journal.SourceOwner == nil {
		return nil
	}
	owner := journal.SourceOwner
	if err := s.verifyLegacyHandoff(owner); err != nil {
		return err
	}
	current, ok, err := s.ReadProfileCredentials(owner.ProfileDir)
	if err != nil {
		return err
	}
	if ok && current != "" && fingerprintOf(current) != fingerprintOf(journal.SourceCredential.Text) {
		return fmt.Errorf("legacy credential changed; account claims remain held until its current lineage is verified")
	}
	if s.Platform == platform.MacOS {
		if err := s.kc.Set(sessprofile.KeychainServiceName(owner.ProfileDir), keychain.AccountName(), journal.SourceCredential.Text); err != nil {
			return err
		}
	}
	return atomicfile.Write(filepath.Join(owner.ProfileDir, sessprofile.CredentialsFileName), []byte(journal.SourceCredential.Text), atomicfile.Opts{})
}

func (s *Store) verifyLegacyHandoff(owner *groups.Owner) error {
	if owner.Scope != "legacy:"+owner.Account.Number || owner.ProfileDir != s.SessionDir(owner.Account.Number, owner.Account.Email) {
		return fmt.Errorf("invalid legacy handoff owner")
	}
	if err := groups.SafePath(filepath.Join(owner.ProfileDir, sessprofile.CredentialsFileName)); err != nil {
		return err
	}
	pids, err := profilePIDs(owner.ProfileDir)
	if err != nil || len(pids) > 0 {
		return fmt.Errorf("the source legacy profile is running; account claims remain held")
	}
	email, org, ok := sessprofile.ReadSessionIdentity(owner.ProfileDir)
	if !ok || email != owner.Account.Email || org != owner.Account.OrgUUID {
		return fmt.Errorf("the source legacy identity changed; account claims remain held")
	}
	return nil
}
