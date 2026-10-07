package storenames

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxEmailLen = 254

const EmailRule = "one @ between a non-empty local part and domain, at most 254 bytes, " +
	"and no whitespace, control character, slash, backslash or < > : \" | ? *"

const forbiddenInName = `/\<>:"|?*`

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

func StrictEmail(email string) bool {
	return len(email) <= MaxEmailLen && strictEmailRE.MatchString(email)
}

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

func KeychainAccount(num, email string) string { return "account-" + num + "-" + email }

// KeychainAccountPrev is the account name of the retained previous credential
// generation in the macOS Keychain.
func KeychainAccountPrev(num, email string) string { return KeychainAccount(num, email) + ".prev" }
