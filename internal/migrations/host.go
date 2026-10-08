// Package migrations holds one-time, self-guarded migrations that rescue legacy backup credentials, tracked in .migrations.json.
package migrations

import (
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/wincred"
)

const StateFilename = ".migrations.json"

// Host lives here, not in store, to break the store→migrations→store cycle (DESIGN A3).
type Host interface {
	// Run never touches disk when BackupDir is absent: a fresh install must not materialize it.
	BackupDir() string
	CredentialsDir() string
	StateFilePath() string
	Platform() platform.Platform
	Clock() clock.Clock
	Logger() *logging.Logger
	// The macOS migration uses the Keychain-only KC ops so a fallback .enc never counts as migrated.
	Creds() credstore.Store
	Keychain() keychain.KeychainClient
	WinCred() wincred.Client
	// ok=false (absent or corrupt) skips without marking applied, so a later repair still migrates.
	SequenceAccounts() (accounts map[string]string, ok bool)
}
