package switching

import (
	"context"
	"errors"
	"fmt"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

// ErrGroupBusy: a session still runs in the group's profile.
var ErrGroupBusy = errors.New("group profile is in use")

func releaseGroup(s *store.Store) (*groups.Account, error) {
	if s.GroupID() == "" {
		return nil, fmt.Errorf("a session group is required")
	}
	if err := groups.SafePath(s.ProfileDir()); err != nil {
		return nil, err
	}
	// An unfinished switch is matched by its credential's refresh token, which
	// a refresh rotates: reconcile it first, and refresh on a later tick.
	journal, err := groups.LoadJournal(s.SharedRoot(), s.GroupID())
	if err != nil {
		return nil, err
	}
	dead := ""
	if journal == nil {
		// A busy profile is refused below, under the locks, with its own message.
		if dead, err = refreshExpiredGroupLogin(s); err != nil && !errors.Is(err, ErrGroupBusy) {
			return nil, err
		}
	}
	prov := prefetchLiveIdentity(s)
	var released *groups.Account
	err = withTripleLock(s, func() error {
		if pids, err := store.LiveProfilePIDs(s.ProfileDir()); err != nil || len(pids) > 0 {
			return ErrGroupBusy
		}
		if err := s.ReconcileGroup(); err != nil {
			return err
		}
		data, err := s.SequenceForUpdate()
		if err != nil {
			return err
		}
		released, err = s.ReleaseGroupAccount(func(active groups.Account, live string) (bool, error) {
			kind, _ := classifyOutgoing(s, active.Number, active.Email, live, prov, data)
			if kind == "own-bytes" || kind == "own-family" || kind == "own-rotated" {
				return true, nil
			}
			// A login whose refresh token the server rejected can never
			// authenticate again: it is dropped, and the backup kept (A78).
			if dead != "" && fingerprintEqual(live, dead) {
				if s.Log != nil {
					s.Log.Warningf("Group %s's login for Account-%s is dead (invalid_grant); dropped without touching the backup", s.GroupID(), active.Number)
				}
				return false, nil
			}
			return false, cerr.Session("the %s profile's credential ownership is unverified (%s); the account claim remains held", s.GroupID(), kind)
		})
		return err
	})
	return released, err
}

// refreshExpiredGroupLogin renews a group login whose access token expired
// while no Claude process ran in its profile, so its identity can be verified
// again (DESIGN A74). The refresh rotates the refresh token, so it runs under
// the account lock and the profile's credential lock and is stored at once.
// It returns a login the server rejected as invalid_grant.
func refreshExpiredGroupLogin(s *store.Store) (string, error) {
	if s.OAuth == nil {
		return "", nil
	}
	dead := ""
	err := s.Lock.With(func() error {
		if pids, err := store.LiveProfilePIDs(s.ProfileDir()); err != nil || len(pids) > 0 {
			return ErrGroupBusy
		}
		live, _, err := s.Creds.ReadActive()
		if err != nil || live == "" || ccfile.SeatWideOnly(live) {
			return err
		}
		if !oauth.IsOAuthTokenExpired(oauth.ExtractOAuthData(live)["expiresAt"], s.Clk.Now()) {
			return nil
		}
		cred, err := cclock.AcquireProfileCredentials(s.ProfileDir(), 0, s.Clk)
		if err != nil {
			return ErrGroupBusy
		}
		defer cred.Release()
		ctx, cancel := context.WithTimeout(context.Background(), store.GuardedRefreshTimeout)
		defer cancel()
		out := s.OAuth.Refresh(ctx, live)
		if out.Credentials == "" {
			if s.Log != nil {
				s.Log.Warningf("Group %s's expired login did not refresh (%s)", s.GroupID(), out.Error)
			}
			if out.Error == oauth.ErrInvalidGrant {
				dead = live
			}
			return nil
		}
		if err := s.Creds.WriteActive(out.Credentials); err != nil {
			return fmt.Errorf("refreshed the %s group's login but could not store it: %w", s.GroupID(), err)
		}
		if s.Log != nil {
			s.Log.Infof("Refreshed group %s's expired login", s.GroupID())
		}
		return nil
	})
	return dead, err
}
