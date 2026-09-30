// paths.go — path resolution for the codex CLI's config and for cswap's own
// Codex store. Implements claude-swap PR #252 codex/paths.py.
//
// Two distinct roots, deliberately kept apart. ~/.codex (or $CODEX_HOME) is the
// codex CLI's directory: cswap reads and writes exactly one file in it,
// auth.json, and reads codex-auth's accounts/registry.json once at import time.
// Nothing else in there is ours. <cswap backup root>/codex/ is cswap's own
// store, a sibling of the existing Claude configs/ and credentials/; keeping it
// under the same root means the existing purge, backup-root migration and the
// tests' real-store isolation cover Codex data for free.
//
// CODEX_HOME mirrors the codex CLI's own env var; resolving it the same way is
// what makes cswap and codex agree on which file is live.

// Package authfile reads and writes the codex CLI's live auth.json, derives the
// account identity it belongs to, and resolves every Codex path cswap uses.
package authfile

import (
	"os"
	"path/filepath"

	"git.dpemmons.com/dpemmons/cswap/internal/paths"
)

// userHome returns the user's home directory, or "" when it cannot be
// resolved (the same fallback internal/paths uses).
func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// Home returns the Codex config home: $CODEX_HOME when set and non-empty,
// else ~/.codex.
func Home() string {
	if env := os.Getenv("CODEX_HOME"); env != "" {
		return env
	}
	return filepath.Join(userHome(), ".codex")
}

// LiveAuthPath returns the live auth.json the codex CLI reads and writes.
func LiveAuthPath() string {
	return filepath.Join(Home(), "auth.json")
}

// AuthRegistryPath returns codex-auth's registry, read once during the
// one-time import and never written.
func AuthRegistryPath() string {
	return filepath.Join(Home(), "accounts", "registry.json")
}

// AuthAccountsDir returns codex-auth's snapshot directory (read-only for cswap).
func AuthAccountsDir() string {
	return filepath.Join(Home(), "accounts")
}

// StoreRoot returns cswap's own Codex store root, under the cswap backup root.
func StoreRoot() string {
	return filepath.Join(paths.GetBackupRoot(), "codex")
}

// SequencePath returns the slot registry (slot -> account key and metadata).
func SequencePath() string {
	return filepath.Join(StoreRoot(), "sequence.json")
}

// CredentialsDir returns the on-disk snapshot directory (non-macOS, and the
// macOS fallback when the Keychain is unavailable).
func CredentialsDir() string {
	return filepath.Join(StoreRoot(), "credentials")
}

// CacheDir returns the usage-cache directory.
func CacheDir() string {
	return filepath.Join(StoreRoot(), "cache")
}

// LockPath returns cswap's Codex lock file. It is separate from the Claude
// lock on purpose: a Codex switch and a Claude switch touch disjoint files and
// must never block each other.
func LockPath() string {
	return filepath.Join(StoreRoot(), ".lock")
}
