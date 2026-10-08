// Package brand holds the names and accent colour tycswap shows; vars, not consts, so a packager can rebrand with -ldflags -X.
package brand

import "regexp"

var (
	Name               = "tycswap"
	DisplayName        = "tycswap"
	SessionCookie      = "tycswap_session"
	RedirectFilePrefix = "tycswap-dashboard-"
	AccentColor        = "#5aa2ff"
	// LaunchAgent label on macOS, XDG autostart file name on Linux, HKCU Run value name on Windows.
	Identifier = "io.github.tyclab.tycswap"
	EnvPrefix  = "TYCSWAP"
)

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

type Values struct {
	Name               string
	DisplayName        string
	SessionCookie      string
	RedirectFilePrefix string
	AccentColor        string
	Identifier         string
	EnvPrefix          string
}

// Sanitized falls back per value so a bad link-time override never reaches a cookie header, stylesheet, title, file name or env lookup.
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
