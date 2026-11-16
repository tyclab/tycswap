package store

import (
	"context"
	"encoding/base64"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/storenames"
	"github.com/tyclab/tycswap/internal/usage"
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

// LegacyOrder places an idle profile login against its slot's stored login.
type LegacyOrder int

const (
	// LegacyLoginNewer: no stored login, one known dead, the same lineage, or
	// a profile login that expires later; the verified release decides.
	LegacyLoginNewer LegacyOrder = iota
	// LegacyLoginOlder: it expires no later than a stored login of another
	// lineage, so it is an older copy.
	LegacyLoginOlder
	// LegacyLoginUnordered: either expiry is unreadable.
	LegacyLoginUnordered
)

// LegacyLoginOrder compares an idle profile login with the slot backup by
// expiresAt (DESIGN A92). Nothing records what the token endpoint does with a
// rotated refresh token presented again, so a profile login goes out only
// when it can be shown to be the newest.
func (s *Store) LegacyLoginOrder(account groups.Account, login string) LegacyOrder {
	if login == "" || ccfile.SeatWideOnly(login) {
		return LegacyLoginNewer
	}
	backup, err := s.ReadAccountCredentials(account.Number, account.Email)
	switch {
	case err != nil:
		return LegacyLoginUnordered
	case backup == "" || sameLegacyLineage(login, backup) || s.backupDead(account):
		return LegacyLoginNewer
	}
	mine, mineOK := oauth.ExpiresAtMS(login)
	stored, storedOK := oauth.ExpiresAtMS(backup)
	switch {
	case !mineOK || !storedOK:
		return LegacyLoginUnordered
	case mine <= stored:
		return LegacyLoginOlder
	}
	return LegacyLoginNewer
}

// backupDead: the slot's stored lineage drew invalid_grant (usage.TokenDead).
func (s *Store) backupDead(a groups.Account) bool {
	if s.Usage == nil {
		return false
	}
	ids := map[string]usage.Identity{a.Number: {Email: a.Email, OrgUUID: a.OrgUUID}}
	return s.Usage.Entries(ids)[a.Number].TokenDead()
}

// DropOlderLegacyLogin drops an idle profile login LegacyLoginOrder finds
// older than the slot's stored login, without saving or presenting it
// anywhere. Same locks as DiscardLegacyLogin; the order is taken again under them.
func (s *Store) DropOlderLegacyLogin(number, expected string) error {
	owner, value, err := s.idleLegacyProfile(number, expected)
	if err != nil {
		return err
	}
	if s.LegacyLoginOrder(owner.Account, value) != LegacyLoginOlder {
		return legacyOrderChanged(number)
	}
	return s.dropLegacyLogin(owner, "Dropped the older session profile login of account %s; its slot holds a newer one")
}

func legacyOrderChanged(number string) error {
	return cerr.Session("Account-%s's stored login changed while its session profile was being released; the next round tries again", number)
}

// RefreshLegacyLogin verifies an idle profile login whose access token
// expired by refreshing its own token (RECLAIM-3): a new credential goes back
// into the profile, as Claude Code would write it, so the release can verify
// it; a refused grant means the profile's lineage is dead, so its login and
// claim go and the slot backup stays. Same locks as DiscardLegacyLogin; a
// login no longer the newest (LegacyLoginOrder) is not sent.
func (s *Store) RefreshLegacyLogin(ctx context.Context, c oauth.Client, number, expected string) (oauth.RefreshOutcome, error) {
	owner, value, err := s.idleLegacyProfile(number, expected)
	if err != nil {
		return oauth.RefreshOutcome{}, err
	}
	if s.LegacyLoginOrder(owner.Account, value) != LegacyLoginNewer {
		return oauth.RefreshOutcome{}, legacyOrderChanged(number)
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
			return out, s.keepRefreshedLegacyLogin(owner, value, out, err)
		}
	case out.Error == oauth.ErrInvalidGrant || out.Error == oauth.ErrNoRefreshToken:
		if err := s.dropLegacyLogin(owner, "Dropped the rejected session profile login of account %s"); err != nil {
			return out, err
		}
	}
	return out, nil
}

// keepRefreshedLegacyLogin keeps a refreshed login its profile could not
// store, the only live copy of its token family now (DESIGN A92). A token
// response naming the account proves it: it becomes the slot backup and the
// profile retires. Otherwise it goes to a 0600 recovery file beside the slot
// backup, in the backup's format, the returned error names it for the
// caller's warning, and the spent login is held, never sent again.
func (s *Store) keepRefreshedLegacyLogin(owner groups.Owner, spent string, out oauth.RefreshOutcome, cause error) error {
	number, email := owner.Account.Number, owner.Account.Email
	login := oauth.AccountOnly(out.Credentials)
	if data, err := s.classifiedRoster(); err == nil && out.TokenAccount != nil && s.SlotForIdentity(data, out.TokenAccount) == number {
		if err := s.Creds.WriteBackup(number, email, login); err == nil {
			if s.Log != nil {
				s.Log.Warningf("Account-%s's session profile could not store its refreshed login (%v); the login is saved in the account's slot", number, cause)
			}
			return s.dropLegacyLogin(owner, "Released idle session profile for account %s")
		}
	}
	path := filepath.Join(s.CredentialsDir, storenames.CredsRecoveredFile(number, email))
	reason := cerr.Session("Account-%s's refreshed session login could not be stored (%v); it is kept in %s. Sign in to that account with tycswap add --login --switch, or move that file over the account's stored login.", number, cause, path)
	if err := atomicfile.Write(path, []byte(base64.StdEncoding.EncodeToString([]byte(login))), atomicfile.Opts{}); err != nil {
		reason = cerr.Session("Account-%s's refreshed session login could not be stored (%v) nor kept in %s (%v); sign in to that account again with tycswap add --login --switch", number, cause, path, err)
	}
	if _, err := s.HoldLegacyRelease(number, spent, reason.Error(), HoldRefused); err != nil {
		return err
	}
	return reason
}

// HoldKind says how a recorded hold is honoured.
type HoldKind int

const (
	// HoldRefused holds until the profile's login changes.
	HoldRefused HoldKind = iota
	// HoldTransient (a transport failure) holds for the usage failure backoff.
	HoldTransient
	// HoldLive, on the live account, holds nothing: it keeps the warning to one.
	HoldLive
)

// HoldLegacyRelease records why the release of credential is held, so
// rotation eligibility shows the reason until the credential changes, and
// reports whether the same login was held for the same reason before: a
// round warns once per login (DESIGN A92). The caller holds the store lock.
func (s *Store) HoldLegacyRelease(number, credential, reason string, kind HoldKind) (bool, error) {
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return false, err
	}
	prev := registry.Held[number]
	hold := groups.Hold{Reason: reason, Digest: releaseDigest(credential), Live: kind == HoldLive}
	repeat := prev.Digest == hold.Digest && prev.Reason == hold.Reason && prev.Live == hold.Live
	if kind == HoldTransient {
		hold.Failures = 1
		if repeat {
			hold.Failures = prev.Failures + 1
		}
		hold.RetryAt = clock.Seconds(s.Clk) + usage.FailureBackoffS(hold.Failures)
	}
	if registry.Held[number] == hold {
		return repeat, nil
	}
	registry.Held[number] = hold
	return repeat, registry.Save(s.backupDir)
}

// LegacyReleaseHeld is the recorded reason while the release of credential
// stays held: refused, or inside a transport failure's pause. The release
// then asks no one.
func (s *Store) LegacyReleaseHeld(number, credential string) (string, bool) {
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return "", false
	}
	hold, ok := registry.Held[number]
	if !ok || hold.Live || hold.Digest != releaseDigest(credential) || hold.RetryAt != 0 && clock.Seconds(s.Clk) >= hold.RetryAt {
		return "", false
	}
	return hold.Reason, true
}

func (s *Store) heldReason(number, credential string) string {
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return ""
	}
	if hold := registry.Held[number]; !hold.Live && hold.Digest == releaseDigest(credential) {
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
// this profile: the credentials file, and on macOS the hashed Keychain entry
// first, which Claude Code reads before the file.
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
