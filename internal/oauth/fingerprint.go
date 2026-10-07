package oauth

import (
	"crypto/sha256"
	"encoding/hex"
)

// CredentialFingerprint returns a stable identity fingerprint for a stored
// credential, or nil only for empty input (the sole None case — real bytes must
// never fingerprint to nil).
func CredentialFingerprint(creds string) *string {
	if creds == "" {
		return nil
	}
	if oauth := ExtractOAuthData(creds); oauth != nil {
		if tok, ok := oauth["refreshToken"].(string); ok && tok != "" {
			sum := sha256.Sum256([]byte(tok))
			s := "sha256:" + hex.EncodeToString(sum[:])
			return &s
		}
	}
	sum := sha256.Sum256([]byte(creds))
	s := "sha256-full:" + hex.EncodeToString(sum[:])
	return &s
}
