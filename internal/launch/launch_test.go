package launch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveDefaultSkipsLauncherAndSelf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	launcher, real, selfDir := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(launcher, "claude"), []byte("#!/usr/bin/env sh\n# "+LauncherMarker+"\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(real, "claude")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if err := os.Symlink(self, filepath.Join(selfDir, "claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", strings.Join([]string{selfDir, launcher, real}, string(os.PathListSeparator)))
	got, err := ResolveDefault()
	if err != nil || got != path {
		t.Fatalf("resolved=%q err=%v", got, err)
	}
	t.Setenv("PATH", launcher)
	_, err = ResolveDefault()
	if err == nil || ExitCode(err) != 127 {
		t.Fatalf("launcher-only err=%v code=%d", err, ExitCode(err))
	}
}

func TestCheckCmdShimArgs(t *testing.T) {
	if err := CheckCmdShimArgs("tool.CMD", []string{"a b", ""}); err != nil {
		t.Fatal(err)
	}
	if err := CheckCmdShimArgs("tool.cmd", []string{"a&b"}); err == nil {
		t.Fatal("unsafe shim argument accepted")
	}
	if err := CheckCmdShimArgs("tool.exe", []string{"a&b"}); err != nil {
		t.Fatal(err)
	}
}
