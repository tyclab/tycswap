// Implements spec 03§4 conftest block_real_keychain parity: an in-memory
// (service, account) → secret map. Delete on an absent key is a no-op (rc 44
// parity); Get on an absent key reports not-found.
//
// The Fake enforces the two limits the real wrapper enforces, so a caller
// that would fail against `security` fails against the Fake too: every call
// refuses a name ValidateName refuses, and Set refuses a payload that does
// not fit `security -i`'s stdin line (FitsStdin) with the same TooLarge
// KeychainError.

package keychain

import (
	"fmt"
	"sync"
)

type Fake struct {
	mu sync.Mutex
	m  map[[2]string]string
}

func NewFake() *Fake {
	return &Fake{m: make(map[[2]string]string)}
}

func key(service, account string) [2]string { return [2]string{service, account} }

func (f *Fake) Get(service, account string) (string, bool, error) {
	if err := ValidateName(service, account); err != nil {
		return "", false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[key(service, account)]
	return v, ok, nil
}

// Set stores the value, under the real wrapper's name and size rules.
func (f *Fake) Set(service, account, password string) error {
	if err := ValidateName(service, account); err != nil {
		return err
	}
	if !FitsStdin(service, account, password) {
		return &KeychainError{
			Msg: fmt.Sprintf("secret for %s/%s is too large for the Keychain's stdin interface (%d > %d bytes); "+
				"refusing to pass it on the command line", service, account, len(setCommand(service, account, password)), SecurityStdinLineLimit),
			TooLarge: true,
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = make(map[[2]string]string)
	}
	f.m[key(service, account)] = password
	return nil
}

// Delete removes the item; absent keys are a no-op (rc 44 parity).
func (f *Fake) Delete(service, account string) error {
	if err := ValidateName(service, account); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, key(service, account))
	return nil
}

// Exists reports whether the item is present; a name the wrapper refuses is
// never present.
func (f *Fake) Exists(service, account string) bool {
	if ValidateName(service, account) != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.m[key(service, account)]
	return ok
}

// Seed stores an item without Set's limits, as another tool may have written
// it (an older wrapper passed the secret through argv, which has no line
// limit). It is for tests that need such an item to exist; tycswap itself
// never writes one.
func (f *Fake) Seed(service, account, password string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = make(map[[2]string]string)
	}
	f.m[key(service, account)] = password
}

var _ KeychainClient = (*Fake)(nil)
