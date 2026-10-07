// guards.go — the live-session guards and session-profile lifecycle helpers the
// store shares with internal/session (via the sessprofile leaf, which breaks the
// store↔session cycle).
//
// Implements spec 01§7 (_ensure_no_live_session, _session_dir,
// _live_session_pids, _invalidate_session_credentials, _delete_session_profile).
// Every destructive slot op funnels through EnsureNoLiveSession, which refuses
// while a session-mode `tycswap run` process is live against that slot.
package store

import (
	"os"
	"strings"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

// SessionDir is the per-account session profile directory
// sessions/{num}-{slugify_email(email)}/ (spec 01§7).
func (s *Store) SessionDir(num, email string) string {
	return sessprofile.SessionDirFor(s.backupDir, num, email)
}

// LiveSessionPidsFor returns the PIDs of Claude instances running against a
// slot's session profile (spec 01§ live_session_pids_for).
func (s *Store) LiveSessionPidsFor(num, email string) []int {
	return sessprofile.LiveSessionPIDs(s.SessionDir(num, email))
}

// EnsureNoLiveSession refuses a destructive operation while a session-mode
// Claude instance is live against the slot (spec 01§7). action names the
// operation for the error message ("the operation", "--remove-account",
// "--swap-accounts", "--move-account").
func (s *Store) EnsureNoLiveSession(num, email, action string) error {
	pids, err := profilePIDs(s.SessionDir(num, email))
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		owner, err := s.destructiveOwner(num, email)
		if err != nil {
			return err
		}
		if strings.HasPrefix(owner.Scope, "legacy:") && len(owner.PIDs) == 0 {
			if !owner.Uncertain {
				return nil
			}
			if _, err := os.Stat(owner.ProfileDir); os.IsNotExist(err) {
				return nil
			}
		}
		if owner.Scope != "" && (owner.Scope != "default" || len(owner.PIDs) > 0 || owner.Uncertain) {
			return cerr.Session("Account-%s (%s) is held by %s%s; release or reconcile that profile before retrying %s.", num, email, owner.Scope, uncertainSuffix(owner.Uncertain), action)
		}
		return nil
	}
	return cerr.Session(
		"Account-%s (%s) has a live session-mode Claude instance (PID %s). Exit it first, then retry %s.",
		num, email, joinPIDs(pids), action)
}

func (s *Store) destructiveOwner(number, email string) (groups.Owner, error) {
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return groups.Owner{}, err
	}
	owner := registry.Claims[number]
	if strings.HasPrefix(owner.Scope, "legacy:") {
		owner.PIDs, err = profilePIDs(owner.ProfileDir)
		if err != nil {
			return groups.Owner{}, err
		}
		profileEmail, org, ok := sessprofile.ReadSessionIdentity(owner.ProfileDir)
		owner.Uncertain = owner.Uncertain || !ok || profileEmail != owner.Account.Email || org != owner.Account.OrgUUID
	}
	for _, id := range groups.All() {
		journal, err := groups.LoadJournal(s.backupDir, id)
		if err != nil {
			return groups.Owner{}, err
		}
		if journal != nil && (journal.Target.Number == number || journal.Previous != nil && journal.Previous.Number == number) {
			return groups.Owner{Scope: string(id), ProfileDir: groups.ProfileDir(s.backupDir, id), Uncertain: true}, nil
		}
		active, err := groups.LoadActive(s.backupDir, id)
		if err != nil {
			return groups.Owner{}, err
		}
		if active != nil && active.Number == number {
			return groups.Owner{Scope: string(id), ProfileDir: groups.ProfileDir(s.backupDir, id), Account: *active}, nil
		}
		dir := groups.ProfileDir(s.backupDir, id)
		value, ok, err := s.ReadProfileCredentials(dir)
		if err != nil {
			return groups.Owner{}, err
		}
		if ok && value != "" {
			profileEmail, _, identityOK := sessprofile.ReadSessionIdentity(dir)
			if !identityOK || profileEmail == email {
				return groups.Owner{Scope: string(id), ProfileDir: dir, Uncertain: true}, nil
			}
		}
	}
	pids, err := profilePIDs(s.DefaultProfileDir())
	if err != nil {
		return groups.Owner{}, err
	}
	if len(pids) > 0 {
		profileEmail, _, ok := ccfile.ReadOAuthIdentityFrom(s.DefaultConfigPath())
		if !ok || profileEmail == email {
			return groups.Owner{Scope: "default", ProfileDir: s.DefaultProfileDir(), PIDs: pids, Uncertain: !ok}, nil
		}
	}
	return owner, nil
}

// DeleteSessionProfile removes a slot's session profile directory and its
// Keychain entry (Keychain first — the hashed service name derives from the dir
// path; spec 01§7). Absent is a no-op. Logs on an actual removal.
func (s *Store) DeleteSessionProfile(num, email string) {
	dir := s.SessionDir(num, email)
	if _, err := os.Stat(dir); err != nil {
		return
	}
	sessprofile.DeleteSessionProfile(s.kc, dir)
	if s.Log != nil {
		s.Log.Infof("Removed session profile for account %s at %s", num, dir)
	}
}
