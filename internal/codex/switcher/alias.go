package switcher

import (
	"regexp"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
)

var aliasRE = regexp.MustCompile(`^[a-z0-9_.-]+$`)

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
