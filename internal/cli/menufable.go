package cli

import (
	"fmt"

	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/termsafe"
)

// fableSignInEntry is Add account's Fable sign-in (DESIGN A107).
const fableSignInEntry = "add --login --lineage fable  Sign in for Fable"

// menuFableStore reports whether this platform can hold a Fable store: on
// macOS the menu offers no Fable entry (DESIGN A100).
var menuFableStore = func() bool { return platform.Detect() != platform.MacOS }

// menuFableRows reads `tycswap fable --list`'s rows for the Fable sign-in's
// account question; tests replace it.
var menuFableRows = func(s ioStreams) ([]fableRow, error) {
	sw, err := constructSwitcher(false, s.err)
	if err != nil {
		return nil, err
	}
	return fableRows(sw)
}

func fableSignInArgs(num string) []string {
	return []string{"add", "--login", "--lineage", "fable", "--slot", num}
}

// menuFableSignIn asks for the account to sign in for Fable: those that run
// Fable and hold no Fable login first, then those that hold one, each in
// roster order.
func menuFableSignIn(s ioStreams) {
	rows, err := menuFableRows(s)
	if err != nil {
		fmt.Fprintln(s.err, err)
		return
	}
	var fresh, again []fableRow
	for _, r := range rows {
		switch {
		case !r.Answer.Capable:
		case r.Login:
			again = append(again, r)
		default:
			fresh = append(fresh, r)
		}
	}
	accounts := append(fresh, again...)
	if len(accounts) == 0 {
		fmt.Fprintln(s.err, "No account runs Fable yet. If one does, choose Allow Fable in its menu (tycswap fable --allow N).")
		return
	}
	labels := make([]string, len(accounts))
	for i, r := range accounts {
		labels[i] = termsafe.Strip("#" + r.Number + "  " + r.Email)
		if r.Login {
			labels[i] += " · sign in again"
		}
	}
	i, _, ok := askMenu(s, "Sign in for Fable as which account?", labels, "")
	if !ok {
		return
	}
	menuExecute(s, fableSignInArgs(accounts[i].Number)...)
}

// fableAccountEntries are an account menu's Fable sign-in and its Allow or
// Disallow Fable; none for Codex, an API key, or where no Fable store can be.
func fableAccountEntries(a reporting.AccountSnapshot) (signIn, mark string) {
	if a.ProviderName() != reporting.ProviderClaude || a.Kind == "api_key" || !menuFableStore() {
		return "", ""
	}
	signIn, mark = "Sign in for Fable…", "Allow Fable"
	if a.FableLogin != nil {
		signIn = "Sign in again for Fable…"
	}
	if a.FableMark {
		mark = "Disallow Fable"
	}
	return signIn, mark
}
