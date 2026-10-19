package cli

import (
	"bytes"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tyclab/tycswap/internal/reporting"
)

func TestHumanMenuGate(t *testing.T) {
	cleanHome(t)
	for _, key := range []string{"CODEX_THREAD_ID", "CLAUDECODE", "TYCSWAP_NO_PROMPT", "TERM"} {
		t.Setenv(key, "")
	}
	oldMenu := runHumanMenu
	t.Cleanup(func() { runHumanMenu = oldMenu })
	called := false
	runHumanMenu = func(string, string, ioStreams) int { called = true; return 0 }
	for _, tc := range []struct {
		name, key, value string
		in, out, want    bool
	}{
		{"human", "", "", true, true, true}, {"pipe", "", "", false, true, false},
		{"redirect", "", "", true, false, false}, {"codex", "CODEX_THREAD_ID", "thread", true, true, false},
		{"claude", "CLAUDECODE", "1", true, true, false}, {"opt out", "TYCSWAP_NO_PROMPT", "1", true, true, false},
		{"dumb", "TERM", "dumb", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			}
			called = false
			code, _, _ := runCLI(t, nil, tc.in, tc.out)
			if called != tc.want || tc.want && code != 0 || !tc.want && code != 2 {
				t.Fatalf("called=%v code=%d", called, code)
			}
		})
	}

}

func TestMenuWhileTrayRuns(t *testing.T) {
	appLockHome(t)
	lock, held, err := acquireAppLock()
	if err != nil || !held {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	oldAsk := askMenu
	t.Cleanup(func() { askMenu = oldAsk })
	called := false
	askMenu = func(ioStreams, string, []string, string) (int, string, bool) { called = true; return 0, "", false }
	if code := humanMenu("tycswap", "", ioStreams{}); code != 0 || !called {
		t.Fatal("running tray prevented menu")
	}
	if !appIsRunning() {
		t.Fatal("closing menu stopped tray")
	}
}

func TestMenuAddRoutesBrowserLoginAndCancel(t *testing.T) {
	oldAsk, oldExec := askMenu, menuExecute
	t.Cleanup(func() { askMenu, menuExecute = oldAsk, oldExec })
	var got []string
	menuExecute = func(_ ioStreams, args ...string) int { got = args; return 0 }
	for _, tc := range []struct {
		choice int
		accept bool
		want   []string
	}{
		{0, true, []string{"add", "--login"}}, {2, true, []string{"add"}}, {3, true, []string{"codex", "add"}}, {0, false, nil},
	} {
		got = nil
		askMenu = func(ioStreams, string, []string, string) (int, string, bool) { return tc.choice, "", tc.accept }
		menuAdd(ioStreams{})
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("got %v want %v", got, tc.want)
		}
	}
}

func TestSetupMenuStaysOpenAfterSignIn(t *testing.T) {
	cleanHome(t)
	oldAsk, oldExec := askMenu, menuExecute
	t.Cleanup(func() { askMenu, menuExecute = oldAsk, oldExec })
	var titles []string
	askMenu = func(_ ioStreams, title string, _ []string, _ string) (int, string, bool) {
		titles = append(titles, title)
		return 0, "", len(titles) == 1
	}
	menuExecute = func(ioStreams, ...string) int { return 1 }
	if code := humanMenu("tycswap", "add", ioStreams{}); code != 0 || !reflect.DeepEqual(titles, []string{"Add account", "tycswap"}) {
		t.Fatalf("code=%d prompts=%v", code, titles)
	}
}

func TestMenuRefusesReassignedSlot(t *testing.T) {
	oldAsk, oldExec, oldAccounts := askMenu, menuExecute, menuAccounts
	t.Cleanup(func() { askMenu, menuExecute, menuAccounts = oldAsk, oldExec, oldAccounts })
	selected := reporting.AccountSnapshot{Number: "1", Email: "same@example.com", OrgUUID: "org-a"}
	reads := 0
	menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) {
		reads++
		if reads > 1 {
			selected.OrgUUID = "org-b"
		}
		return []reporting.AccountSnapshot{selected}, nil
	}
	answers := []int{0, 2}
	askMenu = func(ioStreams, string, []string, string) (int, string, bool) {
		n := answers[0]
		answers = answers[1:]
		return n, "", true
	}
	menuExecute = func(ioStreams, ...string) int { t.Fatal("acted on a different account"); return 0 }
	var stderr bytes.Buffer
	menuManage(ioStreams{err: &stderr})
	if !strings.Contains(stderr.String(), "changed") {
		t.Fatal(stderr.String())
	}
}

func TestMenuPromptFilteringAndCancellation(t *testing.T) {
	m := menuPrompt{choices: []string{"Claude work", "Codex personal"}, width: 80, height: 24}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("codex")})
	m = next.(menuPrompt)
	if !reflect.DeepEqual(m.matches(), []int{1}) {
		t.Fatal(m.matches())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = next.(menuPrompt)
	if m.text != "codex " || !reflect.DeepEqual(m.matches(), []int{1}) {
		t.Fatalf("space was lost: %q", m.text)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(menuPrompt).accepted || !next.(menuPrompt).done {
		t.Fatal("Escape selected an action")
	}
}

func TestAccountSetupLaunchers(t *testing.T) {
	old := setupLookPath
	t.Cleanup(func() { setupLookPath = old })
	for _, tool := range []string{"wt.exe", "powershell.exe", "x-terminal-emulator"} {
		t.Run(tool, func(t *testing.T) {
			setupLookPath = func(name string) (string, error) {
				if name == tool {
					return "/bin/" + name, nil
				}
				return "", exec.ErrNotFound
			}
			distro := "Nix Dev's box"
			if tool == "x-terminal-emulator" {
				distro = ""
			}
			cmd, err := accountSetupCommand("linux", distro, "/opt/my tools/tycswap")
			if err != nil {
				t.Fatal(err)
			}
			want := setupLoginArgs("/opt/my tools/tycswap")
			if tool != "powershell.exe" && !reflect.DeepEqual(cmd.Args[len(cmd.Args)-len(want):], want) {
				t.Fatal(cmd.Args)
			}
			if tool == "powershell.exe" && !strings.Contains(cmd.Args[len(cmd.Args)-1], `"Nix Dev''s box"`) {
				t.Fatal(cmd.Args)
			}
		})
	}
	setupLookPath = func(string) (string, error) { return "", fmt.Errorf("missing") }
	if _, err := accountSetupCommand("linux", "NixOS", "/bin/tycswap"); err == nil {
		t.Fatal("missing terminal reported success")
	}
}

func TestRemoteSetupUsesTokenFileDistro(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{`\\wsl.localhost\Nix Dev\home\user\remote.token`, "Nix Dev"},
		{`\\wsl$\Ubuntu\home\user\remote.token`, "Ubuntu"},
		{`C:\Users\user\remote.token`, ""}, {`\\server\share\remote.token`, ""},
		{`\\wsl.localhost\..\remote.token`, ""},
	} {
		if got := remoteSetupDistro(tc.path); got != tc.want {
			t.Fatalf("%s: %q", tc.path, got)
		}
	}
	c := newRemoteClient("http://127.0.0.1:1234", "unused")
	called := 0
	c.accountSetup = func() { called++ }
	c.dispatch("account-setup", []byte(`{}`), nil, nil, nil)
	if called != 1 {
		t.Fatal(called)
	}
}
