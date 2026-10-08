// Package wincred reads and deletes legacy claude-swap backups in Windows Credential Manager for the one-time migration (A9).
package wincred

import "sync"

// Client is the read/delete seam a migration needs against the legacy
// per-account Windows Credential Manager entries. Get mirrors
// keychain.KeychainClient.Get's (value, found, err) shape so migrations.go can
// treat the macOS (keychain.KeychainClient) and Windows (wincred.Client) legacy
// backends uniformly.
type Client interface {
	Get(service, account string) (value string, found bool, err error)
	Delete(service, account string) error
}

type Fake struct {
	mu sync.Mutex
	m  map[[2]string]string
}

func NewFake() *Fake { return &Fake{m: make(map[[2]string]string)} }

func fakeKey(service, account string) [2]string { return [2]string{service, account} }

// Set seeds an entry (test helper — Real never writes; only Set to arrange
// fixtures).
func (f *Fake) Set(service, account, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = make(map[[2]string]string)
	}
	f.m[fakeKey(service, account)] = value
}

func (f *Fake) Get(service, account string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[fakeKey(service, account)]
	return v, ok, nil
}

func (f *Fake) Delete(service, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, fakeKey(service, account))
	return nil
}

var _ Client = (*Fake)(nil)
