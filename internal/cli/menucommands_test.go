package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

func TestMenuModelAllocationAndIndependentEnrollment(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("independent file-based storage unavailable")
	}
	f := newLoginFixture(t)
	t.Setenv("FAKE_CLAUDE_EMAIL", "a@example.com")
	t.Setenv("FAKE_CLAUDE_TOKEN", "synthetic-independent-token")
	t.Setenv("FAKE_CLAUDE_REFRESH", "synthetic-independent-refresh")
	if code, _, msg := f.run(t, "mythos", "--allow", "1"); code != 0 {
		t.Fatal(msg)
	}
	active := reporting.AccountSnapshot{Number: "1", Email: "a@example.com", Kind: "oauth", Mythos: true}
	oldExec, oldAccounts := menuExecute, menuAccounts
	t.Cleanup(func() { menuExecute, menuAccounts = oldExec, oldAccounts })
	menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) {
		return []reporting.AccountSnapshot{
			{Number: "2", Kind: "oauth", Mythos: true, Disabled: true},
			{Number: "3", Kind: "api_key", Mythos: true},
			{Number: "4", Kind: "oauth"},
			{Number: "5", Kind: "oauth", Mythos: true, Provider: reporting.ProviderCodex},
			active,
		}, nil
	}
	menuExecute = func(_ ioStreams, args ...string) int {
		code, _, msg := f.run(t, args...)
		if code != 0 {
			t.Fatalf("%q: %s", args, msg)
		}
		return code
	}
	s := ioStreams{err: io.Discard}
	menuScript(t, "claude #1", "Preferred models", "fable,opus")
	menuManage(s)
	if code, out, msg := f.run(t, "models", "--list", "--json"); code != 0 || !strings.Contains(out, `"explicit"`) || !strings.Contains(out, `"opus"`) || !strings.Contains(out, `"fable"`) {
		t.Fatalf("explicit allocation: %s %s", out, msg)
	}
	menuScript(t, "claude #1", "Preferred models", "Clear")
	menuManage(s)
	if code, out, msg := f.run(t, "models", "--list"); code != 0 || !strings.Contains(out, "(no explicit allocation)") {
		t.Fatalf("cleared allocation: %s %s", out, msg)
	}
	choices := menuScript(t, mythosSignInEntry, "--debug", "Run", "claude #1")
	menuAdd(s)
	if got := choices["Sign in independently as which account?"]; len(got) != 1 || !strings.Contains(got[0], active.Email) {
		t.Fatalf("eligible enrollment accounts: %v", got)
	}
	raw, err := os.ReadFile(filepath.Join(store.MythosLoginDir(paths.GetBackupRoot(), active.Email, ""), ".credentials.json"))
	if err != nil || !bytes.Contains(raw, []byte("synthetic-independent-token")) {
		t.Fatalf("independent identity missing after native fixture sign-in: %v", err)
	}
}

func TestMenuCommandsNativePromptAndDispatcher(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh unavailable")
	}
	cleanHome(t)
	oldAsk, oldExec := askMenu, menuExecute
	t.Cleanup(func() { askMenu, menuExecute = oldAsk, oldExec })
	answers := []string{"Browse commands", "config get", "--json", "Run", "autoswitch.enabled", "Close menu"}
	var out, trace bytes.Buffer
	askMenu = func(s ioStreams, title string, choices []string, initial string) (int, string, bool) {
		if len(answers) == 0 {
			t.Fatalf("unexpected prompt %q", title)
		}
		s.in = strings.NewReader(answers[0] + "\n")
		answers = answers[1:]
		return askTerminal(s, title, choices, initial)
	}
	var executed []string
	menuExecute = func(s ioStreams, args ...string) int {
		executed = args
		return run("tycswap", args, s, true, true)
	}
	if code := run("tycswap", []string{"menu"}, ioStreams{in: strings.NewReader(""), out: &out, err: &trace}, true, true); code != 0 ||
		len(answers) != 0 || !reflect.DeepEqual(executed, []string{"config", "get", "autoswitch.enabled", "--json"}) ||
		!strings.Contains(out.String(), `"value": false`) {
		t.Fatalf("menu code=%d args=%q outstanding=%v output=%q trace=%q", code, executed, answers, out.String(), trace.String())
	}
}

func TestMenuCommandsPreserveProviderAndArgumentBoundaries(t *testing.T) {
	oldAsk, oldAccounts := askMenu, menuAccounts
	t.Cleanup(func() { askMenu, menuAccounts = oldAsk, oldAccounts })
	menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) {
		return []reporting.AccountSnapshot{{Number: "1", Email: "a@example.com"}, {Number: "3", Provider: reporting.ProviderCodex, Email: "b@example.com"}}, nil
	}
	commands := workflowCommands(t)
	for _, tc := range []struct {
		input, want []string
	}{
		{[]string{"codex alias", "Name", "two words", "--debug", "Run", "codex #3"}, []string{"codex", "alias", "3", "two words", "--debug"}},
		{[]string{"start   Start", "--account", "claude #1", "--   Pass", "claude", "--model", "opus", "--name", "a $(echo nope)", "", "Run"}, []string{"start", "--account", "1", "--", "claude", "--model", "opus", "--name", "a $(echo nope)"}},
		{[]string{"app ", "--autostart", "status", "Run"}, []string{"app", "--autostart", "status"}},
		{[]string{"auto   Run", "--once", "--model-threshold", "87", "--dry-run", "Run"}, []string{"auto", "--once", "--model-threshold", "87", "--dry-run"}},
	} {
		answers := tc.input
		askMenu = func(_ ioStreams, title string, choices []string, _ string) (int, string, bool) {
			if len(answers) == 0 {
				t.Fatalf("unexpected prompt %q", title)
			}
			ans := answers[0]
			answers = answers[1:]
			if choices == nil {
				return 0, ans, true
			}
			for i, choice := range choices {
				if strings.HasPrefix(choice, ans) {
					return i, "", true
				}
			}
			t.Fatalf("%q unavailable in %q", ans, choices)
			return 0, "", false
		}
		*commands = nil
		menuCommands(ioStreams{err: io.Discard})
		if len(answers) != 0 || !reflect.DeepEqual(*commands, [][]string{tc.want}) {
			t.Fatalf("inputs %q: remaining %q, got %q, want %q", tc.input, answers, *commands, tc.want)
		}
	}
}
