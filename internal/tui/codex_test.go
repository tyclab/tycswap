// codex_test.go — the TUI with two providers on screen.
//
// Port of claude-swap PR #252 tests/test_tui_multiprovider.py (the TUI half;
// the menu-bar tests belong to a surface tycswap does not have). The risk this
// file exists for: slot numbers repeat across providers, so a keystroke aimed
// at "account 1" can land on the wrong CLI's account. Every test here is
// ultimately about that.
package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/providers"
	"github.com/tyclab/tycswap/internal/reporting"
)

// fakeCodex records every Codex action the dashboard routes to it.
type fakeCodex struct {
	switched []string
	disabled []disableCall
	removed  []string
	pids     []int
	err      error
}

func (c *fakeCodex) SwitchTo(_ context.Context, id string) (switcher.SwitchResult, error) {
	c.switched = append(c.switched, id)
	if c.err != nil {
		return switcher.SwitchResult{}, c.err
	}
	return switcher.SwitchResult{Number: id, Email: "codex-" + id + "@x", RunningPIDs: c.pids}, nil
}

func (c *fakeCodex) SetAccountDisabled(id string, disabled bool) (string, error) {
	c.disabled = append(c.disabled, disableCall{id, disabled})
	return id, c.err
}

func (c *fakeCodex) Remove(id string, assumeYes bool) (bool, error) {
	c.removed = append(c.removed, id)
	return c.err == nil, c.err
}

// stubProvider is an owners-map value; its snapshot is never taken.
type stubProvider struct{ id string }

func (p stubProvider) ID() string { return p.id }
func (p stubProvider) Snapshot(map[string]bool) (*reporting.AccountsSnapshot, error) {
	return nil, errors.New("unused")
}

// stubSource is a ProviderSource returning a fixed merged pass.
type stubSource struct {
	snap   reporting.AccountsSnapshot
	owners map[string]providers.Provider
	calls  [][2]bool
}

func (s *stubSource) Take(full, storeOnly bool) (reporting.AccountsSnapshot, map[string]providers.Provider) {
	s.calls = append(s.calls, [2]bool{full, storeOnly})
	return s.snap, s.owners
}

func provAcct(provider, number string, active bool) reporting.AccountSnapshot {
	a := acct(number, provider+"-"+number+"@x", active, nil)
	a.Provider = provider
	return a
}

// twoProviders is app_with_two_providers: one row per provider in slot 1, the
// Codex one disabled, and an owners map holding both.
func twoProviders(t *testing.T) (*Model, *fakeFacade, *fakeCodex, *stubSource) {
	t.Helper()
	codexRow := provAcct("codex", "1", false)
	codexRow.Disabled = true
	snap := reporting.AccountsSnapshot{
		ActiveNumber: "1",
		Accounts:     []reporting.AccountSnapshot{provAcct("claude", "1", true), codexRow},
		Provider:     "multi",
	}
	src := &stubSource{snap: snap, owners: map[string]providers.Provider{
		"claude:1": stubProvider{"claude"}, "codex:1": stubProvider{"codex"},
	}}
	f := &fakeFacade{backupDir: t.TempDir()}
	codex := &fakeCodex{}
	m := newModel(f, "dashboard", WithProviders(context.Background(), src, codex))
	m.multi.owners = src.owners
	m.snapshot = &snap
	return m, f, codex, src
}

func codexToasts(m *Model) []string {
	var out []string
	for _, t := range m.toasts {
		out = append(out, t.severity+"|"+t.message)
	}
	return out
}

// deliver runs an action cmd and feeds its message back through Update.
func deliver(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	msg, ok := runCmd(cmd).(actionDoneMsg)
	if !ok {
		t.Fatalf("cmd did not produce an actionDoneMsg")
	}
	m.Update(msg)
}

// ---- the row badge ----------------------------------------------------------

func TestAClaudeRowCarriesNoBadge(t *testing.T) {
	if b := providerBadge(provAcct("claude", "1", false)); b != "" {
		t.Errorf("claude badge = %q, want none", b)
	}
	if b := providerBadge(acct("1", "a@x", false, nil)); b != "" {
		t.Errorf("zero-provider badge = %q, want none", b)
	}
}

func TestACodexRowCarriesABadge(t *testing.T) {
	if b := providerBadge(provAcct("codex", "1", false)); b != " ⟨codex⟩" {
		t.Errorf("codex badge = %q", b)
	}
}

func TestTheBadgeAppearsInTheFullCard(t *testing.T) {
	if text := accountCardText(provAcct("codex", "1", false), 80, nil, 0).plain(); !strings.Contains(text, "⟨codex⟩") {
		t.Errorf("card %q lacks the badge", text)
	}
}

func TestTheBadgeAppearsInTheMiniRow(t *testing.T) {
	if text := miniAccountText(provAcct("codex", "2", false), 100, 0).plain(); !strings.Contains(text, "codex") {
		t.Errorf("mini row %q lacks the badge", text)
	}
}

func TestAClaudeCardRendersExactlyAsBefore(t *testing.T) {
	bare, tagged := acct("1", "a@x", true, nil), acct("1", "a@x", true, nil)
	tagged.Provider = reporting.ProviderClaude
	got := accountCardText(tagged, 80, nil, testNow).render()
	if got != accountCardText(bare, 80, nil, testNow).render() || strings.Contains(got, "⟨") {
		t.Errorf("claude card changed: %q", got)
	}
}

func TestAClaudeOnlyPanelRendersExactlyAsBefore(t *testing.T) {
	bare := snapshotOf("1", acct("1", "a@x", true, nil), acct("2", "b@x", false, nil))
	merged := snapshotOf("1", provAcct("claude", "1", true), provAcct("claude", "2", false))
	merged.Accounts[0].Email, merged.Accounts[1].Email = "a@x", "b@x"
	merged.Provider = "multi"
	want := monitorPanelText(bare, 100, true, nil, testNow, true).render()
	if got := monitorPanelText(merged, 100, true, nil, testNow, true).render(); got != want {
		t.Errorf("claude-only panel changed:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(want, "⟨") {
		t.Errorf("claude-only panel carries a badge: %q", want)
	}
}

func TestTheCodexBadgeIsVisibleOnTheDashboard(t *testing.T) {
	m, _, _, _ := twoProviders(t)
	text := monitorPanelText(m.snapshot, 100, true, nil, testNow, true).plain()
	if !strings.Contains(text, "⟨codex⟩") {
		t.Errorf("panel lacks the codex badge:\n%s", text)
	}
}

// ---- row identity -------------------------------------------------------------

func TestTwoProvidersSlotOneAreDifferentRows(t *testing.T) {
	c, x := provAcct("claude", "1", false), provAcct("codex", "1", false)
	if c.Key() == x.Key() || rowID(c) == rowID(x) {
		t.Errorf("claude %q/%q vs codex %q/%q", c.Key(), rowID(c), x.Key(), rowID(x))
	}
}

func TestTheKeyIsStableAndReadable(t *testing.T) {
	if k := provAcct("codex", "2", false).Key(); k != "codex:2" {
		t.Errorf("key = %q", k)
	}
	if id := rowID(provAcct("claude", "2", false)); id != "2" {
		t.Errorf("claude row id = %q, want the bare number", id)
	}
}

// ---- app-level dispatch -------------------------------------------------------

func TestABareNumberStillResolvesToClaude(t *testing.T) {
	m, _, _, _ := twoProviders(t)
	if got := m.resolveRow("1"); got.codex != nil || got.number != "1" || got.provider != "claude" {
		t.Errorf("resolveRow(1) = %+v", got)
	}
}

func TestACodexKeyResolvesToTheCodexProvider(t *testing.T) {
	m, _, codex, _ := twoProviders(t)
	if got := m.resolveRow("codex:1"); got.codex != CodexActions(codex) || got.number != "1" {
		t.Errorf("resolveRow(codex:1) = %+v", got)
	}
}

func TestTheOwnersMapIsAuthoritative(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", dir+"/.codex")
	t.Setenv("XDG_DATA_HOME", dir+"/data")
	m, _, _, _ := twoProviders(t)
	real := switcher.New(switcher.Options{})
	m.multi.owners["codex:1"] = providers.Codex(context.Background(), real)
	if got := m.resolveRow("codex:1"); got.codex != CodexActions(real) {
		t.Errorf("resolveRow ignored the owner: %+v", got)
	}
}

func TestAnUnknownProviderFallsBackToClaudeInsteadOfPanicking(t *testing.T) {
	m, _, _, _ := twoProviders(t)
	if got := m.resolveRow("gemini:3"); got.codex != nil || got.number != "3" {
		t.Errorf("resolveRow(gemini:3) = %+v", got)
	}
}

func TestSwitchingACodexRowDoesNotTouchClaude(t *testing.T) {
	m, f, codex, _ := twoProviders(t)
	codex.pids = []int{101, 202}
	deliver(t, m, m.doSwitch("codex:1"))
	if !reflect.DeepEqual(codex.switched, []string{"1"}) || len(f.switchToCalls) != 0 {
		t.Fatalf("codex %v claude %v", codex.switched, f.switchToCalls)
	}
	want := []string{
		"|Switched to Codex account 1: codex-1@x",
		"warning|codex is running (pid 101, 202) — restart it for the new account to take effect.",
	}
	if got := codexToasts(m); !reflect.DeepEqual(got, want) {
		t.Errorf("toasts = %q, want %q", got, want)
	}
}

func TestACodexSwitchWithNoRunningSessionWarnsNothing(t *testing.T) {
	m, _, _, _ := twoProviders(t)
	deliver(t, m, m.doSwitch("codex:1"))
	if got := codexToasts(m); !reflect.DeepEqual(got, []string{"|Switched to Codex account 1: codex-1@x"}) {
		t.Errorf("toasts = %q", got)
	}
}

func TestAFailedCodexSwitchOpensTheFailureModal(t *testing.T) {
	m, _, codex, _ := twoProviders(t)
	codex.err = errors.New("no stored credentials")
	deliver(t, m, m.doSwitch("codex:1"))
	o, ok := m.top().(*outputModal)
	if !ok || o.title != "Switch to codex account 1 — failed" || o.output != "Error: no stored credentials" {
		t.Errorf("top = %#v", m.top())
	}
}

func TestSwitchingAClaudeRowDoesNotTouchCodex(t *testing.T) {
	m, f, codex, _ := twoProviders(t)
	runCmd(m.doSwitch("1"))
	if !reflect.DeepEqual(f.switchToCalls, []string{"1"}) || len(codex.switched) != 0 {
		t.Errorf("claude %v codex %v", f.switchToCalls, codex.switched)
	}
}

func TestTogglingACodexRowReadsThatRowsOwnState(t *testing.T) {
	m, f, codex, _ := twoProviders(t)
	deliver(t, m, m.toggleDisabled("codex:1"))
	if !reflect.DeepEqual(codex.disabled, []disableCall{{"1", false}}) || len(f.disabledCalls) != 0 {
		t.Fatalf("codex %v claude %v", codex.disabled, f.disabledCalls)
	}
	if got := codexToasts(m); !reflect.DeepEqual(got, []string{"|Enabled Codex account 1"}) {
		t.Errorf("toasts = %q", got)
	}
}

func TestRemovingACodexRowRemovesItFromCodex(t *testing.T) {
	m, f, codex, _ := twoProviders(t)
	m.confirmRemove(m.snapshot.Accounts[1])
	modal, ok := m.top().(*confirmModal)
	if !ok {
		t.Fatalf("top = %#v, want the confirm modal", m.top())
	}
	if !strings.HasPrefix(modal.message, "Remove codex account 1 (codex-1@x)?") {
		t.Errorf("modal message = %q", modal.message)
	}
	deliver(t, m, modal.onDone(m, true))
	if !reflect.DeepEqual(codex.removed, []string{"1"}) || len(f.removeCalls) != 0 {
		t.Fatalf("codex %v claude %v", codex.removed, f.removeCalls)
	}
	if got := codexToasts(m); !reflect.DeepEqual(got, []string{"|Removed Codex account 1"}) {
		t.Errorf("toasts = %q", got)
	}
}

func TestADeclinedRemovalRemovesNothing(t *testing.T) {
	m, f, codex, _ := twoProviders(t)
	m.confirmRemove(m.snapshot.Accounts[1])
	if cmd := m.top().(*confirmModal).onDone(m, false); cmd != nil {
		t.Errorf("declined removal returned a cmd")
	}
	if len(codex.removed) != 0 || len(f.removeCalls) != 0 {
		t.Errorf("codex %v claude %v", codex.removed, f.removeCalls)
	}
}

func TestARemovalWhoseCodexRowVanishedSaysSo(t *testing.T) {
	m, _, codex, _ := twoProviders(t)
	m.confirmRemove(m.snapshot.Accounts[1])
	modal := m.top().(*confirmModal)
	m.snapshot = snapshotOf("1", provAcct("claude", "1", true))
	modal.onDone(m, true)
	if len(codex.removed) != 0 {
		t.Errorf("removed %v from a vanished row", codex.removed)
	}
	if got := codexToasts(m); !reflect.DeepEqual(got, []string{"warning|Codex account 1 is no longer managed"}) {
		t.Errorf("toasts = %q", got)
	}
}

func TestRowLookupFindsTheRightProviderRow(t *testing.T) {
	m, _, _, _ := twoProviders(t)
	if a := m.accountByID("codex:1"); a == nil || !a.Disabled {
		t.Errorf("codex:1 = %+v", a)
	}
	if a := m.accountByID("1"); a == nil || a.Disabled || a.ProviderName() != "claude" {
		t.Errorf("1 = %+v", a)
	}
	if a := m.accountByID("claude:1"); a == nil || a.ProviderName() != "claude" {
		t.Errorf("claude:1 = %+v", a)
	}
}

// ---- dashboard menus ----------------------------------------------------------

func TestMenuActionIDsCarryTheRowKey(t *testing.T) {
	m, _, _, _ := twoProviders(t)
	d := m.stack[0].(*dashboardScreen)
	for name, entries := range map[string][]menuEntry{"remove": d.removeEntries(m), "disable": d.disableEntries(m)} {
		ids := []string{entries[0].actionID, entries[1].actionID}
		if want := []string{name + ":1", name + ":codex:1"}; !reflect.DeepEqual(ids, want) {
			t.Errorf("%s ids = %v, want %v", name, ids, want)
		}
		if strings.Contains(entries[0].label, "⟨") || !strings.Contains(entries[1].label, "⟨codex⟩") {
			t.Errorf("%s labels = %q / %q", name, entries[0].label, entries[1].label)
		}
	}
}

func TestTheRemoveMenuRoutesACodexRowToCodex(t *testing.T) {
	m, _, _, _ := twoProviders(t)
	d := m.stack[0].(*dashboardScreen)
	d.pushLiveMenu(m, "remove account", d.removeEntries)
	d.dispatch(m, "remove:codex:1")
	modal, ok := m.top().(*confirmModal)
	if !ok || !strings.HasPrefix(modal.message, "Remove codex account 1 ") {
		t.Errorf("top = %#v", m.top())
	}
}

func TestTheDisableMenuRoutesACodexRowToCodex(t *testing.T) {
	m, f, codex, _ := twoProviders(t)
	d := m.stack[0].(*dashboardScreen)
	d.pushLiveMenu(m, "disable / enable", d.disableEntries)
	runCmd(d.dispatch(m, "disable:codex:1"))
	if !reflect.DeepEqual(codex.disabled, []disableCall{{"1", false}}) || len(f.disabledCalls) != 0 {
		t.Errorf("codex %v claude %v", codex.disabled, f.disabledCalls)
	}
}

// ---- the switch screen ----------------------------------------------------------

func fourRows(t *testing.T) (*Model, *fakeFacade, *fakeCodex) {
	m, f, codex, _ := twoProviders(t)
	m.snapshot = &reporting.AccountsSnapshot{ActiveNumber: "1", Provider: "multi", Accounts: []reporting.AccountSnapshot{
		provAcct("claude", "1", true), provAcct("claude", "2", false),
		provAcct("codex", "1", true), provAcct("codex", "2", false),
	}}
	return m, f, codex
}

func TestBothProvidersRowsReachTheSwitchScreen(t *testing.T) {
	m, _, _ := fourRows(t)
	s := newSwitchScreen()
	m.pushScreen(s)
	s.onSnapshot(m)
	if want := []string{"1", "2", "codex:1", "codex:2"}; !reflect.DeepEqual(s.numbers, want) {
		t.Errorf("row ids = %v, want %v", s.numbers, want)
	}
	if s.index == nil || *s.index != 0 {
		t.Errorf("cursor = %v, want the Claude active row", s.index)
	}
}

func TestSelectingACodexRowSwitchesCodexNotClaude(t *testing.T) {
	m, f, codex := fourRows(t)
	s := newSwitchScreen()
	m.pushScreen(s)
	s.onSnapshot(m)
	idx := 2 // the first codex row
	s.index = &idx
	execAll(s.selectHighlighted(m))
	if !reflect.DeepEqual(codex.switched, []string{"1"}) || len(f.switchToCalls) != 0 {
		t.Errorf("codex %v claude %v", codex.switched, f.switchToCalls)
	}
}

func TestTheActiveCursorFollowsClaudeNotASameNumberedCodexRow(t *testing.T) {
	var a accountListScreen
	snap := &reporting.AccountsSnapshot{ActiveNumber: "1", Accounts: []reporting.AccountSnapshot{
		provAcct("codex", "1", true), provAcct("claude", "1", true),
	}}
	if got := a.activeIndex(snap); got != 1 {
		t.Errorf("activeIndex = %d, want 1", got)
	}
}

// ---- the refresh path -----------------------------------------------------------

func TestTheRefreshPassTakesFromTheProviderSource(t *testing.T) {
	m, f, _, src := twoProviders(t)
	m.snapshot, m.multi.owners = nil, nil
	msg, ok := runCmd(m.refreshCmd(false, true)).(refreshDoneMsg)
	if !ok {
		t.Fatal("refreshCmd produced no refreshDoneMsg")
	}
	m.Update(msg)
	if !reflect.DeepEqual(src.calls, [][2]bool{{false, true}}) || len(f.fetchCalls) != 0 {
		t.Errorf("source calls %v facade calls %v", src.calls, f.fetchCalls)
	}
	if m.snapshot == nil || len(m.snapshot.Accounts) != 2 || m.multi.owners["codex:1"] == nil {
		t.Errorf("snapshot %+v owners %v", m.snapshot, m.multi.owners)
	}
}

func TestAPassWithoutOwnersKeepsThePreviousMap(t *testing.T) {
	m, _, _, _ := twoProviders(t)
	m.Update(refreshDoneMsg{snap: m.snapshot})
	if m.multi.owners["codex:1"] == nil {
		t.Errorf("owners dropped: %v", m.multi.owners)
	}
}

func TestWithoutProvidersTheFacadeStillFeedsTheDashboard(t *testing.T) {
	f := &fakeFacade{backupDir: t.TempDir(), snap: snapshotOf("1", acct("1", "a@x", true, nil))}
	m := newModel(f, "dashboard", WithProviders(context.Background(), nil, nil))
	runCmd(m.refreshCmd(false, true))
	if len(f.fetchCalls) != 1 {
		t.Errorf("facade fetch calls = %v, want one", f.fetchCalls)
	}
	if got := m.resolveRow("codex:1"); got.codex != nil {
		t.Errorf("a Claude-only model resolved a codex row to %+v", got)
	}
}
