//go:build windows

package cli

import (
	"os/exec"
	"testing"
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
		if cmd.Args[0] != "wsl.exe" || cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&0x10 == 0 {
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
