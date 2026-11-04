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
	// A busy profile is refused below, under the locks, with its own message.
	if err := refreshExpiredGroupLogin(s); err != nil && !errors.Is(err, ErrGroupBusy) {
		return nil, err
	}
	prov := prefetchLiveIdentity(s)
	var released *groups.Account
	err := withTripleLock(s, func() error {
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
		released, err = s.ReleaseGroupAccount(func(active groups.Account, live string) error {
			kind, _ := classifyOutgoing(s, active.Number, active.Email, live, prov, data)
			if kind != "own-bytes" && kind != "own-family" && kind != "own-rotated" {
				return cerr.Session("the %s profile's credential ownership is unverified (%s); the account claim remains held", s.GroupID(), kind)
			}
			return nil
		})
		return err
	})
	return released, err
}

// refreshExpiredGroupLogin renews a group login whose access token expired
// while no Claude process ran in its profile, so its identity can be verified
// again (DESIGN A74). The refresh rotates the refresh token, so it runs under
// the account lock and the profile's credential lock and is stored at once.
func refreshExpiredGroupLogin(s *store.Store) error {
	if s.OAuth == nil {
		return nil
	}
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
	return err
}
