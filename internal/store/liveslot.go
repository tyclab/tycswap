package store

import (
	"context"
	"strings"
	"sync"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/oauth"
)

// LiveSlot is the managed slot of the live default login. ConfigSlot is the
// slot ~/.claude.json names; Slot differs from it only when ByLineage. Source
// names the evidence that settled Slot.
type LiveSlot struct {
	Slot       string
	ConfigSlot string
	ByLineage  bool
	Source     string
}

// LiveSlot.Source values, in resolution order (DESIGN A68).
const (
	SourceBackup  = "backup"
	SourceChain   = "chain"
	SourceProfile = "profile"
	SourceConfig  = "config"
)

// lineageMemo is shared by a Store and its scoped clones, so one process warns
// once per pair and asks the profile endpoint once per live fingerprint.
type lineageMemo struct {
	mu     sync.Mutex
	warned map[[2]string]bool
	asked  map[string]profileAnswer
}

// profileAnswer is one question to the profile endpoint: claimed, then done
// with its identity (nil for a miss).
type profileAnswer struct {
	id   *oauth.Identity
	done bool
}

// WithoutLineageWrites is the store a dry run or a store-only read uses: it
// resolves as s does but never writes the chain record, and collect never
// harvests through it.
func (s *Store) WithoutLineageWrites() *Store {
	clone := *s
	clone.lineageReadOnly = true
	if clone.group != "" {
		clone.Creds = &profileCredentials{Store: clone.sharedCreds, owner: &clone}
	}
	return &clone
}

// LineageWrites reports whether s may write the chain record and harvest.
func (s *Store) LineageWrites() bool { return !s.lineageReadOnly }

// ResolveLiveSlot maps the live login to a slot. A running Claude Code
// rewrites ~/.claude.json from memory and refreshes its token itself, so when
// the config names a managed OAuth slot the credential decides: a backup
// holding its lineage, then the chain record, then the profile endpoint. An
// unmanaged config identity stays unresolved whatever the credential is.
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
	if ls.ConfigSlot == "" {
		return ls
	}
	ls.Source = SourceConfig
	if kindFrom(data, ls.ConfigSlot) != "oauth" {
		return ls
	}
	live := readLive()
	if live == "" {
		return ls
	}
	if y, ok := s.lineageSlot(data, ls.ConfigSlot, live); ok {
		ls.Slot, ls.Source = y, SourceBackup
	} else if y := s.chainSlot(data, ls.ConfigSlot, live); y != "" {
		ls.Slot, ls.Source = y, SourceChain
	} else if y := s.profileSlot(data, ls.ConfigSlot, live); y != "" {
		ls.Slot, ls.Source = y, SourceProfile
	}
	if ls.Slot != ls.ConfigSlot {
		ls.ByLineage = true
		s.warnLineage(ls, email, orgUUID)
	}
	return ls
}

// lineageSlot is the managed OAuth slot whose backup holds the live
// credential's lineage: the config slot's own, else exactly one other's. A
// copied backup matching twice is no evidence, so ok is false then.
func (s *Store) lineageSlot(data *SequenceData, configSlot, live string) (string, bool) {
	if s.holdsLineage(data, configSlot, live) {
		return configSlot, true
	}
	match := ""
	for _, num := range sortedSlotKeys(data) {
		if num == configSlot || kindFrom(data, num) != "oauth" || !s.holdsLineage(data, num, live) {
			continue
		}
		if match != "" {
			return "", false
		}
		match = num
	}
	return match, match != ""
}

func (s *Store) holdsLineage(data *SequenceData, num, live string) bool {
	rec, _ := recordFor(data, num)
	backup, err := s.ReadAccountCredentials(num, strField(rec, "email"))
	if err != nil || backup == "" {
		return false
	}
	return fingerprintsEqual(backup, live) || oauth.SameAccountBlock(live, backup)
}

// chainSlot is the slot the chain record names for this very credential,
// while the roster still maps the recorded identity to it.
func (s *Store) chainSlot(data *SequenceData, configSlot, live string) string {
	fp := oauth.CredentialFingerprint(live)
	rec, ok := s.readLiveLineage()
	if fp == nil || !ok || rec.Fingerprint != *fp {
		return ""
	}
	id := rec.Identity.oauth()
	if slot := s.identitySlot(data, configSlot, &id); slot == rec.Slot {
		return slot
	}
	return ""
}

// profileSlot asks the profile endpoint whose credential is live, once per
// fingerprint per process and never while this process holds the store lock,
// and records a hit in the chain record.
func (s *Store) profileSlot(data *SequenceData, configSlot, live string) string {
	fp := oauth.CredentialFingerprint(live)
	token := oauth.ExtractAccessToken(live)
	if fp == nil || token == "" {
		return ""
	}
	id, known := s.lineage.answer(*fp)
	if !known {
		if s.OAuth == nil || (s.Lock != nil && s.Lock.Held()) || !s.lineage.claim(*fp) {
			return ""
		}
		id = s.OAuth.Profile(context.Background(), token)
		s.lineage.remember(*fp, id)
	}
	if id == nil {
		return ""
	}
	slot := s.identitySlot(data, configSlot, id)
	if slot != "" && s.LineageWrites() {
		if err := s.writeLiveLineage(*fp, slot, id); err != nil && s.Log != nil {
			s.Log.Debugf("Could not record the live credential's lineage: %v", err)
		}
	}
	return slot
}

// identitySlot is the managed OAuth slot an endpoint identity names, or "".
// Another slot than the config's needs the roster's uuid to agree, as
// classifyOutgoing's cross-slot rule does.
func (s *Store) identitySlot(data *SequenceData, configSlot string, id *oauth.Identity) string {
	slot := s.SlotForIdentity(data, id)
	if slot == "" || kindFrom(data, slot) != "oauth" {
		return ""
	}
	if slot != configSlot {
		rec, _ := recordFor(data, slot)
		if stored := strings.TrimSpace(strField(rec, "uuid")); stored == "" || stored != strings.TrimSpace(id.UUID) {
			return ""
		}
	}
	return slot
}

func (m *lineageMemo) answer(fp string) (*oauth.Identity, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.asked[fp]
	return a.id, a.done
}

// claim reserves the one question for fp; false when it is already asked.
func (m *lineageMemo) claim(fp string) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, asked := m.asked[fp]; asked {
		return false
	}
	if m.asked == nil {
		m.asked = map[string]profileAnswer{}
	}
	m.asked[fp] = profileAnswer{}
	return true
}

func (m *lineageMemo) remember(fp string, id *oauth.Identity) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked[fp] = profileAnswer{id: id, done: true}
}

func (s *Store) warnLineage(ls LiveSlot, email, orgUUID string) {
	if s.Log == nil {
		return
	}
	if m := s.lineage; m != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		pair := [2]string{ls.ConfigSlot, ls.Slot}
		if m.warned[pair] {
			return
		}
		if m.warned == nil {
			m.warned = map[[2]string]bool{}
		}
		m.warned[pair] = true
	}
	s.Log.Warningf("live config names slot %s (%s, %s) but the live credential is slot %s's (by %s); using %s. A running Claude Code may have rewritten ~/.claude.json",
		ls.ConfigSlot, email, orgUUID, ls.Slot, ls.Source, ls.Slot)
}
