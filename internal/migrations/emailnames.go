// email_file_names: renames the per-account backup files of a store written
// before the security pass, whose names carry the raw email
// (.claude-config-<n>-<email>.json, .creds-<n>-<email>.enc[.prev]), to the
// encoded names storenames builds (.claude-config-<n>-<EmailKey>.json, …).
//
// It runs on every platform (config backups are files everywhere; on macOS
// the credentials live in the Keychain, whose account names do not change).
// Only accounts whose email is a plain address are touched: a raw name built
// from any other email could point outside the store, so such a slot is left
// alone with a warning and the user re-adds it.
package migrations

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/storenames"
)

func migrateEmailFileNames(host Host) (completed bool, notices []string, err error) {
	accounts, ok := host.SequenceAccounts()
	if !ok {
		return false, nil, nil // no readable roster yet: retry when there is one
	}
	nums := make([]string, 0, len(accounts))
	for n := range accounts {
		nums = append(nums, n)
	}
	sort.Strings(nums)

	renamed, failed := 0, 0
	for _, num := range nums {
		email := accounts[num]
		if email == "" {
			continue
		}
		if !storenames.ValidEmail(email) {
			host.Logger().Warningf("email_file_names: slot %s has an email that is not a plain address; its files were not renamed (remove and re-add the account)", num)
			continue
		}
		type pair struct{ dir, from, to string }
		pairs := []pair{{host.ConfigsDir(), storenames.LegacyConfigFile(num, email), storenames.ConfigFile(num, email)}}
		for _, n := range []string{num, "None"} {
			pairs = append(pairs,
				pair{host.CredentialsDir(), storenames.LegacyCredsFile(n, email), storenames.CredsFile(n, email)},
				pair{host.CredentialsDir(), storenames.LegacyCredsPrevFile(n, email), storenames.CredsPrevFile(n, email)})
		}
		for _, p := range pairs {
			moved, rerr := renameLegacy(filepath.Join(p.dir, p.from), filepath.Join(p.dir, p.to))
			if rerr != nil {
				host.Logger().Warningf("email_file_names: %v", rerr)
				// A conflict is not retried: no later run can resolve it.
				if !errors.Is(rerr, errNameConflict) {
					failed++
				}
				continue
			}
			if moved {
				renamed++
			}
		}
	}
	if renamed > 0 {
		notices = append(notices, fmt.Sprintf("tycswap: renamed %d backup file(s) to encoded names", renamed))
	}
	if failed > 0 {
		return false, notices, cerr.MigrationIncomplete("%d backup file(s) could not be renamed; will retry on next run", failed)
	}
	return true, notices, nil
}

// errNameConflict marks a legacy file whose encoded counterpart already exists
// with different content.
var errNameConflict = errors.New("both the raw-email and the encoded name exist with different content")

// renameLegacy moves from to to. An absent from is nothing to do. When to
// already exists, an identical from is removed; a different one is a conflict
// and both are left as they are (the encoded name is the one read).
func renameLegacy(from, to string) (moved bool, err error) {
	fi, err := os.Lstat(from)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file; left in place", from)
	}
	if _, err := os.Lstat(to); err == nil {
		a, aerr := os.ReadFile(from)
		b, berr := os.ReadFile(to)
		if aerr != nil || berr != nil {
			return false, fmt.Errorf("compare %s with %s: %v %v", from, to, aerr, berr)
		}
		if !bytes.Equal(a, b) {
			return false, fmt.Errorf("%w: %s, %s; the encoded one is used, remove the other by hand", errNameConflict, from, to)
		}
		return false, os.Remove(from)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	return true, os.Rename(from, to)
}
