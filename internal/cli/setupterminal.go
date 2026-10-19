package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/platform"
)

var setupLookPath = exec.LookPath
var setupStart = func(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func openAccountSetup() error {
	if platform.Detect() == platform.WSL && os.Getenv("WSL_DISTRO_NAME") == "" {
		return fmt.Errorf("this background service has no desktop session; choose Add account in the Windows tray, or run tycswap in a WSL terminal")
	}
	exe := exePath()
	if exe == "" {
		return fmt.Errorf("cannot locate tycswap; open a terminal and run tycswap menu add")
	}
	cmd, err := accountSetupCommand(runtime.GOOS, os.Getenv("WSL_DISTRO_NAME"), exe)
	if err != nil {
		return err
	}
	return setupStart(cmd)
}

func accountSetupCommand(goos, distro, exe string) (*exec.Cmd, error) {
	if goos == "windows" {
		return windowsSetupCommand(exe), nil
	}
	if distro != "" {
		if wt, err := setupLookPath("wt.exe"); err == nil {
			return exec.Command(wt, "new-tab", "wsl.exe", "--distribution", distro, "--exec", exe, "menu", "add"), nil
		}
		if ps, err := setupLookPath("powershell.exe"); err == nil {
			// A quoted PowerShell literal contains one Windows command line; no shell reads the Linux executable path.
			args := "--distribution " + windowsArgument(distro) + " --exec " + windowsArgument(exe) + " menu add"
			script := "Start-Process -FilePath wsl.exe -ArgumentList '" + strings.ReplaceAll(args, "'", "''") + "'"
			return exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", script), nil
		}
		return nil, fmt.Errorf("Windows Terminal is unavailable; open a WSL terminal and run tycswap menu add")
	}
	if goos == "darwin" {
		command := shellWord(exe) + " menu add"
		return exec.Command("osascript", "-e", "tell application \"Terminal\" to do script "+strconv.Quote(command), "-e", "tell application \"Terminal\" to activate"), nil
	}
	for _, candidate := range []struct{ name, flag string }{
		{"x-terminal-emulator", "-e"}, {"gnome-terminal", "--"}, {"konsole", "-e"}, {"xterm", "-e"},
	} {
		if binary, err := setupLookPath(candidate.name); err == nil {
			return exec.Command(binary, candidate.flag, exe, "menu", "add"), nil
		}
	}
	return nil, fmt.Errorf("no desktop terminal is available; run tycswap menu add in a terminal on this computer")
}

func remoteSetupDistro(tokenFile string) string {
	parts := strings.Split(strings.ReplaceAll(tokenFile, "/", `\`), `\`)
	if len(parts) < 5 || parts[0] != "" || parts[1] != "" ||
		(!strings.EqualFold(parts[2], "wsl.localhost") && !strings.EqualFold(parts[2], "wsl$")) ||
		parts[3] == "" || parts[3] == "." || parts[3] == ".." {
		return ""
	}
	return parts[3]
}

func openRemoteAccountSetup(tokenFile string, fallback func() error) error {
	distro := remoteSetupDistro(tokenFile)
	if runtime.GOOS != "windows" || distro == "" {
		return fallback()
	}
	args := []string{"--distribution", distro, "--exec", "/bin/sh", "-lc", "exec tycswap menu add"}
	if wt, err := setupLookPath("wt.exe"); err == nil {
		return setupStart(exec.Command(wt, append([]string{"new-tab", "wsl.exe"}, args...)...))
	}
	cmd := windowsSetupCommand("wsl.exe")
	cmd.Args = append([]string{"wsl.exe"}, args...)
	return setupStart(cmd)
}

func canOpenRemoteSetup(tokenFile string) bool {
	return runtime.GOOS == "windows" && remoteSetupDistro(tokenFile) != ""
}

func windowsArgument(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, c := range s {
		if c == '\\' {
			slashes++
			continue
		}
		if c == '"' {
			b.WriteString(strings.Repeat("\\", slashes*2+1))
		} else {
			b.WriteString(strings.Repeat("\\", slashes))
		}
		slashes = 0
		b.WriteRune(c)
	}
	b.WriteString(strings.Repeat("\\", slashes*2))
	b.WriteByte('"')
	return b.String()
}
