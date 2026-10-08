// Package paths resolves Claude Code config/credential paths, tycswap's store root and the old roots `tycswap migrate` copies from.
// It mirrors Claude Code's own resolution (the .claude.json home-root asymmetry, legacy .config.json precedence): external contracts.
package paths

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/platform"
)

// Store directory names. StoreDirname sits under $XDG_DATA_HOME (Linux/WSL);
// DotStoreDirname sits under $HOME (macOS, Windows, unknown).
const (
	StoreDirname    = "tycswap"
	DotStoreDirname = ".tycswap"
)

const (
	// Only `tycswap migrate` and its hint read these; nothing writes there, since another installed tool may own that store.
	OldStoreDirname    = "claude-swap"
	OldDotStoreDirname = ".claude-swap-backup"
)

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// GetClaudeConfigHome returns CLAUDE_CONFIG_DIR if set, else ~/.claude.
func GetClaudeConfigHome() string {
	if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		return env
	}
	return filepath.Join(home(), ".claude")
}

// GetSecureStorageHome returns the directory Claude Code resolves its secure
// storage under (its storage-write and credential-refresh locks among them):
// CLAUDE_SECURESTORAGE_CONFIG_DIR when set, ~/.claude when set but empty, else
// the config home.
func GetSecureStorageHome() string {
	if env, ok := os.LookupEnv("CLAUDE_SECURESTORAGE_CONFIG_DIR"); ok {
		if env == "" {
			return filepath.Join(home(), ".claude")
		}
		return env
	}
	return GetClaudeConfigHome()
}

// GetGlobalConfigPath returns the legacy <config_home>/.config.json if it exists,
// else (CLAUDE_CONFIG_DIR || $HOME)/.claude.json. Note the asymmetry: by default
// .claude.json sits at the home dir, not inside .claude/.
func GetGlobalConfigPath() string {
	legacy := filepath.Join(GetClaudeConfigHome(), ".config.json")
	if fileExists(legacy) {
		return legacy
	}
	if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		return filepath.Join(env, ".claude.json")
	}
	return filepath.Join(home(), ".claude.json")
}

// GetDefaultGlobalConfigPath returns the default profile's global config path,
// deliberately ignoring CLAUDE_CONFIG_DIR so session sharing mirrors the user's
// real profile rather than the current session's.
func GetDefaultGlobalConfigPath() string {
	legacy := filepath.Join(home(), ".claude", ".config.json")
	if fileExists(legacy) {
		return legacy
	}
	return filepath.Join(home(), ".claude.json")
}

// GetClaudeSettingsPath returns <config_home>/settings.json, Claude Code's own
// user settings (not tycswap's settings.json in the backup root).
func GetClaudeSettingsPath() string {
	return filepath.Join(GetClaudeConfigHome(), "settings.json")
}

// GetCredentialsPath returns <config_home>/.credentials.json.
func GetCredentialsPath() string {
	return filepath.Join(GetClaudeConfigHome(), ".credentials.json")
}

// Linux/WSL: $XDG_DATA_HOME/tycswap (default ~/.local/share/tycswap), else ~/.tycswap; a non-absolute XDG value is ignored, ~ expanded.
func GetBackupRoot() string {
	switch platform.Detect() {
	case platform.Linux, platform.WSL:
		if xdg, ok := xdgDataHome(); ok {
			return filepath.Join(xdg, StoreDirname)
		}
		return filepath.Join(home(), ".local", "share", StoreDirname)
	default:
		return filepath.Join(home(), DotStoreDirname)
	}
}

// `tycswap migrate` copies the first that exists; nothing else reads or writes them.
func OldBackupRoots() []string {
	legacy := filepath.Join(home(), OldDotStoreDirname)
	switch platform.Detect() {
	case platform.Linux, platform.WSL:
		var roots []string
		if xdg, ok := xdgDataHome(); ok {
			roots = append(roots, filepath.Join(xdg, OldStoreDirname))
		} else {
			roots = append(roots, filepath.Join(home(), ".local", "share", OldStoreDirname))
		}
		return append(roots, legacy)
	default:
		return []string{legacy}
	}
}

// xdgDataHome returns $XDG_DATA_HOME, ~-expanded, when it is set and absolute.
func xdgDataHome() (string, bool) {
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg == "" {
		return "", false
	}
	xp := expandUser(xdg)
	if !filepath.IsAbs(xp) {
		return "", false
	}
	return xp, true
}

// expandUser replaces a leading ~ (or ~/) with the user's home directory,
// mirroring os.path.expanduser for the cases GetBackupRoot cares about.
func expandUser(p string) string {
	if p == "~" {
		return home()
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~"+string(os.PathSeparator)) {
		return filepath.Join(home(), p[2:])
	}
	return p
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
