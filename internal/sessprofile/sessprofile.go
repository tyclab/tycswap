// Package sessprofile is the session-profile leaf (identity, location, stale marker); it breaks the store↔session import cycle.
package sessprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/procdetect"
)

// StaleMarkerName is the deferred-invalidation marker file: backup
// credentials changed while a session was live, so the profile must be
// re-bootstrapped on the next non-live `tycswap run` even if it still passes
// the local reuse check.
const StaleMarkerName = ".tycswap-stale-credentials"

// CredentialsFileName is the plaintext credential seed every session profile
// carries (POSIX and Windows both use it as the Claude Code fallback; macOS
// additionally shadows it with a hashed Keychain entry once Claude writes one).
const CredentialsFileName = ".credentials.json"

// Only filesystem-safe (Windows-forbidden characters included), not injective: the "<num>-" prefix makes session dirs unique.
func SlugifyEmail(email string) string {
	normalized := norm.NFC.String(email)
	out := make([]rune, 0, len(normalized))
	for _, ch := range normalized {
		if isSlugSafe(ch) {
			out = append(out, ch)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

func isSlugSafe(ch rune) bool {
	if ch > unicode.MaxASCII {
		return false
	}
	if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
		return true
	}
	switch ch {
	case '.', '_', '-':
		return true
	}
	return false
}

// Claude Code writes <profile>/sessions/<pid>.json, so the double "sessions/" nesting is intentional.
func SessionDirFor(backupDir, accountNum, email string) string {
	return filepath.Join(backupDir, "sessions", accountNum+"-"+SlugifyEmail(email))
}

// IsSessionProfileDir reports whether configDir is strictly inside <backupRoot>/sessions/ (both symlink-resolved as far as they
// exist), so commands other than env/run fall back to the default login in a pinned shell (D2 / FINDING 2).
func IsSessionProfileDir(backupRoot, configDir string) bool {
	if backupRoot == "" || configDir == "" {
		return false
	}
	for _, group := range []string{"fable", "opus"} {
		if resolveProfilePath(configDir) == resolveProfilePath(filepath.Join(backupRoot, "groups", group, "profile")) {
			return true
		}
	}
	sessionsRoot := resolveProfilePath(filepath.Join(backupRoot, "sessions"))
	target := resolveProfilePath(configDir)
	rel, err := filepath.Rel(sessionsRoot, target)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// resolveProfilePath returns p's canonical form for containment comparison:
// EvalSymlinks when it resolves (a ".." after a symlink then leaves the
// symlink's target), else its longest existing prefix symlink-resolved, with
// the rest of the path joined on lexically. A path that does not exist (yet,
// or any more) then compares like an existing one even when an ancestor is a
// symlink (macOS /var is /private/var) or, on Windows, a short (8.3) name.
func resolveProfilePath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	rest := ""
	for dir := abs; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		if filepath.Dir(dir) == dir {
			return abs
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// KeychainServiceName returns the Keychain service name Claude Code derives
// for this CLAUDE_CONFIG_DIR value: Claude hashes the raw, NFC-normalized,
// UNRESOLVED env var string (never a resolved/realpath variant) with SHA-256
// and takes the first 8 hex characters.
func KeychainServiceName(sessionDir string) string {
	normalized := norm.NFC.String(sessionDir)
	sum := sha256.Sum256([]byte(normalized))
	digest := hex.EncodeToString(sum[:])[:8]
	return "Claude Code-credentials-" + digest
}

// StaleMarkerPath returns <sessionDir>/.tycswap-stale-credentials.
func StaleMarkerPath(sessionDir string) string {
	return filepath.Join(sessionDir, StaleMarkerName)
}

func MarkStale(sessionDir string) {
	f, err := os.OpenFile(StaleMarkerPath(sessionDir), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_ = f.Close()
}

// IsStale reports whether the profile carries the stale-credentials marker.
func IsStale(sessionDir string) bool {
	_, err := os.Stat(StaleMarkerPath(sessionDir))
	return err == nil
}

func ClearStaleMarker(sessionDir string) error {
	err := os.Remove(StaleMarkerPath(sessionDir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func LiveSessionPIDs(sessionDir string) []int {
	sessions := procdetect.ListSessions(sessionDir)
	pids := make([]int, 0, len(sessions))
	for _, s := range sessions {
		pids = append(pids, s.PID)
	}
	return pids
}

// Needed before seeding (Claude reads the Keychain before the file) and on removal (the hashed name dies with the dir).
func DeleteMacOSKeychainEntry(kc keychain.KeychainClient, sessionDir string) {
	if platform.Detect() != platform.MacOS {
		return
	}
	_ = kc.Delete(KeychainServiceName(sessionDir), keychain.AccountName())
}

// InvalidateSessionCredentials drops a profile's credentials but keeps its history, so the next run re-bootstraps from backup
// (e.g. after --import --force). A missing profile is a no-op (existed=false, nil error).
func InvalidateSessionCredentials(kc keychain.KeychainClient, sessionDir string) (existed bool, err error) {
	if _, statErr := os.Stat(sessionDir); statErr != nil {
		return false, nil
	}
	DeleteMacOSKeychainEntry(kc, sessionDir)
	if err := removeIfExists(filepath.Join(sessionDir, CredentialsFileName)); err != nil {
		return true, err
	}
	if err := ClearStaleMarker(sessionDir); err != nil {
		return true, err
	}
	return true, nil
}

// Keychain first: its hashed service name cannot be recomputed once the directory is gone.
func DeleteSessionProfile(kc keychain.KeychainClient, sessionDir string) {
	if _, err := os.Stat(sessionDir); err != nil {
		return
	}
	DeleteMacOSKeychainEntry(kc, sessionDir)
	_ = os.RemoveAll(sessionDir)
}

func ReadSessionIdentity(sessionDir string) (email, orgUUID string, ok bool) {
	data, err := os.ReadFile(filepath.Join(sessionDir, ".claude.json"))
	if err != nil {
		return "", "", false
	}
	var cfg any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", "", false
	}
	m, isMap := cfg.(map[string]any)
	if !isMap {
		return "", "", false
	}
	oa, isOAMap := m["oauthAccount"].(map[string]any)
	if !isOAMap {
		return "", "", false
	}
	e, _ := oa["emailAddress"].(string)
	if e == "" {
		return "", "", false
	}
	org, _ := oa["organizationUuid"].(string)
	return e, org, true
}

// SessionIdentityDrifted reports whether the profile is logged in as a
// different account than its slot (an in-session /login can re-point a
// profile without moving its directory). Mirrors _is_session_valid's
// comparison: email must match exactly; org only compared when both sides
// are non-empty. An unreadable identity is NOT drift — it degrades to
// trusting the profile rather than abandoning it over a broken .claude.json.
func SessionIdentityDrifted(sessionDir, email, orgUUID string) bool {
	profileEmail, profileOrg, ok := ReadSessionIdentity(sessionDir)
	if !ok {
		return false
	}
	if profileEmail != email {
		return true
	}
	return profileOrg != "" && orgUUID != "" && profileOrg != orgUUID
}

// ReadSessionCredentials reads the profile's current credential: on macOS the hashed Keychain entry first (Claude migrates the seed
// there on first write), else .credentials.json; ok=false when nothing readable, including a non-UTF-8 file.
func ReadSessionCredentials(kc keychain.KeychainClient, sessionDir string) (creds string, ok bool) {
	if _, err := os.Stat(sessionDir); err != nil {
		return "", false
	}
	if platform.Detect() == platform.MacOS {
		if value, found, err := kc.Get(KeychainServiceName(sessionDir), keychain.AccountName()); err == nil && found && value != "" {
			return value, true
		}
	}
	data, err := os.ReadFile(filepath.Join(sessionDir, CredentialsFileName))
	if err != nil {
		return "", false
	}
	if !utf8.Valid(data) {
		return "", false
	}
	return string(data), true
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
