package switching

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

// ErrStillHeld marks a hold already recorded for the same profile login on
// an earlier round: callers log it at debug instead of warning again.
var ErrStillHeld = errors.New("release still held")

type stillHeld struct{ error }

func (e stillHeld) Unwrap() []error { return []error{e.error, ErrStillHeld} }

// ReleaseIdleLegacy releases number's idle former profile. It writes, so it
// reads afresh whatever store it is handed (DESIGN A116).
func ReleaseIdleLegacy(s *store.Store, number string) error {
	s = s.Fresh()
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
	held, isHeld := "", false
	if err := s.Lock.With(func() error {
		var err error
		if candidate, err = s.LegacyReleaseCandidate(number); err == nil && candidate != nil {
			order = s.LegacyLoginOrder(candidate.Owner.Account, candidate.Credentials)
			held, isHeld = s.LegacyReleaseHeld(number, candidate.Credentials)
		}
		return err
	}); err != nil || candidate == nil {
		return err
	}
	// An older copy of the stored login goes nowhere: neither to the token
	// or profile endpoint nor over the stored login (DESIGN A92).
	switch {
	case order == store.LegacyLoginOlder:
		return withLegacyLocks(s, candidate.Owner.ProfileDir, func() error { return s.DropOlderLegacyLogin(number, candidate.Credentials) })
	case order == store.LegacyLoginUnordered:
		return holdLegacyRelease(s, number, candidate.Credentials, cerr.Session("Account-%s's session login and its stored login cannot be ordered by expiry; its login and claim remain held. Sign in to that account with tycswap add --login --switch to renew it.", number), store.HoldRefused)
	case isHeld:
		// RECLAIM-4: this login was refused, or waits out a transport failure.
		return stillHeld{cerr.Session("%s", held)}
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
	answered := true
	if value != "" && !legacySameLineage(value, backup) {
		prov.Resolved, answered = s.ReaskProfileAnswer(value)
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
	switch {
	case refused != nil && !answered:
		// The profile endpoint did not answer: a failed request, not a refusal.
		return holdLegacyRelease(s, number, value, cerr.Session("Account-%s's session login could not be verified for now (the profile endpoint did not answer); it is tried again after a pause", number), store.HoldTransient)
	case refused != nil:
		return holdLegacyRelease(s, number, value, refused, store.HoldRefused)
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
	case out.Error == oauth.ErrTransient:
		return nil, holdLegacyRelease(s, number, candidate.Credentials, cerr.Session("Account-%s's expired session login could not be refreshed for now; it is tried again after a pause", number), store.HoldTransient)
	case out.Credentials == "":
		return nil, holdLegacyRelease(s, number, candidate.Credentials, cerr.Session("Account-%s's expired session login could not be refreshed to verify it; its login and claim remain held", number), store.HoldRefused)
	}
	var refreshed *store.LegacyRelease
	err := s.Lock.With(func() error {
		var err error
		refreshed, err = s.LegacyReleaseCandidate(number)
		return err
	})
	return refreshed, err
}

// holdLegacyRelease records reason for credential and returns it, marked
// ErrStillHeld when that login was held for it already.
func holdLegacyRelease(s *store.Store, number, credential string, reason error, kind store.HoldKind) error {
	repeat := false
	if err := s.Lock.With(func() error {
		var err error
		repeat, err = s.HoldLegacyRelease(number, credential, reason.Error(), kind)
		return err
	}); err != nil && s.Log != nil {
		s.Log.Warningf("Account-%s release hold not recorded: %v", number, err)
	}
	if repeat {
		return stillHeld{reason}
	}
	return reason
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
