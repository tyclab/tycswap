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

	"github.com/tyclab/tycswap/internal/launch"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

// The root help is the public command inventory. Each documented verb should
// lead to an action in the command picker, including the Codex subcommands.
func TestMenuCoversDocumentedCommands(t *testing.T) {
	choices := menuScript(t)
	menuCommands(ioStreams{err: io.Discard})
	offered := map[string]bool{"menu": true}
	for _, label := range choices["Choose a command"] {
		command, _, _ := strings.Cut(label, "   ")
		words := strings.Fields(command)
		if words[0] == "codex" {
			offered["codex "+words[1]] = true
		} else {
			offered[words[0]] = true
		}
	}
	foundSessionUse := false
	for _, row := range strings.Split(mainDescription, "\n") {
		usage, _, _ := strings.Cut(strings.TrimSpace(row), "  ")
		words := strings.Fields(usage)
		if len(words) < 2 || words[0] != "tycswap" {
			continue
		}
		prefix, verb := "", words[1]
		if verb == "use" {
			// Placement is invoked in a linked shell or against an explicit session ID.
			foundSessionUse = usage == "tycswap use <mythos|fable|default> [--session ID]"
			continue
		}
		if verb == "menubar" {
			verb = "app" // The retired CLI entry only points to the tray command.
		}
		if verb == "codex" {
			prefix, verb = "codex ", "--help"
			if len(words) >= 3 {
				verb = words[2]
			}
		}
		for _, name := range strings.Split(verb, "|") {
			if !offered[prefix+name] {
				t.Errorf("menu is missing documented command %s%s", prefix, name)
			}
		}
	}
	if !foundSessionUse {
		t.Error("root help is missing the session placement command and its selector")
	}
}

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
		{[]string{"codex alias   Set", "--debug", "Run", "codex #3", "two words"}, []string{"codex", "alias", "3", "two words", "--debug"}},
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

func TestMenuStatusCheckReadsAFileAndKeepsItsStore(t *testing.T) {
	cleanHome(t)
	for _, key := range append([]string{"CLAUDE_CONFIG_DIR"}, launch.AuthOverrideEnvVars...) {
		testutil.Unsetenv(t, key)
	}
	status := filepath.Join(t.TempDir(), "status input.json")
	if err := os.WriteFile(status, []byte(`{"session_id":"fixture","model":{"id":"claude-opus-5-5"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	dir := store.MythosLoginDir(paths.GetBackupRoot(), "fixture@example.invalid", "")
	t.Setenv(secureStorageEnv, dir)
	dropFableStoreEnv()
	t.Cleanup(func() { mythosSessionDir = "" })
	original := menuExecute
	t.Cleanup(func() { menuExecute = original })
	menuExecute = func(s ioStreams, args ...string) int {
		if !reflect.DeepEqual(args, []string{"start", "--check"}) {
			t.Fatalf("unexpected diagnostic command %q", args)
		}
		startCheck(s.in, s.out)
		return 0
	}
	menuScript(t, "start --check", "Run", status)
	var out bytes.Buffer
	menuCommands(ioStreams{out: &out, err: io.Discard})
	if got := out.String(); got != "Claude reports opus on the independent Mythos login.\n" {
		t.Fatalf("status diagnostic %q", got)
	}
}

func TestMenuSeparatesExclusiveCommandModes(t *testing.T) {
	oldAsk := askMenu
	t.Cleanup(func() { askMenu = oldAsk })
	commands := workflowCommands(t)
	for _, tc := range []struct {
		command         string
		present, absent []string
	}{
		{"web   Run", []string{"--port=Port", "--no-open"}, []string{"--reuse"}},
		{"web --reuse", nil, []string{"--debug", "--port=Port", "--no-open"}},
		{"app   Run", []string{"--headless", "--port=Port"}, []string{"--remote=URL", "--token-file=Path"}},
		{"app --remote", []string{"--token-file=Path"}, []string{"--headless", "--port=Port", "--interval=Seconds"}},
		{"codex switch   Select", nil, []string{"--strategy=best"}},
		{"codex alias   Set", nil, []string{"--unset"}},
	} {
		askMenu = func(_ ioStreams, title string, choices []string, _ string) (int, string, bool) {
			if title == "Choose a command" {
				for i, choice := range choices {
					if strings.HasPrefix(choice, tc.command) {
						return i, "", true
					}
				}
				t.Fatalf("command unavailable: %s", tc.command)
			}
			for _, value := range tc.present {
				if !contains(choices, value) {
					t.Errorf("%s: missing %s", tc.command, value)
				}
			}
			for _, value := range tc.absent {
				if contains(choices, value) {
					t.Errorf("%s: offered %s", tc.command, value)
				}
			}
			return 0, "", false
		}
		menuCommands(ioStreams{err: io.Discard})
	}
	if len(*commands) != 0 {
		t.Fatalf("browsing ran commands: %q", *commands)
	}
}

func TestMenuSelectsAnIdleEnrolledFableLogin(t *testing.T) {
	fableMenuPlatform(t, true)
	sw := fableCLISwitcher(t)
	active := reporting.AccountSnapshot{Number: "1", Email: "alice@example.com", Kind: "oauth"}
	second := reporting.AccountSnapshot{Number: "2", Email: "bob@example.com", Kind: "oauth"}
	oldAccounts, oldExec := menuAccounts, menuExecute
	t.Cleanup(func() { menuAccounts, menuExecute = oldAccounts, oldExec })
	menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) {
		return []reporting.AccountSnapshot{active, second, {Number: "3", Kind: "oauth", Disabled: true}, {Number: "4", Kind: "oauth"}}, nil
	}
	fableMenuRows(t, capable("1", active.Email, true), capable("2", second.Email, true), capable("3", "disabled@example.com", true), capable("4", "missing@example.com", false))
	menuExecute = func(_ ioStreams, args ...string) int {
		if !reflect.DeepEqual(args, []string{"switch", "--store", "fable", "--if-idle", "2", "--json"}) {
			t.Fatalf("command %v", args)
		}
		code, _, msg := runHuman(t, args...)
		if code != 0 {
			t.Fatal(msg)
		}
		return code
	}
	choices := menuScript(t, "switch --store fable --if-idle", "--json", "Run", "claude #2")
	menuCommands(ioStreams{err: io.Discard})
	if len(choices["Use which saved Fable login?"]) != 2 || sw.FableStore().Live.Slot != "2" {
		t.Fatalf("choices %v, store %v", choices, sw.FableStore().Live.Slot)
	}
}

func TestMenuRechecksStableAccountIdentity(t *testing.T) {
	previous := menuAccounts
	t.Cleanup(func() { menuAccounts = previous })
	for _, selected := range []reporting.AccountSnapshot{
		{Number: "1", Provider: reporting.ProviderCodex, Email: "same@example.invalid", OrgName: "Same team", AccountKey: "workspace-a", Kind: "oauth"},
		{Number: "1", Email: "same@example.invalid", BaseURL: "https://a.example.invalid", Kind: "api_key"},
	} {
		current := selected
		if current.ProviderName() == reporting.ProviderCodex {
			current.AccountKey = "workspace-b"
		} else {
			current.BaseURL = "https://b.example.invalid"
		}
		menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) { return []reporting.AccountSnapshot{current}, nil }
		if menuAccountCurrent(ioStreams{err: io.Discard}, selected) {
			t.Fatal("menu accepted a different native account or API endpoint with the same displayed identity")
		}
	}
}
