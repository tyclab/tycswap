// Package authfile reads and writes the codex CLI's live auth.json, derives its account identity and resolves every Codex path.
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

// $CODEX_HOME is resolved as codex does, so both agree on the live file; tycswap writes only auth.json there.
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

// Under the backup root so purge, backup-root migration and test isolation cover Codex data too.
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
