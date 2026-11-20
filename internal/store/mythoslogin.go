package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/oauth"
)

func MythosLoginRoot(root string) string {
	return filepath.Join(root, "mythos", "accounts")
}

// The identity, including the organization, is stable across slot moves.
// A running Claude keeps this directory for its entire session.
func MythosLoginDir(root, email, org string) string {
	return filepath.Join(MythosLoginRoot(root), AccountIdentityDigest(email, org))
}

func AccountIdentityDigest(email, org string) string {
	hash := sha256.Sum256([]byte(email + "\x00" + org))
	return hex.EncodeToString(hash[:])
}

func (s *Store) MythosLoginReady(data *SequenceData, num string) (string, bool) {
	rec, ok := recordFor(data, num)
	if !ok || DisabledFrom(data, num) || kindFrom(data, num) != "oauth" || !MythosFrom(data, num) {
		return "", false
	}
	email, org := strField(rec, "email"), strField(rec, "organizationUuid")
	dir := MythosLoginDir(s.BackupDir(), email, org)
	raw, err := os.ReadFile(filepath.Join(dir, ccfile.CredentialsFileName))
	if err != nil || oauth.ExtractAccessToken(string(raw)) == "" {
		return "", false
	}
	return dir, true
}
