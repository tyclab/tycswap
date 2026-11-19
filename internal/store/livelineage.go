package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
)

// liveLineageFile is the chain-of-custody record: the profile endpoint's answer
// for one live credential, so later resolutions need neither the network nor a
// matching backup. It is tycswap's own cache, written without a lock.
const liveLineageFile = "live_lineage.json"

type liveLineage struct {
	SchemaVersion int             `json:"schemaVersion"`
	Fingerprint   string          `json:"fingerprint"`
	Slot          string          `json:"slot"`
	Identity      lineageIdentity `json:"identity"`
	SeenAt        float64         `json:"seenAt"`
}

type lineageIdentity struct {
	UUID    string `json:"uuid"`
	Email   string `json:"email"`
	OrgUUID string `json:"orgUuid"`
}

func (id lineageIdentity) oauth() oauth.Identity {
	return oauth.Identity{UUID: id.UUID, Email: id.Email, OrgUUID: id.OrgUUID}
}

// liveLineagePath is the default store's chain record, or the Fable store's own (DESIGN A100).
func (s *Store) liveLineagePath() string {
	if s.fable {
		return filepath.Join(paths.FableScopeDir(s.backupDir), liveLineageFile)
	}
	return filepath.Join(s.backupDir, liveLineageFile)
}

// readLiveLineage reads the chain record; a missing or malformed one is absent.
func (s *Store) readLiveLineage() (liveLineage, bool) {
	raw, err := os.ReadFile(s.liveLineagePath())
	if err != nil {
		return liveLineage{}, false
	}
	var rec liveLineage
	if json.Unmarshal(raw, &rec) != nil || rec.SchemaVersion != 1 || rec.Fingerprint == "" || rec.Slot == "" {
		return liveLineage{}, false
	}
	return rec, true
}

// lastInstall is the slot the chain record names, whatever fingerprint it
// carries, while the roster still maps the identity it was written for to that
// OAuth slot; "" otherwise. The default record holds a profile answer too, and
// installs only while Fable logins exist, so on the default scope the slot
// must also be the roster's active one, which every switch writes.
func (s *Store) lastInstall(data *SequenceData) string {
	rec, ok := s.readLiveLineage()
	if !ok || data == nil {
		return ""
	}
	id := rec.Identity.oauth()
	if s.oauthIdentitySlot(data, &id) != rec.Slot {
		return ""
	}
	if !s.fable && (data.ActiveAccountNumber == nil || strconv.Itoa(*data.ActiveAccountNumber) != rec.Slot) {
		return ""
	}
	return rec.Slot
}

// RecordInstall writes s's chain record for creds, installed in s's live
// store for account num, with the roster's identity of num: the store's last
// install (DESIGN A100). Only an OAuth account's install is recorded.
func (s *Store) RecordInstall(num, creds string) error {
	fp := oauth.CredentialFingerprint(creds)
	data, err := s.ReadSequence()
	rec, ok := recordFor(data, num)
	if fp == nil || err != nil || !ok || kindFrom(data, num) != "oauth" {
		return err
	}
	id := oauth.Identity{UUID: strField(rec, "uuid"), Email: strField(rec, "email"), OrgUUID: strField(rec, "organizationUuid")}
	return s.writeLiveLineage(*fp, num, &id)
}

// forgetLiveLineage removes the chain record: the store holds no login.
func (s *Store) forgetLiveLineage() error {
	if err := os.Remove(s.liveLineagePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// writeLiveLineage records that the credential with fingerprint fp is slot's,
// writing only when the record changes, so seenAt is when it was first seen.
func (s *Store) writeLiveLineage(fp, slot string, id *oauth.Identity) error {
	rec := liveLineage{SchemaVersion: 1, Fingerprint: fp, Slot: slot,
		Identity: lineageIdentity{UUID: id.UUID, Email: id.Email, OrgUUID: id.OrgUUID}}
	if old, ok := s.readLiveLineage(); ok && old.Fingerprint == fp && old.Slot == slot && old.Identity == rec.Identity {
		return nil
	}
	rec.SeenAt = clock.Seconds(s.Clk)
	return atomicfile.WriteJSON(s.liveLineagePath(), rec, atomicfile.Opts{FileMode: 0o600})
}
