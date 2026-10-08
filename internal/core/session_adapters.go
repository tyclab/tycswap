package core

import (
	"encoding/json"

	"github.com/tyclab/tycswap/internal/mappings"
	"github.com/tyclab/tycswap/internal/platform"
)

// ReadAccountConfig reads a slot's backup config text via *store.Store and
// parses it as a JSON object, mirroring Python's `json.loads(text) if text
// else {}` with a JSONDecodeError also collapsing to {} (session.Accounts,
// DESIGN A2 / WP9 note). This method SHADOWS the promoted
// store.Store.ReadAccountConfig(num, email) (string, error) — no other frozen
// interface needs the raw-text form from *core.Switcher directly.
func (sw *Switcher) ReadAccountConfig(num, email string) (map[string]any, error) {
	text, err := sw.Store.ReadAccountConfig(num, email)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(text), &m) != nil {
		return map[string]any{}, nil
	}
	return m, nil
}

// LiveCredentials returns the live default login's credential, "" when absent
// or unreadable (session.Accounts).
func (sw *Switcher) LiveCredentials() string {
	creds, _, _ := sw.Store.Creds.ReadActive()
	return creds
}

// store.Store.Platform is a field, so this method shadows it for session.Accounts.
func (sw *Switcher) Platform() platform.Platform { return sw.Store.Platform }

func (sw *Switcher) SlotForDirectory(dir string) (slot *string, email *string, err error) {
	_, entry, ok := mappings.New(sw.Store.BackupDir()).Resolve(dir)
	if !ok {
		return nil, nil, nil
	}
	e := entry.Email
	data, err := sw.Store.SequenceMigrated()
	if err != nil {
		return nil, nil, err
	}
	num := sw.Store.FindAccountSlot(data, entry.Email, entry.OrganizationUUID)
	if num == "" {
		return nil, &e, nil
	}
	return &num, &e, nil
}
