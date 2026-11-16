package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/testutil"
)

// The operator removed the per-account profile commands on 2026-11-10 (DESIGN A92).
func TestLegacyProfileCommandsAreGone(t *testing.T) {
	for _, argv := range [][]string{
		{"run"}, {"run", "2", "--", "--resume"}, {"run", "--help"},
		{"env", "2"}, {"env", "--unset"}, {"env", "--shell", "fish", "2"},
		{"map"}, {"map", "2", "."}, {"unmap"}, {"unmap", "."},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			cleanHome(t)
			// Nothing may start a real claude, whatever this binary does.
			testutil.Setenv(t, "PATH", t.TempDir())
			code, out, errStr := runSub(t, argv...)
			if code != 2 || out != "" {
				t.Fatalf("exit = %d, stdout %q; want 2 and nothing on stdout (stderr %q)", code, out, errStr)
			}
			if !strings.Contains(errStr, "tycswap "+argv[0]+" was removed") || !strings.Contains(errStr, "shared default login") {
				t.Fatalf("stderr does not name the removal and the shared login: %q", errStr)
			}
			if _, err := os.Stat(paths.GetBackupRoot()); !os.IsNotExist(err) {
				t.Fatalf("a removed command touched the store: %v", err)
			}
		})
	}
	t.Run("help", func(t *testing.T) {
		cleanHome(t)
		code, out, _ := runSub(t, "--help")
		if code != 0 {
			t.Fatalf("help exit = %d", code)
		}
		if listed := regexp.MustCompile(`tycswap (run|env|map|unmap)\b`).FindAllString(out, -1); len(listed) > 0 {
			t.Fatalf("help still lists %v", listed)
		}
	})
}

// A shell whose CLAUDE_CONFIG_DIR names a former profile is told to unset
// it, in each shell's words, before it starts plain claude.
func TestRemovedCommandTellsPinnedShellToUnset(t *testing.T) {
	unset := []string{"unset CLAUDE_CONFIG_DIR", "set -e CLAUDE_CONFIG_DIR", "Remove-Item Env:CLAUDE_CONFIG_DIR"}
	for _, pinned := range []bool{true, false} {
		cleanHome(t)
		testutil.Setenv(t, "PATH", t.TempDir())
		if pinned {
			testutil.Setenv(t, "CLAUDE_CONFIG_DIR", sessprofile.SessionDirFor(paths.GetBackupRoot(), "2", "user@example.com"))
		}
		code, out, errStr := runSub(t, "env", "--unset")
		if code != 2 || out != "" {
			t.Fatalf("pinned %v: exit = %d, stdout %q", pinned, code, out)
		}
		for _, want := range unset {
			if strings.Contains(errStr, want) != pinned {
				t.Fatalf("pinned %v: %q in the message is %v: %q", pinned, want, !pinned, errStr)
			}
		}
		if _, err := os.Stat(paths.GetBackupRoot()); !os.IsNotExist(err) {
			t.Fatalf("the removal message touched the store: %v", err)
		}
	}
}

// A shell left pinned to a former profile works on the default login for
// tycswap's own commands, and is told how to unset it for claude.
func TestPinnedShellStillUsesDefaultProfile(t *testing.T) {
	cleanHome(t)
	profile := sessprofile.SessionDirFor(paths.GetBackupRoot(), "2", "user@example.com")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.Setenv(t, "CLAUDE_CONFIG_DIR", profile)
	code, _, errStr := runSub(t, "list")
	if code != 0 {
		t.Fatalf("list exit = %d (stderr %q)", code, errStr)
	}
	if got := os.Getenv("CLAUDE_CONFIG_DIR"); got != "" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want the default profile", got)
	}
	if lines := strings.Count(errStr, "default login"); lines != 1 {
		t.Fatalf("want one notice about the default login, got %d: %q", lines, errStr)
	}
	for _, want := range []string{"unset CLAUDE_CONFIG_DIR", "set -e CLAUDE_CONFIG_DIR", "Remove-Item Env:CLAUDE_CONFIG_DIR"} {
		if !strings.Contains(errStr, want) {
			t.Fatalf("the notice lacks %q: %q", want, errStr)
		}
	}
}
