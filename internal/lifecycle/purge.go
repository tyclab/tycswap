// purge.go — Purge: remove all tycswap data from the system.
//
// Implements spec 01§11 (purge): refuse while any session-mode instance is live,
// print the warning header + the platform-specific credential line, confirm,
// then delete per-account credential files (including the legacy account-None
// alias), macOS Keychain items and session-profile Keychain entries, and the
// backup directory, never an old store `tycswap migrate` copied from. Every deletion is
// best-effort; the collected "Removed:" list is printed at the end.
package lifecycle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/store"
)

// securityService is SECURITY_SERVICE, the Keychain service for tycswap backups
// (spec 01§1.2). Purge deletes these directly on macOS.
const securityService = keychain.BackupService

// Purge removes all tycswap data (spec 01§11). It refuses while any session-mode
// Claude instance is live.
func Purge(s *store.Store) error {
	// Only tycswap's own store: an old store `tycswap migrate` copied from may
	// belong to another installed tool, so purge never lists or removes it.
	backupDir := s.BackupDir()

	sessionsRoot := filepath.Join(backupDir, "sessions")
	sessionDirs := listSessionDirs(sessionsRoot)

	// Refuse while any session-mode claude is live.
	live := map[string][]int{}
	for _, d := range sessionDirs {
		if pids := sessprofile.LiveSessionPIDs(filepath.Join(sessionsRoot, d)); len(pids) > 0 {
			live[d] = pids
		}
	}
	if len(live) > 0 {
		var parts []string
		for _, d := range sessionDirs {
			pids, ok := live[d]
			if !ok {
				continue
			}
			parts = append(parts, d+" (PID "+joinInts(pids, ", ")+")")
		}
		return cerr.Session("Live session-mode Claude instance(s) found: %s. Exit them first, then retry --purge.", strings.Join(parts, "; "))
	}

	emitWarning("This will remove ALL tycswap data from your system:")
	emitLine("  - Backup directory: " + backupDir)
	if s.Platform == platform.MacOS {
		emitLine("  - All stored account credentials (macOS Keychain and/or files)")
	} else {
		emitLine("  - All stored account credential files")
	}
	if len(sessionDirs) > 0 {
		emitLine("  - All session profiles and their Keychain entries")
	}
	emitLine("")
	emitLine(printer.Dimmed("Note: This does NOT affect your current Claude Code login."))
	emitLine("")

	confirm, ok := ActivePrompter.Prompt("Are you sure you want to purge all data? [y/N] ")
	if !ok || strings.ToLower(confirm) != "y" {
		emitLine(printer.Dimmed("Cancelled"))
		return nil
	}

	var removed []string

	// Purge's only roster read, and the one operation that reads it through
	// ReadSequence by design: it never writes sequence.json — it deletes the
	// directory containing it — so it needs no classification and must not refuse
	// an unparseable file. The roster is used solely to enumerate per-account
	// credential keys; a nil one just means that enumeration contributes nothing,
	// and the directory removal below still takes everything with it.
	data, _ := s.ReadSequence()
	if data != nil {
		for _, num := range sortedSlots(data) {
			email := decodeRecord(data.Accounts[num]).str("email")
			nums := []string{num}
			if num != "None" {
				nums = append(nums, "None")
			}
			for _, n := range nums {
				credFile := filepath.Join(s.CredentialsDir, ".creds-"+n+"-"+email+".enc")
				if pathExists(credFile) {
					if err := os.Remove(credFile); err == nil {
						removed = append(removed, "Credential file: "+filepath.Base(credFile))
					}
				}
			}
			// macOS Keychain items via the security backend.
			if s.Platform == platform.MacOS {
				kc := keychain.Security{}
				for _, n := range nums {
					username := "account-" + n + "-" + email
					_ = kc.Delete(securityService, username)
					removed = append(removed, "Credential: "+username)
				}
			}
		}
	}

	// Session-profile Keychain entries must go BEFORE the backup dir: the hashed
	// service names derive from the dir paths.
	if len(sessionDirs) > 0 {
		if s.Platform == platform.MacOS {
			kc := keychain.Security{}
			for _, d := range sessionDirs {
				sessprofile.DeleteMacOSKeychainEntry(kc, filepath.Join(sessionsRoot, d))
			}
		}
		removed = append(removed, "Session profiles: "+strings.Join(sessionDirs, ", "))
	}

	if pathExists(backupDir) {
		if err := os.RemoveAll(backupDir); err == nil {
			removed = append(removed, "Directory: "+backupDir)
		}
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

// listSessionDirs returns the immediate subdirectory names of the sessions root,
// in sorted order, or nil when the root is absent.
func listSessionDirs(sessionsRoot string) []string {
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
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
