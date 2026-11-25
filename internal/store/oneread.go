package store

import (
	"path/filepath"
	"slices"
	"sync"

	"github.com/tyclab/tycswap/internal/ccfile"
)

// readMemo is what one read operation has read of the two inputs ownership
// costs most to read: each config file's oauthAccount identity, by path, and
// each profile's live Claude PIDs, by directory (DESIGN A116).
type readMemo struct {
	mu         sync.Mutex
	identities map[string]identityRead
	pids       map[string]pidScan
}

type identityRead struct {
	email, org string
	ok         bool
}

type pidScan struct {
	pids []int
	err  error
}

func newReadMemo() *readMemo {
	return &readMemo{identities: map[string]identityRead{}, pids: map[string]pidScan{}}
}

// OneRead is the store one read operation uses (DESIGN A116): while it is
// used, each identity file is read and each profile's live PIDs scanned once,
// and asked again the first answer is given. Everything else is read afresh
// on every question, and the answers die with the copy. A write never runs on
// it: every guard of a write reads through Fresh. OneRead of a OneRead store
// shares its reads.
func (s *Store) OneRead() *Store {
	if s.memo != nil {
		return s
	}
	return s.withMemo(newReadMemo())
}

// Fresh is s reading every identity file and profile afresh, what a decision
// that guards a write reads through.
func (s *Store) Fresh() *Store {
	if s.memo == nil {
		return s
	}
	return s.withMemo(nil)
}

func (s *Store) withMemo(m *readMemo) *Store {
	clone := *s
	clone.memo = m
	if clone.group != "" {
		clone.Creds = &profileCredentials{Store: clone.sharedCreds, owner: &clone}
	}
	return &clone
}

// configIdentity is ccfile.ReadOAuthIdentityFrom, once per path in one read.
func (s *Store) configIdentity(path string) (email, org string, ok bool) {
	m := s.memo
	if m == nil {
		return ccfile.ReadOAuthIdentityFrom(path)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, done := m.identities[path]
	if !done {
		r.email, r.org, r.ok = ccfile.ReadOAuthIdentityFrom(path)
		m.identities[path] = r
	}
	return r.email, r.org, r.ok
}

// profileDirIdentity is ccfile.ReadProfileIdentity, once per profile in one read.
func (s *Store) profileDirIdentity(dir string) (email, org string, ok bool) {
	return s.configIdentity(filepath.Join(dir, ".claude.json"))
}

// livePIDs is profilePIDs, once per profile in one read.
func (s *Store) livePIDs(dir string) ([]int, error) {
	m := s.memo
	if m == nil {
		return profilePIDs(dir)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, done := m.pids[dir]
	if !done {
		r.pids, r.err = profilePIDs(dir)
		m.pids[dir] = r
	}
	return slices.Clone(r.pids), r.err
}
