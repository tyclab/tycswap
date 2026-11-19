package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/slotkey"
)

// ErrFableUnsupported refuses the Fable store on macOS, where Claude Code
// keeps its login in the Keychain (DESIGN A100).
var ErrFableUnsupported = errors.New("The Fable store needs a file-based credential store; macOS keeps Claude Code's login in the Keychain")

// FableState is whether the Fable store is on: it has Fable logins and holds
// one; usable unless a quarantine entry names that login. Reason says why it
// is not on or not usable.
type FableState struct {
	On     bool
	Live   LiveSlot
	Usable bool
	Reason string
}

// ForFable is s's Fable scope: the accounts' Fable logins as its backups, the
// Fable store as its live credential, and its own state file and chain record
// under <root>/fable. It shares the roster, the usage cache and the store
// lock, and touches no disk.
func (s *Store) ForFable() (*Store, error) {
	if s.fable {
		return s, nil
	}
	if s.Platform == platform.MacOS {
		return nil, ErrFableUnsupported
	}
	clone := *s
	clone.defaultCreds = s.Creds
	if s.sharedCreds != nil {
		clone.defaultCreds = s.sharedCreds
	}
	clone.fable, clone.group, clone.sharedCreds = true, "", nil
	clone.CredentialsDir = paths.FableCredentialsDir(s.backupDir)
	clone.Creds = credstore.New(credstore.Config{
		Platform:       s.Platform,
		CredentialsDir: clone.CredentialsDir,
		ActivePath:     filepath.Join(paths.FableStoreDir(s.backupDir), ccfile.CredentialsFileName),
		FileOnly:       true,
	}, s.kc, s.Clk, s.Log)
	return &clone, nil
}

// defaultScope is the default store a Fable scope was made from: the default
// login's resolution reads its backups and chain record, whatever the scope.
func (s *Store) defaultScope() *Store {
	if !s.fable {
		return s
	}
	d := *s
	d.fable, d.Creds, d.defaultCreds = false, s.defaultCreds, nil
	d.CredentialsDir = filepath.Join(s.backupDir, "credentials")
	return &d
}

// IsFable reports whether s is a Fable scope.
func (s *Store) IsFable() bool { return s.fable }

func (s *Store) fableScope() *Store {
	f, err := s.ForFable()
	if err != nil {
		return nil
	}
	return f
}

// SetupFableDirectories creates credentials-fable/, fable/ and fable/store/
// with 0700. Only writers call it: no read creates the scope.
func (s *Store) SetupFableDirectories() error {
	for _, dir := range []string{paths.FableCredentialsDir(s.backupDir), paths.FableScopeDir(s.backupDir), paths.FableStoreDir(s.backupDir)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if !platform.IsWindows() {
			if err := os.Chmod(dir, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}

// WithFableLocks runs fn under the Fable store's locks, in order: the tycswap
// store lock, then Claude Code's credential-refresh and storage-write locks of
// the Fable store. The default store's Claude Code locks are not taken. The
// store lock keeps the profile endpoint out of fn.
func (s *Store) WithFableLocks(fn func() error) error {
	f, err := s.ForFable()
	if err != nil {
		return err
	}
	dir := paths.FableStoreDir(f.backupDir)
	return f.Lock.With(func() error {
		cred, err := cclock.AcquireProfileCredentials(dir, 0, f.Clk)
		if err != nil {
			return err
		}
		defer cred.Release()
		storage, err := cclock.AcquireProfileStorageWrite(dir, 0, f.Clk)
		if err != nil {
			return err
		}
		defer storage.Release()
		return fn()
	})
}

// HasFableLogins reports whether a roster account has a readable Fable login.
func (s *Store) HasFableLogins() bool {
	f := s.fableScope()
	if f == nil {
		return false
	}
	if _, err := os.Stat(f.CredentialsDir); err != nil {
		return false
	}
	data, _ := f.ReadSequence()
	if data == nil {
		return false
	}
	for _, num := range sortedSlotKeys(data) {
		if f.hasFableLogin(data, num) {
			return true
		}
	}
	return false
}

// HasFableLogin reports whether account num has a readable Fable login.
func (s *Store) HasFableLogin(num string) bool {
	f := s.fableScope()
	if f == nil {
		return false
	}
	data, _ := f.ReadSequence()
	return f.hasFableLogin(data, num)
}

func (s *Store) hasFableLogin(data *SequenceData, num string) bool {
	rec, ok := recordFor(data, num)
	if !ok {
		return false
	}
	creds, _ := s.ReadAccountCredentials(num, strField(rec, "email"))
	return creds != ""
}

// FableStore is the Fable store's state. Its live slot comes from backup or
// chain only: no network, so start can ask it.
func (s *Store) FableStore() FableState {
	f := s.fableScope()
	if f == nil {
		return FableState{Reason: ErrFableUnsupported.Error()}
	}
	if !f.HasFableLogins() {
		return FableState{Reason: "no Fable login"}
	}
	live, _, err := f.Creds.ReadActive()
	if err != nil || live == "" {
		return FableState{Reason: "the store is empty"}
	}
	data, _ := f.ReadSequence()
	st := FableState{On: true, Live: f.fableLiveSlot(data, live, false)}
	if num, dead := f.deadFableLogin(live); dead {
		st.Reason = "Account-" + num + "'s Fable login: sign in again"
		return st
	}
	st.Usable = true
	return st
}

// deadFableLogin is the slot of a quarantine entry in the Fable state file,
// whatever its reason (invalid_grant, identity-conflict), whose fingerprint
// is the live credential's.
func (s *Store) deadFableLogin(live string) (string, bool) {
	fp := oauth.CredentialFingerprint(live)
	entries := autoswitch.ReadQuarantineEntries(s.StatePath())
	if fp == nil || len(entries) == 0 {
		return "", false
	}
	// ReadQuarantineEntries leaves the fingerprint out; the entry carries it.
	var state struct {
		Quarantine map[string]struct {
			Fingerprint *string `json:"refreshTokenFingerprint"`
		} `json:"quarantine"`
	}
	raw, err := os.ReadFile(s.StatePath())
	if err != nil || json.Unmarshal(raw, &state) != nil {
		return "", false
	}
	nums := make([]string, 0, len(entries))
	for num := range entries {
		nums = append(nums, num)
	}
	for _, num := range slotkey.Sorted(nums) {
		if got := state.Quarantine[num].Fingerprint; got != nil && *got == *fp {
			return num, true
		}
	}
	return "", false
}

// ResolveFableLiveSlot maps the Fable store's live login to a slot: a Fable
// login holding its lineage, then the Fable chain record, then the profile
// endpoint, else "". ~/.claude.json is never read: every session writes its
// own account there.
func (s *Store) ResolveFableLiveSlot(data *SequenceData) LiveSlot {
	f := s.fableScope()
	if f == nil {
		return LiveSlot{}
	}
	live, _, err := f.Creds.ReadActive()
	if err != nil || live == "" {
		return LiveSlot{}
	}
	return f.fableLiveSlot(data, live, true)
}

// fableLiveSlot resolves live on the Fable scope f; network allows the
// profile endpoint, which the store lock also keeps out.
func (s *Store) fableLiveSlot(data *SequenceData, live string, network bool) LiveSlot {
	if data == nil {
		return LiveSlot{}
	}
	if y, ok := s.fableBackupSlot(data, live); ok {
		return LiveSlot{Slot: y, Source: SourceBackup}
	}
	if y, id := s.fableChainSlot(data, live); y != "" {
		return LiveSlot{Slot: y, Source: SourceChain, Identity: id}
	}
	if !network {
		return LiveSlot{}
	}
	if y, id := s.fableProfileSlot(data, live); y != "" {
		return LiveSlot{Slot: y, Source: SourceProfile, Identity: id}
	}
	return LiveSlot{}
}

// fableBackupSlot is the one OAuth slot whose Fable login holds live's
// lineage; a login copied into two slots is no evidence.
func (s *Store) fableBackupSlot(data *SequenceData, live string) (string, bool) {
	match := ""
	for _, num := range sortedSlotKeys(data) {
		if kindFrom(data, num) != "oauth" || !s.holdsLineage(data, num, live) {
			continue
		}
		if match != "" {
			return "", false
		}
		match = num
	}
	return match, match != ""
}

func (s *Store) fableChainSlot(data *SequenceData, live string) (string, *oauth.Identity) {
	fp := oauth.CredentialFingerprint(live)
	rec, ok := s.readLiveLineage()
	if fp == nil || !ok || rec.Fingerprint != *fp {
		return "", nil
	}
	id := rec.Identity.oauth()
	if slot := s.oauthIdentitySlot(data, &id); slot != "" && slot == rec.Slot {
		return slot, &id
	}
	return "", nil
}

func (s *Store) fableProfileSlot(data *SequenceData, live string) (string, *oauth.Identity) {
	fp := oauth.CredentialFingerprint(live)
	if fp == nil {
		return "", nil
	}
	id := s.profileIdentity(live)
	if id == nil {
		return "", nil
	}
	slot := s.oauthIdentitySlot(data, id)
	if slot == "" {
		return "", nil
	}
	if s.LineageWrites() {
		if err := s.writeLiveLineage(*fp, slot, id); err != nil && s.Log != nil {
			s.Log.Debugf("Could not record the Fable store's live lineage: %v", err)
		}
	}
	return slot, id
}

func (s *Store) oauthIdentitySlot(data *SequenceData, id *oauth.Identity) string {
	slot := s.SlotForIdentity(data, id)
	if slot == "" || kindFrom(data, slot) != "oauth" {
		return ""
	}
	return slot
}

// RecordFableLive writes the Fable chain record for creds, installed in the
// Fable store for account num, with the roster's identity of num: the Fable
// store's last install (DESIGN A100).
func (s *Store) RecordFableLive(num, creds string) error {
	f, err := s.ForFable()
	if err != nil {
		return err
	}
	fp := oauth.CredentialFingerprint(creds)
	data, err := f.ReadSequence()
	rec, ok := recordFor(data, num)
	if fp == nil || err != nil || !ok {
		return err
	}
	id := oauth.Identity{UUID: strField(rec, "uuid"), Email: strField(rec, "email"), OrgUUID: strField(rec, "organizationUuid")}
	return f.writeLiveLineage(*fp, num, &id)
}

// ForgetFableLive removes the Fable chain record once the Fable store holds
// no login, so no later login takes an emptied store's last install as its
// account (DESIGN A100).
func (s *Store) ForgetFableLive() error {
	f, err := s.ForFable()
	if err != nil {
		return err
	}
	return f.forgetLiveLineage()
}

// RefreshFableBackupGuarded is RefreshBackupGuarded on account num's Fable
// login: under the store lock, with the lineage re-checked there, it
// refreshes and writes that login's file only. A login live in the Fable
// store is Claude Code's to refresh. invalid_grant comes back in the outcome.
func (s *Store) RefreshFableBackupGuarded(ctx context.Context, c oauth.Client, num, email, held string) oauth.RefreshOutcome {
	out := oauth.RefreshOutcome{Error: RefreshDeclined}
	f := s.fableScope()
	if f == nil {
		return out
	}
	err := f.Lock.With(func() error {
		live, _, err := f.Creds.ReadActive()
		if err != nil {
			return nil
		}
		if live != "" {
			data, _ := f.ReadSequence()
			if f.fableLiveSlot(data, live, false).Slot == num {
				return nil
			}
		}
		backup, _ := f.ReadAccountCredentials(num, email)
		if backup == "" {
			return nil
		}
		if !fingerprintsEqual(backup, held) {
			if oauth.ExtractAccessToken(backup) != "" {
				out = oauth.RefreshOutcome{Credentials: backup}
			}
			return nil
		}
		reqCtx, cancel := context.WithTimeout(ctx, GuardedRefreshTimeout)
		defer cancel()
		out = c.Refresh(reqCtx, backup)
		if out.Credentials == "" {
			return nil
		}
		if werr := f.WriteAccountCredentials(num, email, out.Credentials); werr != nil {
			if f.Log != nil {
				f.Log.Warningf("Refreshed the Fable login of account %s but could not store it: %v", num, werr)
			}
			out = oauth.RefreshOutcome{Error: oauth.ErrRefreshFailed}
		}
		return nil
	})
	if err != nil && f.Log != nil {
		f.Log.Debugf("Skipped the Fable login refresh for account %s: %v", num, err)
	}
	return out
}

// RefreshFableLiveGuarded refreshes the Fable store's live login, account
// num's, under the Fable store's locks: Claude Code's credential-refresh lock
// keeps its own refresh out, so a rejected grant is the login's verdict, not
// a lost race. A login rotated since held was read comes back as it is. The
// refreshed login goes into the store, with its seat-wide keys, and into num's
// Fable login file. invalid_grant comes back in the outcome (DESIGN A102).
func (s *Store) RefreshFableLiveGuarded(ctx context.Context, c oauth.Client, num, email, held string) oauth.RefreshOutcome {
	out := oauth.RefreshOutcome{Error: RefreshDeclined}
	f := s.fableScope()
	if f == nil {
		return out
	}
	err := f.WithFableLocks(func() error {
		live, _, err := f.Creds.ReadActive()
		if err != nil || live == "" {
			return nil
		}
		if live != held {
			out = oauth.RefreshOutcome{Credentials: live}
			return nil
		}
		data, _ := f.ReadSequence()
		if f.fableLiveSlot(data, live, false).Slot != num {
			return nil
		}
		reqCtx, cancel := context.WithTimeout(ctx, GuardedRefreshTimeout)
		defer cancel()
		out = c.Refresh(reqCtx, live)
		if out.Credentials == "" {
			return nil
		}
		err = f.Creds.WriteActiveAccount(out.Credentials)
		if err == nil {
			err = f.WriteAccountCredentials(num, email, oauth.AccountOnly(out.Credentials))
		}
		if err != nil {
			if f.Log != nil {
				f.Log.Warningf("Refreshed the Fable store's login of account %s but could not store it: %v", num, err)
			}
			out = oauth.RefreshOutcome{Error: oauth.ErrRefreshFailed}
		}
		return nil
	})
	if err != nil && f.Log != nil {
		f.Log.Debugf("Skipped the Fable store's login refresh for account %s: %v", num, err)
	}
	return out
}

// HarvestFableLive stores a Fable login Claude Code rotated in the Fable store
// in the Fable login file of the slot the chain record or the profile named,
// under the Fable store's locks and re-checked there. The live credential is
// never written. A WithoutLineageWrites view never harvests.
func (s *Store) HarvestFableLive() bool {
	f := s.fableScope()
	if f == nil || !f.LineageWrites() {
		return false
	}
	live, _, err := f.Creds.ReadActive()
	if err != nil || live == "" {
		return false
	}
	data, _ := f.ReadSequence()
	ls := f.fableLiveSlot(data, live, true)
	if ls.Source != SourceChain && ls.Source != SourceProfile {
		return false
	}
	rec, _ := recordFor(data, ls.Slot)
	email := strField(rec, "email")
	if backup, _ := f.ReadAccountCredentials(ls.Slot, email); fingerprintsEqual(backup, live) {
		return false
	}
	harvested := false
	err = f.WithFableLocks(func() error {
		now, _, err := f.Creds.ReadActive()
		current, _ := f.ReadSequence()
		if rec, ok := recordFor(current, ls.Slot); err != nil || now != live || !ok || strField(rec, "email") != email {
			return nil
		}
		if err := f.WriteAccountCredentials(ls.Slot, email, oauth.AccountOnly(live)); err != nil {
			return err
		}
		harvested = true
		return nil
	})
	switch {
	case f.Log == nil:
	case err != nil:
		f.Log.Debugf("Skipped harvesting the Fable store's rotated login into Account-%s: %v", ls.Slot, err)
	case harvested:
		f.Log.Infof("Harvested the Fable store's rotated login into Account-%s's Fable login", ls.Slot)
	}
	return harvested
}
