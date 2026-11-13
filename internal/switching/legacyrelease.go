package switching

import (
	"path/filepath"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

func releaseIdleLegacy(s *store.Store, number string) error {
	var candidate *store.LegacyRelease
	if err := s.Lock.With(func() error {
		var err error
		candidate, err = s.LegacyReleaseCandidate(number)
		return err
	}); err != nil || candidate == nil {
		return err
	}
	value := candidate.Credentials
	prov := &Provenance{Live: &value}
	backup, _ := s.ReadAccountCredentials(number, candidate.Owner.Account.Email)
	if value != "" && !legacySameLineage(value, backup) {
		prov.Resolved = s.ReaskProfile(value)
	}
	return s.Lock.With(func() error {
		dir := candidate.Owner.ProfileDir
		cred, err := cclock.AcquireProfileCredentials(dir, 0, s.Clk)
		if err != nil {
			return err
		}
		defer cred.Release()
		config, err := cclock.Acquire(filepath.Join(dir, ".claude.json")+".lock", 0, s.Clk)
		if err != nil {
			return err
		}
		defer config.Release()
		storage, err := cclock.AcquireProfileStorageWrite(dir, 0, s.Clk)
		if err != nil {
			return err
		}
		defer storage.Release()
		return s.ReleaseLegacyAccount(candidate, func(live string) error {
			data, err := s.ReadSequence()
			if err != nil {
				return err
			}
			backup, _ := s.ReadAccountCredentials(number, candidate.Owner.Account.Email)
			if legacySameLineage(live, backup) {
				return nil
			}
			kind, _ := classifyOutgoing(s, number, candidate.Owner.Account.Email, live, prov, data)
			identity := candidate.Owner.Account
			if kind == "own-rotated" && prov.Resolved.OrgUUID == identity.OrgUUID &&
				(identity.UUID == "" || prov.Resolved.UUID == identity.UUID) {
				return nil
			}
			return cerr.Session("Account-%s's session credential ownership could not be verified; its login and claim remain saved. Sign in to that account with tycswap add --login --switch to renew it.", number)
		})
	})
}

func legacySameLineage(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if sameAccountBytes(a, b) {
		return true
	}
	af, bf := oauth.CredentialFingerprint(a), oauth.CredentialFingerprint(b)
	return af != nil && bf != nil && *af == *bf
}
