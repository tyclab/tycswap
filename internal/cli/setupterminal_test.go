package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSetupKeepsWSLOptionNamesUnquoted(t *testing.T) {
	cmd := powershellSetupCommand("powershell.exe", "wsl.exe", "--distribution", "Nix Dev", "--exec", "/bin/sh", "-lc", "exec tycswap menu add")
	want := `-ArgumentList '--distribution "Nix Dev" --exec /bin/sh -lc "exec tycswap menu add"'`
	if !strings.Contains(cmd.Args[len(cmd.Args)-1], want) {
		t.Fatal(cmd.Args)
	}
}

func TestSetupLoadsLoginEnvironmentAndQuotesExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix login shell")
	}
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is unavailable")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "my tools '$dollar")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, ".zprofile"), "export PATH="+shellWord(bin)+":$PATH\n")
	write(filepath.Join(bin, "claude"), "#!/bin/sh\nexit 0\n")
	exe := filepath.Join(bin, "tycswap")
	write(exe, "#!/bin/sh\ncommand -v claude\nprintf '%s\\n' \"$@\"\n")
	for _, name := range []string{exe, "tycswap"} {
		args := setupLoginArgs(name)
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = []string{"HOME=" + root, "ZDOTDIR=" + root, "SHELL=" + zsh, "PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		want := filepath.Join(bin, "claude") + "\nmenu\nadd\n"
		if err != nil || !strings.HasSuffix(string(out), want) {
			t.Fatalf("%q: %v, output %q", name, err, out)
		}
	}
}
