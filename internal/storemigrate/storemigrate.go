// Package storemigrate implements `tycswap migrate`: a one-time COPY of the store
// this fork came from (claude-swap's data directory) into tycswap's own store.
//
// The rule (DESIGN Amendment A23) is "copy once, never touch the old store":
// the old directory and its macOS Keychain items may still belong to another
// installed tool, so this package only ever reads them. It refuses to copy into
// a store that already holds data, so a second run can never merge or clobber.
// The layout inside the store is unchanged; only three kinds of name change on
// the way: the log (claude-swap.log* → tycswap.log*), the per-profile marker
// files (.cswap-* → .tycswap-*), and the Keychain services
// (claude-swap → tycswap, claude-swap-codex → tycswap-codex).
package storemigrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tyclab/tycswap/internal/codex/authfile"
	codexstore "github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

// Store modes: every directory 0700, every file 0600, whatever the source had.
const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
)

const (
	oldLogName    = "claude-swap.log"
	newLogName    = "tycswap.log"
	oldMarkerPfx  = ".cswap-"
	newMarkerPfx  = ".tycswap-"
	lockName      = ".lock"
	sessionsDir   = "sessions"
	codexDir      = "codex"
	sequenceFile  = "sequence.json"
	cacheDir      = "cache"
	migrationsLog = ".migrations.json"
)

// ErrNoOldStore is returned when no old store with data exists.
var ErrNoOldStore = errors.New("no old store to copy")

// ErrNotEmpty is returned (wrapped) when the new store already holds data.
var ErrNotEmpty = errors.New("the tycswap store is not empty")

// Options configures Run. Zero fields take the production defaults.
type Options struct {
	// NewRoot is tycswap's store; default paths.GetBackupRoot().
	NewRoot string
	// OldRoots are the candidate old stores in preference order; default
	// paths.OldBackupRoots().
	OldRoots []string
	// DryRun reports what would be copied and writes nothing.
	DryRun bool
	// Platform selects the Keychain step (macOS only); default platform.Detect().
	Platform *platform.Platform
	// Keychain is the macOS Keychain client; default keychain.Security{}.
	Keychain keychain.KeychainClient
}

// Entry is one copied path, relative to the store roots.
type Entry struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // "dir" | "file" | "symlink"
}

// KeychainItem is one Keychain item copied to its new service.
type KeychainItem struct {
	FromService string `json:"fromService"`
	ToService   string `json:"toService"`
	Account     string `json:"account"`
}

// Report is what Run copied (or, in a dry run, would copy).
type Report struct {
	From     string         `json:"from"`
	To       string         `json:"to"`
	DryRun   bool           `json:"dryRun"`
	Entries  []Entry        `json:"entries"`
	Skipped  []string       `json:"skipped"`
	Keychain []KeychainItem `json:"keychain"`
}

// Counts returns the number of directories, files and symlinks in the report.
func (r Report) Counts() (dirs, files, links int) {
	for _, e := range r.Entries {
		switch e.Kind {
		case "dir":
			dirs++
		case "file":
			files++
		case "symlink":
			links++
		}
	}
	return
}

// IsEmpty reports whether root is absent or holds nothing but what any run lays
// down without user data: the lock file, the cache, the log, the migrations
// ledger, and a codex/ directory holding only the same.
func IsEmpty(root string) bool {
	return onlyThrowaway(root, true)
}

func onlyThrowaway(dir string, top bool) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return os.IsNotExist(err)
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == lockName, name == cacheDir:
			continue
		case top && (strings.HasPrefix(name, newLogName) || strings.HasPrefix(name, oldLogName)):
			continue
		case top && name == migrationsLog:
			continue
		case top && name == codexDir && e.IsDir():
			if onlyThrowaway(filepath.Join(dir, name), false) {
				continue
			}
		}
		return false
	}
	return true
}

// FindOld returns the first candidate that is a directory holding data.
func FindOld(roots []string) (string, bool) {
	for _, r := range roots {
		if r == "" {
			continue
		}
		if fi, err := os.Stat(r); err == nil && fi.IsDir() && !IsEmpty(r) {
			return r, true
		}
	}
	return "", false
}

// Hint is the one-line notice every other command prints on stderr while the
// new store is empty and an old one exists; "" when there is nothing to say.
func Hint(newRoot string, oldRoots []string) string {
	if !IsEmpty(newRoot) {
		return ""
	}
	old, ok := FindOld(oldRoots)
	if !ok || samePath(old, newRoot) {
		return ""
	}
	return fmt.Sprintf("a cswap store exists at %s; `tycswap migrate` copies it once", old)
}

// Run copies the old store into the new one, once. It never writes, moves or
// deletes anything under the old root or in the old Keychain services.
func Run(o Options) (Report, error) {
	if o.NewRoot == "" {
		o.NewRoot = paths.GetBackupRoot()
	}
	if o.OldRoots == nil {
		o.OldRoots = paths.OldBackupRoots()
	}
	plat := platform.Detect()
	if o.Platform != nil {
		plat = *o.Platform
	}
	kc := o.Keychain
	if kc == nil {
		kc = keychain.Security{}
	}

	old, ok := FindOld(o.OldRoots)
	if !ok || samePath(old, o.NewRoot) {
		return Report{To: o.NewRoot, DryRun: o.DryRun}, ErrNoOldStore
	}
	rep := Report{From: old, To: o.NewRoot, DryRun: o.DryRun}

	if !IsEmpty(o.NewRoot) {
		return rep, fmt.Errorf("%w: %s already holds data; migrate copies only into an empty store, and never merges", ErrNotEmpty, o.NewRoot)
	}

	if !o.DryRun {
		// Hold tycswap's own store lock so no tycswap command writes the new
		// store mid-copy. The old store is not locked: it is only read, and its
		// owner writes every file by atomic rename, so each file read is whole.
		if err := os.MkdirAll(o.NewRoot, dirMode); err != nil {
			return rep, fmt.Errorf("create %s: %w", o.NewRoot, err)
		}
		lock := filelock.New(filepath.Join(o.NewRoot, lockName), 0)
		got, err := lock.Acquire(0)
		if err != nil {
			return rep, fmt.Errorf("lock %s: %w", o.NewRoot, err)
		}
		if !got {
			return rep, fmt.Errorf("lock %s: another tycswap command holds it; retry when it finishes", o.NewRoot)
		}
		defer lock.Release()
		// Re-check under the lock: a command may have written in between.
		if !IsEmpty(o.NewRoot) {
			return rep, fmt.Errorf("%w: %s already holds data; migrate copies only into an empty store, and never merges", ErrNotEmpty, o.NewRoot)
		}
	}

	if err := copyTree(old, o.NewRoot, o.DryRun, &rep); err != nil {
		return rep, err
	}
	if plat == platform.MacOS {
		if err := copyKeychain(old, o.NewRoot, kc, o.DryRun, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// newName maps one path component of the old store to its name in the new one.
func newName(name string, top bool) string {
	if top && strings.HasPrefix(name, oldLogName) {
		return newLogName + strings.TrimPrefix(name, oldLogName)
	}
	if strings.HasPrefix(name, oldMarkerPfx) {
		return newMarkerPfx + strings.TrimPrefix(name, oldMarkerPfx)
	}
	return name
}

func copyTree(src, dst string, dryRun bool, rep *Report) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		for i := range parts {
			parts[i] = newName(parts[i], i == 0)
		}
		toRel := filepath.Join(parts...)
		target := filepath.Join(dst, toRel)
		name := d.Name()

		switch {
		case d.Type()&fs.ModeSymlink != 0:
			// A session profile links shared items into ~/.claude; copy the
			// link itself, never what it points at.
			link, err := os.Readlink(p)
			if err != nil {
				return fmt.Errorf("read link %s: %w", p, err)
			}
			rep.Entries = append(rep.Entries, Entry{From: rel, To: toRel, Kind: "symlink"})
			if dryRun {
				return nil
			}
			if err := os.Symlink(link, target); err != nil {
				return fmt.Errorf("link %s: %w", target, err)
			}
		case d.IsDir():
			rep.Entries = append(rep.Entries, Entry{From: rel, To: toRel, Kind: "dir"})
			if dryRun {
				return nil
			}
			if err := os.MkdirAll(target, dirMode); err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			if err := os.Chmod(target, dirMode); err != nil {
				return fmt.Errorf("chmod %s: %w", target, err)
			}
		case d.Type().IsRegular():
			if name == lockName {
				// Lock files carry no data and are created on demand; the
				// new root's lock is the one this run holds.
				rep.Skipped = append(rep.Skipped, rel)
				return nil
			}
			rep.Entries = append(rep.Entries, Entry{From: rel, To: toRel, Kind: "file"})
			if dryRun {
				return nil
			}
			if err := copyFile(p, target); err != nil {
				return err
			}
		default:
			rep.Skipped = append(rep.Skipped, rel)
		}
		return nil
	})
}

// copyFile writes src's bytes to dst as a 0600 file, atomically (temp file in
// dst's directory, then rename), so an interrupted copy leaves no torn file.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tycswap-migrate-*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if err := tmp.Chmod(fileMode); err != nil && !platform.IsWindows() {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", dst, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := os.Rename(name, dst); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// copyKeychain copies the old store's macOS Keychain items to the new service
// names: the per-account Claude backups (and their .prev generation and the
// legacy account-None alias), the Codex snapshots, and each session profile's
// hashed Claude Code entry, whose service name derives from the profile path
// and so changes with the store root. Items are read and written, never deleted.
func copyKeychain(oldRoot, newRoot string, kc keychain.KeychainClient, dryRun bool, rep *Report) error {
	var items []KeychainItem
	for _, acct := range claudeBackupAccounts(oldRoot) {
		items = append(items, KeychainItem{keychain.OldBackupService, keychain.BackupService, acct})
	}
	cs := codexstore.New(codexstore.Options{Root: filepath.Join(oldRoot, codexDir), Keychain: kc, Platform: platform.Linux})
	for _, sl := range cs.Slots() {
		items = append(items, KeychainItem{keychain.OldCodexService, keychain.CodexService, authfile.FileKey(sl.AccountKey)})
	}
	if entries, err := os.ReadDir(filepath.Join(oldRoot, sessionsDir)); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			items = append(items, KeychainItem{
				FromService: sessprofile.KeychainServiceName(filepath.Join(oldRoot, sessionsDir, e.Name())),
				ToService:   sessprofile.KeychainServiceName(filepath.Join(newRoot, sessionsDir, e.Name())),
				Account:     keychain.AccountName(),
			})
		}
	}

	for _, it := range items {
		if dryRun {
			if kc.Exists(it.FromService, it.Account) {
				rep.Keychain = append(rep.Keychain, it)
			}
			continue
		}
		v, found, err := kc.Get(it.FromService, it.Account)
		if err != nil {
			return fmt.Errorf("read Keychain item %s/%s: %w", it.FromService, it.Account, err)
		}
		if !found {
			continue
		}
		if err := kc.Set(it.ToService, it.Account, v); err != nil {
			return fmt.Errorf("write Keychain item %s/%s: %w", it.ToService, it.Account, err)
		}
		rep.Keychain = append(rep.Keychain, it)
	}
	return nil
}

// claudeBackupAccounts lists the Keychain account names the old store's roster
// implies: account-<n>-<email>, its .prev, and the legacy account-None alias.
func claudeBackupAccounts(oldRoot string) []string {
	raw, err := os.ReadFile(filepath.Join(oldRoot, sequenceFile))
	if err != nil {
		return nil
	}
	var doc struct {
		Accounts map[string]struct {
			Email string `json:"email"`
		} `json:"accounts"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	nums := make([]string, 0, len(doc.Accounts))
	for n := range doc.Accounts {
		nums = append(nums, n)
	}
	sort.Strings(nums)
	for _, n := range nums {
		email := doc.Accounts[n].Email
		if email == "" {
			continue
		}
		for _, num := range []string{n, "None"} {
			base := "account-" + num + "-" + email
			add(base)
			add(base + ".prev")
		}
	}
	return out
}

func samePath(a, b string) bool {
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	if ea == nil && eb == nil {
		return ra == rb
	}
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return aa == bb
}
