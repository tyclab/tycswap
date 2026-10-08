// Package session implements `tycswap run`: Claude Code as a stored account in a persistent profile under <backup_dir>/sessions/.
package session

import "github.com/tyclab/tycswap/internal/platform"

type Accounts interface {
	ResolveAccount(id string) (num, email, org string, err error)
	ReadAccountCredentials(num, email string) (string, error)
	WriteAccountCredentials(num, email, creds string) error
	ReadAccountConfig(num, email string) (map[string]any, error)
	AccountKindFor(num string) string
	CurrentAccountNumber() *string
	// LiveCredentials is the live default login's credential ("" when none or unreadable), for lineage comparison.
	LiveCredentials() string
	// BackupDir is the tycswap backup root; the FileLock lives at <BackupDir>/.lock.
	BackupDir() string
	Platform() platform.Platform
	SlotForDirectory(dir string) (slot *string, email *string, err error)
}
