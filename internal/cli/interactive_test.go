package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/reporting"
)

func TestMissingArgumentsUseProviderAndExistingCommands(t *testing.T) {
	cleanHome(t)
	oldAsk, oldAccounts, oldExec := askMenu, menuAccounts, menuExecute
	t.Cleanup(func() { askMenu, menuAccounts, menuExecute = oldAsk, oldAccounts, oldExec })
	menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) {
		return []reporting.AccountSnapshot{
			{Number: "2", Email: "claude@example.com"},
			{Provider: reporting.ProviderCodex, Number: "7", Email: "codex@example.com"},
		}, nil
	}
	for _, tc := range []struct {
		args, want []string
		text       string
	}{
		{[]string{"remove"}, []string{"remove", "2"}, ""},
		{[]string{"disable", "--debug"}, []string{"disable", "--debug", "2"}, ""},
		{[]string{"enable"}, []string{"enable", "2"}, ""},
		{[]string{"codex", "--debug", "remove", "-y"}, []string{"codex", "--debug", "remove", "-y", "7"}, ""},
		{[]string{"alias", "2"}, []string{"alias", "2", "work"}, "work"},
		{[]string{"alias", "2"}, []string{"alias", "--unset", "2"}, ""},
		{[]string{"alias", "--unset"}, []string{"alias", "--unset", "2"}, ""},
		{[]string{"codex", "alias"}, []string{"codex", "alias", "7", "personal"}, "personal"},
		{[]string{"move"}, []string{"move", "2", "9"}, "9"},
		{[]string{"codex", "move", "7"}, []string{"codex", "move", "7", "9"}, "9"},
		{[]string{"swap", "3"}, []string{"swap", "3", "2"}, ""},
		{[]string{"config", "set", "autoswitch.fiveHourThreshold"}, []string{"config", "set", "autoswitch.fiveHourThreshold", "80"}, "80"},
	} {
		t.Run(strings.Join(tc.args, " ")+tc.text, func(t *testing.T) {
			var got []string
			askMenu = func(_ ioStreams, _ string, choices []string, _ string) (int, string, bool) {
				if choices != nil && len(choices) != 1 {
					t.Fatalf("provider leaked into choices: %v", choices)
				}
				return 0, tc.text, true
			}
			menuExecute = func(_ ioStreams, args ...string) int { got = args; return 17 }
			var stderr bytes.Buffer
			code, handled := promptMissingArguments(tc.args, ioStreams{err: &stderr})
			if !handled || code != 17 || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("handled=%v code=%d args=%v stderr=%s", handled, code, got, stderr.String())
			}
		})
	}
}

func TestMissingArgumentsLeaveValidDefaultsAndErrorsToParser(t *testing.T) {
	oldAsk := askMenu
	t.Cleanup(func() { askMenu = oldAsk })
	askMenu = func(ioStreams, string, []string, string) (int, string, bool) {
		t.Fatal("unexpected prompt")
		return 0, "", false
	}
	for _, args := range [][]string{
		{"switch"}, {"switch", "--strategy", "best"}, {"add"}, {"alias"}, {"config"},
		{"codex", "switch"}, {"codex", "add"}, {"codex", "alias", "7"},
		{"codex"}, {"codex", "--debug"}, {"codex", "rm"},
		{"remove", "2"}, {"remove", "--json"}, {"remove", "--help"}, {"remove", "--wat"},
		{"codex", "remove", "--json"}, {"codex", "move", "--help"},
		{"alias", "2", "work"}, {"alias", "--unset", "2", "bad"},
		{"config", "get", "--json"}, {"config", "set", "--help"}, {"config", "set", "key", "value"},
	} {
		if code, handled := promptMissingArguments(args, ioStreams{}); handled || code != 0 {
			t.Fatalf("intercepted %v", args)
		}
	}
}

func TestMissingArgumentsCancellationAndStaleIdentity(t *testing.T) {
	cleanHome(t)
	oldAsk, oldAccounts, oldExec := askMenu, menuAccounts, menuExecute
	t.Cleanup(func() { askMenu, menuAccounts, menuExecute = oldAsk, oldAccounts, oldExec })
	menuExecute = func(ioStreams, ...string) int { t.Fatal("cancelled or stale action executed"); return 0 }
	for _, cancelled := range []bool{true, false} {
		reads := 0
		menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) {
			reads++
			email := "first@example.com"
			if reads > 1 {
				email = "replacement@example.com"
			}
			return []reporting.AccountSnapshot{{Number: "1", Email: email}}, nil
		}
		askMenu = func(ioStreams, string, []string, string) (int, string, bool) { return 0, "", !cancelled }
		var stderr bytes.Buffer
		code, handled := promptMissingArguments([]string{"remove"}, ioStreams{err: &stderr})
		want := 1
		if cancelled {
			want = 130
		}
		if !handled || code != want {
			t.Fatalf("cancelled=%v handled=%v code=%d", cancelled, handled, code)
		}
	}
}

func TestMissingArgumentsHumanGate(t *testing.T) {
	cleanHome(t)
	for _, key := range []string{"CODEX_THREAD_ID", "CLAUDECODE", "TYCSWAP_NO_PROMPT", "TERM"} {
		t.Setenv(key, "")
	}
	oldAsk := askMenu
	t.Cleanup(func() { askMenu = oldAsk })
	for _, tc := range []struct {
		key, value string
		in, out    bool
		want       int
	}{
		{"", "", true, true, 130}, {"", "", false, true, 2}, {"", "", true, false, 2},
		{"CODEX_THREAD_ID", "thread", true, true, 2}, {"CLAUDECODE", "1", true, true, 2},
		{"TYCSWAP_NO_PROMPT", "1", true, true, 2}, {"TERM", "dumb", true, true, 2},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			}
			called := false
			askMenu = func(ioStreams, string, []string, string) (int, string, bool) { called = true; return 0, "", false }
			code, _, _ := runCLI(t, []string{"config", "set", "autoswitch.fiveHourThreshold"}, tc.in, tc.out)
			if code != tc.want || called != (tc.want == 130) {
				t.Fatalf("code=%d called=%v", code, called)
			}
		})
	}
}
