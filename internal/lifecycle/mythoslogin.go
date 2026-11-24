package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

func AddMythosLogin(s *store.Store, src AddSource, slot *int) (string, error) {
	if !src.isLoginDir() {
		return "", errLoginIncomplete()
	}
	creds, _, err := src.material(s)
	if err != nil {
		return "", err
	}
	email, org, ok := src.identity(s)
	if !ok || oauth.CredentialFingerprint(creds) == nil {
		return "", errLoginIncomplete()
	}
	var num string
	err = s.WithRosterLocked(func(data *store.SequenceData) error {
		num = s.FindAccountSlot(data, email, org)
		if num == "" || s.AccountKindFor(num) != "oauth" {
			return fmt.Errorf("add this Claude account before enrolling its Mythos login")
		}
		if slot != nil && strconv.Itoa(*slot) != num {
			return fmt.Errorf("this sign-in belongs to Account-%s, not the requested slot", num)
		}
		if store.DisabledFrom(data, num) || !store.MythosFrom(data, num) {
			return fmt.Errorf("Account-%s must be enabled and marked for Mythos", num)
		}
		if err := refuseDefaultGrant(s, num, email, creds); err != nil {
			return err
		}
		f, err := s.ForFable()
		if err != nil {
			return err
		}
		backup, err := f.ReadAccountCredentials(num, email)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		live, _, err := f.Creds.ReadActive()
		if err != nil {
			return err
		}
		fp := *oauth.CredentialFingerprint(creds)
		for _, other := range []string{live, backup} {
			if existing := oauth.CredentialFingerprint(other); existing != nil && *existing == fp {
				return fmt.Errorf("Account-%s's Mythos login needs its own sign-in", num)
			}
		}
		dir := store.MythosLoginDir(s.BackupDir(), email, org)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		return withMythosCredential(s, dir, func(path string) error {
			previous, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			updated, err := ccfile.SpliceCredentials(creds, string(previous))
			if err != nil {
				return err
			}
			return atomicfile.Write(path, []byte(updated), atomicfile.Opts{})
		})
	})
	if err == nil {
		// A new login writes no roster; statusline.json names it now (DESIGN A113).
		s.PublishStatusline()
	}
	return num, err
}

func withMythosCredential(s *store.Store, dir string, change func(string) error) error {
	refresh, err := cclock.AcquireProfileCredentials(dir, 0, s.Clk)
	if err != nil {
		return err
	}
	defer refresh.Release()
	write, err := cclock.AcquireProfileStorageWrite(dir, 0, s.Clk)
	if err != nil {
		return err
	}
	defer write.Release()
	return change(filepath.Join(dir, ccfile.CredentialsFileName))
}

func deleteMythosLogin(s *store.Store, email, org string) error {
	dir := store.MythosLoginDir(s.BackupDir(), email, org)
	if _, err := os.Stat(filepath.Join(dir, ccfile.CredentialsFileName)); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return withMythosCredential(s, dir, os.Remove)
}
