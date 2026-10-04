//go:build windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

var procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")

// hasConsole reports whether this process has a console window.
func hasConsole() bool {
	h, _, _ := procGetConsoleWindow.Call()
	return h != 0
}

func consoleCountForTest() int { return consoleProcessCount() }

// The console hand-off for real: a child started the way Explorer starts a
// console program — with a console of its own — sees itself as that console's
// only process, frees it, and writes to the log instead (A41).
func TestReleaseOwnConsoleForReal(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), childRoleEnv+"=release-console", "USERPROFILE="+home, "HOME="+home)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
	if err := cmd.Run(); err != nil {
		t.Fatalf("child: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".tycswap", "app.log"))
	if err != nil {
		t.Fatalf("no log written: %v", err)
	}
	log := string(raw)
	for _, want := range []string{"count before: 1", "released", "console: false"} {
		if !strings.Contains(log, want) {
			t.Errorf("child log lacks %q:\n%s", want, log)
		}
	}
}
