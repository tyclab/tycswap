package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
)

func (s *Store) CompleteGroupSwitch(journal *groups.Journal) error {
	if err := s.checkJournal(journal); err != nil {
		return err
	}
	if err := s.ClearDefaultForGroup(journal); err != nil {
		return err
	}
	if err := s.ClearLegacyForGroup(journal); err != nil {
		return err
	}
	if err := groups.WriteJSON(groups.ActivePath(s.backupDir, s.group), journal.Target); err != nil {
		return err
	}
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return err
	}
	for slot, owner := range registry.Claims {
		if owner.Scope == string(s.group) && slot != journal.Target.Number {
			delete(registry.Claims, slot)
		}
	}
	registry.Claims[journal.Target.Number] = groups.Owner{Scope: string(s.group), ProfileDir: s.ProfileDir(), Account: journal.Target}
	if err := registry.Save(s.backupDir); err != nil {
		return err
	}
	return removeGroupFile(groups.JournalPath(s.backupDir, s.group))
}

func (s *Store) RollbackGroupSwitch(journal *groups.Journal) error {
	if err := s.checkJournal(journal); err != nil {
		return err
	}
	if journal.Credential.Exists {
		if err := s.Creds.WriteActive(journal.Credential.Text); err != nil {
			return err
		}
	} else if err := s.Creds.ClearActive(); err != nil {
		return err
	}
	if err := restoreGroupFile(s.GlobalConfigPath(), journal.Config); err != nil {
		return err
	}
	if err := s.restoreDefaultHandoff(journal); err != nil {
		return err
	}
	if err := s.restoreLegacyHandoff(journal); err != nil {
		return err
	}
	if journal.Previous != nil {
		if err := groups.WriteJSON(groups.ActivePath(s.backupDir, s.group), journal.Previous); err != nil {
			return err
		}
	} else if err := removeGroupFile(groups.ActivePath(s.backupDir, s.group)); err != nil {
		return err
	}
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return err
	}
	for slot, owner := range registry.Claims {
		if owner.Scope == string(s.group) {
			delete(registry.Claims, slot)
		}
	}
	if journal.Previous != nil {
		registry.Claims[journal.Previous.Number] = groups.Owner{Scope: string(s.group), ProfileDir: s.ProfileDir(), Account: *journal.Previous}
	}
	if journal.SourceOwner != nil {
		registry.Claims[journal.Target.Number] = *journal.SourceOwner
	}
	if err := registry.Save(s.backupDir); err != nil {
		return err
	}
	return removeGroupFile(groups.JournalPath(s.backupDir, s.group))
}

func (s *Store) ReconcileGroup() error {
	if s.group == "" {
		return nil
	}
	journal, err := groups.LoadJournal(s.backupDir, s.group)
	if err != nil || journal == nil {
		return err
	}
	value, _, err := s.Creds.ReadActive()
	if err != nil {
		return err
	}
	email, org, ok := s.GetCurrentAccount()
	if ok && email == journal.Target.Email && org == journal.Target.OrgUUID && fingerprintOf(value) == journal.TargetFingerprint {
		return s.CompleteGroupSwitch(journal)
	}
	oldFingerprint := fingerprintOf(journal.Credential.Text)
	knownCredential := fingerprintOf(value) == oldFingerprint || fingerprintOf(value) == journal.TargetFingerprint
	knownIdentity := !ok && !journal.Config.Exists || ok && email == journal.Target.Email && org == journal.Target.OrgUUID
	if journal.Previous != nil {
		knownIdentity = knownIdentity || ok && email == journal.Previous.Email && org == journal.Previous.OrgUUID
	}
	if knownCredential && knownIdentity {
		return s.RollbackGroupSwitch(journal)
	}
	return fmt.Errorf("unfinished %s switch has unverified profile credentials or identity; both account claims remain held", s.group)
}

func (s *Store) checkJournal(journal *groups.Journal) error {
	if journal == nil || journal.Group != s.group || s.group == "" {
		return fmt.Errorf("invalid session-group transaction")
	}
	return nil
}

func restoreGroupFile(path string, snapshot groups.Snapshot) error {
	if !snapshot.Exists {
		return removeGroupFile(path)
	}
	if err := groups.SafePath(path); err != nil {
		return err
	}
	return atomicfile.Write(path, []byte(snapshot.Text), atomicfile.Opts{})
}

func removeGroupFile(path string) error {
	if err := groups.SafePath(path); err != nil {
		return err
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err == nil {
		atomicfile.SyncDir(filepath.Dir(path))
	}
	return err
}

func fingerprintOf(value string) string {
	if fingerprint := oauth.CredentialFingerprint(value); fingerprint != nil {
		return *fingerprint
	}
	return ""
}
