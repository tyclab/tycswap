//go:build windows

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// With Windows Terminal installed the remote setup is a tab in it, entered
// through wsl.exe's own login shell: no shell text reaches wt.exe's command
// parser, which splits at ";" and drops double quotes (DESIGN A81). Without
// it, the PowerShell console path remains.
func TestRemoteSetupOpensWindowsTerminalOrANativeConsole(t *testing.T) {
	oldLookPath, oldStart := setupLookPath, setupStart
	t.Cleanup(func() { setupLookPath, setupStart = oldLookPath, oldStart })
	token := `\\wsl.localhost\Nix Dev\home\user\remote.token`
	noStore := func() error {
		t.Fatal("remote setup used the local account store")
		return nil
	}

	setupLookPath = func(name string) (string, error) {
		if name == "wt.exe" {
			return `C:\Users\me\AppData\Local\Microsoft\WindowsApps\wt.exe`, nil
		}
		return "", exec.ErrNotFound
	}
	started := false
	setupStart = func(cmd *exec.Cmd) error {
		want := []string{"-w", "0", "new-tab", "--title", brandName() + " setup", "wsl.exe", "--distribution", "Nix Dev", "--shell-type", "login", "--", "tycswap", "menu", "add"}
		if !strings.HasSuffix(cmd.Path, `\wt.exe`) || !slices.Equal(cmd.Args[1:], want) {
			t.Fatalf("setup is not a Windows Terminal tab: %v", cmd.Args)
		}
		for _, arg := range cmd.Args {
			if strings.ContainsAny(arg, `;"`) {
				t.Fatalf("argument %q cannot pass through wt.exe", arg)
			}
		}
		started = true
		return nil
	}
	if err := openRemoteAccountSetup(token, noStore); err != nil || !started {
		t.Fatalf("setup started=%v, error=%v", started, err)
	}

	setupLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	started = false
	setupStart = func(cmd *exec.Cmd) error {
		if !strings.Contains(cmd.Args[len(cmd.Args)-1], "-FilePath 'wsl.exe'") {
			t.Fatalf("setup does not open a WSL console: %v", cmd.Args)
		}
		started = true
		return nil
	}
	if err := openRemoteAccountSetup(token, noStore); err != nil || !started {
		t.Fatalf("setup started=%v, error=%v", started, err)
	}
}

func TestSetupConsoleHasInteractiveStreams(t *testing.T) {
	report := filepath.Join(t.TempDir(), "console '$ result")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := windowsSetupCommand(exe, "-test.run=^TestSetupConsoleProbe$", "--", report)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	timedOut := make(chan struct{})
	timer := time.AfterFunc(20*time.Second, func() {
		close(timedOut)
		_ = cmd.Process.Kill()
	})
	defer timer.Stop()
	if err := cmd.Wait(); err != nil {
		select {
		case <-timedOut:
			t.Fatalf("console launcher timed out after 20 seconds: %s", output.String())
		default:
		}
		t.Fatalf("console launch: %v: %s", err, output.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if result, err := os.ReadFile(report); err == nil {
			if string(result) != "true" {
				t.Fatalf("setup console: %s", result)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("setup console did not start")
}

func TestSetupConsoleProbe(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	report := os.Args[len(os.Args)-1]
	result := strconv.FormatBool(isTTY(os.Stdin) && isTTY(os.Stdout))
	if err := os.WriteFile(report+".tmp", []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(report+".tmp", report); err != nil {
		t.Fatal(err)
	}
}
