package switching

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/store"
)

func switchGroupTo(s *store.Store, identifier string, jsonOut, force bool) (any, error) {
	number, _, _, err := s.ResolveAccount(identifier)
	if err != nil {
		return nil, err
	}
	op, err := performGroupSwitch(s, number, !jsonOut, force, nil)
	if err != nil {
		return nil, err
	}
	if !jsonOut {
		return nil, nil
	}
	result := switchResultFromOp(op, "direct", nil)
	result["group"] = s.GroupID()
	if force && !result["switched"].(bool) {
		result["reason"] = "activated"
	}
	return result, nil
}

func ReconcileGroup(s *store.Store) error {
	if s.GroupID() == "" {
		return nil
	}
	journal, err := groups.LoadJournal(s.SharedRoot(), s.GroupID())
	if err != nil || journal == nil {
		return err
	}
	return withTripleLock(s, s.ReconcileGroup)
}

func performGroupSwitch(s *store.Store, number string, emit, force bool, prov *Provenance) (switchOp, error) {
	if err := groups.SafePath(s.ProfileDir()); err != nil {
		return switchOp{}, err
	}
	if prov == nil {
		prov = prefetchLiveIdentity(s)
	}
	incomingProv := &Provenance{}
	if s.GroupHandoff() {
		owner, err := s.CredentialOwner(number)
		if err == nil && (owner.Scope == "default" || strings.HasPrefix(owner.Scope, "legacy:")) {
			if value, err := s.ReadOwnedCredentials(number, s.AccountEmail(number)); err == nil && value != "" {
				incomingProv.Live = &value
				backup, _ := s.ReadAccountCredentials(number, s.AccountEmail(number))
				if s.OAuth != nil && !fingerprintEqual(value, backup) {
					incomingProv.Resolved = s.OAuth.Profile(bgCtx(), oauth.ExtractAccessToken(value))
				}
			}
		}
	}
	var op switchOp
	err := withTripleLockTarget(s, number, func() error {
		if err := s.ReconcileGroup(); err != nil {
			return err
		}
		data, err := s.SequenceForUpdate()
		if err != nil {
			return err
		}
		rec, ok := accountRec(data, number)
		if !ok {
			return cerr.AccountNotFound("Account-%s does not exist", number)
		}
		target := groups.Account{Number: number, Email: recStr(rec, "email"), OrgUUID: recStr(rec, "organizationUuid"), UUID: recStr(rec, "uuid")}
		if err := s.EnsureAccountAvailable(number); err != nil {
			return err
		}
		intent := groups.Start
		if len(sessprofile.LiveSessionPIDs(s.ProfileDir())) > 0 || s.GroupIntent() == groups.Continue {
			intent = groups.Continue
		}
		compatible := s.GroupCompatibility(number, intent)
		if !compatible.Known || !compatible.Allowed {
			return cerr.Session("Account-%s cannot be selected for the %s group: %s", number, s.GroupID(), compatible.Reason)
		}
		previous, err := groups.LoadActive(s.SharedRoot(), s.GroupID())
		if err != nil {
			return err
		}
		var from map[string]any
		if previous != nil {
			from = numRef(previous.Number, previous.Email)
			if previous.Number == number && !force {
				op = switchOp{From: from, To: from, Warnings: []string{}}
				return nil
			}
		}
		creds, err := s.ReadAccountCredentials(number, target.Email)
		if err != nil || creds == "" {
			return cerr.CredentialRead("Account-%s has no readable stored credential", number)
		}
		if oauth.ExtractAccessToken(creds) == "" {
			return cerr.Session("session groups require an OAuth credential for Account-%s", number)
		}
		if s.GroupHandoff() {
			owner, err := s.CredentialOwner(number)
			if err != nil {
				return err
			}
			if owner.Scope == "default" || strings.HasPrefix(owner.Scope, "legacy:") {
				live, err := s.ReadOwnedCredentialsLocked(number, target.Email)
				if err != nil || live == "" {
					return cerr.CredentialRead("cannot preserve the default login before handoff")
				}
				if !fingerprintEqual(live, creds) {
					kind, _ := classifyOutgoing(s, number, target.Email, live, incomingProv, data)
					if kind != "own-rotated" {
						return cerr.Session("the default credential's lineage changed; verify its account before group handoff")
					}
				}
				creds = oauth.AccountOnly(live)
				if err := s.Creds.WriteBackup(number, target.Email, creds); err != nil {
					return err
				}
			}
		}
		text, err := s.ReadAccountConfig(number, target.Email)
		if err != nil {
			return err
		}
		var config map[string]any
		if err := json.Unmarshal([]byte(text), &config); err != nil {
			return cerr.Config("Account-%s's config backup is unreadable", number)
		}
		identity, ok := oauthSection(config)
		if !ok || recStr(identity, "emailAddress") != target.Email || recStr(identity, "organizationUuid") != target.OrgUUID {
			return cerr.Config("Account-%s's config backup disagrees with its roster identity", number)
		}
		if _, err := ccfile.ReadGlobalConfigStrict(s.GlobalConfigPath()); err != nil {
			return cerr.Config("cannot update the %s profile: %v", s.GroupID(), err)
		}
		if previous != nil {
			live, _, err := s.Creds.ReadActive()
			if err != nil || live == "" {
				return cerr.CredentialRead("cannot preserve the %s profile's outgoing credential", s.GroupID())
			}
			kind, _ := classifyOutgoing(s, previous.Number, previous.Email, live, prov, data)
			if kind != "own-bytes" && kind != "own-family" && kind != "own-rotated" {
				return cerr.Session("the %s profile's outgoing credential ownership is unverified (%s); account claims remain held", s.GroupID(), kind)
			}
			if err := s.WriteAccountCredentials(previous.Number, previous.Email, oauth.AccountOnly(live)); err != nil {
				return err
			}
			if previous.Number == number {
				creds = oauth.AccountOnly(live)
			}
		}
		journal, err := s.BeginGroupSwitch(target, creds)
		if err != nil {
			if journal != nil {
				if rollbackErr := s.RollbackGroupSwitch(journal); rollbackErr != nil {
					return fmt.Errorf("%w; rollback failed and claims remain held: %v", err, rollbackErr)
				}
			}
			return err
		}
		commit := func() error {
			if err := s.ClearDefaultForGroup(journal); err != nil {
				return err
			}
			if err := s.ClearLegacyForGroup(journal); err != nil {
				return err
			}
			if err := s.Creds.WriteActiveAccount(creds); err != nil {
				return err
			}
			if err := s.WriteGroupIdentity(config); err != nil {
				return err
			}
			return s.CompleteGroupSwitch(journal)
		}
		if err := commit(); err != nil {
			if rollbackErr := s.RollbackGroupSwitch(journal); rollbackErr != nil {
				return fmt.Errorf("group switch failed: %w; rollback failed and claims remain held: %v", err, rollbackErr)
			}
			return fmt.Errorf("group switch failed and was rolled back: %w", err)
		}
		op = switchOp{From: from, To: numRef(number, target.Email), Warnings: []string{}}
		if s.Log != nil {
			s.Log.Infof("Group %s switched to account %s", s.GroupID(), number)
		}
		return nil
	})
	if err == nil && emit {
		printOut("Group " + s.GroupID().Label() + " uses Account-" + number + ".")
	}
	return op, err
}

func defaultHandoffRequired(s *store.Store, target string) bool {
	journal, err := groups.LoadJournal(s.SharedRoot(), s.GroupID())
	if err == nil && journal != nil && journal.DefaultHandoff {
		return true
	}
	if !s.GroupHandoff() || target == "" {
		return false
	}
	owner, err := s.CredentialOwner(target)
	return err == nil && owner.Scope == "default"
}

func legacyHandoffProfile(s *store.Store, target string) (string, error) {
	journal, err := groups.LoadJournal(s.SharedRoot(), s.GroupID())
	if err != nil {
		return "", err
	}
	if journal != nil && journal.SourceOwner != nil {
		return journal.SourceOwner.ProfileDir, nil
	}
	if !s.GroupHandoff() || target == "" {
		return "", nil
	}
	owner, err := s.CredentialOwner(target)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(owner.Scope, "legacy:") {
		return owner.ProfileDir, nil
	}
	return "", nil
}
