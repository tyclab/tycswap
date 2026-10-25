package store

import (
	"sync"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/oauth"
)

// LiveSlot is the managed slot of the live default login. ConfigSlot is the
// slot ~/.claude.json names; Slot differs from it only when ByLineage.
type LiveSlot struct {
	Slot       string
	ConfigSlot string
	ByLineage  bool
}

// lineageWarned is shared by a Store and its scoped clones, so one process warns once per pair.
type lineageWarned struct {
	mu   sync.Mutex
	seen map[[2]string]bool
}

// ResolveLiveSlot maps the live login to a slot. A running Claude Code
// rewrites ~/.claude.json from memory, so when the config names one managed
// OAuth slot and the credential file holds exactly one other managed OAuth
// slot's lineage, the credential wins. An unmanaged config identity stays
// unresolved whatever the credential is.
func (s *Store) ResolveLiveSlot(data *SequenceData) LiveSlot {
	email, orgUUID, ok := s.GetCurrentAccount()
	if !ok {
		return LiveSlot{}
	}
	return s.liveSlotFor(data, email, orgUUID, func() string {
		live, _, err := s.Creds.ReadActive()
		if err != nil {
			return ""
		}
		return live
	})
}

// defaultLiveSlot is ResolveLiveSlot for the default login, whatever the
// store's scope, given the default profile's credential.
func (s *Store) defaultLiveSlot(data *SequenceData, creds string) LiveSlot {
	email, orgUUID, ok := ccfile.ReadOAuthIdentityFrom(s.DefaultConfigPath())
	if !ok {
		return LiveSlot{}
	}
	return s.liveSlotFor(data, email, orgUUID, func() string { return creds })
}

// liveSlotFor resolves a config identity; readLive is called only once the
// config names a managed OAuth slot.
func (s *Store) liveSlotFor(data *SequenceData, email, orgUUID string, readLive func() string) LiveSlot {
	ls := LiveSlot{ConfigSlot: s.FindAccountSlot(data, email, orgUUID)}
	ls.Slot = ls.ConfigSlot
	if ls.ConfigSlot == "" || kindFrom(data, ls.ConfigSlot) != "oauth" {
		return ls
	}
	if y := s.lineageSlot(data, ls.ConfigSlot, readLive()); y != ls.ConfigSlot {
		ls.Slot, ls.ByLineage = y, true
		s.warnLineage(ls, email, orgUUID)
	}
	return ls
}

// lineageSlot is the one managed OAuth slot other than configSlot whose backup
// holds the live credential's lineage, else configSlot: the config slot
// matching, or a copied backup matching twice, is no evidence against the config.
func (s *Store) lineageSlot(data *SequenceData, configSlot, live string) string {
	if live == "" || s.holdsLineage(data, configSlot, live) {
		return configSlot
	}
	match := configSlot
	for _, num := range sortedSlotKeys(data) {
		if num == configSlot || kindFrom(data, num) != "oauth" || !s.holdsLineage(data, num, live) {
			continue
		}
		if match != configSlot {
			return configSlot
		}
		match = num
	}
	return match
}

func (s *Store) holdsLineage(data *SequenceData, num, live string) bool {
	rec, _ := recordFor(data, num)
	backup, err := s.ReadAccountCredentials(num, strField(rec, "email"))
	if err != nil || backup == "" {
		return false
	}
	return fingerprintsEqual(backup, live) || oauth.SameAccountBlock(live, backup)
}

func (s *Store) warnLineage(ls LiveSlot, email, orgUUID string) {
	if s.Log == nil {
		return
	}
	if w := s.lineageWarned; w != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		pair := [2]string{ls.ConfigSlot, ls.Slot}
		if w.seen[pair] {
			return
		}
		if w.seen == nil {
			w.seen = map[[2]string]bool{}
		}
		w.seen[pair] = true
	}
	s.Log.Warningf("live config names slot %s (%s, %s) but the live credential is slot %s's; using %s. A running Claude Code rewrote ~/.claude.json",
		ls.ConfigSlot, email, orgUUID, ls.Slot, ls.Slot)
}
