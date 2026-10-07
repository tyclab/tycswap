package store

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
)

func (s *Store) DefaultProfileDir() string {
	if s.defaultProfileDir != "" {
		return s.defaultProfileDir
	}
	return filepath.Join(s.Home, ".claude")
}

func (s *Store) DefaultProfileUnpinned() bool { return s.defaultUnpinned }
func (s *Store) DefaultKeychainService() string {
	if s.defaultKeychainService != "" {
		return s.defaultKeychainService
	}
	return "Claude Code-credentials"
}

func validDefaultService(value string) bool {
	const prefix = "Claude Code-credentials-"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+8 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}

func (s *Store) DefaultConfigPath() string {
	legacy := filepath.Join(s.DefaultProfileDir(), ".config.json")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	if !s.DefaultProfileUnpinned() {
		return filepath.Join(s.DefaultProfileDir(), ".claude.json")
	}
	return filepath.Join(s.Home, ".claude.json")
}

func (s *Store) ReadDefaultCredentials() (string, error) {
	if s.Platform == platform.MacOS {
		value, found, err := s.kc.Get(s.DefaultKeychainService(), keychain.AccountName())
		if err != nil {
			return "", err
		}
		if found && value != "" {
			return value, nil
		}
	}
	data, err := os.ReadFile(filepath.Join(s.DefaultProfileDir(), ".credentials.json"))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

func (s *Store) writeDefaultCredentials(value string) error {
	path := filepath.Join(s.DefaultProfileDir(), ".credentials.json")
	if s.Platform == platform.MacOS {
		var err error
		if value == "" {
			err = s.kc.Delete(s.DefaultKeychainService(), keychain.AccountName())
		} else {
			err = s.kc.Set(s.DefaultKeychainService(), keychain.AccountName(), value)
		}
		if err != nil {
			return err
		}
	}
	if value == "" {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return atomicfile.Write(path, []byte(value), atomicfile.Opts{})
}

func (s *Store) snapshotDefaultHandoff(journal *groups.Journal) error {
	owner, err := s.CredentialOwner(journal.Target.Number)
	if err != nil {
		return err
	}
	if owner.Scope != "default" {
		return nil
	}
	if !s.groupHandoff || owner.Uncertain || len(owner.PIDs) > 0 {
		return fmt.Errorf("the default login still owns Account-%s; stop its sessions before opting into a group", journal.Target.Number)
	}
	value, err := s.ReadDefaultCredentials()
	if err != nil || value == "" {
		return fmt.Errorf("cannot preserve the default credential before group handoff")
	}
	config, err := snapshotFile(s.DefaultConfigPath())
	if err != nil || !config.Exists {
		return fmt.Errorf("cannot preserve the default config before group handoff")
	}
	journal.DefaultHandoff = true
	journal.DefaultCredential = groups.Snapshot{Exists: true, Text: value}
	journal.DefaultConfig = config
	return nil
}

func (s *Store) ClearDefaultForGroup(journal *groups.Journal) error {
	if !journal.DefaultHandoff {
		return nil
	}
	pids, err := profilePIDs(s.DefaultProfileDir())
	if err != nil || len(pids) > 0 {
		return fmt.Errorf("default Claude sessions are running; the default-to-group handoff is held")
	}
	config, err := ccfile.ReadGlobalConfigStrict(s.DefaultConfigPath())
	if err != nil {
		return err
	}
	if config == nil {
		config = map[string]any{}
	}
	email, org, ok := ccfile.ReadOAuthIdentityFrom(s.DefaultConfigPath())
	if ok && (email != journal.Target.Email || org != journal.Target.OrgUUID) {
		return fmt.Errorf("the default login changed during group handoff; both credential claims remain held")
	}
	current, err := s.ReadDefaultCredentials()
	if err != nil {
		return err
	}
	if current != "" && !ccfile.SeatWideOnly(current) && fingerprintOf(current) != fingerprintOf(journal.DefaultCredential.Text) {
		return fmt.Errorf("the default credential changed during group handoff; both claims remain held")
	}
	remainder, _ := ccfile.SeatWidePart(journal.DefaultCredential.Text)
	if err := s.writeDefaultCredentials(remainder); err != nil {
		return err
	}
	delete(config, "oauthAccount")
	return writeDefaultConfig(s.DefaultConfigPath(), config)
}

func (s *Store) restoreDefaultHandoff(journal *groups.Journal) error {
	if !journal.DefaultHandoff {
		return nil
	}
	pids, err := profilePIDs(s.DefaultProfileDir())
	if err != nil || len(pids) > 0 {
		return fmt.Errorf("default Claude sessions are running; account claims remain held until handoff recovery can be verified")
	}
	email, org, ok := ccfile.ReadOAuthIdentityFrom(s.DefaultConfigPath())
	if ok && (email != journal.Target.Email || org != journal.Target.OrgUUID) {
		return fmt.Errorf("default identity changed; account claims remain held")
	}
	current, err := s.ReadDefaultCredentials()
	if err != nil {
		return err
	}
	if current != "" && !ccfile.SeatWideOnly(current) && fingerprintOf(current) != fingerprintOf(journal.DefaultCredential.Text) {
		return fmt.Errorf("default credential changed; account claims remain held until its current lineage is verified")
	}
	if err := s.writeDefaultCredentials(journal.DefaultCredential.Text); err != nil {
		return err
	}
	return restoreDefaultConfig(s.DefaultConfigPath(), journal.DefaultConfig)
}

func writeDefaultConfig(path string, value any) error {
	parentMode := os.FileMode(0o700)
	if info, err := os.Stat(filepath.Dir(path)); err == nil {
		parentMode = info.Mode().Perm()
	}
	return atomicfile.WriteJSON(path, value, atomicfile.Opts{DirMode: parentMode})
}

func restoreDefaultConfig(path string, snapshot groups.Snapshot) error {
	if !snapshot.Exists {
		return removeGroupFile(path)
	}
	parentMode := os.FileMode(0o700)
	if info, err := os.Stat(filepath.Dir(path)); err == nil {
		parentMode = info.Mode().Perm()
	}
	return atomicfile.Write(path, []byte(snapshot.Text), atomicfile.Opts{DirMode: parentMode})
}
