// Package brand holds the names and the accent colour tycswap shows to people
// and writes to their machine outside its data store: the program name in the
// dashboard and the tray, the dashboard's session cookie, the prefix of the
// dashboard's redirect file, the dashboard and tray accent colour, the
// reverse-DNS identifier of the start-at-login entry and the prefix of the
// environment variables the tray application reads.
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
	// Identifier is the reverse-DNS name of the start-at-login entry the
	// tray application registers: the LaunchAgent label on macOS, the XDG
	// autostart file name on Linux and the HKCU Run value name on Windows.
	Identifier = "io.github.tyclab.tycswap"
	// EnvPrefix prefixes the environment variables the tray application
	// reads and sets (<EnvPrefix>_REMOTE_TOKEN_FILE, <EnvPrefix>_JUST_INSTALLED).
	EnvPrefix = "TYCSWAP"
)

// Defaults, kept for the fallback in Sanitized.
const (
	defaultName               = "tycswap"
	defaultDisplayName        = "tycswap"
	defaultSessionCookie      = "tycswap_session"
	defaultRedirectFilePrefix = "tycswap-dashboard-"
	defaultAccentColor        = "#5aa2ff"
	defaultIdentifier         = "io.github.tyclab.tycswap"
	defaultEnvPrefix          = "TYCSWAP"
)

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	displRe  = regexp.MustCompile(`^[\pL\pN][\pL\pN .\-]{0,63}$`)
	cookieRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	fileRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	colorRe  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	identRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*(\.[A-Za-z0-9-]+){1,7}$`)
	envRe    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,31}$`)
)

// Values is one consistent, validated set of the brand vars.
type Values struct {
	Name               string
	DisplayName        string
	SessionCookie      string
	RedirectFilePrefix string
	AccentColor        string
	Identifier         string
	EnvPrefix          string
}

// Sanitized returns the current brand vars, each replaced by its default when
// a link-time override does not fit its shape: a cookie name with a separator,
// a colour that is not #rrggbb, a display name with markup in it, an
// identifier with a path separator or an environment prefix with a space
// would otherwise reach a Set-Cookie header, a stylesheet, a page title, a
// file name under the user's autostart directory or an environment lookup.
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
		Identifier:         pick(Identifier, identRe, defaultIdentifier),
		EnvPrefix:          pick(EnvPrefix, envRe, defaultEnvPrefix),
	}
}
