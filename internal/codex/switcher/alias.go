// alias.go — alias normalisation for Codex slots.
// Port of claude-swap models.py normalize_alias, as called by claude-swap
// PR #252 codex/switcher.py (add_account, set_alias).
//
// The Claude side's copies (internal/transfer and internal/lifecycle
// normalizeAlias) are unexported, so this is the same rule and the same
// messages, kept byte-identical so an alias accepted by one provider is
// accepted by the other.
package switcher

import (
	"regexp"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
)

var aliasRE = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// NormalizeAlias strips and lower-cases name, then rejects an empty, purely
// numeric (reserved for slot numbers), leading-"-" (would parse as a flag) or
// out-of-charset alias with a ValidationError carrying Python's message.
func NormalizeAlias(name string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	switch {
	case normalized == "":
		return "", cerr.Validation("alias cannot be empty")
	case isDigits(normalized):
		return "", cerr.Validation("alias '%s' cannot be purely numeric (reserved for slot numbers)", name)
	case strings.HasPrefix(normalized, "-"):
		return "", cerr.Validation("alias '%s' cannot start with '-' (would be read as a command flag)", name)
	case !aliasRE.MatchString(normalized):
		return "", cerr.Validation("alias '%s' may only contain letters, digits, '-', '_', and '.'", name)
	}
	return normalized, nil
}
