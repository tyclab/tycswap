package store

import (
	"fmt"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
)

// ReleaseGroupAccount returns the group's account to the shared pool; verify
// must prove the profile's credential is the account's own lineage. The shared
// account lock and this profile's Claude locks must be held. The steps run in
// an order a rerun can finish: the claim and active record go last.
func (s *Store) ReleaseGroupAccount(verify func(active groups.Account, live string) error) (*groups.Account, error) {
	if s.group == "" {
		return nil, fmt.Errorf("a session group is required")
	}
	pids, err := profilePIDs(s.ProfileDir())
	if err != nil {
		return nil, err
	}
	if len(pids) > 0 {
		return nil, cerr.Session("%s has live sessions (PID %s); exit them before releasing its account", s.group.Label(), joinPIDs(pids))
	}
	active, err := groups.LoadActive(s.backupDir, s.group)
	if err != nil {
		return nil, err
	}
	live, _, err := s.Creds.ReadActive()
	if err != nil {
		return nil, err
	}
	login := live != "" && !ccfile.SeatWideOnly(live)
	email, org, identified := s.GetCurrentAccount()
	if active == nil {
		if login || identified {
			return nil, fmt.Errorf("the %s profile has credentials without an ownership record; reconcile before releasing", s.group)
		}
		return nil, s.dropGroupClaims()
	}
	if identified && (email != active.Email || org != active.OrgUUID) {
		return nil, fmt.Errorf("the %s profile no longer matches its recorded account; hold until ownership is verified", s.group)
	}
	if login {
		if err := verify(*active, live); err != nil {
			return nil, err
		}
		if err := s.WriteAccountCredentials(active.Number, active.Email, oauth.AccountOnly(live)); err != nil {
			return nil, err
		}
	}
	if remainder, ok := ccfile.SeatWidePart(live); ok {
		err = s.Creds.WriteActive(remainder)
	} else {
		err = s.Creds.ClearActive()
	}
	if err != nil {
		return nil, err
	}
	config, err := ccfile.ReadGlobalConfigStrict(s.GlobalConfigPath())
	if err != nil {
		return nil, err
	}
	if _, present := config["oauthAccount"]; present {
		delete(config, "oauthAccount")
		if err := groups.WriteJSON(s.GlobalConfigPath(), config); err != nil {
			return nil, err
		}
	}
	if err := s.dropGroupClaims(); err != nil {
		return nil, err
	}
	if err := removeGroupFile(groups.ActivePath(s.backupDir, s.group)); err != nil {
		return nil, err
	}
	if s.Log != nil {
		s.Log.Infof("Group %s released account %s", s.group, active.Number)
	}
	return active, nil
}

func (s *Store) dropGroupClaims() error {
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return err
	}
	changed := false
	for slot, owner := range registry.Claims {
		if owner.Scope == string(s.group) {
			delete(registry.Claims, slot)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return registry.Save(s.backupDir)
}
