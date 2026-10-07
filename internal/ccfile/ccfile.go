package ccfile

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
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
)

// ReadGlobalConfig reads and parses ~/.claude.json (paths.GetGlobalConfigPath),
// preserving every key. It returns (nil, nil) when the file is absent (and when
// its content is the JSON literal null, mirroring Python's isinstance(dict)
// guard), (nil, err) on a read failure or when the content is not a JSON object.
//
// Python's _read_global_config swallows those errors to None with a warning log;
// that log+swallow is the caller's job (credstore), so this primitive surfaces
// the error and lets the caller decide.
func ReadGlobalConfig() (map[string]any, error) {
	path := paths.GetGlobalConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// UpdateGlobalConfig applies mutate to the current ~/.claude.json contents and
// writes the result back atomically with 0600 perms, preserving every key that
// mutate does not touch (oauthAccount, projects, settings, ...).
//
// Only a missing or blank file (or the literal null) starts from an empty
// object. A file that exists but cannot be read, does not parse, or is not a
// JSON object is an error (ErrUnusableConfig) and nothing is written: Python's
// `_read_global_config() or {}` replaced such a file with the few keys mutate
// sets, which destroyed the user's projects, MCP servers and settings over what
// may be a transient read failure or a half-written file.
func UpdateGlobalConfig(mutate func(map[string]any)) error {
	path := paths.GetGlobalConfigPath()
	data, err := ReadGlobalConfigStrict(path)
	if err != nil {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	mutate(data)
	encoded, err := marshalIndent2(data)
	if err != nil {
		return err
	}
	return atomicWrite(path, encoded)
}

// ErrUnusableConfig marks a config file that exists but cannot be read or is
// not a JSON object. Callers must not treat it as empty.
var ErrUnusableConfig = errors.New("config file exists but is unreadable or not a JSON object")

func ReadGlobalConfigStrict(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrUnusableConfig, path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrUnusableConfig, path, err)
	}
	return m, nil
}

// ReadCredentialsFile reads the raw text of ~/.claude/.credentials.json. The
// returned exists flag is false (with a nil error) when the file is absent; the
// text is returned verbatim (callers apply any strip/non-blank check).
func ReadCredentialsFile() (raw string, exists bool, err error) {
	path := paths.GetCredentialsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

// WriteCredentialsFile atomically writes raw verbatim to
// ~/.claude/.credentials.json (0600), creating the config-home directory if
// needed. The payload is stored raw — no JSON re-encoding — matching Claude
// Code's own file, whose changed mtime is what invalidates its cached token.
func WriteCredentialsFile(raw string) error {
	return atomicWrite(paths.GetCredentialsPath(), []byte(raw))
}

// SeatWideKeys are the top-level keys of ~/.claude/.credentials.json that
// belong to the seat, not to the Claude account logged in on it. Claude Code
// keys both by MCP server: "mcpOAuth" holds the MCP server logins,
// "mcpOAuthClientConfig" the client secret an MCP server was added with
// (`claude mcp add --client-secret`), which Claude Code reads beside that
// server's login and cannot recreate. A switch carries the live values over
// the stored account blob, and a capture leaves them out. The list is an
// allow-list: every other key (claudeAiOauth, trustedDeviceToken, ...) travels
// with the account until it is known to be the seat's.
var SeatWideKeys = []string{"mcpOAuth", "mcpOAuthClientConfig"}

func SpliceCredentials(stored, live string) (string, error) {
	carried, err := seatWideOf(live)
	storedObj, ok := decodeObject(stored)
	if !ok {
		if len(carried) > 0 {
			return stored, errors.New("stored credential is not a JSON object")
		}
		return stored, err
	}
	changed := dropSeatWide(storedObj)
	for key, v := range carried {
		storedObj[key] = v
		changed = true
	}
	if !changed {
		return stored, err
	}
	encoded, merr := marshalCompact(storedObj)
	if merr != nil {
		return stored, merr
	}
	return string(encoded), err
}

// SeatWideOnly reports whether creds is a JSON object holding nothing but
// SeatWideKeys, the empty object included: no account part at all. Claude Code
// writes one when an MCP server is signed in to, or added with a client
// secret, while no claude.ai login is stored, as on an API-key seat: every
// write to its credential store is a whole-object read-modify-write, so over an
// absent file the result is {"mcpOAuth": {...}} alone. Claude Code takes its
// login from claudeAiOauth only, and to tycswap such a credential is no OAuth
// login either. Anything that is not a JSON object, a managed API key among
// them, is not seat-wide only.
func SeatWideOnly(creds string) bool {
	obj, ok := decodeObject(creds)
	if !ok {
		return false
	}
	dropSeatWide(obj)
	return len(obj) == 0
}

// SeatWidePart returns the SeatWideKeys of creds alone, as compact JSON; ok is
// false when creds is not a JSON object or holds none of them. It is what is
// left of a live credential once its login is cleared.
func SeatWidePart(creds string) (string, bool) {
	part, err := seatWideOf(creds)
	if err != nil || len(part) == 0 {
		return "", false
	}
	encoded, err := marshalCompact(part)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func seatWideOf(creds string) (map[string]any, error) {
	if strings.TrimSpace(creds) == "" {
		return nil, nil
	}
	obj, ok := decodeObject(creds)
	if !ok {
		return nil, errors.New("live credentials are not a JSON object")
	}
	part := map[string]any{}
	for _, key := range SeatWideKeys {
		if v, present := obj[key]; present {
			part[key] = v
		}
	}
	return part, nil
}

func dropSeatWide(obj map[string]any) bool {
	dropped := false
	for _, key := range SeatWideKeys {
		if _, present := obj[key]; present {
			delete(obj, key)
			dropped = true
		}
	}
	return dropped
}

func decodeObject(text string) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// marshalCompact renders v as compact JSON without HTML escaping, the form
// Claude Code itself writes its credentials file in (JSON.stringify).
func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// SpliceOAuthAccount parses configText, replaces its "oauthAccount" with oauth,
// and returns the re-serialized config text (two-space indent, no trailing
// newline). Every other key of configText is preserved. Empty or non-object
// configText starts from an empty object.
//
// This is the pure form of the switch-time config splice: the caller reads the
// live ~/.claude.json, splices in the target account's stored oauthAccount, and
// writes the result back under the config lock.
func SpliceOAuthAccount(configText string, oauth map[string]any) (string, error) {
	data := map[string]any{}
	if strings.TrimSpace(configText) != "" {
		if err := json.Unmarshal([]byte(configText), &data); err != nil {
			return "", err
		}
		if data == nil {
			data = map[string]any{}
		}
	}
	data["oauthAccount"] = oauth
	encoded, err := marshalIndent2(data)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func ReadOAuthIdentity() (email, orgUUID string, ok bool) {
	return ReadOAuthIdentityFrom(paths.GetGlobalConfigPath())
}

func ReadOAuthIdentityFrom(configPath string) (email, orgUUID string, ok bool) {
	m := readLenient(configPath)
	if m == nil {
		return "", "", false
	}
	oauth, _ := m["oauthAccount"].(map[string]any)
	email, _ = oauth["emailAddress"].(string)
	if email == "" {
		return "", "", false
	}
	orgUUID, _ = oauth["organizationUuid"].(string)
	return email, orgUUID, true
}

func readLenient(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

func marshalIndent2(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return b, nil
}

// atomicWrite writes data to path via a temp sibling + rename, then chmods the
// final file to 0600 (skipped on Windows). It mkdirs a missing parent 0700
// but never chmods an existing one — mirroring Python _update_global_config /
// _write_active_credentials_file, which must not alter $HOME's mode.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "*.tmp")
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	atomicfile.SyncDir(filepath.Dir(path))
	committed = true
	if !platform.IsWindows() {
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	}
	return nil
}
