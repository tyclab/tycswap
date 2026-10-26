package store

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/oauth"
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

func (s *Store) liveLineagePath() string { return filepath.Join(s.backupDir, liveLineageFile) }

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
