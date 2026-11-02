//go:build windows

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRemoteSetupStartsWSLInANativeConsole(t *testing.T) {
	oldLookPath, oldStart := setupLookPath, setupStart
	t.Cleanup(func() { setupLookPath, setupStart = oldLookPath, oldStart })
	setupLookPath = func(string) (string, error) {
		t.Fatal("remote setup must not pass shell text through a terminal command parser")
		return "", exec.ErrNotFound
	}
	started := false
	setupStart = func(cmd *exec.Cmd) error {
		if !strings.Contains(cmd.Args[len(cmd.Args)-1], "-FilePath 'wsl.exe'") {
			t.Fatalf("setup does not open a WSL console: %v", cmd.Args)
		}
		started = true
		return nil
	}
	err := openRemoteAccountSetup(`\\wsl.localhost\Nix Dev\home\user\remote.token`, func() error {
		t.Fatal("remote setup used the local account store")
		return nil
	})
	if err != nil || !started {
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
