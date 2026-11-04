package switching

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/store"
)

var errGroupBusy = errors.New("group profile is in use")

// ReleaseGroup hands a session group's account back to the shared pool, its
// newest credential written to the account's backup first.
func ReleaseGroup(s *store.Store, emit bool) (map[string]any, error) {
	released, err := releaseGroup(s, false)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"group": s.GroupID(), "released": nil}
	if released != nil {
		result["released"] = numRef(released.Number, released.Email)
	}
	if emit {
		if released == nil {
			printOut("Group " + s.GroupID().Label() + " holds no account.")
		} else {
			printOut("Group " + s.GroupID().Label() + " released Account-" + released.Number + " (" + released.Email + ").")
		}
	}
	return result, nil
}

// ReleaseIdleGroup releases the account of a group whose profile has shown no
// activity for grace and has no live or launching session (DESIGN A74). It
// returns nil without an error while the group is still in use.
func ReleaseIdleGroup(s *store.Store, grace time.Duration) (*groups.Account, error) {
	if s.GroupID() == "" {
		return nil, fmt.Errorf("a session group is required")
	}
	if last := groupActivity(s); s.Clk.Now().Sub(last) < grace {
		return nil, nil
	}
	released, err := releaseGroup(s, true)
	if errors.Is(err, errGroupBusy) {
		return nil, nil
	}
	return released, err
}

func releaseGroup(s *store.Store, idleOnly bool) (*groups.Account, error) {
	if s.GroupID() == "" {
		return nil, fmt.Errorf("a session group is required")
	}
	if err := groups.SafePath(s.ProfileDir()); err != nil {
		return nil, err
	}
	// A busy profile is refused below, under the locks, with its own message.
	if err := refreshExpiredGroupLogin(s); err != nil && !errors.Is(err, errGroupBusy) {
		return nil, err
	}
	prov := prefetchLiveIdentity(s)
	var released *groups.Account
	err := withTripleLock(s, func() error {
		if idleOnly {
			state, err := routingstate.Read(s.SharedRoot())
			if err != nil {
				return err
			}
			state.Prune()
			if state.CheckIdle(string(s.GroupID())) != nil {
				return errGroupBusy
			}
			if pids, err := store.LiveProfilePIDs(s.ProfileDir()); err != nil || len(pids) > 0 {
				return errGroupBusy
			}
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
			return errGroupBusy
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
			return errGroupBusy
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

// groupActivity is the newest change a session start, exit, launch, switch or
// token refresh leaves in the group's files.
func groupActivity(s *store.Store) time.Time {
	scope := groups.ScopeDir(s.SharedRoot(), s.GroupID())
	paths := []string{
		groups.ActivePath(s.SharedRoot(), s.GroupID()),
		filepath.Join(s.ProfileDir(), ".credentials.json"),
		filepath.Join(s.ProfileDir(), "sessions"),
		filepath.Join(s.ProfileDir(), "ide"),
		filepath.Join(scope, "launches"),
	}
	if entries, err := os.ReadDir(filepath.Join(scope, "launches")); err == nil {
		for _, e := range entries {
			paths = append(paths, filepath.Join(scope, "launches", e.Name()))
		}
	}
	var last time.Time
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(last) {
			last = info.ModTime()
		}
	}
	return last
}
