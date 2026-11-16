package ccfile

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"golang.org/x/text/unicode/norm"
)

// CredentialsFileName is a Claude Code config dir's plaintext credential file (macOS shadows it in the Keychain).
const CredentialsFileName = ".credentials.json"

// KeychainServiceName is Claude Code's Keychain service for a CLAUDE_CONFIG_DIR value: SHA-256 of the raw, NFC-normalized, unresolved string, first 8 hex characters.
func KeychainServiceName(configDir string) string {
	sum := sha256.Sum256([]byte(norm.NFC.String(configDir)))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

// ReadProfileIdentity reads the oauthAccount of a CLAUDE_CONFIG_DIR profile's own .claude.json; ok=false without an email.
func ReadProfileIdentity(configDir string) (email, orgUUID string, ok bool) {
	return ReadOAuthIdentityFrom(filepath.Join(configDir, ".claude.json"))
}
