package autoswitch

import (
	"context"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/usage"
)

// Switcher is FROZEN (Amendment A13): *core.Switcher implements it and cli carries the compile assertion.
type Switcher interface {
	CurrentAccountNumber() *string
	HasLiveLogin() bool
	AccountEmail(num string) string
	SwitchableAccountNumbers() []string
	AccountKindFor(num string) string
	AccountIdentity(num string) map[string]string
	// ReadAccountCredentials returns the slot's stored backup credentials JSON,
	// or "" when absent (05§12).
	ReadAccountCredentials(num, email string) string
	// PersistBackupCredentials writes a rotated credential to the slot's backup
	// store under the store lock (05§12).
	PersistBackupCredentials(num, email, creds string) error
	// RefreshBackupGuarded refreshes and persists under the store lock; a lineage that moved on is returned unrefreshed (DESIGN A25 item 4).
	RefreshBackupGuarded(ctx context.Context, c oauth.Client, num, email, held string) oauth.RefreshOutcome
	BackfillAccountUUID(num, uuid string)
	UsageEntriesByAccount(fetch map[string]bool) map[string]usage.UsageEntry
	SwitchTo(num string, jsonOut bool) (map[string]any, error)
	LiveSessionPidsFor(num, email string) []int
	SetPollPolicyInputs(threshold float64, models []string)
	ClearPollPolicyInputs()
	BackupDir() string
}
