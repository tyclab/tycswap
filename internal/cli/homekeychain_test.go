package cli

import (
	"os"
	"sync"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/store"
)

// homeKeychains stands in for the login Keychain of each test's home. The
// cli builds the real client, keychain.Security, which on macOS runs
// /usr/bin/security against the login Keychain of whoever runs the tests.
// The tests get a keychain.Fake instead, one per home, so an item one command
// stores is there for the next command of the same test, as in a real
// Keychain, and no item outlives the test's home.
var homeKeychains = struct {
	sync.Mutex
	byHome map[string]*keychain.Fake
}{byHome: map[string]*keychain.Fake{}}

// homeKeychain returns the fake login Keychain of the current home.
func homeKeychain() *keychain.Fake {
	home, _ := os.UserHomeDir()
	homeKeychains.Lock()
	defer homeKeychains.Unlock()
	kc, ok := homeKeychains.byHome[home]
	if !ok {
		kc = keychain.NewFake()
		homeKeychains.byHome[home] = kc
	}
	return kc
}

// useHomeKeychains puts homeKeychain where the cli builds a real Keychain
// client: the switcher's (constructSwitcher) and the scratch login's
// (loginKeychain, which has none off macOS and still has none). TestMain
// calls it once; a test that replaces either seam restores this one after.
func useHomeKeychains() {
	buildSwitcher, buildLoginKeychain := newSwitcher, loginKeychain
	newSwitcher = func(opts store.Options) (*core.Switcher, error) {
		if _, isReal := opts.Keychain.(keychain.Security); isReal || opts.Keychain == nil {
			opts.Keychain = homeKeychain()
		}
		return buildSwitcher(opts)
	}
	loginKeychain = func() keychain.KeychainClient {
		if buildLoginKeychain() == nil {
			return nil
		}
		return homeKeychain()
	}
}
