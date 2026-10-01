// transfer.go — export and import Codex accounts, and purge tycswap's Codex
// data. Implements claude-swap PR #252 codex/transfer.py.
//
// The export file contains live OAuth tokens. That is the point — an export
// you cannot log in with is not a backup — but it makes the file exactly as
// sensitive as the Keychain items it came from, so it is created 0600 before
// any token goes in, and the format says so in a top-level "warning" field
// that any tool reading it will surface.
//
// Deliberately a separate format from the Claude side's .tycswap envelope
// (internal/transfer): the two providers store different things (an
// account_key and an auth.json payload here, an org-scoped credential blob
// there), and one file that had to describe both would be a union type nobody
// could validate. A "provider" field means an import can refuse a file from
// the wrong side rather than half-applying it.
//
// Purge removes tycswap's Codex store root (the Store's Root) and every
// snapshot in it, Keychain items included — a purge that left the secrets
// behind would be worse than none, since nothing would list them any more. It
// never touches the live login or anything else under ~/.codex: tycswap manages
// copies, and the user's actual codex login is not ours to delete. As
// hardening over the Python, Purge refuses a root that is, or contains, the
// codex home.
//
// Errors are TransferError (cerr.Transfer): transfer.py raises the bare
// ClaudeSwitchError base, which has no Kind in tycswap, and TransferError is the
// Kind the Claude-side transfer package uses for the same failures.

// Package transfer is the Codex provider's export/import format and purge.
package transfer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/storenames"
	"github.com/tyclab/tycswap/internal/termsafe"
)

// ExportVersion is bumped when the on-disk export shape changes incompatibly.
const ExportVersion = 1

// Provider is the export file's provider tag.
const Provider = "codex"

// Warning is the export's top-level warning, verbatim from transfer.py.
const Warning = "This file contains live OAuth tokens for the accounts below. " +
	"Anyone who can read it can use those accounts. Keep it private and " +
	"delete it once imported."

// exportAccount is one exported row, in transfer.py's key order.
type exportAccount struct {
	AccountKey    string         `json:"accountKey"`
	Email         string         `json:"email"`
	Plan          string         `json:"plan"`
	WorkspaceName string         `json:"workspaceName"`
	Alias         string         `json:"alias"`
	Disabled      bool           `json:"disabled"`
	AuthMode      string         `json:"authMode"`
	Auth          map[string]any `json:"auth"`
}

type exportDoc struct {
	Version  int             `json:"version"`
	Provider string          `json:"provider"`
	Warning  string          `json:"warning"`
	Accounts []exportAccount `json:"accounts"`
}

// Export writes accounts (with credentials) to destination; "-" writes the
// document plus a newline to stdout (os.Stdout when nil). A non-empty account
// (NUM|EMAIL|ALIAS) limits the export to that one slot. Slots without a stored
// snapshot are left out: an account with no credentials cannot be imported
// anywhere, and a silent empty row would look like a successful backup.
// Returns how many accounts were written.
func Export(st *store.Store, destination, account string, stdout io.Writer) (int, error) {
	slots := st.Slots()
	if account != "" {
		target, err := ResolveSlot(st, account)
		if err != nil {
			return 0, err
		}
		var only []store.Slot
		for _, s := range slots {
			if s.Number == target.Number {
				only = append(only, s)
			}
		}
		slots = only
	}
	if len(slots) == 0 {
		return 0, cerr.Transfer("No Codex accounts to export")
	}

	accounts := []exportAccount{}
	for _, s := range slots {
		payload := st.ReadSnapshot(s.AccountKey)
		if payload == nil {
			continue
		}
		accounts = append(accounts, exportAccount{
			AccountKey: s.AccountKey, Email: s.Email, Plan: s.Plan,
			WorkspaceName: s.WorkspaceName, Alias: s.Alias, Disabled: s.Disabled,
			AuthMode: s.AuthMode, Auth: payload,
		})
	}
	if len(accounts) == 0 {
		return 0, cerr.Transfer("No Codex accounts with stored credentials to export")
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(exportDoc{ExportVersion, Provider, Warning, accounts}); err != nil {
		return 0, cerr.Transfer("cannot encode export: %s", err.Error()).Wrap(err)
	}
	blob := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

	if destination == "-" {
		if stdout == nil {
			stdout = os.Stdout
		}
		if _, err := stdout.Write(append(blob, '\n')); err != nil {
			return 0, err
		}
		return len(accounts), nil
	}

	path := expandUser(destination)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, cerr.Transfer("Cannot write %s: %s", destination, err.Error()).Wrap(err)
	}
	if err := writeExportFile(path, blob); err != nil {
		return 0, cerr.Transfer("Cannot write %s: %s", destination, err.Error()).Wrap(err)
	}
	return len(accounts), nil
}

// writeExportFile writes blob to path atomically: a temp file in path's own
// directory, created 0600 before the tokens go in (never chmod'ed afterwards,
// when it would already have been readable), then renamed over path. A reader
// never sees a half-written export, an interrupted write leaves the previous
// file intact, and an existing wider-mode file is replaced by a 0600 one. The
// parent directory's mode is left alone: it is the user's.
func writeExportFile(path string, blob []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tycswap-codex-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(name)
		}
	}()
	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(0o600); err != nil {
			tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(blob); err != nil {
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
	if err := os.Rename(name, path); err != nil {
		return err
	}
	atomicfile.SyncDir(filepath.Dir(path))
	committed = true
	return nil
}

// Import reads accounts from source ("-" reads stdin, os.Stdin when nil) into
// st and returns how many it wrote. It refuses a file whose provider is set
// and is not "codex", or whose integer version is newer than ExportVersion.
// An account already in the store is skipped unless force; malformed rows
// (no accountKey or no auth object) are skipped silently, as in the Python.
//
// Hardening over the Python: a document larger than MaxImportBytes is refused
// before it is parsed; a row whose auth payload decodes to an identity with a
// different account_key is skipped (a row with no decodable identity is
// accepted, as the Python accepts every row); an alias the switcher's
// NormalizeAlias would reject is dropped rather than imported. The rows are
// written under the store lock (st.WithLock).
//
// Like the Claude import, every row that would be imported is validated
// before anything is written, and one bad row refuses the whole import: a
// non-empty email must be one a file name can carry (storenames.ValidEmail), an alias must
// hold no control character and be at most 64 bytes, and the
// accountKey may hold no control character. Plan and workspace names are
// stored without control characters (termsafe.Strip).
func Import(st *store.Store, source string, force bool, stdin io.Reader) (int, error) {
	var raw []byte
	var err error
	if source == "-" {
		if stdin == nil {
			stdin = os.Stdin
		}
		raw, err = readLimited(stdin)
	} else {
		var f *os.File
		if f, err = os.Open(expandUser(source)); err == nil {
			raw, err = readLimited(f)
			f.Close()
		}
	}
	if err != nil {
		return 0, cerr.Transfer("Cannot read %s: %s", source, err.Error()).Wrap(err)
	}
	if len(raw) > MaxImportBytes {
		return 0, cerr.Transfer("%s is larger than %d MiB; refusing to import it", source, MaxImportBytes>>20)
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		var probe any
		if json.Unmarshal(raw, &probe) == nil {
			return 0, cerr.Transfer("%s is not a tycswap export", source)
		}
		return 0, cerr.Transfer("%s is not valid JSON: %s", source, err.Error()).Wrap(err)
	}
	if document == nil {
		return 0, cerr.Transfer("%s is not a tycswap export", source)
	}

	if p, has := document["provider"]; has && string(bytes.TrimSpace(p)) != "null" {
		name, isStr := stringValue(p)
		if !isStr {
			name = strings.TrimSpace(string(p))
		}
		if !isStr || name != Provider {
			// Refuse rather than half-apply: a Claude export's rows describe
			// different fields entirely.
			return 0, cerr.Transfer("%s is a '%s' export, not a Codex one", source, name)
		}
	}
	if v, ok := pyInt(document["version"]); ok && v > ExportVersion {
		return 0, cerr.Transfer("%s uses export version %d, newer than this tycswap understands", source, v)
	}

	var rows []json.RawMessage
	if json.Unmarshal(document["accounts"], &rows) != nil || len(rows) == 0 {
		return 0, cerr.Transfer("%s contains no accounts", source)
	}

	if err := validateRows(rows); err != nil {
		return 0, err
	}

	imported := 0
	err = st.WithLock(func() error {
		existing := map[string]bool{}
		for _, s := range st.Slots() {
			existing[s.AccountKey] = true
		}
		for _, r := range rows {
			var row map[string]json.RawMessage
			if json.Unmarshal(r, &row) != nil || row == nil {
				continue
			}
			key, _ := stringValue(row["accountKey"])
			payload, ok := decodeObject(row["auth"])
			if key == "" || !ok {
				continue
			}
			if id := authfile.ParseIdentity(payload); id != nil && id.Identifiable() && id.AccountKey() != key {
				continue // the tokens belong to another account than the row claims
			}
			if existing[key] && !force {
				continue
			}
			if _, err := st.UpsertSlot(key, store.Upsert{
				Email:         pyStr(row["email"], ""),
				Plan:          termsafe.Strip(pyStr(row["plan"], "")),
				WorkspaceName: termsafe.Strip(pyStr(row["workspaceName"], "")),
				AuthMode:      pyStr(row["authMode"], "chatgpt"),
			}); err != nil {
				return err
			}
			if alias, _ := stringValue(row["alias"]); alias != "" {
				if norm, valid := store.ValidAlias(alias); valid {
					if err := st.SetAlias(key, norm); err != nil {
						return err
					}
				}
			}
			if truthy(row["disabled"]) {
				if err := st.SetDisabled(key, true); err != nil {
					return err
				}
			}
			if err := st.WriteSnapshot(key, payload); err != nil {
				return err
			}
			imported++
		}
		return nil
	})
	return imported, err
}

// maxAliasLen bounds an imported alias, as the Claude import does.
const maxAliasLen = 64

// validateRows is the import's pass 1: every row the write pass would take
// (an object with an accountKey and an auth object) is checked before any
// write, so a bad row late in the file cannot leave earlier rows imported.
func validateRows(rows []json.RawMessage) error {
	for _, r := range rows {
		var row map[string]json.RawMessage
		if json.Unmarshal(r, &row) != nil || row == nil {
			continue
		}
		key, _ := stringValue(row["accountKey"])
		if _, ok := decodeObject(row["auth"]); key == "" || !ok {
			continue
		}
		if termsafe.HasControl(key) {
			return cerr.Transfer("invalid accountKey in imported account: %q contains a control character", key)
		}
		if email, isStr := stringValue(row["email"]); isStr && email != "" && !storenames.ValidEmail(email) {
			return cerr.Transfer("invalid email in imported account: %q", email)
		}
		if alias, isStr := stringValue(row["alias"]); isStr && alias != "" {
			if len(alias) > maxAliasLen {
				return cerr.Transfer("invalid alias for %s: longer than %d bytes", key, maxAliasLen)
			}
			// Surrounding spaces are trimmed and an alias the switcher would
			// reject is dropped (ValidAlias); a control character is refused.
			if strings.IndexFunc(alias, termsafe.IsControl) >= 0 {
				return cerr.Transfer("invalid alias for %s: %q contains a control character", key, alias)
			}
		}
	}
	return nil
}

// MaxImportBytes caps what Import reads from a file or stdin: an export of a
// few accounts is kilobytes, and an unbounded read of a wrong file (or an
// endless pipe) would exhaust memory before JSON parsing could reject it.
const MaxImportBytes = 8 << 20

// readLimited reads at most MaxImportBytes+1 bytes, so an oversized document
// is detectable without reading all of it.
func readLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, MaxImportBytes+1))
}

// Purge deletes every Codex account tycswap manages and st's store root, after
// a "[y/N]" confirmation read from in (os.Stdin when nil) unless assumeYes.
// Messages go to out (os.Stdout when nil). Returns whether it ran; EOF at the
// prompt reads as "no".
func Purge(st *store.Store, assumeYes bool, in io.Reader, out io.Writer) (bool, error) {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	slots := st.Slots()
	root := st.Root()

	if err := guardRoot(root); err != nil {
		return false, err
	}
	if _, err := os.Stat(root); len(slots) == 0 && os.IsNotExist(err) {
		io.WriteString(out, "No tycswap Codex data to remove.\n")
		return false, nil
	}

	if !assumeYes {
		io.WriteString(out, "Remove "+strconv.Itoa(len(slots))+" managed Codex account(s) and all tycswap Codex "+
			"data? Your ~/.codex login is left alone. [y/N] ")
		// A read error (EOF included) leaves whatever was read, usually "",
		// which is not "y" — the Python's EOFError becomes a plain "no".
		line, _ := bufio.NewReader(in).ReadString('\n')
		line = strings.TrimSuffix(strings.TrimRight(line, "\n"), "\r")
		if strings.ToLower(line) != "y" {
			io.WriteString(out, "Cancelled\n")
			return false, nil
		}
	}

	for _, s := range slots {
		if err := st.DeleteSnapshot(s.AccountKey); err != nil {
			return false, err
		}
	}
	_ = os.RemoveAll(root)
	io.WriteString(out, "Removed "+strconv.Itoa(len(slots))+" Codex account(s) and "+root+"\n")
	return true, nil
}

// guardRoot refuses a store root that is empty, is the codex home, or
// contains it: RemoveAll on such a root would take the live login with it.
// Both paths are compared with symlinks resolved, so a codex home reached
// through a link into the root is caught, and a path that cannot be resolved
// refuses the purge rather than waving it through.
func guardRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return cerr.Transfer("refusing to purge: the Codex store root is empty")
	}
	r, err := resolvePath(root)
	if err != nil {
		return cerr.Transfer("refusing to purge %s: cannot resolve it: %s", root, err.Error()).Wrap(err)
	}
	home := authfile.Home()
	h, err := resolvePath(home)
	if err != nil {
		return cerr.Transfer("refusing to purge %s: cannot resolve the codex home %s: %s", root, home, err.Error()).Wrap(err)
	}
	if h == r || strings.HasPrefix(h, withSeparator(r)) {
		return cerr.Transfer("refusing to purge %s: it contains the codex login at %s", r, h)
	}
	return nil
}

// resolvePath is p made absolute and clean with every symlink resolved. The
// deepest existing ancestor is resolved and the not-yet-existing remainder
// appended, so a missing path under a linked directory still resolves
// through the link; any error other than not-exist is returned.
func resolvePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	cur, rest := abs, ""
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Clean(filepath.Join(real, rest)), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// withSeparator appends one path separator unless p already ends in one (a
// volume root), so "/a/b" is not read as a prefix of "/a/bc".
func withSeparator(p string) string {
	if strings.HasSuffix(p, string(filepath.Separator)) {
		return p
	}
	return p + string(filepath.Separator)
}

// ---- small JSON helpers (Python value semantics) ------------------------

func expandUser(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path[1:], "/"))
		}
	}
	return path
}

// decodeObject decodes raw as a JSON object, keeping numbers as their
// literals so the stored snapshot is the exported document, not a float64
// approximation of it.
func decodeObject(raw json.RawMessage) (map[string]any, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

// stringValue decodes raw when it is a JSON string; ok is false otherwise.
func stringValue(raw json.RawMessage) (string, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(t, &s) != nil {
		return "", false
	}
	return s, true
}

// truthy is Python truthiness for a JSON value.
func truthy(raw json.RawMessage) bool {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch x := v.(type) {
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

// pyInt reports whether raw is what isinstance(x, int) accepts after
// json.loads: an integer literal or a bool.
func pyInt(raw json.RawMessage) (int, bool) {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "true":
		return 1, true
	case "false":
		return 0, true
	}
	if s == "" || strings.ContainsAny(s, ".eE\"") {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// pyStr is `str(value or def)`: falsy yields def, a string itself, true
// "True", anything else its JSON literal.
func pyStr(raw json.RawMessage, def string) string {
	if !truthy(raw) {
		return def
	}
	if s, ok := stringValue(raw); ok {
		return s
	}
	if t := strings.TrimSpace(string(raw)); t != "true" {
		return t
	}
	return "True"
}
