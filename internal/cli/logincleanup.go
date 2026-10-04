package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/lifecycle"
)

const loginCleanupMarker = ".cleanup-pending"

// makeLoginScratch registers cleanup for normal exit and Ctrl-C. A failed
// deletion retains the private directory and a non-secret marker so the next
// add --login can retry. Unmarked profiles may still be running and are ignored.
func makeLoginScratch(root string, kc keychain.KeychainClient) (dir string, remove func() error, err error) {
	dir, err = os.MkdirTemp(root, "login.")
	if err != nil {
		return "", nil, err
	}
	var mu sync.Mutex
	done := false
	var id uint64
	remove = func() error {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return nil
		}
		err := cleanupLoginDir(root, dir, kc, true)
		if err == nil {
			done = true
			lifecycle.Unregister(id)
		}
		return err
	}
	// Holding mu until registration finishes also protects id against SIGINT.
	mu.Lock()
	id = lifecycle.RegisterCleanup(func() {
		if err := remove(); err != nil {
			errorTo(os.Stderr, "Warning: "+err.Error())
		}
	})
	mu.Unlock()
	return dir, remove, nil
}

func retryLoginCleanups(root string, kc keychain.KeychainClient) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "login.") {
			continue
		}
		if err := cleanupLoginDir(root, filepath.Join(root, entry.Name()), kc, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func cleanupLoginDir(root, dir string, kc keychain.KeychainClient, mark bool) error {
	// This is a separate lock: cleanup neither holds nor needs the roster lock.
	err := filelock.New(filepath.Join(root, ".login-cleanup.lock"), 0).With(func() error {
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return nil
		} // a previous attempt already finished
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("scratch profile is not a directory")
		}
		marker := filepath.Join(dir, loginCleanupMarker)
		if mark {
			// Exclusive creation refuses a pre-existing symlink. The marker only
			// records which cleanup backend is required, never a credential.
			f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err == nil {
				backend := "file"
				if kc != nil {
					backend = "keychain"
				}
				_, writeErr := f.WriteString(backend)
				closeErr := f.Close()
				if err := errors.Join(writeErr, closeErr); err != nil {
					return err
				}
			} else if !os.IsExist(err) {
				return err
			}
		}
		mi, err := os.Lstat(marker)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !mi.Mode().IsRegular() {
			return fmt.Errorf("cleanup marker is not a regular file")
		}
		backend, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		switch string(backend) {
		case "keychain":
			if kc == nil {
				return fmt.Errorf("Keychain is unavailable")
			}
			// Only the OAuth item derived from this scratch directory is ours.
			// Never delete the shared Console/API-key item or the live login.
			if err := kc.Delete(lifecycle.LoginKeychainService(dir), keychain.AccountName()); err != nil {
				return fmt.Errorf("scratch Keychain item could not be deleted")
			}
		case "file":
		default:
			return fmt.Errorf("unrecognized cleanup marker")
		}
		// Keep the recovery marker until every other entry is gone. RemoveAll
		// on the whole directory could delete the marker and then fail midway.
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == loginCleanupMarker {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
		if err := os.Remove(marker); err != nil {
			return err
		}
		if err := os.Remove(dir); err != nil {
			// A late writer may have added another file. Preserve retry state.
			f, markerErr := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if markerErr == nil {
				_, markerErr = f.Write(backend)
				markerErr = errors.Join(markerErr, f.Close())
			}
			return errors.Join(err, markerErr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("login cleanup incomplete at %s: %v; retained for retry on the next add --login", dir, err)
	}
	return nil
}
