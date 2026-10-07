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

type Host interface {
	BackupDir() string
	CredentialsDir() string
	StateFilePath() string
	Platform() platform.Platform
	// Clock supplies .migrations.json's "applied" timestamp (get_timestamp
	// parity, spec 07§5.1): clock.System in production, clock.Fake in tests.
	Clock() clock.Clock
	// Logger receives every warning a migration logs. Run never raises
	// through this (spec 07§5.5) — every failure is logged here instead.
	Logger() *logging.Logger
	Creds() credstore.Store
	Keychain() keychain.KeychainClient
	WinCred() wincred.Client
	SequenceAccounts() (accounts map[string]string, ok bool)
}
