package store

import (
	"errors"
	"github.com/tyclab/tycswap/internal/atomicfile"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/mappings"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/storenames"
)

func (s *Store) configBackupPath(num, email string) string {
	return filepath.Join(s.ConfigsDir, storenames.ConfigFile(num, email))
}

func (s *Store) ReadAccountCredentials(num, email string) (string, error) {
	creds, err := s.Creds.ReadBackup(num, email)
	if ccfile.SeatWideOnly(creds) {
		return "", err
	}
	return creds, err
}

func (s *Store) ReadAccountConfig(num, email string) (string, error) {
	data, err := os.ReadFile(s.configBackupPath(num, email))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func (s *Store) WriteAccountConfig(num, email, config string) error {
	// Atomic (temp file, fsync, rename): a crash mid-write must not leave an
	// empty config backup, which would make the slot unswitchable.
	return atomicfile.Write(s.configBackupPath(num, email), []byte(config), atomicfile.Opts{})
}

// DeleteConfigBackup unconditionally unlinks a slot's config backup, treating a
// missing file as success (spec 01§10.5 _delete_config_backup). It is never
// exists()-guarded — that would fail open on an inaccessible dir in the
// required-clear paths — so permission/I/O errors propagate.
func (s *Store) DeleteConfigBackup(num, email string) error {
	err := os.Remove(s.configBackupPath(num, email))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// WriteAccountCredentials writes a slot's backup credential and then invalidates
// the slot's session profile exactly once (spec 01§3.2 / the switcher wrapper).
// The store raises on write failure before _post_backup_write runs.
func (s *Store) WriteAccountCredentials(num, email, creds string) error {
	if err := s.Creds.WriteBackup(num, email, creds); err != nil {
		return err
	}
	s.postBackupWrite(num, email)
	return nil
}

// postBackupWrite is _post_backup_write (spec 01§7): a LIVE session keeps its own
// credential copy but is stale-marked so setup_session re-bootstraps it once it
// exits; a non-live profile has its credential material dropped immediately so
// the next `tycswap run` re-bootstraps from the fresh backup (history preserved).
func (s *Store) postBackupWrite(num, email string) {
	dir := s.SessionDir(num, email)
	if len(sessprofile.LiveSessionPIDs(dir)) > 0 {
		sessprofile.MarkStale(dir)
		return
	}
	existed, _ := sessprofile.InvalidateSessionCredentials(s.kc, dir)
	if existed && s.Log != nil {
		s.Log.Infof("Invalidated session credentials for account %s", num)
	}
}

// DeleteAccountFiles is the single chokepoint for every path that removes or
// displaces a slot (spec 01§7 _delete_account_files): refuse while a live
// session-mode instance holds the slot, then delete the backup credential, the
// config file, and the session profile (Keychain entry before the dir).
func (s *Store) DeleteAccountFiles(num, email string) error {
	if err := s.EnsureNoLiveSession(num, email, "the operation"); err != nil {
		return err
	}
	_ = s.Creds.DeleteBackup(num, email)
	if err := os.Remove(s.configBackupPath(num, email)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.DeleteSessionProfile(num, email)
	registry, err := groups.LoadRegistry(s.backupDir)
	if err != nil {
		return err
	}
	if owner, claimed := registry.Claims[num]; claimed && owner.Scope == "legacy:"+num {
		if _, err := os.Stat(owner.ProfileDir); !os.IsNotExist(err) {
			return errors.New("the legacy profile could not be fully removed; its credential claim remains held")
		}
		delete(registry.Claims, num)
		if err := registry.Save(s.backupDir); err != nil {
			return err
		}
	}
	return nil
}

// PruneMappings drops directory mappings for an identity that no longer has a
// slot, returning the number pruned (spec 01§7 _prune_mappings). Slot migration
// and swap keep the (email, org) identity, so they never call this.
func (s *Store) PruneMappings(email, orgUUID string) (int, error) {
	return mappings.New(s.backupDir).PruneAccount(email, orgUUID)
}

// PersistBackupCredentials writes a rotated credential to an inactive slot's
// backup store under the account FileLock (spec 01§ persist_backup_credentials).
// The caller must NOT already hold the FileLock (it is non-reentrant).
func (s *Store) PersistBackupCredentials(num, email, creds string) error {
	return s.Lock.With(func() error {
		owner, err := s.CredentialOwner(num)
		if err != nil {
			return err
		}
		if owner.Scope != "" {
			return errors.New("cannot overwrite the credential backup while a profile owns the account")
		}
		return s.WriteAccountCredentials(num, email, creds)
	})
}

// BackfillAccountUUID records a resolved account uuid on a slot that lacks one,
// under the FileLock (spec 01§ backfill_account_uuid). It only ever fills an
// EMPTY uuid — an existing uuid is identity and is never rewritten. An empty
// uuid argument is a no-op. The caller must NOT already hold the FileLock.
func (s *Store) BackfillAccountUUID(num, uuid string) error {
	if uuid == "" {
		return nil
	}
	return s.Lock.With(func() error {
		data, err := s.ReadSequence()
		if err != nil || data == nil {
			return err
		}
		rec, ok := recordFor(data, num)
		if !ok {
			return nil
		}
		if strings.TrimSpace(strField(rec, "uuid")) != "" {
			return nil
		}
		rec["uuid"] = uuid
		nb, err := encodeRecord(rec)
		if err != nil {
			return err
		}
		data.Accounts[num] = nb
		data.LastUpdated = s.timestamp()
		return s.WriteSequence(data)
	})
}
