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

func setupLoginArgs(exe string) []string {
	return []string{"/bin/sh", "-lc", `exec "${SHELL:-/bin/sh}" -lc "$1"`, "tycswap-setup", "exec " + shellWord(exe) + " menu add"}
}

func powershellSetupCommand(ps, exe string, args ...string) *exec.Cmd {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = windowsArgument(arg)
	}
	script := "Start-Process -ErrorAction Stop -WindowStyle Normal -FilePath '" + strings.ReplaceAll(exe, "'", "''") + "' -ArgumentList '" + strings.ReplaceAll(strings.Join(quoted, " "), "'", "''") + "'"
	return exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", script)
}

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
		return windowsSetupCommand(exe, "menu", "add"), nil
	}
	if distro != "" {
		args := append([]string{"--distribution", distro, "--exec"}, setupLoginArgs(exe)...)
		if ps, err := setupLookPath("powershell.exe"); err == nil {
			return powershellSetupCommand(ps, "wsl.exe", args...), nil
		}
		return nil, fmt.Errorf("Windows console launcher is unavailable; open a WSL terminal and run tycswap menu add")
	}
	if goos == "darwin" {
		command := shellWord(exe) + " menu add"
		return exec.Command("osascript", "-e", "tell application \"Terminal\" to do script "+strconv.Quote(command), "-e", "tell application \"Terminal\" to activate"), nil
	}
	for _, candidate := range []struct{ name, flag string }{
		{"x-terminal-emulator", "-e"}, {"gnome-terminal", "--"}, {"konsole", "-e"}, {"xterm", "-e"},
	} {
		if binary, err := setupLookPath(candidate.name); err == nil {
			return exec.Command(binary, append([]string{candidate.flag}, setupLoginArgs(exe)...)...), nil
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
	args := append([]string{"--distribution", distro, "--exec"}, setupLoginArgs("tycswap")...)
	return setupStart(windowsSetupCommand("wsl.exe", args...))
}

func canOpenRemoteSetup(tokenFile string) bool {
	return runtime.GOOS == "windows" && remoteSetupDistro(tokenFile) != ""
}

func windowsArgument(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
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
