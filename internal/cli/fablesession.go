package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/paths"
)

// fableSessionProcess: this process runs under a Fable-store session (its status line, quota hook or an agent; DESIGN A100).
var fableSessionProcess bool
var mythosSessionDir string

const secureStorageEnv = "CLAUDE_SECURESTORAGE_CONFIG_DIR"

// dropFableStoreEnv unsets CLAUDE_SECURESTORAGE_CONFIG_DIR when it names the
// Fable store, so this process takes the default store's Claude Code locks
// for the default login it acts on. Any other value is the user's own.
func dropFableStoreEnv() {
	mythosSessionDir = ""
	if value := os.Getenv(secureStorageEnv); isMythosDirectory(paths.GetBackupRoot(), value) {
		mythosSessionDir = value
		_ = os.Unsetenv(secureStorageEnv)
		return
	}
	if namesFableStore(os.Getenv(secureStorageEnv), paths.FableStoreDir(paths.GetBackupRoot()), startWindows()) {
		_ = os.Unsetenv(secureStorageEnv)
		fableSessionProcess = true
	}
}

// namesFableStore reports whether value names storeDir, both cleaned; on
// Windows without case.
func namesFableStore(value, storeDir string, windows bool) bool {
	if value == "" {
		return false
	}
	got, want := filepath.Clean(value), filepath.Clean(storeDir)
	if windows {
		return strings.EqualFold(got, want)
	}
	return got == want
}
