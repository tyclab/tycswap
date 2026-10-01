// Package storenames builds the file names of the per-account backups in the
// Claude store, and validates the email they derive from.
//
// The names carry the email as it is: configs/.claude-config-<n>-<email>.json,
// credentials/.creds-<n>-<email>.enc and its .prev generation, the layout
// tools that integrate with the store read (DESIGN A23). What keeps such a
// name inside its directory is ValidEmail: every email reaches a name builder
// only after it has passed that check at the store's entry points (add,
// add --login, import, codex import, purge), and the pattern admits no path
// separator, whitespace or control character, so a validated email is a
// single path component. One scheme serves the config backup and the
// credential files, so store, credstore and purge can never disagree.
package storenames

import "regexp"

// MaxEmailLen is the longest email accepted (RFC 5321 path limit).
const MaxEmailLen = 254

var emailRE = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// ValidEmail reports whether email is a plain address tycswap stores: the
// claude-swap _validate_email pattern (which admits no whitespace, control
// character, slash or backslash) and at most MaxEmailLen bytes.
func ValidEmail(email string) bool {
	return len(email) <= MaxEmailLen && emailRE.MatchString(email)
}

// ConfigFile is the config backup's base name under configs/.
func ConfigFile(num, email string) string {
	return ".claude-config-" + num + "-" + email + ".json"
}

// CredsFile is the file-backed credential backup's base name under
// credentials/.
func CredsFile(num, email string) string {
	return ".creds-" + num + "-" + email + ".enc"
}

// CredsPrevFile is the retained previous credential generation's base name.
func CredsPrevFile(num, email string) string { return CredsFile(num, email) + ".prev" }
