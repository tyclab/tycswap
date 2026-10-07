package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

func (s *Store) accountFrom(data *SequenceData, number string) (groups.Account, error) {
	rec, ok := recordFor(data, number)
	if !ok {
		return groups.Account{}, cerr.AccountNotFound("Account-%s does not exist", number)
	}
	return groups.Account{Number: number, Email: strField(rec, "email"), OrgUUID: strField(rec, "organizationUuid"), UUID: strField(rec, "uuid")}, nil
}

func (s *Store) CredentialOwner(number string) (groups.Owner, error) {
	owners, err := s.credentialOwners(number)
	if err != nil {
		return groups.Owner{}, err
	}
	if len(owners) > 1 {
		return groups.Owner{}, fmt.Errorf("Account-%s has conflicting credential owners; hold switching until its profiles are reconciled", number)
	}
	if len(owners) == 1 {
		return owners[0], nil
	}
	return groups.Owner{}, nil
}

func (s *Store) credentialOwners(number string) ([]groups.Owner, error) {
	data, err := s.classifiedRoster()
	if err != nil {
		return nil, err
	}
	account, err := s.accountFrom(data, number)
	if err != nil {
		registry, registryErr := groups.LoadRegistry(s.backupDir)
		if registryErr != nil {
			return nil, registryErr
		}
		if owner, claimed := registry.Claims[number]; claimed {
			owner.Uncertain = true
			return []groups.Owner{owner}, nil
		}
		return nil, nil
	}
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return nil, err
	}
	backup, _ := s.ReadAccountCredentials(number, account.Email)
	owners := map[string]groups.Owner{}
	add := func(owner groups.Owner) {
		if old, ok := owners[owner.Scope]; ok {
			owner.Uncertain = owner.Uncertain || old.Uncertain
		}
		owners[owner.Scope] = owner
	}
	for slot, owner := range registry.Claims {
		if slot == number || owner.Account.SameIdentity(account) {
			if !owner.Account.SameIdentity(account) {
				return nil, fmt.Errorf("credential claim disagrees with Account-%s's roster identity", number)
			}
			if strings.HasPrefix(owner.Scope, "legacy:") {
				pids, err := profilePIDs(owner.ProfileDir)
				if err != nil {
					return nil, err
				}
				owner.PIDs = pids
				email, org, ok := sessprofile.ReadSessionIdentity(owner.ProfileDir)
				_, credsOK, err := s.ReadProfileCredentials(owner.ProfileDir)
				if err != nil {
					return nil, err
				}
				owner.Uncertain = owner.Uncertain || !ok || !credsOK || email != owner.Account.Email || org != owner.Account.OrgUUID
			}
			add(owner)
		}
	}
	for _, id := range groups.All() {
		journal, err := groups.LoadJournal(s.backupDir, id)
		if err != nil {
			return nil, err
		}
		if journal != nil {
			for _, candidate := range []*groups.Account{&journal.Target, journal.Previous} {
				if candidate != nil && (candidate.Number == number || candidate.SameIdentity(account)) {
					add(groups.Owner{Scope: string(id), ProfileDir: groups.ProfileDir(s.backupDir, id), Account: *candidate, Uncertain: true})
				}
			}
		}
		active, err := groups.LoadActive(s.backupDir, id)
		if err != nil {
			return nil, err
		}
		dir := groups.ProfileDir(s.backupDir, id)
		profileCreds, _, err := s.ReadProfileCredentials(dir)
		if err != nil {
			return nil, err
		}
		email, org, ok := sessprofile.ReadSessionIdentity(dir)
		if active != nil && (active.Number == number || active.SameIdentity(account)) {
			owner := groups.Owner{Scope: string(id), ProfileDir: dir, Account: *active, Uncertain: !ok || profileCreds == "" || email != active.Email || org != active.OrgUUID}
			if _, present := registry.Claims[active.Number]; !present {
				owner.Uncertain = true
			}
			add(owner)
		}
		if ok && (email == account.Email && org == account.OrgUUID || fingerprintsEqual(profileCreds, backup)) {
			add(groups.Owner{Scope: string(id), ProfileDir: dir, Account: account, Uncertain: active == nil || !active.SameIdentity(account)})
		}
		if !ok && profileCreds != "" && fingerprintsEqual(profileCreds, backup) {
			add(groups.Owner{Scope: string(id), ProfileDir: dir, Account: account, Uncertain: true})
		}
	}
	defaultDir := s.DefaultProfileDir()
	defaultConfig := s.DefaultConfigPath()
	email, org, ok := ccfile.ReadOAuthIdentityFrom(defaultConfig)
	pids, err := profilePIDs(defaultDir)
	if err != nil {
		return nil, err
	}
	if !ok && len(pids) > 0 {
		return nil, fmt.Errorf("default Claude sessions are running with an unreadable account identity")
	}
	defaultCreds, defaultErr := s.ReadDefaultCredentials()
	if defaultErr != nil && len(pids) > 0 {
		return nil, defaultErr
	}
	if ok && email == account.Email && org == account.OrgUUID || fingerprintsEqual(defaultCreds, backup) {
		add(groups.Owner{Scope: "default", ProfileDir: defaultDir, Account: account, PIDs: pids, Uncertain: !ok || email != account.Email || org != account.OrgUUID})
	}
	for _, slot := range sortedSlotKeys(data) {
		legacy, err := s.accountFrom(data, slot)
		if err != nil {
			return nil, err
		}
		dir := s.SessionDir(slot, legacy.Email)
		pids, err := profilePIDs(dir)
		if err != nil {
			return nil, err
		}
		email, org, ok := sessprofile.ReadSessionIdentity(dir)
		profileCreds, readable, err := s.ReadProfileCredentials(dir)
		if err != nil {
			return nil, err
		}
		if len(pids) == 0 && (!ok || !readable || profileCreds == "" || sessprofile.IsStale(dir)) {
			continue
		}
		if !ok && slot != number && !fingerprintsEqual(profileCreds, backup) {
			return nil, fmt.Errorf("live legacy Claude profile has an unreadable account identity: %s", dir)
		}
		if slot == number || ok && email == account.Email && org == account.OrgUUID || fingerprintsEqual(profileCreds, backup) {
			add(groups.Owner{Scope: "legacy:" + slot, ProfileDir: dir, Account: account, PIDs: pids, Uncertain: !ok || email != legacy.Email || org != legacy.OrgUUID})
		}
	}
	keys := make([]string, 0, len(owners))
	for scope := range owners {
		keys = append(keys, scope)
	}
	sort.Strings(keys)
	out := make([]groups.Owner, 0, len(keys))
	for _, scope := range keys {
		out = append(out, owners[scope])
	}
	return out, nil
}

func profilePIDs(dir string) ([]int, error) {
	seen := map[int]bool{}
	for registry, suffix := range map[string]string{"sessions": ".json", "ide": ".lock"} {
		registryDir := filepath.Join(dir, registry)
		entries, err := os.ReadDir(registryDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot verify credential ownership in %s: %w", registryDir, err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), suffix) {
				continue
			}
			path := filepath.Join(registryDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("cannot verify Claude process state at %s: %w", path, err)
			}
			var record struct {
				PID *int64 `json:"pid"`
			}
			if err := json.Unmarshal(data, &record); err != nil {
				return nil, fmt.Errorf("Claude process state at %s is unreadable; credential ownership is unknown", path)
			}
			if record.PID == nil || *record.PID < 0 {
				return nil, fmt.Errorf("Claude process state at %s has no valid PID; credential ownership is unknown", path)
			}
			pid := *record.PID
			if procdetect.IsPIDAlive(int(pid)) {
				seen[int(pid)] = true
			}
		}
	}
	pids := make([]int, 0, len(seen))
	for pid := range seen {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}

func VerifyProfileState(dir string) error { _, err := profilePIDs(dir); return err }

func (s *Store) EnsureAccountAvailable(number string) error {
	owner, err := s.CredentialOwner(number)
	if err != nil {
		return err
	}
	scope := string(s.group)
	if scope == "" {
		scope = "default"
	}
	if s.group != "" && s.groupHandoff && owner.Scope == "default" && len(owner.PIDs) == 0 && !owner.Uncertain {
		return nil
	}
	if s.group != "" && s.groupHandoff && strings.HasPrefix(owner.Scope, "legacy:") && len(owner.PIDs) == 0 && !owner.Uncertain {
		return nil
	}
	if owner.Scope != "" && (owner.Scope != scope || owner.Uncertain) {
		return cerr.Session("Account-%s is owned by %s%s; choose another account or reconcile that profile before switching", number, owner.Scope, uncertainSuffix(owner.Uncertain))
	}
	return nil
}

func (s *Store) SetGroupCapability(number string, intent groups.Intent, allowed bool) error {
	if intent != groups.Start && intent != groups.Continue {
		return fmt.Errorf("capability intent must be start or continue")
	}
	return s.Lock.With(func() error {
		data, err := s.SequenceForUpdate()
		if err != nil {
			return err
		}
		num, _, _, err := s.ResolveAccountFrom(data, number)
		if err != nil {
			return err
		}
		rec, _ := recordFor(data, num)
		if allowed {
			creds, _ := s.ReadAccountCredentials(num, strField(rec, "email"))
			plan, _ := oauth.ExtractOAuthData(creds)["subscriptionType"].(string)
			if decision := groups.Entitlement(groups.Fable, intent, plan, nil, nil); decision.Known && !decision.Allowed {
				return fmt.Errorf("Account-%s cannot be granted Fable capability: %s", num, decision.Reason)
			}
		}
		key := "fableStart"
		if intent == groups.Continue {
			key = "fableContinue"
		}
		rec[key] = allowed
		raw, err := encodeRecord(rec)
		if err != nil {
			return err
		}
		data.Accounts[num] = raw
		data.LastUpdated = s.timestamp()
		return s.WriteSequence(data)
	})
}

func uncertainSuffix(uncertain bool) string {
	if uncertain {
		return " (ownership uncertain)"
	}
	return ""
}

func (s *Store) ReadOwnedCredentials(number, email string) (string, error) {
	var value string
	err := s.Lock.With(func() error {
		var err error
		value, err = s.ReadOwnedCredentialsLocked(number, email)
		return err
	})
	return value, err
}

func (s *Store) ReadOwnedCredentialsLocked(number, email string) (string, error) {
	owner, err := s.CredentialOwner(number)
	if err != nil {
		return "", err
	}
	if owner.Uncertain {
		return "", fmt.Errorf("Account-%s credential ownership is uncertain", number)
	}
	if owner.Scope == "" {
		return s.ReadAccountCredentials(number, email)
	}
	if owner.Scope == "default" {
		value, err := s.ReadDefaultCredentials()
		profileEmail, profileOrg, ok := ccfile.ReadOAuthIdentityFrom(s.DefaultConfigPath())
		if !ok || profileEmail != owner.Account.Email || profileOrg != owner.Account.OrgUUID {
			return "", fmt.Errorf("default account identity changed while reading its credential")
		}
		return value, err
	}
	value, ok, err := s.ReadProfileCredentials(owner.ProfileDir)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("Account-%s's owning profile credential is unreadable", number)
	}
	profileEmail, profileOrg, identityOK := sessprofile.ReadSessionIdentity(owner.ProfileDir)
	if !identityOK || profileEmail != owner.Account.Email || profileOrg != owner.Account.OrgUUID {
		return "", fmt.Errorf("owning profile identity changed while reading Account-%s", number)
	}
	return value, nil
}

func (s *Store) GroupStatuses() ([]groups.Status, error) {
	out := make([]groups.Status, 0, 2)
	registry, registryErr := groups.LoadRegistry(s.backupDir)
	for _, id := range groups.All() {
		status := groups.Status{ID: id, Label: id.Label(), ProfileDir: groups.ProfileDir(s.backupDir, id)}
		if registryErr != nil {
			status.Blocker = registryErr.Error()
		}
		active, err := groups.LoadActive(s.backupDir, id)
		if err != nil {
			status.Blocker = err.Error()
		} else if active != nil {
			status.ActiveNumber = &active.Number
			status.Email = active.Email
			status.Enabled = true
			owner, oerr := s.CredentialOwner(active.Number)
			if oerr != nil {
				status.Blocker = oerr.Error()
			} else if owner.Uncertain || owner.Scope != string(id) {
				status.Blocker = "credential ownership needs reconciliation"
			}
		} else if registry != nil {
			for _, owner := range registry.Claims {
				if owner.Scope == string(id) {
					status.Blocker = "credential claim has no group active state; ownership needs reconciliation"
				}
			}
		}
		journal, err := groups.LoadJournal(s.backupDir, id)
		status.Pending = journal != nil || err != nil
		if err != nil {
			status.Blocker = err.Error()
		} else if journal != nil {
			status.Blocker = "unfinished account switch; reconcile before launch"
		}
		pids, err := profilePIDs(status.ProfileDir)
		status.LiveSessions = len(pids)
		if err != nil {
			status.Blocker = err.Error()
		}
		out = append(out, status)
	}
	return out, nil
}

func (s *Store) GroupCompatibility(number string, intent groups.Intent) groups.Compatibility {
	if s.group == "" {
		return groups.Compatibility{Known: true, Allowed: true}
	}
	data, err := s.ReadSequence()
	if err != nil || data == nil {
		return groups.Compatibility{Reason: "account roster is unreadable"}
	}
	rec, ok := recordFor(data, number)
	if !ok {
		return groups.Compatibility{Reason: "account is absent from the roster"}
	}
	if strField(rec, "kind") == "api_key" {
		return groups.Compatibility{Known: true, Reason: "session groups require OAuth accounts"}
	}
	creds, _ := s.ReadAccountCredentials(number, strField(rec, "email"))
	plan := strField(rec, "subscriptionType")
	if value, ok := oauth.ExtractOAuthData(creds)["subscriptionType"].(string); ok {
		plan = value
	}
	boolPtr := func(key string) *bool {
		if value, ok := rec[key].(bool); ok {
			return &value
		}
		return nil
	}
	return groups.Entitlement(s.group, intent, plan, boolPtr("fableStart"), boolPtr("fableContinue"))
}
