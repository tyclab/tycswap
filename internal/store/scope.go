package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

func (s *Store) ForGroup(id groups.ID) (*Store, error) {
	id, err := groups.Parse(string(id))
	if err != nil {
		return nil, err
	}
	clone := *s
	clone.group = id
	clone.sharedCreds = s.sharedCreds
	if clone.sharedCreds == nil {
		clone.sharedCreds = s.Creds
	}
	clone.Creds = &profileCredentials{Store: clone.sharedCreds, owner: &clone}
	return &clone, nil
}

func (s *Store) GroupID() groups.ID         { return s.group }
func (s *Store) GroupIntent() groups.Intent { return s.groupIntent }
func (s *Store) GroupHandoff() bool         { return s.groupHandoff }
func (s *Store) WithGroupHandoff() *Store {
	clone := *s
	clone.groupHandoff = true
	if clone.group != "" {
		clone.Creds = &profileCredentials{Store: clone.sharedCreds, owner: &clone}
	}
	return &clone
}
func (s *Store) WithGroupIntent(intent groups.Intent) *Store {
	clone := *s
	clone.groupIntent = intent
	if clone.group != "" {
		clone.Creds = &profileCredentials{Store: clone.sharedCreds, owner: &clone}
	}
	return &clone
}
func (s *Store) SharedRoot() string { return s.backupDir }

func (s *Store) ScopeRoot() string {
	if s.group == "" {
		return s.backupDir
	}
	return groups.ScopeDir(s.backupDir, s.group)
}

func (s *Store) StatePath() string {
	return filepath.Join(s.ScopeRoot(), "autoswitch_state.json")
}

func (s *Store) ProfileDir() string {
	if s.group == "" {
		return s.DefaultProfileDir()
	}
	return groups.ProfileDir(s.backupDir, s.group)
}

func (s *Store) GlobalConfigPath() string {
	if s.group == "" {
		return s.DefaultConfigPath()
	}
	return filepath.Join(s.ProfileDir(), ".claude.json")
}

func (s *Store) SettingsPath() string { return filepath.Join(s.ProfileDir(), "settings.json") }

type profileCredentials struct {
	credstore.Store
	owner *Store
}

func (p *profileCredentials) ReadActive() (string, bool, error) {
	s := p.owner
	path := filepath.Join(s.ProfileDir(), sessprofile.CredentialsFileName)
	if err := groups.SafePath(path); err != nil {
		return "", false, err
	}
	if s.Platform == platform.MacOS {
		value, found, err := s.kc.Get(sessprofile.KeychainServiceName(s.ProfileDir()), keychain.AccountName())
		if err != nil {
			return "", true, err
		}
		if found && value != "" {
			return value, false, nil
		}
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	return string(data), false, err
}

func (p *profileCredentials) WriteActive(value string) error {
	s := p.owner
	if err := groups.SafePath(filepath.Join(s.ProfileDir(), sessprofile.CredentialsFileName)); err != nil {
		return err
	}
	if credstore.LooksLikeAPIKey(value) {
		return fmt.Errorf("managed session groups require OAuth accounts")
	}
	if s.Platform == platform.MacOS {
		if err := s.kc.Set(sessprofile.KeychainServiceName(s.ProfileDir()), keychain.AccountName(), value); err != nil {
			return err
		}
	}
	return atomicfile.Write(filepath.Join(s.ProfileDir(), sessprofile.CredentialsFileName), []byte(value), atomicfile.Opts{})
}

func (p *profileCredentials) WriteActiveAccount(value string) error {
	live, _, err := p.ReadActive()
	if err != nil {
		return err
	}
	merged, err := ccfile.SpliceCredentials(value, live)
	if err != nil {
		return err
	}
	return p.WriteActive(merged)
}

func (p *profileCredentials) ReadLiveOAuth() string {
	value, _, _ := p.ReadActive()
	return value
}

func (p *profileCredentials) ClearActive() error {
	s := p.owner
	path := filepath.Join(s.ProfileDir(), sessprofile.CredentialsFileName)
	if err := groups.SafePath(path); err != nil {
		return err
	}
	if s.Platform == platform.MacOS {
		if err := s.kc.Delete(sessprofile.KeychainServiceName(s.ProfileDir()), keychain.AccountName()); err != nil {
			return err
		}
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (p *profileCredentials) LastActiveBackend() string {
	if p.owner.Platform == platform.MacOS {
		return "keychain"
	}
	return "file"
}
