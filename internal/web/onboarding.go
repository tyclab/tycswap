// onboarding.go — two things the first page says before the account table
// (DESIGN A27): the login Claude Code has that is not stored yet ("Add your
// current login"), and the authentication overrides that would make Claude
// Code ignore the stored login altogether.
package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/paths"
)

// CurrentLoginView is the account Claude Code is signed in with. Saved: a
// stored account matches it (exactly "the active account is managed"), so
// the page shows the callout while it is false.
type CurrentLoginView struct {
	Email string `json:"email"`
	Saved bool   `json:"saved"`
}

// AuthOverridesView lists what makes Claude Code authenticate with something
// other than its stored login: the variables set in the server's own
// environment (which a plain `claude` inherits; `tycswap run` and `tycswap
// env` scrub the key and token ones), and the keys set in Claude Code's
// settings.json (`env.ANTHROPIC_API_KEY` and the like, or an `apiKeyHelper`).
// Names only, never a value.
type AuthOverridesView struct {
	Env          []string `json:"env"`      // never null
	Settings     []string `json:"settings"` // never null; dotted keys
	SettingsPath string   `json:"settingsPath"`
	// Profile are the settings keys that are tycswap's own endpoint profile
	// (DESIGN A46), set aside from Settings: they are the active account's
	// login while an account with a base URL is active, and an override of
	// whatever else is (buildState decides). Not sent.
	Profile []string `json:"-"`
}

// Any reports whether something overrides the login.
func (v AuthOverridesView) Any() bool { return len(v.Env) > 0 || len(v.Settings) > 0 }

// authOverrideEnv are the environment variables Claude Code takes over the
// stored login: a key or token in place of OAuth (CLAUDE_CODE_OAUTH_TOKEN,
// a setup-token, among them, as session.AuthOverrideEnvVars has it), and a
// base URL that sends the requests elsewhere (verified against Claude Code's
// documented settings).
var authOverrideEnv = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL"}

// authOverrideSettings are the settings.json keys with the same effect.
var authOverrideSettings = []string{"apiKeyHelper", "env.ANTHROPIC_API_KEY", "env.ANTHROPIC_AUTH_TOKEN", "env.CLAUDE_CODE_OAUTH_TOKEN", "env.ANTHROPIC_BASE_URL"}

// DetectAuthOverrides reads the overrides from getenv and from the Claude
// Code settings file at settingsPath. An empty value selects nothing; a
// settings file that is missing or does not parse declares nothing. It is
// the Deps.AuthOverrides default, over os.Getenv and the live config home's
// settings.json.
func DetectAuthOverrides(getenv func(string) string, settingsPath string) AuthOverridesView {
	return detectAuthOverrides(getenv, settingsPath, "")
}

// detectAuthOverrides is DetectAuthOverrides that knows tycswap's own
// endpoint profile (DESIGN A46): while the record at sidecarPath says the
// profile is in settingsPath, its env.ANTHROPIC_BASE_URL and
// env.ANTHROPIC_AUTH_TOKEN go to Profile instead of Settings.
func detectAuthOverrides(getenv func(string) string, settingsPath, sidecarPath string) AuthOverridesView {
	v := AuthOverridesView{Env: []string{}, Settings: []string{}, SettingsPath: settingsPath}
	ours := map[string]bool{}
	if sidecarPath != "" && ccsettings.RecordsFile(sidecarPath, settingsPath) {
		for _, k := range ccsettings.OwnedKeys() {
			ours[k] = true
		}
	}
	if getenv != nil {
		for _, k := range authOverrideEnv {
			if strings.TrimSpace(getenv(k)) != "" {
				v.Env = append(v.Env, k)
			}
		}
	}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return v
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return v
	}
	for _, k := range authOverrideSettings {
		if val, ok := lookup(root, k); ok && !blank(val) {
			if ours[k] {
				v.Profile = append(v.Profile, k)
				continue
			}
			v.Settings = append(v.Settings, k)
		}
	}
	sort.Strings(v.Settings)
	return v
}

// lookup follows a dotted key through nested objects.
func lookup(root map[string]any, dotted string) (any, bool) {
	cur := any(root)
	for _, part := range strings.Split(dotted, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// blank: a value that selects nothing (null, or an empty string).
func blank(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// defaultAuthOverrides is DetectAuthOverrides over this process, with the
// endpoint profile's record in the backup root backupDir ("" for none).
func defaultAuthOverrides(backupDir string) AuthOverridesView {
	sidecar := ""
	if backupDir != "" {
		sidecar = filepath.Join(backupDir, ccsettings.SidecarName)
	}
	return detectAuthOverrides(os.Getenv, paths.GetClaudeSettingsPath(), sidecar)
}
