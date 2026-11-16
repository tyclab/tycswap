package launch

import "strings"

// AuthOverrideEnvVars make claude bypass account OAuth (verified against claude 2.1.175); commands that start claude drop them.
var AuthOverrideEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
	"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
	// Not an auth override, but Claude Code keeps its credentials under this
	// dir instead of the profile's, and in the default ~/.claude storage when
	// it is set but empty: the live login's (DESIGN A56).
	"CLAUDE_SECURESTORAGE_CONFIG_DIR",
}

// ScrubAuthOverrides returns a fresh environ without the AuthOverrideEnvVars, set or empty.
func ScrubAuthOverrides(environ []string) []string {
	drop := make(map[string]bool, len(AuthOverrideEnvVars))
	for _, key := range AuthOverrideEnvVars {
		drop[key] = true
	}
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if !drop[key] {
			out = append(out, entry)
		}
	}
	return out
}
