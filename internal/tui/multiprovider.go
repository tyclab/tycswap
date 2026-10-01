// multiprovider.go — Codex rows on the dashboard: the multi-provider snapshot
// source, the owners map, row keys, the provider badge, and the Codex action
// router.
//
// Port of claude-swap PR #252 tui/app.py (MultiSnapshotSource use, the owners
// map, _resolve_row, _row_for, do_switch / do_toggle_disabled / confirm_remove
// by key), tui/dashboard.py (_row_action_id) and tui/widgets.py
// (provider_badge, AccountItem.key_id). The risk all of it exists for: slot
// numbers repeat across providers, so a keystroke aimed at "account 1" must
// never land on the other CLI's account 1. Every row is therefore identified by
// rowID — the bare number for a Claude row, exactly as before, and the
// composite key ("codex:1") for any other provider — and every action resolves
// that id back to the provider that owns the row.
//
// The frozen Facade (A13) is untouched: the multi-provider path is additive.
// WithProviders swaps the refresh source for a ProviderSource (satisfied by
// *providers.MultiSnapshotSource) and names a CodexActions router (satisfied by
// *switcher.Switcher). Without it the Model reads the Facade exactly as it
// always has, so a Claude-only install renders byte-for-byte as before; with
// it, Claude rows still keep their bare-number ids and carry no badge, and
// only the other provider's rows differ. Dropping a provider whose snapshot
// fails or panics is the providers package's job, not this one's.
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/providers"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/termsafe"
)

// ProviderSource takes one merged, provider-major snapshot pass plus the owners
// map from row key ("claude:3", "codex:1") to the provider that produced the
// row. full and storeOnly carry the same meaning as snapshotSource.take.
type ProviderSource interface {
	Take(full, storeOnly bool) (reporting.AccountsSnapshot, map[string]providers.Provider)
}

// CodexActions is the Codex account surface the dashboard drives. Add stays
// Claude-only, as in PR #252.
type CodexActions interface {
	SwitchTo(ctx context.Context, identifier string) (switcher.SwitchResult, error)
	SetAccountDisabled(identifier string, disabled bool) (number string, err error)
	Remove(identifier string, assumeYes bool) (removed bool, err error)
}

var (
	_ ProviderSource = (*providers.MultiSnapshotSource)(nil)
	_ CodexActions   = (*switcher.Switcher)(nil)
)

// rowOwners is the owners map a ProviderSource pass returns.
type rowOwners = map[string]providers.Provider

// providerWiring is the Model's multi-provider state; the zero value means
// Claude-only (the Facade is the whole world).
type providerWiring struct {
	src    ProviderSource
	codex  CodexActions
	ctx    context.Context
	owners rowOwners
}

// adopt keeps the owners map of the latest pass (app.py _apply_snapshot: a pass
// that carries no map leaves the previous one in place).
func (w *providerWiring) adopt(owners rowOwners) {
	if owners != nil {
		w.owners = owners
	}
}

// WithProviders feeds the dashboard from a multi-provider source and routes
// non-Claude rows' actions to codex. A nil src leaves the Model Claude-only.
func WithProviders(ctx context.Context, src ProviderSource, codex CodexActions) Option {
	return func(m *Model) {
		if src == nil {
			return
		}
		if ctx == nil {
			ctx = context.Background()
		}
		m.multi = providerWiring{src: src, codex: codex, ctx: ctx}
	}
}

// multiRefreshCmd is refreshCmd over the ProviderSource, or nil when none is
// wired. The fetch rule is the source's own: storeOnly → no network, else every
// stale row eligible.
func (m *Model) multiRefreshCmd(full, storeOnly bool) tea.Cmd {
	src := m.multi.src
	if src == nil {
		return nil
	}
	return func() tea.Msg {
		snap, owners := src.Take(full, storeOnly)
		return refreshDoneMsg{snap: &snap, owners: owners}
	}
}

// rowID is a row's action id (dashboard.py _row_action_id): Claude rows keep
// the historical bare number, so a Claude-only install and every test written
// against it are untouched; other providers use the composite key.
func rowID(acc reporting.AccountSnapshot) string {
	if acc.ProviderName() == reporting.ProviderClaude {
		return acc.Number
	}
	return acc.Key()
}

// splitRowID reads a row id back into (provider, slot number); a bare number is
// Claude (app.py _resolve_row).
func splitRowID(id string) (provider, number string) {
	if p, n, ok := strings.Cut(id, ":"); ok {
		return p, n
	}
	return reporting.ProviderClaude, id
}

// providerBadge is the short provider tag a non-Claude row carries after its org
// tag (widgets.py provider_badge), or "" for Claude: a Claude-only install must
// not gain a badge on every line to say the only thing it could possibly say.
func providerBadge(acc reporting.AccountSnapshot) string {
	if acc.ProviderName() == reporting.ProviderClaude {
		return ""
	}
	return " ⟨" + providers.ProviderLabel(acc.ProviderName()) + "⟩"
}

// rowTarget is a resolved row: the owning provider's id, the slot number, and
// the Codex router when that provider is Codex (nil → the Claude Facade).
type rowTarget struct {
	provider string
	number   string
	codex    CodexActions
}

// resolveRow maps a row id to the provider that owns it (app.py _resolve_row).
// The owners map is authoritative; a key it does not hold falls back on the
// key's provider prefix, and a provider that has since gone away falls back on
// the Claude Facade — better a no-op on the default provider than a panic in
// the event loop.
func (m *Model) resolveRow(id string) rowTarget {
	pid, number := splitRowID(id)
	if owner := m.multi.owners[pid+":"+number]; owner != nil {
		if sw := providers.Switcher(owner); sw != nil {
			return rowTarget{provider: reporting.ProviderCodex, number: number, codex: sw}
		}
		pid = owner.ID()
	}
	if pid == reporting.ProviderCodex && m.multi.codex != nil {
		return rowTarget{provider: reporting.ProviderCodex, number: number, codex: m.multi.codex}
	}
	return rowTarget{provider: reporting.ProviderClaude, number: number}
}

// accountByID is the snapshot row for a row id (app.py _row_for), or nil.
func (m *Model) accountByID(id string) *reporting.AccountSnapshot { return m.accountByNumber(id) }

// rowNoun names a row id in a sentence: "Account 3" for Claude (unchanged), "Codex
// account 1" for Codex.
func rowNoun(id string) string {
	pid, number := splitRowID(id)
	if pid == reporting.ProviderClaude {
		return "Account " + number
	}
	return providerTitle(pid) + " account " + number
}

// providerTitle is the capitalized provider name the Codex CLI verbs print.
func providerTitle(pid string) string {
	if pid == reporting.ProviderCodex {
		return "Codex"
	}
	return providers.ProviderLabel(pid)
}

// runMessageAction projects a non-Facade action's (message, warning, error)
// into an actionResult: the message is the success toast, the warning a second
// toast at warning severity.
func runMessageAction(fn func() (msg, warn string, err error)) actionResult {
	msg, warn, err := fn()
	if err != nil {
		return actionResult{OK: false, Message: "Error: " + err.Error()}
	}
	return actionResult{OK: true, Message: msg, Warning: warn}
}

// startMessageAction is startAction for a provider that returns its own result
// rather than a Facade payload; same single-flight gate.
func (m *Model) startMessageAction(label string, fn func() (msg, warn string, err error)) tea.Cmd {
	if m.busy {
		return m.notify("Another action is still running", "", "warning")
	}
	m.busy = true
	return func() tea.Msg {
		return actionDoneMsg{label: label, result: runMessageAction(fn)}
	}
}

// codexSwitch switches a Codex row (app.py do_switch for a non-Claude
// provider). The toast names the resolved slot, and running codex sessions are
// reported exactly as `tycswap codex switch` reports them: they keep the old
// account until restarted.
func (m *Model) codexSwitch(t rowTarget) tea.Cmd {
	ctx, codex, number := m.multi.ctx, t.codex, t.number
	if ctx == nil {
		ctx = context.Background()
	}
	return m.startMessageAction("Switch to codex account "+number, func() (string, string, error) {
		res, err := codex.SwitchTo(ctx, number)
		if err != nil {
			return "", "", err
		}
		if res.AlreadyActive {
			return fmt.Sprintf("Codex account %s is already active: %s", res.Number, res.Email), "", nil
		}
		msg := fmt.Sprintf("Switched to Codex account %s: %s", res.Number, res.Email)
		return msg, runningPIDsWarning(res.RunningPIDs), nil
	})
}

// runningPIDsWarning is the Codex CLI's restart warning, or "" when no codex
// session is running.
func runningPIDsWarning(pids []int) string {
	if len(pids) == 0 {
		return ""
	}
	parts := make([]string, len(pids))
	for i, p := range pids {
		parts[i] = fmt.Sprint(p)
	}
	return "codex is running (pid " + strings.Join(parts, ", ") + ") — restart it for the new account to take effect."
}

// codexToggleDisabled holds a Codex row out of rotation or returns it
// (app.py do_toggle_disabled); target was read from that row's own state.
func (m *Model) codexToggleDisabled(t rowTarget, target bool) tea.Cmd {
	codex, number := t.codex, t.number
	verb, done := "Enable", "Enabled"
	if target {
		verb, done = "Disable", "Disabled"
	}
	return m.startMessageAction(fmt.Sprintf("%s codex account %s", verb, number), func() (string, string, error) {
		resolved, err := codex.SetAccountDisabled(number, target)
		if err != nil {
			return "", "", err
		}
		return fmt.Sprintf("%s Codex account %s", done, resolved), "", nil
	})
}

// codexConfirmRemove is confirmRemove for a Codex row (app.py confirm_remove /
// _on_remove_confirm by key), with the same identity re-check the Claude path
// makes before it fires.
func (m *Model) codexConfirmRemove(t rowTarget, id string, acc reporting.AccountSnapshot) tea.Cmd {
	codex, number, email, orgUUID := t.codex, t.number, acc.Email, acc.OrgUUID
	return m.pushScreen(&confirmModal{
		title:    "Remove account",
		yesLabel: "Remove",
		focusYes: true,
		message: fmt.Sprintf("Remove codex account %s (%s)?\n\nIts stored credentials and config backup are deleted.",
			number, termsafe.Strip(email)),
		onDone: func(m *Model, confirmed bool) tea.Cmd {
			if !confirmed {
				return nil
			}
			live := m.accountByID(id)
			switch {
			case live == nil:
				return m.vanishedToast(id)
			case live.Email != email || live.OrgUUID != orgUUID:
				return m.notify(fmt.Sprintf("%s is now %s — nothing removed", rowNoun(id), live.Email),
					"Remove account", "warning")
			}
			return m.startMessageAction("Remove codex account "+number, func() (string, string, error) {
				removed, err := codex.Remove(number, true)
				if err != nil {
					return "", "", err
				}
				if !removed {
					return "Codex account " + number + " was not removed", "", nil
				}
				return "Removed Codex account " + number, "", nil
			})
		},
	})
}
