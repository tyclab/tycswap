package cli

import (
	"fmt"

	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
)

// The Fable entries of the menu (DESIGN A107).
const (
	fableSignInEntry = "add --login --lineage fable  Sign in for Fable"
	fableAllow       = "Allow Fable"
	fableDisallow    = "Disallow Fable"
)

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

// menuFableAnswer reads what decides whether one account runs Fable, for its
// own menu only; tests replace it.
var menuFableAnswer = func(s ioStreams, num string) (store.FableAnswer, error) {
	sw, err := constructSwitcher(false, s.err)
	if err != nil {
		return store.FableAnswer{}, err
	}
	data, err := sw.ReadSequence()
	if err != nil {
		return store.FableAnswer{}, err
	}
	return sw.Store.FableEntitlement(data, num), nil
}

func fableSignInArgs(num string) []string {
	return []string{"add", "--login", "--lineage", "fable", "--slot", num}
}

// menuFableSignIn asks for the account to sign in for Fable: those that run
// Fable and hold no Fable login first, then those that hold one, each in
// roster order; with two or more that fable --login asks, all of them first.
func menuFableSignIn(s ioStreams) {
	rows, err := menuFableRows(s)
	if err != nil {
		fmt.Fprintln(s.err, err)
		return
	}
	var fresh, again []fableRow
	missing := 0
	for _, r := range rows {
		if r.Missing {
			missing++
		}
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
	var labels []string
	if missing > 1 {
		labels = append(labels, fmt.Sprintf("All %d accounts without a Fable login, one after another", missing))
	}
	walk := len(labels)
	for _, r := range accounts {
		label := termsafe.Strip("#" + r.Number + "  " + r.Email)
		if r.Login {
			label += " · sign in again"
		}
		labels = append(labels, label)
	}
	i, _, ok := askMenu(s, "Sign in for Fable as which account?", labels, "")
	switch {
	case !ok:
	case i < walk:
		menuExecute(s, "fable", "--login")
	default:
		menuExecute(s, fableSignInArgs(accounts[i-walk].Number)...)
	}
}

// fableAccountEntries are an account menu's Fable sign-in and, where it
// changes the answer, Allow Fable (nothing decides yet) or Disallow Fable (a
// mark decides); none for Codex, an API key, or where no Fable store can be.
// Only then is the account's answer read, and without it there is no toggle.
func fableAccountEntries(s ioStreams, a reporting.AccountSnapshot) (signIn, mark string) {
	if a.ProviderName() != reporting.ProviderClaude || a.Kind == "api_key" || !menuFableStore() {
		return "", ""
	}
	signIn = "Sign in for Fable…"
	if a.FableLogin != nil {
		signIn = "Sign in again for Fable…"
	}
	answer, err := menuFableAnswer(s, a.Number)
	switch {
	case err != nil:
	case answer.By == store.FableUnknown:
		mark = fableAllow
	case answer.By == store.FableByMark:
		mark = fableDisallow
	}
	return signIn, mark
}
