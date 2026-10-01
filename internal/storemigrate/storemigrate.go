// Package storemigrate implements `tycswap migrate`: a one-time COPY of the store
// this fork came from (claude-swap's data directory) into tycswap's own store.
//
// The rule (DESIGN Amendment A23) is "copy once, never touch the old store":
// the old directory and its macOS Keychain items may still belong to another
// installed tool, so this package only ever reads them. It copies only into a
// store that is empty or holds a part of this same copy: a rerun after an
// interrupted copy resumes it (copies what is missing, verifies byte for byte
// what is there), and anything else in the new store refuses the run, so it
// can never merge or clobber.
// The layout inside the store is unchanged; only three kinds of name change on
// the way: the log (claude-swap.log* → tycswap.log*), the per-profile marker
// files (.cswap-* → .tycswap-*), and the Keychain services
// (claude-swap → tycswap, claude-swap-codex → tycswap-codex).
package storemigrate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	codexstore "github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/storenames"
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

// ErrNotEmpty is returned (wrapped) when the new store holds data that is not
// part of a copy of the old store.
var ErrNotEmpty = errors.New("the tycswap store is not empty")

// ErrConflict is returned (wrapped) when a path the copy would write already
// exists in the new store with different content.
var ErrConflict = errors.New("the tycswap store differs from the old store")

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
	// File is set (relative to the new root) when the item was too large for
	// `security -i` and its value went to the file tycswap reads instead.
	File string `json:"file,omitempty"`
}

// kcItem is one Keychain item to copy, with the file that holds its value
// when it does not fit the Keychain and the encoding that file uses.
type kcItem struct {
	KeychainItem
	fallback string              // slash-separated, relative to the new root
	encode   func(string) []byte // the fallback file's format
	// fileWins says which copy the reader serves when both exist: credstore
	// and the Codex store serve the file first (so an existing file makes the
	// Keychain item stale), a session profile serves the Keychain first (so
	// the item's value replaces the profile's seed file).
	fileWins bool
}

// Report is what Run copied (or, in a dry run, would copy).
type Report struct {
	From     string         `json:"from"`
	To       string         `json:"to"`
	DryRun   bool           `json:"dryRun"`
	Entries  []Entry        `json:"entries"`
	Skipped  []string       `json:"skipped"`
	Keychain []KeychainItem `json:"keychain"`
	// Verified lists paths (relative to To) a resumed run found already
	// copied, byte for byte; they were not written again.
	Verified []string `json:"verified"`
	// Resumed is true when the new store already held part of the copy.
	Resumed bool `json:"resumed"`
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
		case isLockFile(name), name == cacheDir:
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

	plan, err := planTree(old)
	if err != nil {
		return rep, err
	}
	var items []kcItem
	if plat == platform.MacOS {
		items = keychainItems(old, o.NewRoot, kc)
		planKeychainFiles(plan, items)
	}
	if !IsEmpty(o.NewRoot) {
		// Check only: nothing is removed before the lock is held.
		if err := checkResumable(o.NewRoot, plan, false); err != nil {
			return rep, err
		}
		rep.Resumed = true
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
		// Temp files an interrupted copy left are removed in this pass only.
		if !IsEmpty(o.NewRoot) {
			if err := checkResumable(o.NewRoot, plan, true); err != nil {
				return rep, err
			}
			rep.Resumed = true
		}
	}

	// Keychain items go first: one too large for the Keychain lands in a file
	// of the tree, which the tree copy then leaves to it.
	var inFiles map[string]bool
	if plat == platform.MacOS {
		var err error
		if inFiles, err = copyKeychain(old, o.NewRoot, items, kc, o.DryRun, &rep); err != nil {
			return rep, err
		}
	}
	if err := copyTree(old, o.NewRoot, inFiles, o.DryRun, &rep); err != nil {
		return rep, err
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

// copyTree copies the old store's tree. inFiles names the paths (relative to
// dst) a Keychain item's value was written to; the old store's file there, a
// session profile's seed, is skipped, since the profile serves the Keychain
// value first and that value is what the file now holds.
func copyTree(src, dst string, inFiles map[string]bool, dryRun bool, rep *Report) error {
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
			if existing, err := os.Readlink(target); err == nil {
				if existing != link {
					return conflict(dst, toRel)
				}
				rep.Verified = append(rep.Verified, toRel)
				return nil
			}
			rep.Entries = append(rep.Entries, Entry{From: rel, To: toRel, Kind: "symlink"})
			if dryRun {
				return nil
			}
			if err := os.Symlink(link, target); err != nil {
				return fmt.Errorf("link %s: %w", target, err)
			}
		case d.IsDir():
			if fi, err := os.Lstat(target); err == nil {
				if !fi.IsDir() {
					return conflict(dst, toRel)
				}
				return nil // created by an interrupted run; its contents are checked one by one
			}
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
			if isLockFile(name) {
				// Lock files carry no data and are created on demand; the
				// new root's lock is the one this run holds.
				rep.Skipped = append(rep.Skipped, rel)
				return nil
			}
			if inFiles[toRel] {
				rep.Skipped = append(rep.Skipped, rel)
				return nil
			}
			if _, err := os.Lstat(target); err == nil {
				if throwawayTop(toRel) {
					// The new store's own log, cache or ledger, written since
					// the interrupted run: keep it.
					rep.Skipped = append(rep.Skipped, rel)
					return nil
				}
				same, err := sameFile(p, target)
				if err != nil {
					return err
				}
				if !same {
					return conflict(dst, toRel)
				}
				rep.Verified = append(rep.Verified, toRel)
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

// planned is one path the copy would write, keyed by its path in the new store.
type planned struct {
	src  string // absolute path in the old store
	kind string // "dir" | "file" | "symlink" | "keychain-file"
}

// planKeychainFiles adds to plan the files a Keychain item too large for
// `security -i` is written to (and their directories), so a rerun accepts
// them as part of the copy. Their content is checked by copyKeychain against
// the item's value, not here.
func planKeychainFiles(plan map[string]planned, items []kcItem) {
	for _, it := range items {
		rel := filepath.FromSlash(it.fallback)
		if _, ok := plan[rel]; !ok || !it.fileWins {
			plan[rel] = planned{kind: "keychain-file"}
		}
		for dir := filepath.Dir(rel); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
			if _, ok := plan[dir]; !ok {
				plan[dir] = planned{kind: "dir"}
			}
		}
	}
}

// planTree maps every path the copy would write (relative to the new root) to
// its source, applying the same name changes copyTree does.
func planTree(src string) (map[string]planned, error) {
	plan := map[string]planned{}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		parts := strings.Split(rel, string(filepath.Separator))
		for i := range parts {
			parts[i] = newName(parts[i], i == 0)
		}
		toRel := filepath.Join(parts...)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			plan[toRel] = planned{p, "symlink"}
		case d.IsDir():
			plan[toRel] = planned{p, "dir"}
		case d.Type().IsRegular() && !isLockFile(d.Name()):
			plan[toRel] = planned{p, "file"}
		}
		return nil
	})
	return plan, err
}

// checkResumable accepts a non-empty new store only when everything in it is
// throwaway or part of this copy: every other path must be one the copy
// writes, of the same kind, and a file byte for byte the source's, a symlink
// pointing at the same target. A file a previous run left half-written cannot
// exist (files are written by temp file and rename); such temp files are
// accepted, and removed only when removeTemps is set, which Run does in the
// pass under the store lock and never in a dry run: an unlocked pass must not
// remove a temp file a run in progress is about to rename. Nothing else is
// written.
func checkResumable(newRoot string, plan map[string]planned, removeTemps bool) error {
	return filepath.WalkDir(newRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		rel, err := filepath.Rel(newRoot, p)
		if err != nil || rel == "." {
			return err
		}
		name := d.Name()
		top := !strings.Contains(rel, string(filepath.Separator))
		switch {
		case d.IsDir() && name == cacheDir:
			return filepath.SkipDir
		case isLockFile(name):
			return nil
		case top && (name == migrationsLog || strings.HasPrefix(name, newLogName)):
			return nil
		case strings.HasPrefix(name, ".tycswap-migrate-") && strings.HasSuffix(name, ".tmp"):
			if removeTemps {
				return os.Remove(p) // left by an interrupted copyFile
			}
			return nil
		}
		want, ok := plan[rel]
		if !ok {
			return fmt.Errorf("%w: %s holds %s, which is not part of a copy of the old store; "+
				"migrate copies only into an empty store or resumes its own copy, and never merges", ErrNotEmpty, newRoot, rel)
		}
		switch want.kind {
		case "dir":
			if !d.IsDir() {
				return conflict(newRoot, rel)
			}
		case "symlink":
			a, aerr := os.Readlink(want.src)
			b, berr := os.Readlink(p)
			if aerr != nil || berr != nil || a != b {
				return conflict(newRoot, rel)
			}
		case "keychain-file":
			if !d.Type().IsRegular() {
				return conflict(newRoot, rel)
			}
		case "file":
			if !d.Type().IsRegular() {
				return conflict(newRoot, rel)
			}
			same, err := sameFile(want.src, p)
			if err != nil {
				return err
			}
			if !same {
				return conflict(newRoot, rel)
			}
		}
		return nil
	})
}

func conflict(root, rel string) error {
	return fmt.Errorf("%w: %s already exists in %s with other content than the old store's; "+
		"nothing was copied — move it aside (or remove the store with `tycswap purge`) and run migrate again", ErrConflict, rel, root)
}

// throwawayTop reports whether rel (in the new store) is the log, the cache or
// the migrations ledger, which the new store may have written itself.
func throwawayTop(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if parts[0] == cacheDir || (parts[0] == codexDir && len(parts) > 1 && parts[1] == cacheDir) {
		return true
	}
	return len(parts) == 1 && (parts[0] == migrationsLog || strings.HasPrefix(parts[0], newLogName))
}

func sameFile(a, b string) (bool, error) {
	x, err := os.ReadFile(a)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", a, err)
	}
	y, err := os.ReadFile(b)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", b, err)
	}
	return bytes.Equal(x, y), nil
}

// copyFile writes src's bytes to dst as a 0600 file, atomically (temp file in
// dst's directory, then rename), so an interrupted copy leaves no torn file.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	return writeFile(dst, data)
}

// writeFile writes data to dst by temp file and rename, mode 0600.
func writeFile(dst string, data []byte) error {
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
	if err := atomicfile.SyncFile(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := os.Rename(name, dst); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	atomicfile.SyncDir(filepath.Dir(dst))
	return nil
}

// keychainItems lists the old store's macOS Keychain items with their new
// service names: the per-account Claude backups (and their .prev generation
// and the legacy account-None alias), the Codex snapshots, and each session
// profile's hashed Claude Code entry, whose service name derives from the
// profile path and so changes with the store root. Each item names the file
// under the new root that holds its value when it does not fit the Keychain:
// the file tycswap reads first for that item (credstore's .enc[.prev] in
// base64, the Codex snapshot file, the profile's .credentials.json).
func keychainItems(oldRoot, newRoot string, kc keychain.KeychainClient) []kcItem {
	b64 := func(v string) []byte { return []byte(base64.StdEncoding.EncodeToString([]byte(v))) }
	raw := func(v string) []byte { return []byte(v) }
	var items []kcItem
	for _, acct := range claudeBackupAccounts(oldRoot) {
		items = append(items, kcItem{
			KeychainItem: KeychainItem{keychain.OldBackupService, keychain.BackupService, acct.name, ""},
			fallback:     "credentials/" + acct.file,
			encode:       b64,
			fileWins:     true,
		})
	}
	cs := codexstore.New(codexstore.Options{Root: filepath.Join(oldRoot, codexDir), Keychain: kc, Platform: platform.Linux})
	for _, sl := range cs.Slots() {
		key := authfile.FileKey(sl.AccountKey)
		items = append(items, kcItem{
			KeychainItem: KeychainItem{keychain.OldCodexService, keychain.CodexService, key, ""},
			fallback:     codexDir + "/credentials/" + key + ".json",
			encode:       raw,
			fileWins:     true,
		})
	}
	if entries, err := os.ReadDir(filepath.Join(oldRoot, sessionsDir)); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			items = append(items, kcItem{
				KeychainItem: KeychainItem{
					FromService: sessprofile.KeychainServiceName(filepath.Join(oldRoot, sessionsDir, e.Name())),
					ToService:   sessprofile.KeychainServiceName(filepath.Join(newRoot, sessionsDir, e.Name())),
					Account:     keychain.AccountName(),
				},
				fallback: sessionsDir + "/" + e.Name() + "/" + sessprofile.CredentialsFileName,
				encode:   raw,
			})
		}
	}
	return items
}

// copyKeychain copies the Keychain items to their new services. Items are
// read and written, never deleted. An item too large for `security -i`'s
// stdin line (one the old tool stored through argv) is written to its
// fallback file instead, 0600, so the account keeps working; the report
// names the file. Where the reader serves the file first and the old store
// has that file, the item is stale and nothing is written for it. The
// returned set names the fallback files (relative to newRoot) that hold an
// item's value after this call.
func copyKeychain(oldRoot, newRoot string, items []kcItem, kc keychain.KeychainClient, dryRun bool, rep *Report) (map[string]bool, error) {
	inFiles := map[string]bool{}
	for _, it := range items {
		if dryRun {
			if kc.Exists(it.FromService, it.Account) {
				rep.Keychain = append(rep.Keychain, it.KeychainItem)
			}
			continue
		}
		v, found, err := kc.Get(it.FromService, it.Account)
		if err != nil {
			return nil, fmt.Errorf("read Keychain item %s/%s: %w", it.FromService, it.Account, err)
		}
		if !found {
			continue
		}
		// A resumed run may find the item copied already: identical is
		// verified, different is a conflict, never overwritten.
		if cur, ok, err := kc.Get(it.ToService, it.Account); err == nil && ok {
			if cur != v {
				return nil, fmt.Errorf("%w: Keychain item %s/%s already exists with other content", ErrConflict, it.ToService, it.Account)
			}
			continue
		}
		err = kc.Set(it.ToService, it.Account, v)
		if keychain.IsTooLarge(err) {
			if it.fileWins {
				if fi, err := os.Lstat(filepath.Join(oldRoot, filepath.FromSlash(it.fallback))); err == nil && fi.Mode().IsRegular() {
					continue // the old store served that file; the item is stale
				}
			}
			written, ferr := writeFallback(newRoot, it, v)
			if ferr != nil {
				return nil, ferr
			}
			inFiles[filepath.FromSlash(it.fallback)] = true
			if written {
				it.KeychainItem.File = it.fallback
				rep.Keychain = append(rep.Keychain, it.KeychainItem)
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("write Keychain item %s/%s: %w", it.ToService, it.Account, err)
		}
		rep.Keychain = append(rep.Keychain, it.KeychainItem)
	}
	return inFiles, nil
}

// writeFallback writes an item's value to its fallback file. A file already
// holding the same bytes (a resumed run) is left as it is and reported as
// not written; one with other content is a conflict.
func writeFallback(newRoot string, it kcItem, v string) (written bool, err error) {
	target := filepath.Join(newRoot, filepath.FromSlash(it.fallback))
	data := it.encode(v)
	if existing, rerr := os.ReadFile(target); rerr == nil {
		if !bytes.Equal(existing, data) {
			return false, conflict(newRoot, filepath.FromSlash(it.fallback))
		}
		return false, nil
	} else if !errors.Is(rerr, fs.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", target, rerr)
	}
	if err := os.MkdirAll(filepath.Dir(target), dirMode); err != nil {
		return false, fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	if err := writeFile(target, data); err != nil {
		return false, err
	}
	return true, nil
}

// backupAccount is one per-account Keychain account name and the credstore
// file that holds the same value when it does not fit the Keychain.
type backupAccount struct{ name, file string }

// claudeBackupAccounts lists the Keychain account names the old store's roster
// implies: account-<n>-<email>, its .prev, and the legacy account-None alias.
func claudeBackupAccounts(oldRoot string) []backupAccount {
	emails := rosterEmails(oldRoot)
	seen := map[string]bool{}
	var out []backupAccount
	add := func(name, file string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, backupAccount{name, file})
		}
	}
	nums := make([]string, 0, len(emails))
	for n := range emails {
		nums = append(nums, n)
	}
	sort.Strings(nums)
	for _, n := range nums {
		email := emails[n]
		if email == "" {
			continue
		}
		for _, num := range []string{n, "None"} {
			base := "account-" + num + "-" + email
			add(base, storenames.CredsFile(num, email))
			add(base+".prev", storenames.CredsPrevFile(num, email))
		}
	}
	return out
}

// rosterEmails reads slot → email from the old store's sequence.json; nil when
// it is absent or unreadable.
func rosterEmails(oldRoot string) map[string]string {
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
	out := make(map[string]string, len(doc.Accounts))
	for n, a := range doc.Accounts {
		out[n] = a.Email
	}
	return out
}

// isLockFile reports whether name is one of the store's lock files (.lock,
// .settings.lock, .mappings.lock, .autoswitch_state.lock): they carry no data
// and are created on demand.
func isLockFile(name string) bool {
	return name == lockName || (strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".lock"))
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
