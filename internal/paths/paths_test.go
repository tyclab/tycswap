package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolate points HOME and USERPROFILE (os.UserHomeDir on Windows) at a fresh
// temp dir and clears the two env vars that bypass the home in path
// resolution. testutil.IsolateHome imports this package, so this is its copy.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	unset(t, "CLAUDE_CONFIG_DIR")
	unset(t, "XDG_DATA_HOME")
	return home
}

func unset(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // installs parallel guard + restore
	_ = os.Unsetenv(key)
}

func TestGetGlobalConfigPathDefault(t *testing.T) {
	home := isolate(t)
	// Default: $HOME/.claude.json, NOT inside .claude/.
	if got := GetGlobalConfigPath(); got != filepath.Join(home, ".claude.json") {
		t.Errorf("GetGlobalConfigPath = %q", got)
	}
}

func TestGetGlobalConfigPathLegacyWins(t *testing.T) {
	home := isolate(t)
	legacy := filepath.Join(home, ".claude", ".config.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := GetGlobalConfigPath(); got != legacy {
		t.Errorf("GetGlobalConfigPath = %q, want legacy %q", got, legacy)
	}
}

func TestGetGlobalConfigPathRespectsCCD(t *testing.T) {
	isolate(t)
	ccd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", ccd)
	if got := GetGlobalConfigPath(); got != filepath.Join(ccd, ".claude.json") {
		t.Errorf("GetGlobalConfigPath with CCD = %q", got)
	}
	if got := GetClaudeConfigHome(); got != ccd {
		t.Errorf("GetClaudeConfigHome = %q, want %q", got, ccd)
	}
	if got := GetCredentialsPath(); got != filepath.Join(ccd, ".credentials.json") {
		t.Errorf("GetCredentialsPath = %q", got)
	}
}

// TestGetSecureStorageHome: Claude Code's KS is CLAUDE_SECURESTORAGE_CONFIG_DIR
// when set, ~/.claude when set but empty (not CLAUDE_CONFIG_DIR), else the
// config home.
func TestGetSecureStorageHome(t *testing.T) {
	home := isolate(t)
	ccd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", ccd)
	unset(t, "CLAUDE_SECURESTORAGE_CONFIG_DIR")
	if got := GetSecureStorageHome(); got != ccd {
		t.Errorf("unset: %q, want %q", got, ccd)
	}
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "")
	if got, want := GetSecureStorageHome(), filepath.Join(home, ".claude"); got != want {
		t.Errorf("empty: %q, want %q", got, want)
	}
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "/ks")
	if got := GetSecureStorageHome(); got != "/ks" {
		t.Errorf("set: %q, want /ks", got)
	}
}

func TestGetBackupRootXDG(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG layout is the Linux/WSL store root")
	}
	home := isolate(t)
	defaultRoot := filepath.Join(home, ".local", "share", "tycswap")
	absXDG := t.TempDir()

	tests := []struct {
		name    string
		xdg     string
		set     bool
		want    string
		wantOld string
	}{
		{"unset", "", false, defaultRoot, filepath.Join(home, ".local", "share", "claude-swap")},
		{"empty ignored", "", true, defaultRoot, filepath.Join(home, ".local", "share", "claude-swap")},
		{"absolute honored", absXDG, true, filepath.Join(absXDG, "tycswap"), filepath.Join(absXDG, "claude-swap")},
		{"relative ignored", "rel/data", true, defaultRoot, filepath.Join(home, ".local", "share", "claude-swap")},
		{"tilde expanded", "~/data", true, filepath.Join(home, "data", "tycswap"), filepath.Join(home, "data", "claude-swap")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("XDG_DATA_HOME", tt.xdg)
			} else {
				unset(t, "XDG_DATA_HOME")
			}
			if got := GetBackupRoot(); got != tt.want {
				t.Errorf("GetBackupRoot = %q, want %q", got, tt.want)
			}
			old := OldBackupRoots()
			want := []string{tt.wantOld, filepath.Join(home, ".claude-swap-backup")}
			if strings.Join(old, "|") != strings.Join(want, "|") {
				t.Errorf("OldBackupRoots = %q, want %q", old, want)
			}
		})
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
