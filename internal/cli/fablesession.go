package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
)

// fableSessionProcess: this process runs under a Fable-store session (its status line, quota hook or an agent; DESIGN A100).
var fableSessionProcess bool
var mythosSessionDir string
var sessionLinkPath string
var sessionLinkErr error

const secureStorageEnv = "CLAUDE_SECURESTORAGE_CONFIG_DIR"

// Keep the calling session's store identity, then use the default credential
// locks for tycswap's own operations.
func dropFableStoreEnv() {
	root := paths.GetBackupRoot()
	dir := os.Getenv(secureStorageEnv)
	fableSessionProcess, mythosSessionDir = false, ""
	sessionLinkPath, sessionLinkErr = "", nil
	if reporting.IsSessionLink(root, dir) {
		sessionLinkPath = dir
		dir, sessionLinkErr = reporting.ResolveSessionLink(root, dir)
		if sessionLinkErr != nil {
			dir = ""
		}
		_ = os.Unsetenv(secureStorageEnv)
	}
	if isMythosDirectory(root, dir) {
		mythosSessionDir = dir
		_ = os.Unsetenv(secureStorageEnv)
	} else if namesFableStore(dir, paths.FableStoreDir(root), startWindows()) {
		fableSessionProcess = true
		_ = os.Unsetenv(secureStorageEnv)
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
