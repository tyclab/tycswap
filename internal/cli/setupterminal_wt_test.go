package cli

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func lookPathOnly(t *testing.T, found map[string]string) {
	t.Helper()
	prev := setupLookPath
	t.Cleanup(func() { setupLookPath = prev })
	setupLookPath = func(name string) (string, error) {
		if path, ok := found[name]; ok {
			return path, nil
		}
		return "", exec.ErrNotFound
	}
}

const wtPath = `C:\Users\me\AppData\Local\Microsoft\WindowsApps\wt.exe`

func TestSetupOpensATabInTheCurrentWindowsTerminal(t *testing.T) {
	lookPathOnly(t, map[string]string{"wt.exe": wtPath, "powershell.exe": `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`})
	cmd, err := accountSetupCommand("windows", "", `C:\tycswap\tycswap.exe`, "add")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-w", "0", "new-tab", "--title", brandName() + " setup", `C:\tycswap\tycswap.exe`, "menu", "add"}
	if !strings.HasSuffix(cmd.Path, "wt.exe") || !slices.Equal(cmd.Args[1:], want) {
		t.Fatal(cmd.Args)
	}
	cmd, err = accountSetupCommand("linux", "Nix", "/opt/my tools/tycswap", "manage")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"-w", "0", "new-tab", "--title", brandName() + " setup", "wsl.exe", "--distribution", "Nix", "--shell-type", "login", "--", "/opt/my tools/tycswap", "menu", "manage"}
	if !strings.HasSuffix(cmd.Path, "wt.exe") || !slices.Equal(cmd.Args[1:], want) {
		t.Fatal(cmd.Args)
	}
	// Windows Terminal splits arguments at ";" and drops double quotes: none
	// may appear in what it is handed.
	for _, arg := range cmd.Args {
		if strings.ContainsAny(arg, `;"`) {
			t.Fatalf("argument %q cannot pass through wt.exe", arg)
		}
	}
}

func TestSetupFallsBackToAConsoleWithoutWindowsTerminal(t *testing.T) {
	lookPathOnly(t, map[string]string{"powershell.exe": `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`})
	cmd, err := accountSetupCommand("linux", "Nix", "/usr/bin/tycswap", "add")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cmd.Path, "powershell.exe") || !strings.Contains(cmd.Args[len(cmd.Args)-1], "wsl.exe") {
		t.Fatal(cmd.Args)
	}
	lookPathOnly(t, map[string]string{})
	if _, err := accountSetupCommand("linux", "Nix", "/usr/bin/tycswap", "add"); err == nil {
		t.Fatal("no launcher at all should be refused")
	}
}
