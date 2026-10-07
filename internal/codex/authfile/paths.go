package authfile

import (
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/paths"
)

func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func Home() string {
	if env := os.Getenv("CODEX_HOME"); env != "" {
		return env
	}
	return filepath.Join(userHome(), ".codex")
}

func LiveAuthPath() string {
	return filepath.Join(Home(), "auth.json")
}

// AuthRegistryPath returns codex-auth's registry, read once during the
// one-time import and never written.
func AuthRegistryPath() string {
	return filepath.Join(Home(), "accounts", "registry.json")
}

func AuthAccountsDir() string {
	return filepath.Join(Home(), "accounts")
}

func StoreRoot() string {
	return filepath.Join(paths.GetBackupRoot(), "codex")
}

func SequencePath() string {
	return filepath.Join(StoreRoot(), "sequence.json")
}

// CredentialsDir returns the on-disk snapshot directory (non-macOS, and the
// macOS fallback when the Keychain is unavailable).
func CredentialsDir() string {
	return filepath.Join(StoreRoot(), "credentials")
}

func CacheDir() string {
	return filepath.Join(StoreRoot(), "cache")
}

// LockPath returns tycswap's Codex lock file. It is separate from the Claude
// lock on purpose: a Codex switch and a Claude switch touch disjoint files and
// must never block each other.
func LockPath() string {
	return filepath.Join(StoreRoot(), ".lock")
}
