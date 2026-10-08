package core

import "github.com/tyclab/tycswap/internal/lifecycle"

func (sw *Switcher) AddAccount(slot *int, assumeYes bool, alias *string) error {
	return lifecycle.AddAccount(sw.Store, slot, assumeYes, alias)
}

func (sw *Switcher) AddAccountFromToken(token string, email, slotArg *string, assumeYes bool) error {
	return lifecycle.AddAccountFromToken(sw.Store, token, email, slotArg, assumeYes)
}

// AddAccountFromTokenWithBaseURL delegates to
// lifecycle.AddAccountFromTokenWithBaseURL (DESIGN A46). It is not on the
// frozen tui.Facade: the TUI and the dashboard reach it as an optional
// method, so the pinned shape of AddAccountFromToken stays as it is.
func (sw *Switcher) AddAccountFromTokenWithBaseURL(token, baseURL string, email, slotArg *string, assumeYes bool) error {
	return lifecycle.AddAccountFromTokenWithBaseURL(sw.Store, token, baseURL, email, slotArg, assumeYes)
}

func (sw *Switcher) RemoveAccount(id string, assumeYes bool) error {
	return lifecycle.RemoveAccount(sw.Store, id, assumeYes)
}

func (sw *Switcher) MoveAccount(account, target string) (srcNum, tgtNum string, swapped bool, err error) {
	return lifecycle.MoveAccount(sw.Store, account, target)
}

func (sw *Switcher) SwapAccounts(first, second string) (numA, numB string, err error) {
	return lifecycle.SwapAccounts(sw.Store, first, second)
}

func (sw *Switcher) SetAlias(id, alias string) (num, normalized string, err error) {
	return lifecycle.SetAlias(sw.Store, id, alias)
}

func (sw *Switcher) UnsetAlias(id string) (num string, err error) {
	return lifecycle.UnsetAlias(sw.Store, id)
}

func (sw *Switcher) ListAliases() ([]lifecycle.AliasRow, error) {
	return lifecycle.ListAliases(sw.Store)
}

// SetAccountDisabled delegates to lifecycle.SetAccountDisabled (spec 01§8.4).
// The frozen tui.Facade (§2.20) pins exactly this method.
func (sw *Switcher) SetAccountDisabled(id string, disabled bool) error {
	return lifecycle.SetAccountDisabled(sw.Store, id, disabled)
}

func (sw *Switcher) Purge() error {
	return lifecycle.Purge(sw.Store)
}

// AddAccountFromLogin stores a scratch-profile login without touching the live login or the recorded active account.
func (sw *Switcher) AddAccountFromLogin(src lifecycle.AddSource, slot *int, assumeYes bool, alias *string) (string, error) {
	return lifecycle.AddAccountFrom(sw.Store, src, slot, assumeYes, alias)
}
