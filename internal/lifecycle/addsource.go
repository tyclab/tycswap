// addsource.go — where an add reads the login it stores: the live login, or a
// Claude config directory a fresh `claude auth login` was run in (`cswap add
// --login`), and the check that decides whether such a login completed.
package lifecycle

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"git.dpemmons.com/dpemmons/cswap/internal/ccfile"
	"git.dpemmons.com/dpemmons/cswap/internal/cerr"
	"git.dpemmons.com/dpemmons/cswap/internal/credstore"
	"git.dpemmons.com/dpemmons/cswap/internal/keychain"
	"git.dpemmons.com/dpemmons/cswap/internal/sessprofile"
	"git.dpemmons.com/dpemmons/cswap/internal/store"
)

// AddSource names the login an add stores. The zero value (LiveLogin) is the
// live login: ~/.claude.json and the active credential store. A source built
// by LoginDir reads <dir>/.claude.json and <dir>/.credentials.json instead,
// and never touches the live login.
type AddSource struct {
	configDir string
	kc        keychain.KeychainClient
}

// LiveLogin is the live Claude Code login — what plain `cswap add` stores.
var LiveLogin = AddSource{}

// LoginDir is the login a `claude auth login` run with CLAUDE_CONFIG_DIR=dir
// left behind. kc is the Keychain Claude Code may have written the credential
// to instead of <dir>/.credentials.json — it does on macOS, under the item
// LoginKeychainService(dir) names — or nil where it never does.
func LoginDir(dir string, kc keychain.KeychainClient) AddSource {
	return AddSource{configDir: dir, kc: kc}
}

// LoginKeychainService is the Keychain service Claude Code keys a login's
// credential under when CLAUDE_CONFIG_DIR=dir (the account is
// keychain.AccountName()).
func LoginKeychainService(dir string) string { return sessprofile.KeychainServiceName(dir) }

// loginCredentials is the credential the login left: <dir>/.credentials.json,
// or — when that file is absent and the source has a Keychain — the item
// Claude Code created for dir. found is false when neither holds one.
func (src AddSource) loginCredentials() (raw string, found bool, err error) {
	data, err := os.ReadFile(src.credentialsPath())
	if err == nil {
		return string(data), true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	if src.kc == nil {
		return "", false, nil
	}
	value, ok, kerr := src.kc.Get(LoginKeychainService(src.configDir), keychain.AccountName())
	if kerr != nil {
		return "", false, kerr
	}
	if !ok || strings.TrimSpace(value) == "" {
		return "", false, nil
	}
	return value, true, nil
}

func (src AddSource) isLoginDir() bool { return src.configDir != "" }

func (src AddSource) configPath() string {
	return filepath.Join(src.configDir, ".claude.json")
}

func (src AddSource) credentialsPath() string {
	return filepath.Join(src.configDir, ".credentials.json")
}

// identity is the (email, organizationUuid) the source is logged in as.
func (src AddSource) identity(s *store.Store) (email, orgUUID string, ok bool) {
	if !src.isLoginDir() {
		return s.GetCurrentAccount()
	}
	return ccfile.ReadOAuthIdentityFrom(src.configPath())
}

// material reads the credential and the config text an add stores, refusing an
// API-key credential (a different auth axis, added with --add-token).
func (src AddSource) material(s *store.Store) (creds, configText string, err error) {
	if !src.isLoginDir() {
		creds, err = readActiveCredential(s)
		if err != nil {
			return "", "", err
		}
		if err := rejectLiveAPIKeyCapture(creds); err != nil {
			return "", "", err
		}
		configText, err = readLiveConfigText()
		return creds, configText, err
	}
	if err := CheckLogin(src); err != nil {
		return "", "", err
	}
	rawCreds, found, err := src.loginCredentials()
	if err != nil || !found {
		return "", "", errLoginIncomplete()
	}
	rawConfig, err := os.ReadFile(src.configPath())
	if err != nil {
		return "", "", errLoginIncomplete()
	}
	return rawCreds, string(rawConfig), nil
}

// errLoginIncomplete is the one answer for a login that left nothing usable
// behind: a non-zero exit, no oauthAccount, or no credential file.
func errLoginIncomplete() error {
	return cerr.Config("claude's login did not complete; nothing stored, the live login untouched")
}

// errLoginAPIKey refuses a console login: it yields an API key, which cswap
// manages on the --add-token axis, not as an OAuth slot.
func errLoginAPIKey() error {
	return cerr.Validation("claude's login made an API key, which is a different auth axis: use cswap --add-token")
}

// ErrLoginIncomplete is errLoginIncomplete for callers that observe the failure
// before reading the directory (the login's own non-zero exit).
func ErrLoginIncomplete() error { return errLoginIncomplete() }

// CheckLogin decides whether the `claude auth login` a LoginDir source names
// produced a subscription (OAuth) login cswap can store.
//
//   - a credential (file or Keychain item) without a claudeAiOauth block, or a
//     bare API key; or no credential but a primaryApiKey in the config: an API
//     key.
//   - no credential at all, or no oauthAccount with an email: incomplete.
func CheckLogin(src AddSource) error {
	raw, found, err := src.loginCredentials()
	switch {
	case err != nil:
		return errLoginIncomplete()
	case found:
		if !hasClaudeAIOAuth(raw) {
			return errLoginAPIKey()
		}
	default:
		if configHasAPIKey(src.configPath()) {
			return errLoginAPIKey()
		}
		return errLoginIncomplete()
	}
	if _, _, ok := ccfile.ReadOAuthIdentityFrom(src.configPath()); !ok {
		return errLoginIncomplete()
	}
	return nil
}

// hasClaudeAIOAuth reports whether a credential file carries a claudeAiOauth
// block with an access token — the shape a subscription login writes.
func hasClaudeAIOAuth(raw string) bool {
	if credstore.LooksLikeAPIKey(raw) {
		return false
	}
	var m map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &m) != nil {
		return false
	}
	block, _ := m["claudeAiOauth"].(map[string]any)
	token, _ := block["accessToken"].(string)
	return token != ""
}

// configHasAPIKey reports whether a config file records a primaryApiKey (where
// a console login keeps the key it created when no credential file is used).
func configHasAPIKey(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	key, _ := m["primaryApiKey"].(string)
	return key != ""
}
