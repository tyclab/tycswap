// Package ccsettings points Claude Code at an API-key account's endpoint
// through Claude Code's OWN settings.json, and takes it back out exactly
// (DESIGN A46).
//
// An API-key account may carry a base URL. Claude Code has no credential
// store slot for "this key, at that endpoint": it reads both from its
// environment, and the `env` block of `<config home>/settings.json` is
// exported into that environment when Claude Code starts. A switch onto such
// an account therefore writes two keys there, `env.ANTHROPIC_BASE_URL` and
// the key as `env.ANTHROPIC_AUTH_TOKEN` (sent as a bearer token, which is
// what a gateway or proxy expects, and ranked above every stored login), and
// a switch to any other account puts them back.
//
// The one thing that makes this a switchable login rather than a one-way
// installer is the record: Apply writes the prior value (or absence) of every
// key it touches to a sidecar file BEFORE it writes settings.json, and Revert
// restores exactly those keys from it. Everything else in settings.json
// (hooks, permissions, MCP servers, the user's own env keys) is read and
// written back untouched; the two keys in OwnedKeys are the whole allowlist.
//
// tycswap's internal/settings is tycswap's own settings.json in the backup
// root; the two files never meet, and this package never reads that one.
package ccsettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/platform"
)

// The two settings.json keys the profile owns. "env.X" addresses
// settings["env"]["X"].
const (
	KeyBaseURL   = "env.ANTHROPIC_BASE_URL"
	KeyAuthToken = "env.ANTHROPIC_AUTH_TOKEN"
)

// SidecarName is the record's file name in tycswap's backup root.
const SidecarName = "claude-settings.prev.json"

// SidecarVersion is the sidecar schema version.
const SidecarVersion = 1

// ownedKeys is the allowlist: the complete set of keys Apply may write and
// Revert restores. Nothing outside it is ever changed, apart from the "env"
// container Apply creates when there was none and Revert removes again.
var ownedKeys = []string{KeyBaseURL, KeyAuthToken}

// OwnedKeys returns a copy of the allowlist, in write order.
func OwnedKeys() []string { return append([]string(nil), ownedKeys...) }

// envContainerKey is the sidecar entry recording whether "env" existed
// before the first Apply, so Revert can remove a container Apply created.
const envContainerKey = "env"

// Profile is what Apply writes: the endpoint and the key sent to it.
type Profile struct {
	BaseURL string
	Token   string
}

// prior is one recorded key state: absent, or present with its value.
type prior struct {
	Present bool `json:"present"`
	Value   any  `json:"value,omitempty"`
}

// sidecar is <backup root>/claude-settings.prev.json.
type sidecar struct {
	Version      int              `json:"version"`
	SettingsPath string           `json:"settingsPath"`
	Keys         map[string]prior `json:"keys"`
}

// Apply writes the profile into settingsPath, recording the prior state of
// every owned key in sidecarPath BEFORE the settings file is touched.
//
// A second Apply keeps the sidecar's ORIGINAL priors, so a switch from one
// endpoint account to another and then to a subscription account lands on the
// settings.json from before the first one, not on the first profile. A
// sidecar recorded for a different settings file (CLAUDE_CONFIG_DIR changed in
// between) is reverted there first, so one record always describes one file.
//
// A new record never takes tycswap's own endpoint for the user's: when there
// is no record (it was removed by hand) and the two keys hold exactly p or one
// of known (the endpoint accounts tycswap holds), they are recorded as absent,
// so the way back removes them instead of putting that endpoint back.
//
// A missing settings file is {}; an unparseable one is an error and is left
// untouched, as is a corrupt sidecar. The record names the settings file by
// its absolute path.
func Apply(settingsPath, sidecarPath string, p Profile, known []Profile) error {
	if _, err := ValidateBaseURL(p.BaseURL); err != nil {
		return err
	}
	if _, err := ValidateToken(p.Token); err != nil {
		return err
	}
	settingsPath = absPath(settingsPath)
	if err := refuseSymlink(settingsPath); err != nil {
		return err // before the record is written: nothing changes
	}
	sc, err := loadSidecar(sidecarPath)
	if err != nil {
		return err
	}
	if sc != nil && sc.SettingsPath != "" && sc.SettingsPath != settingsPath {
		if _, err := Revert(sc.SettingsPath, sidecarPath, nil); err != nil {
			return err
		}
		sc = nil
	}
	root, err := readSettings(settingsPath)
	if err != nil {
		return err
	}
	ours := false
	if sc == nil {
		sc = &sidecar{Version: SidecarVersion, SettingsPath: settingsPath, Keys: map[string]prior{}}
		ours = holdsKnown(root, append([]Profile{p}, known...))
	}
	envVal, envPresent := root[envContainerKey]
	if _, recorded := sc.Keys[envContainerKey]; !recorded {
		pr := prior{Present: envPresent}
		if env, isMap := envVal.(map[string]any); ours && isMap && len(env) == len(ownedKeys) {
			pr.Present = false // the container holds nothing but tycswap's own two keys
		}
		if _, isMap := envVal.(map[string]any); envPresent && !isMap {
			pr.Value = envVal // a non-object env is replaced by set(); keep it for Revert
		}
		sc.Keys[envContainerKey] = pr
	}
	for _, k := range ownedKeys {
		if _, recorded := sc.Keys[k]; recorded {
			continue
		}
		v, present := get(root, k)
		if ours {
			v, present = nil, false
		}
		sc.Keys[k] = prior{Present: present, Value: v}
	}
	if err := atomicfile.WriteJSON(sidecarPath, sc, atomicfile.Opts{FileMode: 0o600, DirMode: 0o700}); err != nil {
		return fmt.Errorf("record the prior Claude Code settings: %w", err)
	}

	set(root, KeyBaseURL, strings.TrimSpace(p.BaseURL))
	set(root, KeyAuthToken, strings.TrimSpace(p.Token))
	return writeSettings(settingsPath, root)
}

// RevertOutcome says how a Revert ended.
type RevertOutcome int

const (
	// RevertedNothing: no record and nothing of ours in the file.
	RevertedNothing RevertOutcome = iota
	// RevertedFromRecord: the sidecar's priors were restored exactly.
	RevertedFromRecord
	// RevertedByValue: no sidecar, but settings.json held exactly one known
	// endpoint and its key, and both were removed. What they replaced cannot
	// be brought back this way: there is no record of it.
	RevertedByValue
)

// Revert restores every recorded key to its recorded state and removes the
// sidecar. Keys the user changed after Apply are still reverted (the sidecar
// wins); every other key is left as it is now. The settings file is the one
// the sidecar records; settingsPath is used when the record names none.
//
// Without a sidecar it does not simply give up: a profile whose record was
// lost would otherwise keep Claude Code on the endpoint with no way off. When
// settingsPath holds env.ANTHROPIC_BASE_URL and env.ANTHROPIC_AUTH_TOKEN
// equal to one of known (the endpoint accounts tycswap holds), both are
// removed; anything else is the user's and stays.
func Revert(settingsPath, sidecarPath string, known []Profile) (RevertOutcome, error) {
	sc, err := loadSidecar(sidecarPath)
	if err != nil {
		return RevertedNothing, err
	}
	if sc == nil {
		return revertByValue(settingsPath, known)
	}
	if sc.SettingsPath != "" {
		settingsPath = sc.SettingsPath
	}
	root, err := readSettings(settingsPath)
	if err != nil {
		return RevertedNothing, err
	}
	for _, k := range ownedKeys {
		pr, ok := sc.Keys[k]
		if !ok {
			continue
		}
		if pr.Present {
			set(root, k, pr.Value)
		} else {
			del(root, k)
		}
	}
	// Remove an "env" container Apply created if nothing else has moved in;
	// put back a non-object env Apply had to replace.
	if pr, ok := sc.Keys[envContainerKey]; ok {
		switch {
		case !pr.Present:
			if env, isMap := root[envContainerKey].(map[string]any); isMap && len(env) == 0 {
				delete(root, envContainerKey)
			}
		case pr.Value != nil:
			root[envContainerKey] = pr.Value
		}
	}
	if err := writeSettings(settingsPath, root); err != nil {
		return RevertedNothing, err
	}
	if err := os.Remove(sidecarPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return RevertedFromRecord, err
	}
	return RevertedFromRecord, nil
}

// revertByValue removes an endpoint profile that has no sidecar to restore
// from, and only one that is recognisably tycswap's: both owned keys present
// and equal to one known endpoint and its key.
func revertByValue(settingsPath string, known []Profile) (RevertOutcome, error) {
	if settingsPath == "" || len(known) == 0 {
		return RevertedNothing, nil
	}
	root, err := readSettings(settingsPath)
	if err != nil {
		return RevertedNothing, err
	}
	if !holdsKnown(root, known) {
		return RevertedNothing, nil
	}
	del(root, KeyBaseURL)
	del(root, KeyAuthToken)
	if env, isMap := root[envContainerKey].(map[string]any); isMap && len(env) == 0 {
		delete(root, envContainerKey)
	}
	if err := writeSettings(settingsPath, root); err != nil {
		return RevertedNothing, err
	}
	return RevertedByValue, nil
}

// holdsKnown reports whether root's two owned keys hold exactly one of known:
// an endpoint and its key that tycswap wrote.
func holdsKnown(root map[string]any, known []Profile) bool {
	base, okBase := get(root, KeyBaseURL)
	token, okToken := get(root, KeyAuthToken)
	if !okBase || !okToken {
		return false
	}
	b, _ := base.(string)
	t, _ := token.(string)
	if b == "" || t == "" {
		return false
	}
	for _, p := range known {
		if b == strings.TrimSpace(p.BaseURL) && t == strings.TrimSpace(p.Token) {
			return true
		}
	}
	return false
}

// absPath is path made absolute, or path itself when that fails: a relative
// CLAUDE_CONFIG_DIR would otherwise name a different file from another
// working directory.
func absPath(path string) string {
	if path == "" {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// IsApplied reports whether a readable sidecar exists, i.e. the profile is in
// place and Revert would restore from the record.
func IsApplied(sidecarPath string) bool {
	sc, err := loadSidecar(sidecarPath)
	return err == nil && sc != nil
}

// SidecarExists reports whether there is a sidecar file at all, readable or
// not. A corrupt one still means a profile may be in place, so a switch must
// stop at it rather than walk past it.
func SidecarExists(sidecarPath string) bool {
	_, err := os.Lstat(sidecarPath)
	return err == nil
}

// Check reads settingsPath and sidecarPath the way Apply and Revert will and
// returns the refusal they would return: an unparseable settings file or a
// corrupt sidecar. A switch calls it before it writes anything, so such a
// file stops the switch while the credential is still the old one.
func Check(settingsPath, sidecarPath string) error {
	settingsPath = absPath(settingsPath)
	if err := checkWritable(settingsPath); err != nil {
		return err
	}
	sc, err := loadSidecar(sidecarPath)
	if err != nil {
		return err
	}
	if sc != nil && sc.SettingsPath != "" && sc.SettingsPath != settingsPath {
		if err := checkWritable(sc.SettingsPath); err != nil {
			return err
		}
	}
	return nil
}

// checkWritable answers what writeSettings would: the file parses (or is
// absent), and it is not a symlink.
func checkWritable(path string) error {
	if err := refuseSymlink(path); err != nil {
		return err
	}
	_, err := readSettings(path)
	return err
}

// refuseSymlink refuses a settings file that is a symbolic link. A dotfile
// manager links it into a repository or a read-only store: replacing the link
// with a regular file would break that management, and writing through it
// would put a key into a file that is tracked or shared. Neither is undone by
// a revert, so the endpoint is not written at all.
func refuseSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return nil
	}
	target, _ := os.Readlink(path)
	return fmt.Errorf("%s is a symbolic link (to %s); refusing to write an API key over or through it. Make it a regular file to use an API-key account with a base URL", path, target)
}

// RecordedSettingsPath is the settings file the sidecar records, "" when there
// is no readable sidecar or it names none.
func RecordedSettingsPath(sidecarPath string) string {
	sc, err := loadSidecar(sidecarPath)
	if err != nil || sc == nil {
		return ""
	}
	return sc.SettingsPath
}

// RecordsFile reports whether a readable record exists and names
// settingsPath, i.e. whether the profile is in place in that file.
func RecordsFile(sidecarPath, settingsPath string) bool {
	sc, err := loadSidecar(sidecarPath)
	return err == nil && sc != nil && sc.SettingsPath == absPath(settingsPath)
}

// Live returns the endpoint and key settingsPath currently carries in the two
// owned keys ("" for one that is absent or not a string). A file that cannot
// be read or parsed carries nothing.
func Live(settingsPath string) Profile {
	root, err := readSettings(settingsPath)
	if err != nil {
		return Profile{}
	}
	var p Profile
	if v, ok := get(root, KeyBaseURL); ok {
		p.BaseURL, _ = v.(string)
	}
	if v, ok := get(root, KeyAuthToken); ok {
		p.Token, _ = v.(string)
	}
	return p
}

// ---- snapshot for a switch's rollback ----

// Snapshot is the exact bytes (or absence) of a set of files, taken before a
// switch writes any of them, so a switch that fails afterwards can put every
// one of them back as it was: settings.json and the sidecar alike.
type Snapshot struct {
	files []fileState
}

type fileState struct {
	path    string
	present bool
	data    []byte
	mode    os.FileMode
	// link is the target when the path was a symbolic link; such a path is
	// put back as that link, never as a copy of what it pointed to.
	link string
}

// Take records the current state of each path ("" entries and duplicates are
// skipped). A file that exists but cannot be read is an error: a rollback
// that could not restore it must not be promised.
func Take(paths ...string) (*Snapshot, error) {
	snap := &Snapshot{}
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		fsState := fileState{path: p}
		info, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return nil, err
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return nil, err
			}
			fsState.present, fsState.link = true, target
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			fsState.present, fsState.data, fsState.mode = true, data, info.Mode().Perm()
		}
		snap.files = append(snap.files, fsState)
	}
	return snap, nil
}

// Restore puts every recorded file back: its bytes and mode, or no file. It
// tries every file and returns the errors joined.
func (s *Snapshot) Restore() error {
	if s == nil {
		return nil
	}
	var errs []error
	for _, f := range s.files {
		if f.link != "" {
			if cur, err := os.Readlink(f.path); err == nil && cur == f.link {
				continue // still the same link: nothing was written over it
			}
			if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
				continue
			}
			if err := os.Symlink(f.link, f.path); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if !f.present {
			if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if err := writeAtomic(f.path, f.data, f.mode); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---- file helpers ----

// readSettings reads Claude Code's settings.json. A missing (or blank) file is
// an empty object; an unparseable one is an ERROR, never {}: rewriting it
// would wipe every hook, permission and MCP entry the user has, and Revert
// could only bring back the keys this package owns. Numbers are kept as
// written (json.Number), so a rewrite changes no value it does not own.
func readSettings(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil || root == nil || dec.More() {
		return nil, fmt.Errorf("%s is not a JSON object; refusing to rewrite it (fix or move the file, then retry)", path)
	}
	return root, nil
}

// writeSettings writes root as 2-space-indented JSON, atomically, mode 0600:
// the file now holds a key.
func writeSettings(path string, root map[string]any) error {
	if err := refuseSymlink(path); err != nil {
		return err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return err
	}
	return writeAtomic(path, bytes.TrimSuffix(b.Bytes(), []byte("\n")), 0o600)
}

// writeAtomic writes data to path through a temp sibling and a rename. A
// missing parent is created 0700; an existing one keeps its mode (it is
// Claude Code's config home, not ours to tighten).
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := atomicfile.SyncFile(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !platform.IsWindows() {
		if err := os.Chmod(tmpName, mode); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	atomicfile.SyncDir(dir)
	committed = true
	return nil
}

// loadSidecar reads the record; nil when there is none. A sidecar that does
// not parse is an error: guessing the priors would put back values nobody
// had.
func loadSidecar(path string) (*sidecar, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var sc sidecar
	if err := dec.Decode(&sc); err != nil {
		return nil, fmt.Errorf("%s is corrupt (%v); it records what Claude Code's settings.json held before an API-key account's endpoint was written there. Fix or remove it, then retry", path, err)
	}
	if sc.Keys == nil {
		sc.Keys = map[string]prior{}
	}
	return &sc, nil
}

// ---- dotted-key access (one level: "env.X") ----

func split(key string) (container, leaf string) {
	if i := strings.IndexByte(key, '.'); i >= 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

func get(root map[string]any, key string) (any, bool) {
	c, leaf := split(key)
	if c == "" {
		v, ok := root[leaf]
		return v, ok
	}
	m, ok := root[c].(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := m[leaf]
	return v, ok
}

func set(root map[string]any, key string, v any) {
	c, leaf := split(key)
	if c == "" {
		root[leaf] = v
		return
	}
	m, ok := root[c].(map[string]any)
	if !ok {
		m = map[string]any{}
		root[c] = m
	}
	m[leaf] = v
}

func del(root map[string]any, key string) {
	c, leaf := split(key)
	if c == "" {
		delete(root, leaf)
		return
	}
	if m, ok := root[c].(map[string]any); ok {
		delete(m, leaf)
	}
}
