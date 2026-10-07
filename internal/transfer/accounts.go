package transfer

import "github.com/tyclab/tycswap/internal/platform"

type Accounts interface {
	MigratedSequence() (*SequenceData, error)
	MigratedSequenceForUpdate() (*SequenceData, error)
	Sequence() (*SequenceData, error)
	WriteSequence(data *SequenceData) error

	ResolveSlot(id string) (num string, err error)

	CurrentAccount() (email, orgUUID string, ok bool)
	ReadActiveCredentials() (string, error)
	ReadActiveConfig() (text string, found bool, err error)

	ReadAccountCredentials(num, email string) (string, error)
	ReadAccountConfig(num, email string) (string, error)
	WriteAccountCredentials(num, email, creds string) error
	WriteAccountConfig(num, email, config string) error

	LiveSessionPidsFor(num, email string) []int
	TokenDead(num, email, orgUUID string) bool
	ClearDeadToken(num, email, orgUUID string) error

	// SetupDirectories creates the backup/configs/credentials dirs (0700 on
	// non-Windows) (== _setup_directories). → store.SetupDirectories.
	SetupDirectories() error
	InitSequenceFile() error

	// Timestamp is get_timestamp(): the current wall time in UTC, seconds
	// precision, Z-suffixed. → an adapter over the store's clock.
	Timestamp() string
	Platform() platform.Platform
	// BackupDir is the tycswap backup root; the import write-pass FileLock lives at
	// <BackupDir>/.lock (DESIGN Deviation 9). → store.BackupDir.
	BackupDir() string
}
