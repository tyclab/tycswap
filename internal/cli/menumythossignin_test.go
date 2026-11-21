package cli

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/reporting"
)

// An account's action list names the Mythos sign-in in words, like the Fable one beside it.
func TestMenuAccountNamesMythosSignInInWords(t *testing.T) {
	oldFable, oldExec, oldAccounts := menuFableStore, menuExecute, menuAccounts
	t.Cleanup(func() { menuFableStore, menuExecute, menuAccounts = oldFable, oldExec, oldAccounts })
	menuFableStore = func() bool { return true }
	for _, tc := range []struct {
		name  string
		login *reporting.MythosLogin
		want  string
	}{
		{"no Mythos login yet", nil, "Sign in for Mythos…"},
		{"Mythos login present", &reporting.MythosLogin{Status: reporting.FableSignInAgain}, "Sign in again for Mythos…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := reporting.AccountSnapshot{Number: "1", Email: "a@example.com", Kind: "oauth", Mythos: true, MythosLogin: tc.login}
			menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) {
				return []reporting.AccountSnapshot{account}, nil
			}
			var ran []string
			menuExecute = func(_ ioStreams, args ...string) int { ran = args; return 0 }
			offered := menuScript(t, "claude #1", tc.want)
			menuManage(ioStreams{err: io.Discard})
			choices := offered[account.Email]
			found := false
			for _, choice := range choices {
				if strings.HasPrefix(choice, "add ") {
					t.Errorf("account action shown as a command: %q", choice)
				}
				found = found || choice == tc.want
			}
			if !found {
				t.Errorf("account actions %q lack %q", choices, tc.want)
			}
			if want := []string{"add", "--login", "--lineage", "mythos", "--slot", "1"}; !reflect.DeepEqual(ran, want) {
				t.Errorf("selecting it ran %q, want %q", ran, want)
			}
		})
	}
}
