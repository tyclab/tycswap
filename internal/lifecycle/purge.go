package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/storenames"
)

const securityService = keychain.BackupService

func Purge(s *store.Store) error {
	backupDir := s.BackupDir()
	var profiles []string
	if err := routingstate.With(s.SharedRoot(), func(state *routingstate.State) error {
		if err := state.CheckIdle(""); err != nil {
			return cerr.Session("Cannot purge: %s. Exit managed Claude sessions first.", err)
		}
		var err error
		profiles, err = purgeProfiles(backupDir)
		return err
	}); err != nil {
		return err
	}
	if _, _, err := purgeRoster(s); err != nil {
		return err
	}

	emitWarning("This will remove ALL tycswap data from your system:")
	emitLine("  - Backup directory contents: " + backupDir)
	if s.Platform == platform.MacOS {
		emitLine("  - All stored account credentials (macOS Keychain and/or files)")
	} else {
		emitLine("  - All stored account credential files")
	}
	if len(profiles) > 0 {
		emitLine("  - All session profiles and their Keychain entries")
	}
	emitLine("")
	emitLine(printer.Dimmed("Note: This does NOT affect your current Claude Code login."))
	emitLine(printer.Dimmed("Empty coordination files remain in the backup directory."))
	if ccsettings.SidecarExists(filepath.Join(backupDir, ccsettings.SidecarName)) {
		emitWarning("Claude Code's settings.json carries an API-key account's endpoint (env.ANTHROPIC_BASE_URL, " +
			"env.ANTHROPIC_AUTH_TOKEN). Purge deletes the record that puts back what the switch changed there " +
			"(env.ANTHROPIC_API_KEY included): switch to another account first, or remove the two keys by hand afterwards.")
	}
	emitLine("")

	confirm, ok := ActivePrompter.Prompt("Are you sure you want to purge all data? [y/N] ")
	if !ok || strings.ToLower(confirm) != "y" {
		emitLine(printer.Dimmed("Cancelled"))
		return nil
	}

	var removed []string
	complete := errors.New("purge completed")
	err := routingstate.With(s.SharedRoot(), func(state *routingstate.State) error {
		if err := state.CheckIdle(""); err != nil {
			return cerr.Session("Cannot purge: %s. Exit managed Claude sessions first.", err)
		}
		if err := s.Lock.With(func() error {
			var err error
			profiles, err = purgeProfiles(backupDir)
			if err != nil {
				return err
			}
			data, slots, err := purgeRoster(s)
			if err != nil {
				return err
			}
			for _, num := range slots {
				email := decodeRecord(data.Accounts[num]).str("email")
				nums := []string{num}
				if num != "None" {
					nums = append(nums, "None")
				}
				for _, n := range nums {
					credFile := filepath.Join(s.CredentialsDir, storenames.CredsFile(n, email))
					if pathExists(credFile) {
						if err := os.Remove(credFile); err == nil {
							removed = append(removed, "Credential file: "+filepath.Base(credFile))
						}
					}
				}
				if s.Platform == platform.MacOS {
					kc := s.Keychain()
					for _, n := range nums {
						for _, username := range []string{storenames.KeychainAccount(n, email), storenames.KeychainAccountPrev(n, email)} {
							_ = kc.Delete(securityService, username)
							removed = append(removed, "Credential: "+username)
						}
					}
				}
			}
			if len(profiles) > 0 {
				if s.Platform == platform.MacOS {
					for _, profile := range profiles {
						_ = s.Keychain().Delete(sessprofile.KeychainServiceName(profile), keychain.AccountName())
					}
				}
				removed = append(removed, "Session profiles: "+strings.Join(profiles, ", "))
			}
			entries, err := os.ReadDir(backupDir)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.Name() == ".lock" || entry.Name() == "routing.lock" {
					continue
				}
				if err := os.RemoveAll(filepath.Join(backupDir, entry.Name())); err != nil {
					return err
				}
				removed = append(removed, "Store entry: "+entry.Name())
			}
			return nil
		}); err != nil {
			return err
		}
		// Keep the lock inodes stable and prevent pruned routing state being rewritten.
		return complete
	})
	if err != nil && !errors.Is(err, complete) {
		return err
	}
	if len(removed) > 0 {
		emitLine("\n" + printer.Accent("Removed:"))
		for _, item := range removed {
			emitLine("  " + printer.Dimmed("-") + " " + item)
		}
	} else {
		emitLine("\n" + printer.Dimmed("No tycswap data found to remove."))
	}
	emitLine("\n" + printer.Accent("Purge complete."))
	return nil
}

func purgeRoster(s *store.Store) (*store.SequenceData, []string, error) {
	// A corrupt roster must remain purgeable; use it only to enumerate Keychain keys.
	data, _ := s.ReadSequence()
	var slots []string
	if data != nil {
		slots = sortedSlots(data)
	}
	for _, num := range slots {
		if email := decodeRecord(data.Accounts[num]).str("email"); !storenames.ValidEmail(email) {
			return nil, nil, cerr.Validation("Slot %s has an email that cannot name a store file: %s. purge builds credential file names from it and refuses; fix the record in %s or remove %s by hand.", num, strconv.Quote(email), s.SequenceFile, s.BackupDir())
		}
	}
	return data, slots, nil
}

func purgeProfiles(root string) ([]string, error) {
	var profiles []string
	sessionsRoot := filepath.Join(root, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			profiles = append(profiles, filepath.Join(sessionsRoot, entry.Name()))
		}
	}
	for _, id := range groups.All() {
		profile := groups.ProfileDir(root, id)
		if _, err := os.Lstat(profile); err == nil {
			profiles = append(profiles, profile)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	for _, profile := range profiles {
		if err := groups.SafePath(profile); err != nil {
			return nil, err
		}
		pids, err := store.LiveProfilePIDs(profile)
		if err != nil {
			return nil, cerr.Session("Cannot verify Claude sessions before purge: %s", err)
		}
		if len(pids) > 0 {
			return nil, cerr.Session("Live session-mode Claude instance(s) found: %s (PID %s). Exit them first, then retry --purge.", profile, joinInts(pids, ", "))
		}
	}
	return profiles, nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func joinInts(xs []int, sep string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, sep)
}
