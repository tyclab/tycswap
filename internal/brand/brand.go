// Package brand holds the names and the accent colour tycswap shows to people
// and writes to their machine outside its data store: the program name in the
// dashboard, the dashboard's session cookie, the prefix of the dashboard's
// redirect file and the dashboard accent colour.
//
// Every value is a package var, not a const, so a packager can rebrand a
// build at link time without patching the source:
//
//	go build -ldflags "-X github.com/tyclab/tycswap/internal/brand.DisplayName=Example" ./cmd/tycswap
//
// The defaults are tycswap's own. Values must stay within the shapes Valid
// checks; the dashboard falls back to the defaults for any value that does not.
package brand

import "regexp"

var (
	// Name is the command name, lower case ("tycswap").
	Name = "tycswap"
	// DisplayName is the product name shown in page titles and headers.
	DisplayName = "tycswap"
	// SessionCookie names the dashboard's session cookie.
	SessionCookie = "tycswap_session"
	// RedirectFilePrefix prefixes the 0600 redirect page `tycswap web` writes
	// under the user's runtime or cache directory so the launch token never
	// appears on a command line.
	RedirectFilePrefix = "tycswap-dashboard-"
	// AccentColor is the dashboard's accent: links, focus rings, the active
	// tab, primary buttons and the switch target highlight. A #rrggbb colour.
	AccentColor = "#5aa2ff"
)

// Defaults, kept for the fallback in Sanitized.
const (
	defaultName               = "tycswap"
	defaultDisplayName        = "tycswap"
	defaultSessionCookie      = "tycswap_session"
	defaultRedirectFilePrefix = "tycswap-dashboard-"
	defaultAccentColor        = "#5aa2ff"
)

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	displRe  = regexp.MustCompile(`^[\pL\pN][\pL\pN .\-]{0,63}$`)
	cookieRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	fileRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	colorRe  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// Values is one consistent, validated set of the brand vars.
type Values struct {
	Name               string
	DisplayName        string
	SessionCookie      string
	RedirectFilePrefix string
	AccentColor        string
}

// Sanitized returns the current brand vars, each replaced by its default when
// a link-time override does not fit its shape: a cookie name with a separator,
// a colour that is not #rrggbb, or a display name with markup in it would
// otherwise reach a Set-Cookie header, a stylesheet or a page title.
func Sanitized() Values {
	pick := func(v string, re *regexp.Regexp, def string) string {
		if re.MatchString(v) {
			return v
		}
		return def
	}
	return Values{
		Name:               pick(Name, nameRe, defaultName),
		DisplayName:        pick(DisplayName, displRe, defaultDisplayName),
		SessionCookie:      pick(SessionCookie, cookieRe, defaultSessionCookie),
		RedirectFilePrefix: pick(RedirectFilePrefix, fileRe, defaultRedirectFilePrefix),
		AccentColor:        pick(AccentColor, colorRe, defaultAccentColor),
	}
}
