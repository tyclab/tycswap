package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
)

// fableSessionProcess: this process runs under a Fable-store session (its status line, quota hook or an agent; DESIGN A100).
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
