// store.go — tycswap's own Codex account store: the slot registry plus the
// credential snapshots. Implements claude-swap PR #252 codex/store.py.
//
// Two pieces with different lifetimes and different security postures.
// sequence.json holds non-secret metadata (slot number, email, plan, alias,
// workspace name, disabled flag, auth mode) keyed by slot number, each row
// naming its account_key. The snapshot store holds one auth.json payload per
// account, keyed by account_key, in the macOS Keychain (service
// "tycswap-codex", account = authfile.FileKey(key)) or in 0600 files under
// a 0700 credentials/ directory everywhere else.
//
// Snapshots are keyed by account_key rather than slot number on purpose. Slot
// numbers are a presentation concern that `tycswap codex swap`/`move` renumber;
// the account key never changes. Keying secrets by a mutable number would turn
// that feature into a data migration, so Renumber only ever rewrites
// sequence.json and never touches a secret.
//
// Slot numbers are not compacted when an account is removed: renumbering would
// silently repoint every alias and every number a user has memorised. The gap
// is left, and the next add reuses the lowest free number — the same rule the
// Claude side follows.
//
// sequence.json is byte-compatible with the Python writer: two-space indent,
// no trailing newline, rows in to_dict key order, and a read-modify-write that
// keeps the file's own key order and any keys this port does not know about
// (the Python round-trips a plain dict, so it does the same). A missing, torn
// or non-object file degrades to "no accounts" rather than making every tycswap
// command fail; lastUpdated is stamped on every write in get_timestamp's
// seconds-precision, Z-suffixed UTC form.
//
// The Store holds no in-memory state: every call re-reads the file, exactly
// like CodexStore. Every read-modify-write of sequence.json holds the store's
// file lock for the whole operation, so a background `tycswap auto` recording a
// workspace name cannot interleave with an add in another terminal and drop
// its slot. A transaction supplies a scoped Store view that may reuse its
// non-reentrant lock. Ordinary Store values always acquire a lock, even when
// another goroutine in this process has a transaction open.
//
// Deviation from the Python: a sequence.json that exists but does not parse
// is never overwritten. Listing reads still treat it as "no accounts", but a
// mutation fails with ErrCorruptRegistry instead of replacing the registry
// with an empty one. A zero-length file is read as fresh.

// Package store is tycswap's Codex slot registry and credential snapshot store.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
)

// KeychainService is the Keychain service for Codex snapshots. Distinct from
// the Claude side's so a purge or an audit can tell the two apart at a glance.
const KeychainService = keychain.CodexService

// defaultAuthMode is CodexSlot.auth_mode's default ("chatgpt" | "apikey").
const defaultAuthMode = "chatgpt"

// ErrUnknownAccount is returned (wrapped) by Renumber when the mapping names an
// account_key with no slot — the Python's KeyError.
var ErrUnknownAccount = errors.New("unknown account_key")

// ErrCorruptRegistry is returned (wrapped) by every mutation when
// sequence.json exists but is not a JSON object: rewriting it would replace
// the user's registry with an empty one.
var ErrCorruptRegistry = errors.New("codex sequence.json is unreadable; refusing to overwrite it")

// fileOpts is the private posture for everything under the store root:
// directories 0700, files 0600 (codex-auth's documented posture). atomicfile
// also chmods the parent to DirMode, which is what the store root wants.
var fileOpts = atomicfile.Opts{FileMode: 0o600, DirMode: 0o700}

// Slot is one managed Codex account as stored. There are no secrets in here.
type Slot struct {
	Number, AccountKey, Email, Plan, WorkspaceName, Alias, Added string
	Disabled                                                     bool
	AuthMode                                                     string // "chatgpt" | "apikey"
}

// DisplayLabel is "email [workspace]", or "email [personal]" when the slot has
// no workspace name — mirrors the Claude side's AccountInfo.display_label.
func (s Slot) DisplayLabel() string {
	tag := s.WorkspaceName
	if tag == "" {
		tag = "personal"
	}
	return s.Email + " [" + tag + "]"
}

// Options configures a Store. Zero fields take their production defaults.
type Options struct {
	// Root is the store root; defaults to authfile.StoreRoot().
	Root string
	// Keychain is the macOS snapshot backend; defaults to keychain.Security{}.
	Keychain keychain.KeychainClient
	// Clock stamps added/lastUpdated; defaults to clock.System{}.
	Clock clock.Clock
	// Platform selects the Keychain (MacOS) or files (anything else). MacOS is
	// the zero Platform, so a zero value is ambiguous; see New.
	Platform platform.Platform
}

// Store reads and writes the Codex slot registry and snapshot store.
type Store struct {
	root     string
	kc       keychain.KeychainClient
	clk      clock.Clock
	platform platform.Platform
	tx       *StoreLock // non-nil only on the transaction-scoped view
}

// New returns a Store with Options defaults filled in: Root →
// authfile.StoreRoot(), Keychain → keychain.Security{}, Clock → clock.System{},
// Platform → platform.Detect(). Because platform.MacOS is the zero Platform, a
// MacOS value is honoured only together with an injected Keychain — the test
// seam that runs the darwin branch on Linux against keychain.Fake — and is
// otherwise read as "unset" and detected. Any non-zero Platform is honoured
// as given. Production callers pass a zero Options.
func New(o Options) *Store {
	s := &Store{root: o.Root, kc: o.Keychain, clk: o.Clock, platform: o.Platform}
	if s.root == "" {
		s.root = authfile.StoreRoot()
	}
	if s.platform == platform.MacOS && o.Keychain == nil {
		s.platform = platform.Detect()
	}
	if s.kc == nil {
		s.kc = keychain.Security{}
	}
	if s.clk == nil {
		s.clk = clock.System{}
	}
	return s
}

// Root returns the store root.
func (s *Store) Root() string { return s.root }

// CacheDir returns the usage-cache directory under Root.
func (s *Store) CacheDir() string { return s.under(authfile.CacheDir()) }

func (s *Store) lockPath() string { return s.under(authfile.LockPath()) }

// StoreLock owns one transaction. Only its Store view may reuse the lock;
// another goroutine using the original Store must acquire its own lock.
// Obtain a fresh StoreLock for each transaction; a released view stays expired.
type StoreLock struct {
	fl       *filelock.FileLock
	store    *Store
	mu       sync.Mutex
	acquired bool
	used     bool
}

// ErrTransactionClosed prevents an escaped transaction view writing unlocked.
var ErrTransactionClosed = errors.New("codex store transaction is not active")

func (s *Store) Lock() *StoreLock {
	return &StoreLock{fl: filelock.New(s.lockPath(), 0), store: s}
}

func (l *StoreLock) Acquire(timeout time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used {
		return false, ErrTransactionClosed
	}
	ok, err := l.fl.Acquire(timeout)
	if ok && err == nil {
		l.acquired, l.used = true, true
	}
	return ok, err
}

// Store returns a view whose writes are valid only during this transaction.
// Pass this view down the call chain instead of changing a shared Store.
func (l *StoreLock) Store() *Store {
	view := *l.store
	view.tx = l
	return &view
}

func (l *StoreLock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.acquired {
		return nil
	}
	l.acquired = false
	return l.fl.Release()
}

func (l *StoreLock) With(fn func(*Store) error) error {
	ok, err := l.Acquire(0)
	if err != nil {
		return err
	}
	if !ok {
		return cerr.Lock("Failed to acquire lock - another instance may be running")
	}
	defer l.Release()
	return fn(l.Store())
}

// WithLock supplies a transaction-scoped store for a multi-operation write.
// Nested work must use that view; an ordinary Store never inherits ownership.
func (s *Store) WithLock(fn func(*Store) error) error {
	if s.tx != nil {
		s.tx.mu.Lock()
		active := s.tx.acquired
		s.tx.mu.Unlock()
		if !active {
			return ErrTransactionClosed
		}
		return fn(s)
	}
	return s.Lock().With(fn)
}

func (s *Store) withWrite(fn func() error) error {
	if s.tx != nil {
		s.tx.mu.Lock()
		defer s.tx.mu.Unlock()
		if !s.tx.acquired {
			return ErrTransactionClosed
		}
		return fn()
	}
	return filelock.New(s.lockPath(), 0).With(fn)
}

// update holds the transaction across the entire roster read-modify-write.
func (s *Store) update(fn func(d *seqDoc) error) error {
	return s.withWrite(func() error {
		d, err := s.readDoc()
		if err != nil {
			return err
		}
		return fn(d)
	})
}

// under re-roots one of authfile's StoreRoot-relative paths onto s.root, so the
// layout has a single definition (authfile) while tests redirect the root.
func (s *Store) under(p string) string {
	rel, err := filepath.Rel(authfile.StoreRoot(), p)
	if err != nil {
		rel = filepath.Base(p)
	}
	return filepath.Join(s.root, rel)
}

func (s *Store) sequencePath() string   { return s.under(authfile.SequencePath()) }
func (s *Store) credentialsDir() string { return s.under(authfile.CredentialsDir()) }

// timestamp is get_timestamp(): UTC, seconds precision, Z-suffixed — the same
// format internal/store stamps on the Claude sequence.json.
func (s *Store) timestamp() string {
	return s.clk.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// ---- slot registry ------------------------------------------------------

// seqDoc is sequence.json as an ordered document: the top level and the
// accounts object keep their on-disk key order and unknown keys.
type seqDoc struct {
	top      object
	accounts object
}

// freshDoc is {"accounts": {}, "activeAccountKey": null}.
func freshDoc() *seqDoc {
	d := &seqDoc{}
	d.top.set("accounts", json.RawMessage("{}"))
	d.top.set("activeAccountKey", json.RawMessage("null"))
	return d
}

// read loads sequence.json for listing. A missing file is the normal
// fresh-install case; a corrupt one is a torn write; both — and a non-object
// top level — degrade to the fresh document rather than an error. Mutations
// use readDoc, which refuses the corrupt case.
func (s *Store) read() *seqDoc {
	d, err := s.readDoc()
	if err != nil {
		return freshDoc()
	}
	return d
}

func (s *Store) readDoc() (*seqDoc, error) {
	b, err := os.ReadFile(s.sequencePath())
	if errors.Is(err, os.ErrNotExist) {
		return freshDoc(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read codex sequence.json: %w", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return freshDoc(), nil
	}
	top, ok := parseObject(b)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrCorruptRegistry, s.sequencePath())
	}
	d := &seqDoc{top: top}
	if raw, has := top.get("accounts"); has {
		if acc, ok := parseObject(raw); ok {
			d.accounts = acc
		}
	}
	// Python: `data["accounts"] = {}` when not a dict (in place), and
	// setdefault("activeAccountKey", None) (appended when missing).
	d.top.set("accounts", json.RawMessage("{}"))
	if _, has := d.top.get("activeAccountKey"); !has {
		d.top.set("activeAccountKey", json.RawMessage("null"))
	}
	return d, nil
}

// write stamps lastUpdated and replaces sequence.json atomically.
func (s *Store) write(d *seqDoc) error {
	acc, err := d.accounts.MarshalJSON()
	if err != nil {
		return err
	}
	d.top.set("accounts", json.RawMessage(acc))
	d.top.set("lastUpdated", s.timestamp())
	if err := atomicfile.WriteJSON(s.sequencePath(), d.top, fileOpts); err != nil {
		return fmt.Errorf("write codex sequence.json: %w", err)
	}
	return nil
}

// isSlotNumber is Python's str.isdigit() restricted to ASCII, which is all a
// slot number ever is and all int() is guaranteed to parse.
func isSlotNumber(n string) bool {
	if n == "" {
		return false
	}
	for i := 0; i < len(n); i++ {
		if n[i] < '0' || n[i] > '9' {
			return false
		}
	}
	return true
}

// rowKey returns a row's account_key when the row is an object whose
// account_key is a string; rows that are not objects never match anything.
func rowKey(raw json.RawMessage) (object, string, bool) {
	row, ok := parseObject(raw)
	if !ok {
		return nil, "", false
	}
	v, has := row.get("account_key")
	if !has {
		return row, "", false
	}
	var k string
	if json.Unmarshal(v, &k) != nil {
		return row, "", false
	}
	return row, k, true
}

// slotFromRow is CodexSlot.from_dict. Non-string fields read as "" (the
// Python's `or ""` for null; a non-string value has no Go string form).
func slotFromRow(number string, row object) Slot {
	str := func(k string) string {
		v, _ := row.get(k)
		var out string
		_ = json.Unmarshal(v, &out)
		return out
	}
	sl := Slot{
		Number:        number,
		AccountKey:    str("account_key"),
		Email:         str("email"),
		Plan:          str("plan"),
		WorkspaceName: str("workspaceName"),
		Alias:         str("alias"),
		Added:         str("added"),
		AuthMode:      str("authMode"),
	}
	if v, has := row.get("disabled"); has {
		sl.Disabled = truthy(v)
	}
	if sl.AuthMode == "" {
		sl.AuthMode = defaultAuthMode
	}
	return sl
}

// truthy is Python bool() over a JSON value.
func truthy(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return false
}

// rowFromSlot is CodexSlot.to_dict, in its key order.
func rowFromSlot(sl Slot) object {
	var o object
	o.set("account_key", sl.AccountKey)
	o.set("email", sl.Email)
	o.set("plan", sl.Plan)
	o.set("workspaceName", sl.WorkspaceName)
	o.set("alias", sl.Alias)
	o.set("added", sl.Added)
	o.set("disabled", sl.Disabled)
	o.set("authMode", sl.AuthMode)
	return o
}

// Slots returns every managed slot, ordered by slot number (numerically).
// Rows that are not objects, or whose number is not all digits, are skipped.
func (s *Store) Slots() []Slot {
	d := s.read()
	out := []Slot{}
	for _, f := range d.accounts {
		if !isSlotNumber(f.key) {
			continue
		}
		row, ok := parseObject(f.val)
		if !ok {
			continue
		}
		out = append(out, slotFromRow(f.key, row))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return numLess(out[i].Number, out[j].Number)
	})
	return out
}

// numLess compares two all-digit strings by integer value without overflow.
func numLess(a, b string) bool {
	a, b = trimZeros(a), trimZeros(b)
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func trimZeros(n string) string {
	for len(n) > 1 && n[0] == '0' {
		n = n[1:]
	}
	return n
}

// SlotForKey returns the slot holding accountKey, or nil.
func (s *Store) SlotForKey(accountKey string) *Slot {
	for _, sl := range s.Slots() {
		if sl.AccountKey == accountKey {
			sl := sl
			return &sl
		}
	}
	return nil
}

// nextFreeNumber is the lowest positive integer not already a slot number.
func nextFreeNumber(accounts object) string {
	taken := map[int]bool{}
	for _, f := range accounts {
		if isSlotNumber(f.key) {
			if n, err := strconv.Atoi(f.key); err == nil {
				taken[n] = true
			}
		}
	}
	n := 1
	for taken[n] {
		n++
	}
	return strconv.Itoa(n)
}

// Upsert carries the identity-derived fields UpsertSlot writes.
type Upsert struct{ Email, Plan, WorkspaceName, AuthMode string }

// UpsertSlot creates or updates the slot for accountKey and returns it. On an
// existing row email and plan are refreshed when non-empty, authMode always
// (default "chatgpt"), and workspaceName only when non-empty — a login that
// cannot see the workspace name must not blank one already learned. A new
// slot takes the lowest free number.
func (s *Store) UpsertSlot(accountKey string, u Upsert) (Slot, error) {
	if u.AuthMode == "" {
		u.AuthMode = defaultAuthMode
	}
	var out Slot
	err := s.update(func(d *seqDoc) error {
		sl, err := s.upsertIn(d, accountKey, u)
		out = sl
		return err
	})
	return out, err
}

// upsertIn is UpsertSlot's body over an already-read document.
func (s *Store) upsertIn(d *seqDoc, accountKey string, u Upsert) (Slot, error) {
	for i, f := range d.accounts {
		row, k, ok := rowKey(f.val)
		if !ok || k != accountKey {
			continue
		}
		if u.Email != "" {
			row.set("email", u.Email)
		} else if _, has := row.get("email"); !has {
			row.set("email", "")
		}
		if u.Plan != "" {
			row.set("plan", u.Plan)
		} else if _, has := row.get("plan"); !has {
			row.set("plan", "")
		}
		if u.WorkspaceName != "" {
			row.set("workspaceName", u.WorkspaceName)
		}
		row.set("authMode", u.AuthMode)
		raw, err := row.MarshalJSON()
		if err != nil {
			return Slot{}, err
		}
		d.accounts[i].val = raw
		if err := s.write(d); err != nil {
			return Slot{}, err
		}
		return slotFromRow(f.key, row), nil
	}

	sl := Slot{
		Number:        nextFreeNumber(d.accounts),
		AccountKey:    accountKey,
		Email:         u.Email,
		Plan:          u.Plan,
		WorkspaceName: u.WorkspaceName,
		Added:         s.timestamp(),
		AuthMode:      u.AuthMode,
	}
	d.accounts.set(sl.Number, rowFromSlot(sl))
	if err := s.write(d); err != nil {
		return Slot{}, err
	}
	return sl, nil
}

func (s *Store) RemoveSlot(accountKey string) (bool, error) {
	removed := false
	err := s.update(func(d *seqDoc) error {
		target := -1
		for i, f := range d.accounts {
			if _, k, ok := rowKey(f.val); ok && k == accountKey {
				target = i
				break
			}
		}
		if target < 0 {
			return nil
		}
		d.accounts = append(d.accounts[:target:target], d.accounts[target+1:]...)
		if activeKey(d) == accountKey {
			d.top.set("activeAccountKey", json.RawMessage("null"))
		}
		if err := s.write(d); err != nil {
			return err
		}
		removed = true
		return s.deleteSnapshot(accountKey)
	})
	return removed, err
}

// Renumber reassigns slot numbers, mapping account_key → new number, in one
// sequence.json write. Only sequence.json moves; snapshots are keyed by
// account_key precisely so renumbering never has to touch a secret. An empty
// mapping is a no-op. Every key must name a slot (ErrUnknownAccount), and —
// beyond the Python, which would silently lose rows — every target must be an
// all-digit number and no two keys may share one. Nothing is written unless
// the whole mapping is valid. A target held by an account outside the mapping
// is overwritten, as in the Python; callers (swap/move) always include the
// occupant.
func (s *Store) Renumber(mapping map[string]string) error {
	if len(mapping) == 0 {
		return nil
	}
	return s.update(func(d *seqDoc) error {
		type located struct {
			number string
			raw    json.RawMessage
		}
		rows := map[string]located{}
		for _, f := range d.accounts {
			if _, k, ok := rowKey(f.val); ok {
				rows[k] = located{f.key, f.val} // last row wins, as in the dict comprehension
			}
		}
		keys := make([]string, 0, len(mapping))
		seen := map[string]string{}
		for key, num := range mapping {
			if _, ok := rows[key]; !ok {
				return fmt.Errorf("%w: %s", ErrUnknownAccount, key)
			}
			if !isSlotNumber(num) {
				return fmt.Errorf("invalid slot number %q for account_key %s", num, key)
			}
			n := trimZeros(num)
			if other, dup := seen[n]; dup {
				return fmt.Errorf("slot number %s assigned to both %s and %s", num, other, key)
			}
			seen[n] = key
			keys = append(keys, key)
		}
		// Go maps are unordered; apply in target-number order so the rewritten
		// file is deterministic.
		sort.Slice(keys, func(i, j int) bool { return numLess(mapping[keys[i]], mapping[keys[j]]) })
		for _, key := range keys {
			d.accounts.del(rows[key].number)
		}
		for _, key := range keys {
			d.accounts.set(mapping[key], rows[key].raw)
		}
		return s.write(d)
	})
}

// activeKey reads activeAccountKey; null or a non-string reads as "".
func activeKey(d *seqDoc) string {
	v, _ := d.top.get("activeAccountKey")
	var k string
	_ = json.Unmarshal(v, &k)
	return k
}

// SetActive records tycswap's intent; "" clears it (null on disk). The live
// auth.json remains the authority on what is actually active — see
// authfile.ReadLiveIdentity.
func (s *Store) SetActive(accountKey string) error {
	return s.update(func(d *seqDoc) error {
		if accountKey == "" {
			d.top.set("activeAccountKey", json.RawMessage("null"))
		} else {
			d.top.set("activeAccountKey", accountKey)
		}
		return s.write(d)
	})
}

// ActiveKey returns the recorded active account_key, or "".
func (s *Store) ActiveKey() string { return activeKey(s.read()) }

// ActiveNumber returns the active account's slot number, or "" when nothing is
// active or the active key has no slot.
func (s *Store) ActiveNumber() string {
	key := s.ActiveKey()
	if key == "" {
		return ""
	}
	if sl := s.SlotForKey(key); sl != nil {
		return sl.Number
	}
	return ""
}

// SetAlias sets (or, with "", clears) a slot's alias.
func (s *Store) SetAlias(accountKey, alias string) error {
	return s.mutate(accountKey, "alias", alias)
}

// SetDisabled sets a slot's disabled flag.
func (s *Store) SetDisabled(accountKey string, disabled bool) error {
	return s.mutate(accountKey, "disabled", disabled)
}

// SetWorkspaceName sets a slot's workspace name.
func (s *Store) SetWorkspaceName(accountKey, name string) error {
	return s.mutate(accountKey, "workspaceName", name)
}

// mutate sets one field on the first row naming accountKey. An unknown key is
// silently a no-op with no write, as in the Python.
func (s *Store) mutate(accountKey, field string, value any) error {
	return s.update(func(d *seqDoc) error {
		for i, f := range d.accounts {
			row, k, ok := rowKey(f.val)
			if !ok || k != accountKey {
				continue
			}
			row.set(field, value)
			raw, err := row.MarshalJSON()
			if err != nil {
				return err
			}
			d.accounts[i].val = raw
			return s.write(d)
		}
		return nil
	})
}

// aliasRE and ValidAlias are the switcher's NormalizeAlias rule (which cannot
// be imported here or by the importers without a cycle), for callers that
// must drop an invalid alias rather than fail: strip, lower-case, then reject
// an empty, purely numeric (reserved for slot numbers), leading-"-" or
// out-of-charset alias.
var aliasRE = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// ValidAlias returns name normalised and true, or "" and false when the
// switcher's NormalizeAlias would reject it.
func ValidAlias(name string) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || isSlotNumber(n) || strings.HasPrefix(n, "-") || !aliasRE.MatchString(n) {
		return "", false
	}
	return n, true
}

// ---- snapshot store -----------------------------------------------------

func (s *Store) useKeychain() bool { return s.platform == platform.MacOS }

func (s *Store) snapshotPath(accountKey string) string {
	return filepath.Join(s.credentialsDir(), authfile.FileKey(accountKey)+".json")
}

// WriteSnapshot persists one account's auth.json payload: to the Keychain on
// macOS, else atomically to credentials/<filekey>.json (dir 0700, file 0600).
//
// On macOS a blob too large for `security -i`'s stdin line (keychain.FitsStdin;
// a Codex auth.json with three JWTs usually is) would otherwise be passed on
// the security command line, readable by every local user through ps. Such a
// blob goes to the 0600 file instead and any older Keychain item is dropped; a
// blob that fits goes to the Keychain and any older file is removed, so only
// one copy is ever current.
func (s *Store) WriteSnapshot(accountKey string, payload map[string]any) error {
	return s.withWrite(func() error { return s.writeSnapshot(accountKey, payload) })
}

func (s *Store) writeSnapshot(accountKey string, payload map[string]any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // Python json.dumps does not HTML-escape
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("encode codex snapshot: %w", err)
	}
	blob := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if s.useKeychain() {
		account := authfile.FileKey(accountKey)
		if keychain.FitsStdin(KeychainService, account, string(blob)) {
			if err := s.kc.Set(KeychainService, account, string(blob)); err != nil {
				return fmt.Errorf("write codex snapshot to keychain: %w", err)
			}
			if err := os.Remove(s.snapshotPath(accountKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove superseded codex snapshot file: %w", err)
			}
			return nil
		}
		if err := atomicfile.Write(s.snapshotPath(accountKey), blob, fileOpts); err != nil {
			return fmt.Errorf("write codex snapshot: %w", err)
		}
		// Best effort: ReadSnapshot prefers the file, so a stale item that
		// could not be deleted is never read back.
		_ = s.kc.Delete(KeychainService, account)
		return nil
	}
	if err := atomicfile.Write(s.snapshotPath(accountKey), blob, fileOpts); err != nil {
		return fmt.Errorf("write codex snapshot: %w", err)
	}
	return nil
}

// ReadSnapshot returns one account's stored payload, or nil when it is absent,
// unreadable (including a Keychain failure), corrupt, or not a JSON object.
// On macOS the file fallback is consulted first, then the Keychain.
func (s *Store) ReadSnapshot(accountKey string) map[string]any {
	if b, err := os.ReadFile(s.snapshotPath(accountKey)); err == nil {
		if data := decodeSnapshot(string(b)); data != nil || !s.useKeychain() {
			return data
		}
	} else if !s.useKeychain() {
		return nil
	}
	v, found, err := s.kc.Get(KeychainService, authfile.FileKey(accountKey))
	if err != nil || !found {
		return nil
	}
	return decodeSnapshot(v)
}

// decodeSnapshot parses a stored blob; nil for empty, corrupt, non-object or
// a literal null.
func decodeSnapshot(blob string) map[string]any {
	if blob == "" {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(blob), &data); err != nil {
		return nil
	}
	return data
}

// DeleteSnapshot removes one account's snapshot. A missing file is not an
// error; a Keychain failure is surfaced, as in the Python. On macOS both the
// Keychain item and the file fallback are removed.
func (s *Store) DeleteSnapshot(accountKey string) error {
	return s.withWrite(func() error { return s.deleteSnapshot(accountKey) })
}

func (s *Store) deleteSnapshot(accountKey string) error {
	_ = os.Remove(s.snapshotPath(accountKey))
	if s.useKeychain() {
		if err := s.kc.Delete(KeychainService, authfile.FileKey(accountKey)); err != nil {
			return fmt.Errorf("delete codex snapshot from keychain: %w", err)
		}
	}
	return nil
}

// ---- ordered JSON object ------------------------------------------------

// field is one member of an ordered JSON object.
type field struct {
	key string
	val json.RawMessage
}

// object is a JSON object that remembers member order, so a read-modify-write
// of sequence.json keeps the layout (and unknown keys) a Python dict would.
type object []field

// parseObject decodes b as a JSON object. ok is false for anything else. A
// duplicate key keeps its first position and takes the last value, as a
// Python dict built by json.loads does.
func parseObject(b []byte) (object, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	o := object{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		k, ok := tok.(string)
		if !ok {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		o.set(k, v)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err == nil {
		return nil, false // trailing data: not a single JSON document
	}
	return o, true
}

func (o object) get(k string) (json.RawMessage, bool) {
	for _, f := range o {
		if f.key == k {
			return f.val, true
		}
	}
	return nil, false
}

// set replaces k's value in place, or appends k. A value that is not already a
// json.RawMessage is marshalled (without HTML escaping).
func (o *object) set(k string, v any) {
	raw, ok := v.(json.RawMessage)
	if !ok {
		var err error
		raw, err = marshalCompact(v)
		if err != nil {
			raw = json.RawMessage("null")
		}
	}
	for i := range *o {
		if (*o)[i].key == k {
			(*o)[i].val = raw
			return
		}
	}
	*o = append(*o, field{k, raw})
}

func (o *object) del(k string) {
	for i := range *o {
		if (*o)[i].key == k {
			*o = append((*o)[:i:i], (*o)[i+1:]...)
			return
		}
	}
}

// MarshalJSON renders the members in order. The enclosing encoder re-indents
// the result, so atomicfile.WriteJSON yields the Python's indent=2 layout.
func (o object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := marshalCompact(f.key)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		if len(f.val) == 0 {
			buf.WriteString("null")
		} else {
			buf.Write(f.val)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// marshalCompact is json.Marshal without HTML escaping or a trailing newline.
func marshalCompact(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}
