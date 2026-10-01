package credstore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
)

// TestOversizedBackupNeverReachesSecurityArgv drives the real keychain.Security
// against a stand-in `security` that records every argv it is given. A backup
// too large for `security -i`'s stdin line must be stored in the 0600 .enc file,
// and no invocation may carry -X (the hex secret) on its command line.
func TestOversizedBackupNeverReachesSecurityArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell stand-in for security")
	}
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	script := filepath.Join(dir, "security")
	// Records argv; answers "not found" (rc 44) to reads and deletes, and reads
	// and discards stdin for -i.
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + argvLog + "\ncase \"$1\" in -i) cat >/dev/null; exit 0;; esac\nexit 44\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	credDir := filepath.Join(dir, "credentials")
	s := newStore(t, platform.MacOS, credDir, keychain.Security{Path: script}, nil)

	big := `{"claudeAiOauth": {"accessToken": "a", "refreshToken": "r"}, "mcpOAuth": {"pad": "` +
		strings.Repeat("s", keychain.SecurityStdinLineLimit) + `"}}`
	if err := s.WriteBackup("1", "a@example.com", big); err != nil {
		t.Fatalf("WriteBackup: %v", err)
	}
	got, err := s.ReadBackup("1", "a@example.com")
	if err != nil || got != big {
		t.Fatalf("ReadBackup = %d bytes, %v; want the oversized payload back", len(got), err)
	}
	if _, err := os.Stat(s.backupEncPath("1", "a@example.com")); err != nil {
		t.Fatalf("no .enc fallback file: %v", err)
	}
	logged, _ := os.ReadFile(argvLog)
	for _, line := range strings.Split(string(logged), "\n") {
		if strings.Contains(line, "-X") || strings.Contains(line, "add-generic-password") {
			t.Fatalf("security was given the secret on its command line: %.80q", line)
		}
	}
	// The Keychain is still considered usable: a small secret goes there.
	if !s.useKeychain() {
		t.Fatal("an oversized payload disabled the Keychain for everything else")
	}
}
