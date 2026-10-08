package transfer

import "github.com/tyclab/tycswap/internal/platform"

type Accounts interface {
	// No path that can end in WriteSequence may start here: absent and corrupt both read nil.
	MigratedSequence() (*SequenceData, error)
	// Called inside Import's write-pass FileLock, so it must not take that lock itself (non-reentrant).
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

	// SetupDirectories creates the backup/configs/credentials dirs, 0700 off Windows.
	SetupDirectories() error
	InitSequenceFile() error

	// Timestamp is get_timestamp(): UTC, seconds precision, Z-suffixed.
	Timestamp() string
	Platform() platform.Platform
	// BackupDir is the tycswap backup root; the import write-pass FileLock lives at
	// <BackupDir>/.lock (DESIGN Deviation 9). → store.BackupDir.
	BackupDir() string
}
