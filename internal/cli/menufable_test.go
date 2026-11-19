// Tests for the Fable entries of the terminal menu (DESIGN A107): the Fable
// sign-in under Add account, and in an account's own menu the sign-in and
// Allow / Disallow Fable; none of them where no Fable store can exist.
package cli

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

// fableMenuPlatform sets whether the platform can hold a Fable store.
func fableMenuPlatform(t *testing.T, supported bool) {
	t.Helper()
	old := menuFableStore
	t.Cleanup(func() { menuFableStore = old })
	menuFableStore = func() bool { return supported }
}

// fableMenuRows serves rows to the Add account entry's account question.
func fableMenuRows(t *testing.T, rows ...fableRow) *int {
	t.Helper()
	old := menuFableRows
	t.Cleanup(func() { menuFableRows = old })
	calls := 0
	menuFableRows = func(ioStreams) ([]fableRow, error) { calls++; return rows, nil }
	return &calls
}

// menuScript answers each prompt by the label it names ("" cancels) and
// records every prompt's choices by title.
func menuScript(t *testing.T, picks ...string) map[string][]string {
	t.Helper()
	old := askMenu
	t.Cleanup(func() { askMenu = old })
	offered := map[string][]string{}
	askMenu = func(_ ioStreams, title string, choices []string, _ string) (int, string, bool) {
		offered[title] = choices
		if len(picks) == 0 {
			return 0, "", false
		}
		pick := picks[0]
		picks = picks[1:]
		for i, c := range choices {
			if pick != "" && strings.HasPrefix(c, pick) {
				return i, "", true
			}
		}
		return 0, "", false
	}
	return offered
}

func capable(num, email string, login bool) fableRow {
	return fableRow{Number: num, Email: email, Answer: store.FableAnswer{Capable: true, By: store.FableByPlan, Plan: "max"}, Login: login}
}

func TestMenuAddFableSignInAsksForTheAccount(t *testing.T) {
	fableMenuPlatform(t, true)
	fableMenuRows(t,
		capable("1", "a@example.com", true),
		capable("2", "b@example.com", false),
		fableRow{Number: "3", Email: "c@example.com", Answer: store.FableAnswer{By: store.FableUnknown}},
		fableRow{Number: "4", Email: "d@example.com", Answer: store.FableAnswer{Capable: true, By: store.FableByMark}},
		capable("5", "e@example.com", true))
	commands := workflowCommands(t)
	offered := menuScript(t, "add --login --lineage fable", "#5")
	menuAdd(ioStreams{err: io.Discard})
	add := offered["Add account"]
	if len(add) != 6 || add[5] != "add --login --lineage fable  Sign in for Fable" {
		t.Fatalf("Add account choices = %q", add)
	}
	want := []string{"#2  b@example.com", "#4  d@example.com", "#1  a@example.com · sign in again", "#5  e@example.com · sign in again"}
	if got := offered["Sign in for Fable as which account?"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("accounts = %q, want %q", got, want)
	}
	if want := [][]string{{"add", "--login", "--lineage", "fable", "--slot", "5"}}; !reflect.DeepEqual(*commands, want) {
		t.Fatalf("commands = %v", *commands)
	}
}

func TestMenuAddFableSignInWithoutAFableAccount(t *testing.T) {
	fableMenuPlatform(t, true)
	fableMenuRows(t, fableRow{Number: "3", Email: "c@example.com", Answer: store.FableAnswer{By: store.FableUnknown}})
	commands := workflowCommands(t)
	offered := menuScript(t, "add --login --lineage fable", "#3")
	var errb strings.Builder
	menuAdd(ioStreams{err: &errb})
	if len(*commands) != 0 || offered["Sign in for Fable as which account?"] != nil {
		t.Fatalf("commands = %v, offered %q", *commands, offered)
	}
	if got := errb.String(); got != "No account runs Fable yet. If one does, choose Allow Fable in its menu (tycswap fable --allow N).\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestMenuAddHasNoFableSignInWithoutAFableStore(t *testing.T) {
	fableMenuPlatform(t, false)
	calls := fableMenuRows(t)
	workflowCommands(t)
	offered := menuScript(t)
	menuAdd(ioStreams{err: io.Discard})
	if add := offered["Add account"]; len(add) != 5 || strings.Contains(strings.Join(add, "|"), "Fable") || *calls != 0 {
		t.Fatalf("Add account choices = %q, rows read %d times", add, *calls)
	}
}

// Without an answer (the store unreadable) the menu keeps the sign-in and
// offers no toggle.
func TestMenuManageWithoutAFableAnswerOffersNoToggle(t *testing.T) {
	fableMenuPlatform(t, true)
	manageAccount(t, reporting.AccountSnapshot{Number: "3", Email: "c@example.com", Kind: "oauth"})
	workflowCommands(t)
	offered := menuScript(t, "claude")
	menuManage(ioStreams{err: io.Discard})
	choices := offered["c@example.com"]
	if last := choices[len(choices)-1]; last != "Sign in for Fable…" || strings.Contains(strings.Join(choices, "|"), "Allow Fable") {
		t.Fatalf("choices = %q", choices)
	}
}

func manageAccount(t *testing.T, a reporting.AccountSnapshot) {
	t.Helper()
	old := menuAccounts
	t.Cleanup(func() { menuAccounts = old })
	menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) { return []reporting.AccountSnapshot{a}, nil }
}

// fableMenuAnswers serves the account menu's Fable answers by account and
// records which accounts were asked.
func fableMenuAnswers(t *testing.T, by map[string]string) *[]string {
	t.Helper()
	old := menuFableAnswer
	t.Cleanup(func() { menuFableAnswer = old })
	var asked []string
	menuFableAnswer = func(_ ioStreams, num string) (store.FableAnswer, error) {
		asked = append(asked, num)
		return store.FableAnswer{By: by[num]}, nil
	}
	return &asked
}

func TestMenuManageFableEntries(t *testing.T) {
	plain := reporting.AccountSnapshot{Number: "3", Email: "c@example.com", Kind: "oauth"}
	marked := reporting.AccountSnapshot{Number: "4", Email: "d@example.com", Kind: "oauth",
		FableLogin: &reporting.FableLogin{Status: reporting.FableLoginOK}}
	byPlan := reporting.AccountSnapshot{Number: "7", Email: "g@example.com", Kind: "oauth"}
	answers := map[string]string{"3": store.FableUnknown, "4": store.FableByMark, "7": store.FableByPlan}
	for _, tc := range []struct {
		name      string
		account   reporting.AccountSnapshot
		supported bool
		picks     []string
		entries   []string
		want      [][]string
	}{
		{"allow", plain, true, []string{"Allow Fable", "Continue"},
			[]string{"Sign in for Fable…", "Allow Fable"}, [][]string{{"fable", "--allow", "3"}}},
		{"allow cancelled", plain, true, []string{"Allow Fable", "Cancel"},
			[]string{"Sign in for Fable…", "Allow Fable"}, nil},
		{"sign in", plain, true, []string{"Sign in for Fable"},
			[]string{"Sign in for Fable…", "Allow Fable"}, [][]string{{"add", "--login", "--lineage", "fable", "--slot", "3"}}},
		{"disallow", marked, true, []string{"Disallow Fable", "Continue"},
			[]string{"Sign in again for Fable…", "Disallow Fable"}, [][]string{{"fable", "--disallow", "4"}}},
		{"sign in again", marked, true, []string{"Sign in again for Fable"},
			[]string{"Sign in again for Fable…", "Disallow Fable"}, [][]string{{"add", "--login", "--lineage", "fable", "--slot", "4"}}},
		{"a plan that decides", byPlan, true, []string{"Sign in for Fable"},
			[]string{"Sign in for Fable…"}, [][]string{{"add", "--login", "--lineage", "fable", "--slot", "7"}}},
		{"no Fable store", plain, false, nil, nil, nil},
		{"an API key", reporting.AccountSnapshot{Number: "5", Email: "k@example.com", Kind: "api_key"}, true, nil, nil, nil},
		{"Codex", reporting.AccountSnapshot{Number: "6", Email: "x@example.com", Provider: reporting.ProviderCodex}, true, nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fableMenuPlatform(t, tc.supported)
			manageAccount(t, tc.account)
			asked := fableMenuAnswers(t, answers)
			commands := workflowCommands(t)
			offered := menuScript(t, append([]string{tc.account.ProviderName()}, tc.picks...)...)
			menuManage(ioStreams{err: io.Discard})
			choices := offered[tc.account.Email]
			var fable []string
			for _, c := range choices {
				if strings.Contains(c, "Fable") {
					fable = append(fable, c)
				}
			}
			if !reflect.DeepEqual(fable, tc.entries) {
				t.Fatalf("Fable entries = %q, want %q (all: %q)", fable, tc.entries, choices)
			}
			if len(tc.entries) > 0 && choices[len(choices)-len(tc.entries)] != tc.entries[0] {
				t.Errorf("the Fable entries are not last: %q", choices)
			}
			if len(*commands) != len(tc.want) || len(tc.want) > 0 && !reflect.DeepEqual(*commands, tc.want) {
				t.Fatalf("commands = %v, want %v", *commands, tc.want)
			}
			if wantAsked := len(tc.entries) > 0; len(*asked) != map[bool]int{true: 1}[wantAsked] || wantAsked && (*asked)[0] != tc.account.Number {
				t.Errorf("Fable answers read for %v", *asked)
			}
		})
	}
}
