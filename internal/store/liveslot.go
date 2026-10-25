package store

import (
	"sync"

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
	last [2]string
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
	ls := LiveSlot{ConfigSlot: s.FindAccountSlot(data, email, orgUUID)}
	ls.Slot = ls.ConfigSlot
	if ls.ConfigSlot == "" || kindFrom(data, ls.ConfigSlot) != "oauth" {
		return ls
	}
	if y := s.lineageSlot(data, ls.ConfigSlot); y != ls.ConfigSlot {
		ls.Slot, ls.ByLineage = y, true
		s.warnLineage(ls, email, orgUUID)
	}
	return ls
}

// lineageSlot is the one managed OAuth slot other than configSlot whose backup
// holds the live credential's lineage, else configSlot: the config slot
// matching, or a copied backup matching twice, is no evidence against the config.
func (s *Store) lineageSlot(data *SequenceData, configSlot string) string {
	live, _, err := s.Creds.ReadActive()
	if err != nil || live == "" || s.holdsLineage(data, configSlot, live) {
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
	return *oauth.CredentialFingerprint(backup) == *oauth.CredentialFingerprint(live) || oauth.SameAccountBlock(live, backup)
}

func (s *Store) warnLineage(ls LiveSlot, email, orgUUID string) {
	if s.Log == nil {
		return
	}
	if w := s.lineageWarned; w != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		pair := [2]string{ls.ConfigSlot, ls.Slot}
		if w.last == pair {
			return
		}
		w.last = pair
	}
	s.Log.Warningf("live config names slot %s (%s, %s) but the live credential is slot %s's; using %s. A running Claude Code rewrote ~/.claude.json",
		ls.ConfigSlot, email, orgUUID, ls.Slot, ls.Slot)
}
