// import.go — import_accounts (spec 07§3).
//
// Two passes: pass 1 validates every account (path-traversal defence on email +
// slot number, kind/OAuth credential shape, duplicate identity/alias, alias
// collision against a different local owner) and builds an in-memory normalized
// list with ZERO writes, so a malformed account late in the list can never
// half-import an earlier one; pass 2 writes each account, skipping / overwriting
// (--force) / freshly-allocating a slot on the composite (email, organizationUuid)
// identity, clearing any dead-token quarantine on every successful write, and
// seeding activeAccountNumber only into a destination with no prior preference.
//
// Per DESIGN Deviation 9 the whole span from the roster read through the last
// write runs under one FileLock. The local roster is read ONCE, classified
// (MigratedSequenceForUpdate), INSIDE that lock: an absent sequence.json is a
// fresh install and yields an empty roster, while one that is there but
// unreadable refuses before anything is written, since an empty roster
// substituted there would be renamed over every record whose backups this
// import does not carry. That single roster is then threaded through the alias
// check, every slot decision, and every write, so no record can land in a roster
// other than the one its slot was chosen against — and because the read is
// inside the lock, that roster is also the bytes on disk: a second tycswap cannot
// commit between the read and the writes, so its records cannot be renamed away
// by this import's own commit. Only the envelope read (which may drain stdin)
// stays outside.
package transfer

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/filelock"
)

// Stdin is the source for "-"/stdin imports (transfer.py::sys.stdin.read). Tests
// redirect it; production leaves it at os.Stdin.
var Stdin io.Reader = os.Stdin

// importLockTimeout is the acquire budget for the write-pass lock. Zero means
// filelock's default (10s), which is what production uses; tests shorten it to
// reach the contention path without waiting out the real budget.
var importLockTimeout time.Duration

// normalizedEntry is one validated account ready for pass 2. creds_text is the
// raw API-key string or the compact-JSON OAuth object; config_text is the
// two-space-indented config; alias is "" when absent or dropped on collision.
type normalizedEntry struct {
	email       string
	exportedNum string
	orgUUID     string
	orgName     string
	uuid        string
	added       string
	kind        string // "api_key" | "oauth"
	alias       string
	credsText   string
	configText  string
}

// Import reads a .tycswap envelope from source ("-" for stdin) and writes its
// accounts into the local store. force overwrites the matching local slot in
// place. Mirrors import_accounts (spec 07§3).
func Import(acc Accounts, source string, force bool) error {
	text, err := readSource(source)
	if err != nil {
		return err
	}

	envelope, accountsRaw, rawCreds, err := decodeEnvelope(text)
	if err != nil {
		return err
	}

	if v, ok := intValue(envelope["version"]); !ok || v != FormatVersion {
		return cerr.Transfer("unsupported export version: %s (expected %d)",
			pyRepr(envelope["version"]), FormatVersion)
	}
	if enc, ok := envelope["encrypted"].(bool); ok && enc {
		return cerr.Transfer("encrypted exports are not supported in this version — " +
			"decrypt before piping (e.g. gpg -d backup.gpg | tycswap --import -)")
	}
	if len(accountsRaw) == 0 {
		return cerr.Transfer("export file has no accounts to import")
	}
	// rawCreds holds each account's credentials as raw JSON from the same
	// decode, aligned index-for-index with accountsRaw. An OAuth credential is
	// re-serialized from these (Python json.dumps form: spaced, source key
	// order) rather than from the order-losing decoded map, so the stored blob
	// is byte-identical to Python and to Go's add-token path.

	var (
		data                           *SequenceData
		imported, skipped, overwritten int
	)
	writtenSlots := map[string]bool{}

	// DESIGN Deviation 9: the classified roster read, pass 1, the bootstrap and
	// the whole write pass run under ONE FileLock — a hardening over Python's
	// unlocked RMW. The read has to be inside it, not merely the writes: a roster
	// read before the lock is a roster another tycswap can commit over while this
	// import waits for the lock, and this import's own commit would then rename a
	// file built from the pre-lock roster over that record. None of the callees
	// re-acquire this lock (they are the non-locking store primitives; the usage
	// store's dead-token clear takes a different file), so non-reentrancy holds.
	// filelock.Acquire creates its own parent directory, so a fresh $HOME reaches
	// SetupDirectories from inside the lock.
	lock := filelock.New(filepath.Join(acc.BackupDir(), ".lock"), importLockTimeout)
	writeErr := lock.With(func() error {
		// Pass 1 — validate everything before any write. This classified read is
		// the operation's ONE roster read: the same in-memory roster answers the
		// alias collision check here, every slot decision in pass 2, and every
		// write, so no write can land in a roster other than the one it was
		// planned against.
		var err error
		data, err = rosterForUpdate(acc)
		if err != nil {
			return err
		}
		localAliases := localAliasOwners(data)

		var normalized []normalizedEntry
		seenKeys := map[[2]string]bool{}
		seenAliases := map[string]bool{}
		for i, raw := range accountsRaw {
			email, exportedNum, err := validateImportedAccount(raw)
			if err != nil {
				return err
			}
			m, _ := asObject(raw)
			orgUUID := strOrEmpty(m["organizationUuid"])
			credsObj := m["credentials"]
			configObj, ok := asObject(m["config"])
			if !ok {
				return cerr.Transfer("config for %s must be a JSON object", email)
			}
			// Only the account identity is imported. A config is spliced into the
			// live ~/.claude.json on a switch, so mcpServers,
			// projects.*.allowedTools, hooks or any other key an export carried
			// would otherwise configure commands on this machine.
			configObj, err = importedConfig(configObj, email)
			if err != nil {
				return err
			}

			_, credsIsString := credsObj.(string)
			isAPIKey := m["kind"] == "api_key" || credsIsString
			var credsText string
			if isAPIKey {
				s, isStr := credsObj.(string)
				if !isStr || !credstore.LooksLikeAPIKey(s) {
					return cerr.Transfer("API-key credentials for %s must be a raw sk-ant-api… string", email)
				}
				credsText = strings.TrimSpace(s)
			} else {
				obj, ok := asObject(credsObj)
				if !ok {
					return cerr.Transfer("credentials for %s must be a JSON object", email)
				}
				// Prefer the order-preserving raw bytes (Python json.dumps parity);
				// fall back to the decoded map only if the parallel parse desynced,
				// which cannot happen for the same text — this is purely defensive.
				var b []byte
				if i < len(rawCreds) && len(rawCreds[i]) > 0 {
					b, err = marshalSpacedNoHTML(rawCreds[i])
				} else {
					b, err = marshalNoHTML(obj)
				}
				if err != nil {
					return err
				}
				credsText = string(b)
			}

			key := [2]string{email, orgUUID}
			if seenKeys[key] {
				return cerr.Transfer("duplicate account in export: %s (org=%s)", email, orgOrPersonal(orgUUID))
			}
			seenKeys[key] = true

			alias := ""
			if aliasStr := strOrEmpty(m["alias"]); aliasStr != "" {
				aliasKey, err := normalizeAlias(aliasStr) // already format-validated in pass 1
				if err != nil {
					return cerr.Transfer("invalid alias for %s: %s", email, err.Error())
				}
				if seenAliases[aliasKey] {
					return cerr.Transfer("duplicate alias in export: %s", aliasKey)
				}
				seenAliases[aliasKey] = true
				if owner, ok := localAliases[aliasKey]; ok && owner != key {
					eprint("Warning: alias '" + aliasKey + "' for " + email + " already used by an " +
						"existing account, dropping the imported alias")
				} else {
					alias = aliasKey
				}
			}

			added := strOrEmpty(m["added"])
			if added == "" {
				added = acc.Timestamp()
			}
			kind := "oauth"
			if isAPIKey {
				kind = "api_key"
			}
			configBytes, err := marshalIndent2NoHTML(configObj)
			if err != nil {
				return err
			}
			normalized = append(normalized, normalizedEntry{
				email:       email,
				exportedNum: exportedNum,
				orgUUID:     orgUUID,
				orgName:     strOrEmpty(m["organizationName"]),
				uuid:        strOrEmpty(m["uuid"]),
				added:       added,
				kind:        kind,
				alias:       alias,
				credsText:   credsText,
				configText:  string(configBytes),
			})
		}

		// Pass 2 — writes. Fresh $HOME self-bootstraps here.
		if err := acc.SetupDirectories(); err != nil {
			return err
		}
		if err := acc.InitSequenceFile(); err != nil {
			return err
		}

		envelopeActiveStr := ""
		if v, ok := intValue(envelope["activeAccountNumber"]); ok {
			envelopeActiveStr = strconv.Itoa(v)
		}
		resolvedActiveSlot := ""

		for _, entry := range normalized {
			isEnvelopeActive := envelopeActiveStr != "" && entry.exportedNum == envelopeActiveStr

			existingSlot := findAccountSlot(data, entry.email, entry.orgUUID)

			var targetNum, outcome string
			if existingSlot != "" {
				if !force {
					eprint("Skipped " + entry.email + " (already exists, use --force)")
					if acc.TokenDead(existingSlot, entry.email, entry.orgUUID) {
						eprint("  └ currently quarantined — refresh token dead; " +
							"--force replaces the backup and lifts the old verdict")
					}
					skipped++
					if isEnvelopeActive {
						resolvedActiveSlot = existingSlot
					}
					continue
				}
				targetNum = existingSlot
				outcome = "overwrote"
				if pids := acc.LiveSessionPidsFor(targetNum, entry.email); len(pids) > 0 {
					eprint("Warning: " + entry.email + " (slot " + targetNum + ") has a live " +
						"session-mode instance (PID " + joinPIDs(pids) + "); its session profile keeps " +
						"the pre-import credentials until it is restarted via 'tycswap run'.")
				}
			} else {
				if !slotOccupied(data, entry.exportedNum) {
					targetNum = entry.exportedNum
				} else {
					targetNum = strconv.Itoa(nextAccountNumber(data))
				}
				outcome = "imported"
			}

			if err := acc.WriteAccountCredentials(targetNum, entry.email, entry.credsText); err != nil {
				return err
			}
			if err := acc.WriteAccountConfig(targetNum, entry.email, entry.configText); err != nil {
				return err
			}
			// Every successful write introduces fresh credential material whose old
			// auth verdict is no longer authoritative; lift any dead-token quarantine
			// on this slot number (issue #138) for both imported and overwrote.
			if err := acc.ClearDeadToken(targetNum, entry.email, entry.orgUUID); err != nil {
				return err
			}

			rec, err := buildRecord(entry.email, entry.uuid, entry.orgUUID, entry.orgName,
				entry.added, entry.kind, entry.alias)
			if err != nil {
				return err
			}
			data.Accounts[targetNum] = rec
			if tnum, err := strconv.Atoi(targetNum); err == nil && !containsInt(data.Sequence, tnum) {
				data.Sequence = append(data.Sequence, tnum)
				sort.Ints(data.Sequence)
			}
			data.LastUpdated = acc.Timestamp()
			if err := acc.WriteSequence(data); err != nil {
				return err
			}

			if isEnvelopeActive {
				resolvedActiveSlot = targetNum
			}
			writtenSlots[targetNum] = true

			if outcome == "overwrote" {
				eprint("Overwrote " + entry.email + " (slot " + targetNum + ")")
				overwritten++
			} else {
				eprint("Imported " + entry.email + " → slot " + targetNum)
				imported++
			}
		}

		// Seed activeAccountNumber only when the destination had no prior
		// preference (None or the literal 0), from the *resolved* local slot. The
		// roster in hand is the one every write above went out with, so it needs
		// no re-read to be current.
		if (data.ActiveAccountNumber == nil || *data.ActiveAccountNumber == 0) &&
			resolvedActiveSlot != "" {
			n, _ := strconv.Atoi(resolvedActiveSlot)
			data.ActiveAccountNumber = &n
			data.LastUpdated = acc.Timestamp()
			if err := acc.WriteSequence(data); err != nil {
				return err
			}
		}
		return nil
	})
	if writeErr != nil {
		return writeErr
	}

	eprint("Done: " + strconv.Itoa(imported) + " imported, " +
		strconv.Itoa(overwritten) + " overwritten, " + strconv.Itoa(skipped) + " skipped")

	// If we just rewrote the backup of the currently-live login, a plain switch
	// would back the stale live creds up over it (issue #79) — point at the
	// explicit --switch-to <slot> --force activation path instead.
	if email, org, ok := acc.CurrentAccount(); ok {
		if liveSlot := findAccountSlot(data, email, org); liveSlot != "" && writtenSlots[liveSlot] {
			eprint("Note: " + email + " is your current live login — activate the " +
				"imported credentials with: tycswap --switch-to " + liveSlot + " --force")
		}
	}
	return nil
}

// rosterForUpdate is the classified entry read for the import write pass, plus
// the two invariants this side of the Accounts seam cannot see enforced: a
// roster is always returned (never nil — a nil one here would make the writes
// build a fresh file over records this operation never saw, exactly what the
// refusal exists to prevent), and its containers are never nil, since the write
// pass assigns into Accounts and appends to Sequence.
func rosterForUpdate(acc Accounts) (*SequenceData, error) {
	data, err := acc.MigratedSequenceForUpdate()
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, cerr.Config("the local account roster could not be read — refusing to import over it")
	}
	if data.Accounts == nil {
		data.Accounts = map[string]json.RawMessage{}
	}
	if data.Sequence == nil {
		data.Sequence = []int{}
	}
	return data, nil
}

// readSource reads the import text from stdin ("-") or a file (spec 07§3.1).
//
// Any file name is read: tycswap writes its exports as .tycswap, and an old
// .cswap export from the tool it was forked from carries the same envelope.
// Reading such a file is migration of the user's data, not a compatibility
// promise for the old name (DESIGN Amendment A23).
func readSource(source string) (string, error) {
	var (
		b   []byte
		err error
	)
	if source == "-" {
		if b, err = readLimited(Stdin); err != nil {
			return "", err
		}
	} else {
		inPath := expandUser(source)
		f, oerr := os.Open(inPath)
		if oerr != nil {
			if os.IsNotExist(oerr) {
				return "", cerr.Transfer("import file not found: %s", inPath)
			}
			return "", oerr
		}
		b, err = readLimited(f)
		f.Close()
		if err != nil {
			return "", err
		}
	}
	if len(b) > MaxImportBytes {
		return "", cerr.Transfer("%s is larger than %d MiB; refusing to import it", sourceLabel(source), MaxImportBytes>>20)
	}
	return string(b), nil
}

// MaxImportBytes caps what Import reads from a file or stdin. An export of a
// few accounts is kilobytes; an unbounded read of a wrong file or an endless
// pipe would exhaust memory before the JSON parse could reject it. The Codex
// import uses the same cap.
const MaxImportBytes = 8 << 20

// readLimited reads at most MaxImportBytes+1 bytes, so an oversized document is
// detectable without reading all of it.
func readLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, MaxImportBytes+1))
}

// sourceLabel names the import source in the size error.
func sourceLabel(source string) string {
	if source == "-" {
		return "stdin"
	}
	return expandUser(source)
}

// decodeEnvelope parses the export text ONCE into raw members and derives both
// views from that one decode: the envelope and each account as decoded values
// (for validation), and each account's credentials as the raw bytes of the very
// member that was validated (for storage, preserving source key order).
//
// Keys are matched exactly. An earlier version took the raw credentials from a
// second, struct-based decode, whose key matching is case-insensitive: an entry
// carrying both "credentials" and "CREDENTIALS" (or a top-level "ACCOUNTS")
// was validated on one value and stored the other.
//
// accounts is empty when the member is absent or not an array; the caller
// reports that as "no accounts". Only the first JSON value of text is read.
func decodeEnvelope(text string) (envelope map[string]any, accounts []any, rawCreds []json.RawMessage, err error) {
	dec := json.NewDecoder(strings.NewReader(text))
	var first json.RawMessage
	if err := dec.Decode(&first); err != nil {
		return nil, nil, nil, cerr.Transfer("export file is not valid JSON: %s", err.Error())
	}
	top, ok := rawObject(first)
	if !ok {
		return nil, nil, nil, cerr.Transfer("export file must be a JSON object")
	}
	envelope = make(map[string]any, len(top))
	for k, v := range top {
		envelope[k] = decodeUseNumber(v)
	}
	var rawAccounts []json.RawMessage
	if v := bytes.TrimSpace(top["accounts"]); len(v) > 0 && v[0] == '[' {
		if json.Unmarshal(v, &rawAccounts) != nil {
			rawAccounts = nil
		}
	}
	accounts = make([]any, len(rawAccounts))
	rawCreds = make([]json.RawMessage, len(rawAccounts))
	for i, ra := range rawAccounts {
		members, ok := rawObject(ra)
		if !ok {
			accounts[i] = decodeUseNumber(ra) // validateImportedAccount refuses it
			continue
		}
		m := make(map[string]any, len(members))
		for k, v := range members {
			m[k] = decodeUseNumber(v)
		}
		accounts[i] = m
		rawCreds[i] = members["credentials"]
	}
	return envelope, accounts, rawCreds, nil
}

// rawObject decodes raw as a JSON object into its raw members; ok is false for
// any other JSON value, null included.
func rawObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(t, &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

// decodeUseNumber decodes one raw member with numbers kept as json.Number, the
// form intValue expects. The bytes already parsed as part of the document, so
// a failure cannot happen; it would yield nil.
func decodeUseNumber(raw json.RawMessage) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}

// importedConfig reduces an imported config to {"oauthAccount": …}, the shape a
// default export carries (slimConfig). A --full export imports only its
// identity too: every other key of ~/.claude.json is machine-local, and some of
// them (mcpServers, allowed tools, hooks) name commands to run.
func importedConfig(config map[string]any, email string) (map[string]any, error) {
	oauth, ok := config["oauthAccount"].(map[string]any)
	if !ok {
		return nil, cerr.Transfer("config for %s is missing oauthAccount", email)
	}
	return map[string]any{"oauthAccount": oauth}, nil
}

// validateImportedAccount validates one account's fields BEFORE any filename is
// constructed from them — the path-traversal defence (email + slot flow into
// .creds-{num}-{email}.enc). Returns (email, str(number)). Mirrors
// _validate_imported_account (spec 07§3.3).
func validateImportedAccount(raw any) (email, exportedNum string, err error) {
	m, ok := asObject(raw)
	if !ok {
		return "", "", cerr.Transfer("account entry must be a JSON object")
	}

	emailV := m["email"]
	emailStr, isStr := emailV.(string)
	if !isStr || !validateEmail(emailStr) {
		return "", "", cerr.Transfer("invalid or missing email in imported account: %s", pyRepr(emailV))
	}

	rawNumber := m["number"]
	n, ok := intValue(rawNumber)
	if !ok || n < 1 || n > maxSlotValue {
		return "", "", cerr.Transfer("invalid slot number in imported account (%s): %s",
			emailStr, pyRepr(rawNumber))
	}

	// Org/uuid/added/alias, when present and non-null, must be strings — a
	// list/dict would break the composite-key matching and pollute sequence.json.
	for _, field := range []string{"organizationUuid", "organizationName", "uuid", "added", "alias"} {
		v, present := m[field]
		if present && v != nil {
			if _, isStr := v.(string); !isStr {
				return "", "", cerr.Transfer("%s for %s must be a string, got %s",
					field, emailStr, pyTypeName(v))
			}
		}
	}

	if aliasStr, isStr := m["alias"].(string); isStr {
		// Checked on the raw value: normalizeAlias trims, which would let a
		// crafted " work\r\n" through as "work".
		if len(aliasStr) > maxAliasLen {
			return "", "", cerr.Transfer("invalid alias for %s: longer than %d bytes", emailStr, maxAliasLen)
		}
		if hasSpaceOrControl(aliasStr) {
			return "", "", cerr.Transfer("invalid alias for %s: %s contains whitespace or a control character", emailStr, pyRepr(aliasStr))
		}
		if _, e := normalizeAlias(aliasStr); e != nil {
			return "", "", cerr.Transfer("invalid alias for %s: %s", emailStr, e.Error())
		}
	}

	return emailStr, strconv.Itoa(n), nil
}

// localAliasOwners builds the case-folded {alias_lower: (email, org)} map used for
// import-time collision detection (spec 07§3.3). Only accounts carrying a truthy
// alias contribute; the key is the lowercased alias string (matching Python).
func localAliasOwners(data *SequenceData) map[string][2]string {
	out := map[string][2]string{}
	if data == nil {
		return out
	}
	for _, raw := range data.Accounts {
		rec := decodeRecord(raw)
		alias := strOrEmpty(rec["alias"])
		if alias == "" {
			continue
		}
		out[strings.ToLower(alias)] = [2]string{strOrEmpty(rec["email"]), strOrEmpty(rec["organizationUuid"])}
	}
	return out
}

// orgOrPersonal renders an org uuid or the "personal" placeholder for the
// duplicate-account error (spec 07§3.3).
func orgOrPersonal(orgUUID string) string {
	if orgUUID == "" {
		return "personal"
	}
	return orgUUID
}

// joinPIDs renders a []int as a comma+space-joined list for the live-session
// warning (Python ', '.join(map(str, pids))).
func joinPIDs(pids []int) string {
	parts := make([]string, len(pids))
	for i, p := range pids {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ", ")
}
