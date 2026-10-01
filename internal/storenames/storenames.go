// Package storenames builds the file names of the per-account backups in the
// Claude store and holds the two email checks the store applies.
//
// ValidEmail is the path-safety rule. Every email that reaches a name builder
// has passed it at the store's entry points (add, add --login, add-token,
// purge, codex import), and it admits nothing a file name cannot carry, so a
// validated email is a single path component in
// configs/.claude-config-<n>-<email>.json and
// credentials/.creds-<n>-<email>.enc[.prev], the layout tools that integrate
// with the store read (DESIGN A23). One scheme serves the config backup and
// the credential files, so store, credstore and purge can never disagree.
//
// StrictEmail is the import contract: claude-swap's _validate_email pattern,
// which the accounts of an export must match (internal/transfer).
package storenames

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxEmailLen is the longest email accepted (RFC 5321 path limit).
const MaxEmailLen = 254

// EmailRule says, for an error message, what ValidEmail accepts.
const EmailRule = "one @ between a non-empty local part and domain, at most 254 bytes, " +
	"and no whitespace, control character, slash, backslash or < > : \" | ? *"

// forbiddenInName are the characters a file name cannot carry on some
// platform: the two path separators and the set Windows refuses.
const forbiddenInName = `/\<>:"|?*`

// ValidEmail reports whether email can name a store file: at most MaxEmailLen
// bytes of valid UTF-8, exactly one '@' with a non-empty local part and
// domain, and no whitespace, control character (NUL included) or character
// of forbiddenInName. It accepts every real address (an apostrophe, a
// non-ASCII letter, an IDN domain) that StrictEmail refuses.
func ValidEmail(email string) bool {
	if email == "" || len(email) > MaxEmailLen || !utf8.ValidString(email) {
		return false
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 || strings.IndexByte(email[at+1:], '@') >= 0 {
		return false
	}
	for _, r := range email {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(forbiddenInName, r) {
			return false
		}
	}
	return true
}

var strictEmailRE = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// StrictEmail reports whether email matches claude-swap's _validate_email
// pattern (spec 01§6.1, 07§1.2), anchored at both ends, within MaxEmailLen
// bytes: the contract import holds an export's accounts to. The pattern is
// ASCII-only and admits nothing ValidEmail refuses.
func StrictEmail(email string) bool {
	return len(email) <= MaxEmailLen && strictEmailRE.MatchString(email)
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

// KeychainAccount is the account name of a per-account backup in the macOS
// Keychain (service keychain.BackupService).
func KeychainAccount(num, email string) string { return "account-" + num + "-" + email }

// KeychainAccountPrev is the account name of the retained previous credential
// generation in the macOS Keychain.
func KeychainAccountPrev(num, email string) string { return KeychainAccount(num, email) + ".prev" }
