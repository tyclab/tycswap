package recovery

import (
	"crypto/sha256"
	"encoding/hex"
)

func IdentityKey(email, org string) string {
	if email == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(email + "\x00" + org))
	return hex.EncodeToString(hash[:])
}
