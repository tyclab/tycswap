// Package termsafe removes terminal control sequences from strings that come
// from outside tycswap (API responses, exports, other tools' config files)
// before they are stored as display fields or printed.
package termsafe

import (
	"strings"
	"unicode/utf8"
)

// IsControl reports whether r is a C0 control (ESC included), DEL or a C1
// control: the characters a terminal may act on rather than display.
func IsControl(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}

// Invalid UTF-8 becomes U+FFFD (a lone 0x9b is CSI to some terminals); an ESC is dropped and its sequence stays inert text.
func Strip(s string) string {
	clean := true
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && n == 1) || IsControl(r) {
			clean = false
			break
		}
		i += n
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n == 1:
			b.WriteRune(utf8.RuneError)
		case IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// HasControl reports whether s holds a character Strip would remove or
// replace.
func HasControl(s string) bool { return Strip(s) != s }
