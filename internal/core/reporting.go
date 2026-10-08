package core

import (
	"fmt"
	"os"
	"strings"

	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

func (sw *Switcher) ListAccounts(showTokenStatus, jsonOut bool, fetch map[string]bool) (any, error) {
	return reporting.ListAccounts(sw.Store, showTokenStatus, jsonOut, fetch)
}

func (sw *Switcher) Status(jsonOut bool) (any, error) {
	return reporting.Status(sw.Store, jsonOut)
}

// AccountsSnapshot delegates to reporting.Snapshot (spec 02§13, DESIGN A11).
// The frozen tui.Facade (§2.20) pins exactly this method.
func (sw *Switcher) AccountsSnapshot(fetch map[string]bool) *reporting.AccountsSnapshot {
	return reporting.Snapshot(sw.Store, fetch)
}

// UsageFetchStamps delegates to reporting.UsageFetchStamps: the pure
// usage-fetch-stamp read the TUI watch view diffs to flash refreshed rows
// (spec 02§13 usage_fetch_stamps). Not pinned by any frozen interface; exposed
// for symmetry with AccountsSnapshot.
func (sw *Switcher) UsageFetchStamps() map[string]*float64 {
	return reporting.UsageFetchStamps(sw.Store)
}

func (sw *Switcher) UsageByAccount() map[string]any {
	return reporting.UsageByAccount(sw.Store)
}

// UsageEntriesByAccount delegates to reporting.UsageEntriesByAccount (spec
// 02§13). The frozen autoswitch.Switcher (§2.18) pins exactly this method.
func (sw *Switcher) UsageEntriesByAccount(fetch map[string]bool) map[string]usage.UsageEntry {
	return reporting.UsageEntriesByAccount(sw.Store, fetch)
}

func (sw *Switcher) SetPollPolicyInputs(threshold float64, models []string) {
	if reporting.SetScopedPollInputs(sw.BackupDir(), sw.ScopeRoot(), threshold, models) {
		reporting.ReplanCachedUsage(sw.Store)
	}
}

// ClearPollPolicyInputs delegates to reporting.ClearPollPolicyInputs (spec
// 02§13 clear_poll_policy_inputs). Pinned by both frozen interfaces, same as
// SetPollPolicyInputs above.
func (sw *Switcher) ClearPollPolicyInputs() {
	reporting.ClearScopedPollInputs(sw.BackupDir(), sw.ScopeRoot())
}

func firstRunSetup(s *store.Store) error {
	email, _, ok := s.GetCurrentAccount()
	if !ok {
		fmt.Fprintln(os.Stdout, printer.Dimmed("No active Claude account found. Please log in first."))
		return nil
	}
	answer, gotInput := lifecycle.ActivePrompter.Prompt(fmt.Sprintf(
		"No managed accounts found. Add current account (%s) to managed list? [Y/n] ", email))
	if !gotInput {
		fmt.Fprintln(os.Stdout, printer.Dimmed("Cancelled"))
		return nil
	}
	if strings.ToLower(strings.TrimSpace(answer)) == "n" {
		fmt.Fprintln(os.Stdout, printer.Dimmed("Setup cancelled. You can run 'tycswap --add-account' later."))
		return nil
	}
	return lifecycle.AddAccount(s, nil, false, nil)
}
