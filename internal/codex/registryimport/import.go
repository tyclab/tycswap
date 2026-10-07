// import.go — one-time import of codex-auth's accounts into tycswap's own Codex
// store. Implements claude-swap PR #252 codex/registry_import.py.
//
// Read-only against ~/.codex/accounts/: tycswap never writes that tree. The user
// keeps a working codex-auth install and can go back to it — the price is that
// the two stores diverge after the import, which is the accepted cost of
// owning our own format.
//
// Schema support mirrors codex-auth's own history. `version = 2` is
// email-keyed and has no account_key: there is nothing to key a snapshot by,
// so its rows are counted as skipped rather than guessed at — a guessed key
// would file one account's tokens under another's identity. `schema_version`
// 3 and 4 are record-key based with an identical account layout; v4 renamed
// the plan tiers, and v3 rows are normalised to v4 semantics on the way in so
// a Business account does not display as Enterprise.
//
// An unknown, newer schema is refused outright. Misreading a format we do not
// know would corrupt the user's account list; reporting "too new" costs them
// nothing but an upgrade.
//
// Deviation from the Python: registry_import.py runs normalize_plan over every
// row whatever the schema, which would relabel a v4 "business" row (already
// final semantics) as "enterprise". The module docstring says v3 rows are the
// ones normalised, so this port normalises rows of schema < 4 only.

package registryimport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/logging"
)

// MaxSchema is the highest schema_version this importer understands.
const MaxSchema = 4

// Log is the package logger seam (mirrors internal/oauth.Log). When nil, the
// "schema too new" warning is dropped; the Result still carries it.
var Log *logging.Logger

type Result struct {
	Imported          int
	Skipped           int
	Source            string
	UnsupportedSchema *int
}

// DidAnything reports whether any account was imported.
func (r Result) DidAnything() bool { return r.Imported > 0 }

// Options configures Import. Empty paths default to the authfile locations.
type Options struct {
	OnlyIfEmpty bool
	// RegistryPath defaults to authfile.AuthRegistryPath().
	RegistryPath string
	// AccountsDir defaults to authfile.AuthAccountsDir().
	AccountsDir string
}

// Import copies codex-auth's accounts into st. Safe to call repeatedly: every
// row is an upsert keyed by account_key, so a re-run refreshes rather than
// duplicates. A missing, unreadable or non-object registry is a no-op
// (Result{}), and a row whose snapshot is missing, torn or not an object costs
// that one account (Skipped), never the whole import. The returned error is
// only ever a store lock or write failure.
//
// The whole pass uses the transaction-scoped view supplied by st.WithLock,
// so OnlyIfEmpty's check and its writes cannot interleave with another writer.
//
// Hardening over the Python: a row whose snapshot decodes to an identity with
// a different account_key is skipped (the registry key is not trusted to name
// the tokens filed under it); a snapshot with no decodable identity is
// accepted, as the Python accepts every row. An alias the switcher's
// NormalizeAlias would reject (for example "2", which would shadow slot 2) is
// dropped, not imported and not fatal.
func Import(st *store.Store, opts Options) (Result, error) {
	// Cheap no-op checks first, outside the lock: taking it creates the store
	// root, which a pass that imports nothing must not do. OnlyIfEmpty is
	// checked again under the lock.
	if opts.OnlyIfEmpty && len(st.Slots()) > 0 {
		return Result{}, nil
	}
	registryPath := opts.RegistryPath
	if registryPath == "" {
		registryPath = authfile.AuthRegistryPath()
	}
	if _, ok := loadRegistry(registryPath); !ok {
		return Result{}, nil
	}
	var res Result
	err := st.WithLock(func(st *store.Store) error {
		var err error
		res, err = importLocked(st, opts)
		return err
	})
	return res, err
}

// importLocked is Import's body; the caller holds the store lock.
func importLocked(st *store.Store, opts Options) (Result, error) {
	registryPath := opts.RegistryPath
	if registryPath == "" {
		registryPath = authfile.AuthRegistryPath()
	}
	accountsDir := opts.AccountsDir
	if accountsDir == "" {
		accountsDir = authfile.AuthAccountsDir()
	}

	if opts.OnlyIfEmpty && len(st.Slots()) > 0 {
		return Result{}, nil
	}

	data, ok := loadRegistry(registryPath)
	if !ok {
		return Result{}, nil
	}

	schemaRaw := json.RawMessage("2")
	if v, has := data["schema_version"]; has && truthy(v) {
		schemaRaw = v
	} else if v, has := data["version"]; has && truthy(v) {
		schemaRaw = v
	}
	schema, isInt := pyInt(schemaRaw)
	if !isInt || schema > MaxSchema {
		if Log != nil {
			Log.Warningf("codex-auth registry uses schema %s, newer than this tycswap "+
				"understands (max %d); not importing", string(schemaRaw), MaxSchema)
		}
		r := Result{}
		if isInt {
			r.UnsupportedSchema = &schema
		}
		return r, nil
	}

	imported, skipped := 0, 0
	for _, row := range rows(data["accounts"]) {
		var key string
		if err := json.Unmarshal(row["account_key"], &key); err != nil || key == "" {
			skipped++
			continue
		}

		payload, ok := readSnapshot(filepath.Join(accountsDir, authfile.FileKey(key)+".auth.json"))
		if !ok {
			// The registry row outlived its auth file. Costs this one account.
			skipped++
			continue
		}
		if id := authfile.ParseIdentity(payload); id != nil && id.Identifiable() && id.AccountKey() != key {
			// The snapshot belongs to another account: filing it under this
			// key would log the user into the wrong account.
			skipped++
			continue
		}

		// normalize_plan's rule — anything but a non-empty string is "" —
		// holds for every schema; only the tier renames are v3-only.
		plan, _ := stringValue(row["plan"])
		if schema < 4 {
			plan = authfile.NormalizePlan(plan)
		}
		if _, err := st.UpsertSlot(key, store.Upsert{
			Email:         pyStr(row["email"], ""),
			Plan:          plan,
			WorkspaceName: pyStr(row["account_name"], ""),
			AuthMode:      pyStr(row["auth_mode"], "chatgpt"),
		}); err != nil {
			return Result{}, fmt.Errorf("import codex-auth account: %w", err)
		}
		var alias string
		if json.Unmarshal(row["alias"], &alias) == nil && alias != "" {
			if norm, valid := store.ValidAlias(alias); valid {
				if err := st.SetAlias(key, norm); err != nil {
					return Result{}, fmt.Errorf("import codex-auth alias: %w", err)
				}
			}
		}
		if err := st.WriteSnapshot(key, payload); err != nil {
			return Result{}, fmt.Errorf("import codex-auth snapshot: %w", err)
		}
		imported++
	}

	var active string
	if json.Unmarshal(data["active_account_key"], &active) == nil && active != "" && st.SlotForKey(active) != nil {
		if err := st.SetActive(active); err != nil {
			return Result{}, fmt.Errorf("import codex-auth active account: %w", err)
		}
	}

	return Result{Imported: imported, Skipped: skipped, Source: registryPath}, nil
}

// loadRegistry reads the registry as a top-level JSON object; ok is false for
// an unreadable file, invalid JSON, or any non-object document.
func loadRegistry(path string) (map[string]json.RawMessage, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(b, &data); err != nil || data == nil {
		return nil, false
	}
	return data, true
}

func rows(raw json.RawMessage) []map[string]json.RawMessage {
	var members []json.RawMessage
	trimmed := bytes.TrimSpace(raw)
	switch {
	case len(trimmed) > 0 && trimmed[0] == '[':
		if json.Unmarshal(trimmed, &members) != nil {
			return nil
		}
	case len(trimmed) > 0 && trimmed[0] == '{':
		members = objectValues(trimmed)
	default:
		return nil
	}
	out := make([]map[string]json.RawMessage, 0, len(members))
	for _, m := range members {
		var row map[string]json.RawMessage
		if json.Unmarshal(m, &row) == nil && row != nil {
			out = append(out, row)
		}
	}
	return out
}

// objectValues returns an object's member values in document order (a Python
// dict's .values()). A duplicate key keeps its first position and last value.
func objectValues(b []byte) []json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var order []string
	vals := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		k, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil
		}
		if _, seen := vals[k]; !seen {
			order = append(order, k)
		}
		vals[k] = v
	}
	out := make([]json.RawMessage, 0, len(order))
	for _, k := range order {
		out = append(out, vals[k])
	}
	return out
}

func readSnapshot(path string) (map[string]any, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil || payload == nil {
		return nil, false
	}
	if _, err := dec.Token(); err == nil {
		return nil, false // trailing data: json.loads would reject it
	}
	return payload, true
}

// truthy is Python truthiness for a JSON value.
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

// pyInt reports whether raw is what Python's isinstance(x, int) accepts after
// json.loads: an integer literal (no fraction or exponent) or a bool.
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
	if err != nil {
		return 0, false
	}
	return n, true
}

// stringValue decodes raw when it is a JSON string; ok is false otherwise
// (including null, which json.Unmarshal would silently accept).
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

// pyStr is `str(value or def)` for a JSON value: a falsy value yields def, a
// string itself, a number its literal, true "True", anything else its JSON.
func pyStr(raw json.RawMessage, def string) string {
	if len(raw) == 0 || !truthy(raw) {
		return def
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	t := strings.TrimSpace(string(raw))
	if t == "true" {
		return "True"
	}
	return t
}
