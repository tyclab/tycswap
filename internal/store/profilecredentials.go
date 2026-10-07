package store

import (
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

func (s *Store) ReadProfileCredentials(dir string) (string, bool, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}
	if s.Platform == platform.MacOS {
		value, found, err := s.kc.Get(sessprofile.KeychainServiceName(dir), keychain.AccountName())
		if err != nil {
			return "", false, err
		}
		if found && value != "" {
			return value, true, nil
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, sessprofile.CredentialsFileName))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !utf8.Valid(data) {
		return "", false, fmt.Errorf("profile credentials are not valid UTF-8: %s", dir)
	}
	return string(data), true, nil
}
