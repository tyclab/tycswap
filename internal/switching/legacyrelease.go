package switching

import (
	"context"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

func ReleaseIdleLegacy(s *store.Store, number string) error {
	var profile store.LegacyProfile
	if err := s.Lock.With(func() error {
		var err error
		profile, err = s.LegacyProfileOf(number)
		return err
	}); err != nil {
		return err
	}
	// A stale login lost to a newer slot backup is dropped, never saved (DESIGN A92).
	if profile.Stale && profile.Idle && profile.Pending() {
		return withLegacyLocks(s, profile.Dir, func() error { return s.DiscardLegacyLogin(number, profile.Credential) })
	}
	var candidate *store.LegacyRelease
	order := store.LegacyLoginNewer
	if err := s.Lock.With(func() error {
		var err error
		if candidate, err = s.LegacyReleaseCandidate(number); err == nil && candidate != nil {
			order = s.LegacyLoginOrder(candidate.Owner.Account, candidate.Credentials)
		}
		return err
	}); err != nil || candidate == nil {
		return err
	}
	// An older copy of the stored login goes nowhere: neither to the token
	// or profile endpoint nor over the stored login (DESIGN A92).
	switch order {
	case store.LegacyLoginOlder:
		return withLegacyLocks(s, candidate.Owner.ProfileDir, func() error { return s.DropOlderLegacyLogin(number, candidate.Credentials) })
	case store.LegacyLoginUnordered:
		held := cerr.Session("Account-%s's session login and its stored login cannot be ordered by expiry; its login and claim remain held. Sign in to that account with tycswap add --login --switch to renew it.", number)
		holdLegacyRelease(s, number, candidate.Credentials, held)
		return held
	}
	backup, _ := s.ReadAccountCredentials(number, candidate.Owner.Account.Email)
	if candidate.Credentials != "" && !legacySameLineage(candidate.Credentials, backup) &&
		oauth.IsOAuthTokenExpired(oauth.ExtractOAuthData(candidate.Credentials)["expiresAt"], s.Clk.Now()) {
		// An expired access token cannot name its account: refresh it (RECLAIM-3).
		refreshed, err := refreshIdleLegacy(s, candidate)
		if err != nil || refreshed == nil {
			return err
		}
		candidate = refreshed
	}
	value := candidate.Credentials
	prov := &Provenance{Live: &value}
	if value != "" && !legacySameLineage(value, backup) {
		prov.Resolved = s.ReaskProfile(value)
	}
	var refused error
	err := withLegacyLocks(s, candidate.Owner.ProfileDir, func() error {
		return s.ReleaseLegacyAccount(candidate, func(live string) error {
			data, err := s.ReadSequence()
			if err != nil {
				return err
			}
			backup, _ := s.ReadAccountCredentials(number, candidate.Owner.Account.Email)
			if legacySameLineage(live, backup) {
				return nil
			}
			if s.LegacyLoginOrder(candidate.Owner.Account, live) != store.LegacyLoginNewer {
				return cerr.Session("Account-%s's stored login changed while its session profile was being released; the next round tries again", number)
			}
			kind, _ := classifyOutgoing(s, number, candidate.Owner.Account.Email, live, prov, data)
			identity := candidate.Owner.Account
			if kind == "own-rotated" && prov.Resolved.OrgUUID == identity.OrgUUID &&
				(identity.UUID == "" || prov.Resolved.UUID == identity.UUID) {
				return nil
			}
			refused = cerr.Session("Account-%s's session credential ownership could not be verified; its login and claim remain saved. Sign in to that account with tycswap add --login --switch to renew it.", number)
			return refused
		})
	})
	if refused != nil {
		holdLegacyRelease(s, number, value, refused)
	}
	return err
}

// refreshIdleLegacy refreshes an expired, rotated profile login under the
// release's locks. It returns the candidate holding the new login, or nil
// when the login was dropped (a refused grant) or is held (an error).
func refreshIdleLegacy(s *store.Store, candidate *store.LegacyRelease) (*store.LegacyRelease, error) {
	number := candidate.Owner.Account.Number
	var out oauth.RefreshOutcome
	if err := withLegacyLocks(s, candidate.Owner.ProfileDir, func() error {
		var err error
		out, err = s.RefreshLegacyLogin(context.Background(), s.OAuth, number, candidate.Credentials)
		return err
	}); err != nil {
		return nil, err
	}
	switch {
	case out.Error == oauth.ErrInvalidGrant || out.Error == oauth.ErrNoRefreshToken:
		return nil, nil
	case out.Credentials == "":
		held := cerr.Session("Account-%s's expired session login could not be refreshed to verify it; its login and claim remain held", number)
		holdLegacyRelease(s, number, candidate.Credentials, held)
		return nil, held
	}
	var refreshed *store.LegacyRelease
	err := s.Lock.With(func() error {
		var err error
		refreshed, err = s.LegacyReleaseCandidate(number)
		return err
	})
	return refreshed, err
}

func holdLegacyRelease(s *store.Store, number, credential string, reason error) {
	if err := s.Lock.With(func() error { return s.HoldLegacyRelease(number, credential, reason.Error()) }); err != nil && s.Log != nil {
		s.Log.Warningf("Account-%s release hold not recorded: %v", number, err)
	}
}

// withLegacyLocks runs fn under the store lock, then the profile's credential,
// config and storage-write locks, the order every release takes (DESIGN A88).
func withLegacyLocks(s *store.Store, dir string, fn func() error) error {
	return s.Lock.With(func() error {
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
		return fn()
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
