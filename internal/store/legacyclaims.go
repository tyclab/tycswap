package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
)

func (s *Store) ClearLegacyForGroup(journal *groups.Journal) error {
	s = s.Fresh()
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
		if err := s.kc.Delete(ccfile.KeychainServiceName(owner.ProfileDir), keychain.AccountName()); err != nil {
			return err
		}
	}
	err = os.Remove(filepath.Join(owner.ProfileDir, ccfile.CredentialsFileName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Store) restoreLegacyHandoff(journal *groups.Journal) error {
	s = s.Fresh()
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
		if err := s.kc.Set(ccfile.KeychainServiceName(owner.ProfileDir), keychain.AccountName(), journal.SourceCredential.Text); err != nil {
			return err
		}
	}
	return atomicfile.Write(filepath.Join(owner.ProfileDir, ccfile.CredentialsFileName), []byte(journal.SourceCredential.Text), atomicfile.Opts{})
}

func (s *Store) verifyLegacyHandoff(owner *groups.Owner) error {
	if owner.Scope != "legacy:"+owner.Account.Number || owner.ProfileDir != s.SessionDir(owner.Account.Number, owner.Account.Email) {
		return fmt.Errorf("invalid legacy handoff owner")
	}
	if err := groups.SafePath(filepath.Join(owner.ProfileDir, ccfile.CredentialsFileName)); err != nil {
		return err
	}
	pids, err := s.livePIDs(owner.ProfileDir)
	if err != nil || len(pids) > 0 {
		return fmt.Errorf("the source legacy profile is running; account claims remain held")
	}
	email, org, ok := s.profileDirIdentity(owner.ProfileDir)
	if !ok || email != owner.Account.Email || org != owner.Account.OrgUUID {
		return fmt.Errorf("the source legacy identity changed; account claims remain held")
	}
	return nil
}
