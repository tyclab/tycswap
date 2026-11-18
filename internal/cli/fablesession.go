package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
)

// fableSessionProcess is true when this process was started under a session
// on the Fable store: its status line, its quota hook or an agent's tycswap
// (DESIGN A100).
var fableSessionProcess bool

const secureStorageEnv = "CLAUDE_SECURESTORAGE_CONFIG_DIR"

// dropFableStoreEnv unsets CLAUDE_SECURESTORAGE_CONFIG_DIR when it names the
// Fable store, so this process takes the default store's Claude Code locks
// for the default login it acts on. Any other value is the user's own.
func dropFableStoreEnv() {
	if namesFableStore(os.Getenv(secureStorageEnv)) {
		_ = os.Unsetenv(secureStorageEnv)
		fableSessionProcess = true
	}
}

// namesFableStore reports whether dir is tycswap's Fable store directory,
// compared cleaned and, on Windows, case-insensitively.
func namesFableStore(dir string) bool {
	if dir == "" {
		return false
	}
	got, want := filepath.Clean(dir), filepath.Clean(paths.FableStoreDir(paths.GetBackupRoot()))
	if platform.IsWindows() {
		return strings.EqualFold(got, want)
	}
	return got == want
}
