// One Go Keychain backend serves both services, so every Keychain failure is hard and retries next run (DESIGN A9).

package migrations

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/storenames"
)

// securityService is Python's SECURITY_SERVICE (credentials.py) — the
// security-CLI-backed Keychain service tycswap's own per-account backups live
// under. Duplicated from internal/credstore's private constant of the same
// name/value (spec 07§7's "external system knowledge": a stable cross-package
// contract, not an accidental copy) because credstore.Store exposes no
// Keychain-only *delete* primitive narrow enough for this migration's
// discard-a-bad-write step — only the broader best-effort DeleteBackup sweep,
// which would also touch an unrelated .enc file.
const securityService = keychain.BackupService

// backupUsername mirrors credstore's private per-account Keychain username
// scheme exactly ("account-{num}-{email}"), needed only for the Keychain-only
// discard-on-mismatch below (see securityService's doc comment).
func backupUsername(num, email string) string { return "account-" + num + "-" + email }

// migrateMacOSKeyringToSecurity returns (completed, notices, err) per the
// package doc's migrationFunc contract, mirroring migrateWindowsKeyringToFiles
// but against the Keychain-only credstore primitives and with the extra
// security-service pre-check spec 07§5.4 has and Windows doesn't.
func migrateMacOSKeyringToSecurity(host Host) (completed bool, notices []string, err error) {
	if host.Platform() != platform.MacOS {
		return false, nil, nil
	}
	accounts, ok := host.SequenceAccounts()
	if !ok {
		return false, nil, nil // never mark applied — see windows.go's identical comment
	}
	if len(accounts) == 0 {
		return true, nil, nil // Readable sequence, nothing to migrate → done.
	}

	store := host.Creds()

	// Read the Keychain directly (KCReadBackup): a fallback .enc must never count as migrated; a failure defers via MigrationIncomplete.
	pending := map[string]string{}
	for num, email := range accounts {
		v, err := store.KCReadBackup(num, email)
		if err != nil {
			return false, nil, cerr.MigrationIncomplete("Keychain unavailable, deferring macOS keyring migration: %v", err)
		}
		if v == "" {
			pending[num] = email
		}
	}
	if len(pending) == 0 {
		return true, nil, nil // All accounts already in the security service.
	}

	kc := host.Keychain()
	inFile := map[string]bool{} // slots whose credential went to the .enc file
	migrated, failed := relocate(relocateConfig{
		label:       "macos_keyring_to_security",
		pending:     pending,
		allAccounts: accounts,
		readLegacy: func(username string) (string, error) {
			v, found, err := kc.Get(legacyKeyringService, username)
			if err != nil {
				return "", err
			}
			if !found {
				return "", nil
			}
			return v, nil
		},
		deleteLegacy: func(username string) {
			if err := kc.Delete(legacyKeyringService, username); err != nil {
				host.Logger().Warningf("macos_keyring_to_security: best-effort delete of %s failed: %v", username, err)
			}
		},
		writeNew: func(num, email, creds string) error {
			err := store.KCWriteBackup(num, email, creds)
			if keychain.IsTooLarge(err) {
				// Too large for `security -i`'s stdin line (the keyring
				// library had no such limit): the 0600 .enc file, which
				// ReadBackup serves first, holds it instead.
				inFile[num] = true
				return store.WriteBackup(num, email, creds)
			}
			return err
		},
		readNew: func(num, email string) (string, error) {
			if inFile[num] {
				return store.ReadBackup(num, email)
			}
			return store.KCReadBackup(num, email)
		},
		deleteBadNew: func(num, email string) {
			if inFile[num] {
				if err := os.Remove(filepath.Join(host.CredentialsDir(), storenames.CredsFile(num, email))); err != nil && !errors.Is(err, fs.ErrNotExist) {
					host.Logger().Warningf("Failed to delete the credentials file: %v", err)
				}
				return
			}
			if err := kc.Delete(securityService, backupUsername(num, email)); err != nil {
				host.Logger().Warningf("Failed to delete credentials from Keychain: %v", err)
			}
		},
		afterSuccess: func(num, email, sourceUsername string) {
			// keyring's PasswordDeleteError covers both not-found and a denied prompt, so check explicitly.
			if kc.Exists(legacyKeyringService, sourceUsername) {
				host.Logger().Warningf(
					"macos_keyring_to_security: legacy keyring entry %s was left behind (delete failed or was denied); harmless — remove manually or via purge",
					sourceUsername)
			}
		},
		log: host.Logger(),
	})

	if migrated > 0 {
		notices = append(notices, fmt.Sprintf(
			"tycswap: migrated %d macOS credential(s) from the keyring into the Keychain via security", migrated))
	}
	if failed > 0 {
		return false, notices, cerr.MigrationIncomplete(
			"%d account(s) could not be migrated to the security service; will retry on next run", failed)
	}
	return true, notices, nil
}
