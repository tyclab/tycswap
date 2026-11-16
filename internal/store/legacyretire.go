package store

import (
	"context"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

// LegacyProfile is what an account's former `tycswap run`/`env` profile holds
// (DESIGN A92), read under the store lock.
type LegacyProfile struct {
	Dir        string
	Credential string
	Claimed    bool
	Stale      bool
	// Idle: no Claude process runs in it, as far as its records tell.
	Idle bool
}

// Pending reports whether the profile still holds a login or a claim to retire.
func (p LegacyProfile) Pending() bool {
	return p.Claimed || p.Credential != "" && !ccfile.SeatWideOnly(p.Credential)
}

func (s *Store) LegacyProfileOf(number string) (LegacyProfile, error) {
	data, err := s.classifiedRoster()
	if err != nil {
		return LegacyProfile{}, err
	}
	account, err := s.accountFrom(data, number)
	if err != nil {
		return LegacyProfile{}, err
	}
	p := LegacyProfile{Dir: s.SessionDir(number, account.Email)}
	if p.Credential, _, err = s.ReadProfileCredentials(p.Dir); err != nil {
		return LegacyProfile{}, err
	}
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return LegacyProfile{}, err
	}
	_, p.Claimed = registry.Claims[number]
	p.Stale = sessprofile.IsStale(p.Dir)
	pids, err := profilePIDs(p.Dir)
	p.Idle = err == nil && len(pids) == 0
	return p, nil
}

// DiscardLegacyLogin drops an idle profile's login and claim without saving
// that login anywhere (DESIGN A92). Only a stale login (the slot backup was
// rewritten while it ran, or a drop was interrupted), or on the live default
// account one that is empty or the live login's own lineage, qualifies; the
// slot backup and the live credential are never written. The caller holds
// the store lock and the profile's credential, config and storage-write locks.
func (s *Store) DiscardLegacyLogin(number, expected string) error {
	owner, value, err := s.idleLegacyProfile(number, expected)
	if err != nil {
		return err
	}
	if !sessprofile.IsStale(owner.ProfileDir) {
		live, err := s.ReadDefaultCredentials()
		if err != nil {
			return err
		}
		data, err := s.classifiedRoster()
		if err != nil {
			return err
		}
		login := value
		if ccfile.SeatWideOnly(login) {
			login = ""
		}
		if s.defaultLiveSlot(data, live).Slot != number || login != "" && !sameLegacyLineage(login, live) {
			return cerr.Session("Account-%s's session profile holds a login the live one does not continue; its login and claim remain held", number)
		}
	}
	return s.dropLegacyLogin(owner, "Discarded the idle session profile login of account %s")
}

// RefreshLegacyLogin verifies an idle profile login whose access token
// expired by refreshing its own token (RECLAIM-3): a new credential goes back
// into the profile, as Claude Code would write it, so the release can verify
// it; a refused grant means the profile's lineage is dead, so its login and
// claim go and the slot backup stays. Same locks as DiscardLegacyLogin.
func (s *Store) RefreshLegacyLogin(ctx context.Context, c oauth.Client, number, expected string) (oauth.RefreshOutcome, error) {
	owner, value, err := s.idleLegacyProfile(number, expected)
	if err != nil {
		return oauth.RefreshOutcome{}, err
	}
	if c == nil || oauth.ExtractAccessToken(value) == "" {
		return oauth.RefreshOutcome{Error: oauth.ErrTransient}, nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, GuardedRefreshTimeout)
	defer cancel()
	out := c.Refresh(reqCtx, value)
	switch {
	case out.Credentials != "":
		if err := s.writeLegacyLogin(owner.ProfileDir, out.Credentials); err != nil {
			if s.Log != nil {
				s.Log.Warningf("Refreshed the session profile login of account %s but could not store it: %v", number, err)
			}
			return oauth.RefreshOutcome{Error: oauth.ErrRefreshFailed}, err
		}
	case out.Error == oauth.ErrInvalidGrant || out.Error == oauth.ErrNoRefreshToken:
		if err := s.dropLegacyLogin(owner, "Dropped the rejected session profile login of account %s"); err != nil {
			return out, err
		}
	}
	return out, nil
}

// HoldLegacyRelease records why a release of credential was refused, so
// rotation eligibility shows the reason until the credential changes.
func (s *Store) HoldLegacyRelease(number, credential, reason string) error {
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return err
	}
	hold := groups.Hold{Reason: reason, Digest: releaseDigest(credential)}
	if registry.Held[number] == hold {
		return nil
	}
	registry.Held[number] = hold
	return registry.Save(s.backupDir)
}

func (s *Store) heldReason(number, credential string) string {
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return ""
	}
	if hold := registry.Held[number]; hold.Digest == releaseDigest(credential) {
		return hold.Reason
	}
	return ""
}

// idleLegacyProfile checks, under the caller's locks, that the account's
// profile runs nothing, matches its claim (when one exists) and still holds
// expected; a profile with a login must name the account.
func (s *Store) idleLegacyProfile(number, expected string) (groups.Owner, string, error) {
	data, err := s.classifiedRoster()
	if err != nil {
		return groups.Owner{}, "", err
	}
	account, err := s.accountFrom(data, number)
	if err != nil {
		return groups.Owner{}, "", err
	}
	owner := groups.Owner{Scope: "legacy:" + number, ProfileDir: s.SessionDir(number, account.Email), Account: account}
	if err := groups.SafePath(filepath.Join(owner.ProfileDir, ccfile.CredentialsFileName)); err != nil {
		return groups.Owner{}, "", err
	}
	if pids, err := profilePIDs(owner.ProfileDir); err != nil || len(pids) > 0 {
		return groups.Owner{}, "", cerr.Session("Account-%s's session profile is running or unreadable; its login and claim remain held", number)
	}
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return groups.Owner{}, "", err
	}
	if claim, ok := registry.Claims[number]; ok && (claim.Scope != owner.Scope || claim.ProfileDir != owner.ProfileDir || !claim.Account.SameIdentity(account) || claim.Account.UUID != "" && claim.Account.UUID != account.UUID) {
		return groups.Owner{}, "", cerr.Session("Account-%s's session claim disagrees with its account; reconcile it before switching", number)
	}
	value, _, err := s.ReadProfileCredentials(owner.ProfileDir)
	if err != nil {
		return groups.Owner{}, "", err
	}
	if value != expected {
		return groups.Owner{}, "", cerr.Session("Account-%s's session credential changed while it was being released; its claim remains held", number)
	}
	if value != "" && !ccfile.SeatWideOnly(value) {
		email, org, ok := ccfile.ReadProfileIdentity(owner.ProfileDir)
		if !ok || email != account.Email || org != account.OrgUUID {
			return groups.Owner{}, "", cerr.Session("Account-%s's session profile is signed in as another identity; its login and claim remain held", number)
		}
	}
	return owner, value, nil
}

// dropLegacyLogin marks the profile stale first, so an interrupted drop
// finishes on the next attempt, then clears its login (seat-wide sign-ins
// stay), drops the claim and its hold, and unmarks it.
func (s *Store) dropLegacyLogin(owner groups.Owner, done string) error {
	dir := owner.ProfileDir
	sessprofile.MarkStale(dir)
	if !sessprofile.IsStale(dir) {
		return cerr.Session("Account-%s's session profile could not be marked stale; its login and claim remain held", owner.Account.Number)
	}
	if err := s.clearLegacyLogin(dir); err != nil {
		return err
	}
	if value, _, err := s.ReadProfileCredentials(dir); err != nil {
		return err
	} else if value != "" && !ccfile.SeatWideOnly(value) {
		return cerr.Session("Account-%s's session credential could not be cleared; its claim remains held", owner.Account.Number)
	}
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return err
	}
	delete(registry.Claims, owner.Account.Number)
	delete(registry.Held, owner.Account.Number)
	if err := registry.Save(s.backupDir); err != nil {
		return err
	}
	if err := sessprofile.ClearStaleMarker(dir); err != nil {
		return err
	}
	if err := removeGroupFile(legacyReleasePath(dir)); err != nil {
		return err
	}
	if s.Log != nil {
		s.Log.Infof(done, owner.Account.Number)
	}
	return nil
}

// writeLegacyLogin stores a refreshed login where Claude Code reads it for
// this profile: the hashed Keychain entry on macOS, else the credentials file.
func (s *Store) writeLegacyLogin(dir, value string) error {
	if s.Platform == platform.MacOS {
		if err := s.kc.Set(ccfile.KeychainServiceName(dir), keychain.AccountName(), value); err != nil {
			return err
		}
	}
	return atomicfile.Write(filepath.Join(dir, ccfile.CredentialsFileName), []byte(value), atomicfile.Opts{})
}

func sameLegacyLineage(a, b string) bool {
	return a != "" && b != "" && (oauth.SameAccountBlock(a, b) || fingerprintsEqual(a, b))
}
