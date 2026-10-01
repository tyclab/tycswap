// Package storenames builds the file names of the per-account backups in the
// Claude store, and validates the email they derive from.
//
// The email of an account never appears raw in a file name. It is encoded as
// unpadded base64url (EmailKey), the scheme the Codex store already uses for
// its snapshots: injective, and free of path separators, dots-only names and
// characters Windows forbids, whatever the email holds. One scheme serves both
// the config backup and the credential files, so the two can never disagree.
// Stores written before this used the raw email; the email_file_names
// migration renames those once (internal/migrations).
package storenames

import (
	"encoding/base64"
	"regexp"
)

// MaxEmailLen is the longest email accepted (RFC 5321 path limit).
const MaxEmailLen = 254

var emailRE = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// ValidEmail reports whether email is a plain address tycswap stores: the
// claude-swap _validate_email pattern (which admits no whitespace, control
// character, slash or backslash) and at most MaxEmailLen bytes.
func ValidEmail(email string) bool {
	return len(email) <= MaxEmailLen && emailRE.MatchString(email)
}

// EmailKey encodes email for use inside a file name.
func EmailKey(email string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(email))
}

// ConfigFile is the config backup's base name under configs/.
func ConfigFile(num, email string) string {
	return ".claude-config-" + num + "-" + EmailKey(email) + ".json"
}

// CredsFile is the file-backed credential backup's base name under
// credentials/.
func CredsFile(num, email string) string {
	return ".creds-" + num + "-" + EmailKey(email) + ".enc"
}

// CredsPrevFile is the retained previous credential generation's base name.
func CredsPrevFile(num, email string) string { return CredsFile(num, email) + ".prev" }

// LegacyConfigFile, LegacyCredsFile and LegacyCredsPrevFile are the raw-email
// names of stores written before EmailKey (and of claude-swap). They are used
// only to find and rename such files; nothing is written under them.
func LegacyConfigFile(num, email string) string {
	return ".claude-config-" + num + "-" + email + ".json"
}

func LegacyCredsFile(num, email string) string { return ".creds-" + num + "-" + email + ".enc" }

func LegacyCredsPrevFile(num, email string) string { return LegacyCredsFile(num, email) + ".prev" }
