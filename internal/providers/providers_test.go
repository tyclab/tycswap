// providers_test.go — merging several providers into the one view a shell
// renders, and which providers an installation has. Ports claude-swap PR #252
// tests/test_providers_aggregate.py.
package providers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/testutil"
)

// fakeProvider returns a canned snapshot and records what it was asked.
type fakeProvider struct {
	id         string
	numbers    []string
	fail       bool
	panics     bool
	fetchCalls []map[string]bool
}

func (p *fakeProvider) ID() string { return p.id }

func (p *fakeProvider) Snapshot(fetch map[string]bool) (*reporting.AccountsSnapshot, error) {
	p.fetchCalls = append(p.fetchCalls, fetch)
	if p.panics {
		panic("store is corrupt")
	}
	if p.fail {
		return nil, errors.New("store is corrupt")
	}
	snap := &reporting.AccountsSnapshot{Provider: p.id}
	if len(p.numbers) > 0 {
		snap.ActiveNumber = p.numbers[0]
	}
	for _, n := range p.numbers {
		snap.Accounts = append(snap.Accounts, reporting.AccountSnapshot{
			Number: n, Email: p.id + "-" + n + "@x", IsActive: n == p.numbers[0], Kind: "oauth", Switchable: true, Provider: p.id,
		})
	}
	return snap, nil
}

func fake(id string, numbers ...string) *fakeProvider { return &fakeProvider{id: id, numbers: numbers} }

func keys(s reporting.AccountsSnapshot) []string {
	out := []string{}
	for _, a := range s.Accounts {
		out = append(out, a.Key())
	}
	return out
}

func set(ks ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range ks {
		m[k] = true
	}
	return m
}

func TestRowKeysStayUniqueAcrossProviders(t *testing.T) {
	snap, owners := MergedSnapshot([]Provider{fake("claude", "1", "2"), fake("codex", "1", "2")}, nil)
	if got := keys(snap); !reflect.DeepEqual(got, []string{"claude:1", "claude:2", "codex:1", "codex:2"}) {
		t.Errorf("keys = %v", got)
	}
	if len(owners) != 4 {
		t.Errorf("owners = %d, want 4", len(owners))
	}
	if snap.Provider != "multi" {
		t.Errorf("provider = %q, want multi", snap.Provider)
	}
}

func TestOwnersMapEachRowToTheProviderThatOwnsIt(t *testing.T) {
	claude, codex := fake("claude", "1"), fake("codex", "1")
	_, owners := MergedSnapshot([]Provider{claude, codex}, nil)
	if owners["claude:1"] != Provider(claude) || owners["codex:1"] != Provider(codex) {
		t.Errorf("owners = %v", owners)
	}
}

func TestOrderingIsProviderMajorAndStable(t *testing.T) {
	ps := []Provider{fake("claude", "1", "2"), fake("codex", "1")}
	first, _ := MergedSnapshot(ps, nil)
	second, _ := MergedSnapshot(ps, nil)
	want := []string{"claude:1", "claude:2", "codex:1"}
	if !reflect.DeepEqual(keys(first), want) || !reflect.DeepEqual(keys(second), want) {
		t.Errorf("first %v second %v", keys(first), keys(second))
	}
}

func TestActiveNumberStillMeansClaude(t *testing.T) {
	snap, _ := MergedSnapshot([]Provider{fake("claude", "3", "4"), fake("codex", "1")}, nil)
	if snap.ActiveNumber != "3" {
		t.Errorf("ActiveNumber = %q, want 3", snap.ActiveNumber)
	}
	snap, _ = MergedSnapshot([]Provider{fake("codex", "1")}, nil)
	if snap.ActiveNumber != "" {
		t.Errorf("a Codex-only merge reports ActiveNumber %q", snap.ActiveNumber)
	}
}

func TestEachRowCarriesItsOwnActiveFlag(t *testing.T) {
	snap, _ := MergedSnapshot([]Provider{fake("claude", "1", "2"), fake("codex", "5", "6")}, nil)
	var active []string
	for _, a := range snap.Accounts {
		if a.IsActive {
			active = append(active, a.Key())
		}
	}
	if !reflect.DeepEqual(active, []string{"claude:1", "codex:5"}) {
		t.Errorf("active = %v", active)
	}
}

func TestOneProvidersFailureDoesNotBlankTheOthers(t *testing.T) {
	for _, bad := range []*fakeProvider{{id: "codex", numbers: []string{"1"}, fail: true}, {id: "codex", numbers: []string{"1"}, panics: true}} {
		snap, owners := MergedSnapshot([]Provider{fake("claude", "1"), bad}, nil)
		if got := keys(snap); !reflect.DeepEqual(got, []string{"claude:1"}) {
			t.Errorf("keys = %v", got)
		}
		if len(owners) != 1 || owners["claude:1"] == nil {
			t.Errorf("owners = %v", owners)
		}
	}
}

func TestFetchNilIsPassedThroughToEveryProvider(t *testing.T) {
	claude, codex := fake("claude", "1"), fake("codex", "1")
	MergedSnapshot([]Provider{claude, codex}, nil)
	if len(claude.fetchCalls) != 1 || claude.fetchCalls[0] != nil || len(codex.fetchCalls) != 1 || codex.fetchCalls[0] != nil {
		t.Errorf("claude %v codex %v", claude.fetchCalls, codex.fetchCalls)
	}
}

func TestAFetchSetIsSplitPerProvider(t *testing.T) {
	claude, codex := fake("claude", "1", "2"), fake("codex", "1")
	MergedSnapshot([]Provider{claude, codex}, set("claude:2", "codex:1"))
	if !reflect.DeepEqual(claude.fetchCalls, []map[string]bool{set("2")}) || !reflect.DeepEqual(codex.fetchCalls, []map[string]bool{set("1")}) {
		t.Errorf("claude %v codex %v", claude.fetchCalls, codex.fetchCalls)
	}
}

func TestAProviderNamedByNoFetchKeyGetsAnEmptySetNotNil(t *testing.T) {
	claude, codex := fake("claude", "1"), fake("codex", "1")
	MergedSnapshot([]Provider{claude, codex}, set("claude:1"))
	if len(codex.fetchCalls) != 1 || codex.fetchCalls[0] == nil || len(codex.fetchCalls[0]) != 0 {
		t.Errorf("codex fetch = %#v, want an empty non-nil set", codex.fetchCalls)
	}
}

func TestGroupingPreservesTheRenderOrder(t *testing.T) {
	snap, _ := MergedSnapshot([]Provider{fake("claude", "1", "2"), fake("codex", "1")}, nil)
	groups := GroupByProvider(snap)
	type g struct {
		id   string
		keys []string
	}
	var got []g
	for _, gr := range groups {
		ks := []string{}
		for _, a := range gr.Accounts {
			ks = append(ks, a.Key())
		}
		got = append(got, g{gr.Provider, ks})
	}
	want := []g{{"claude", []string{"claude:1", "claude:2"}}, {"codex", []string{"codex:1"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %v", got)
	}
}

func TestGroupingAnEmptySnapshotYieldsNothing(t *testing.T) {
	if got := GroupByProvider(reporting.AccountsSnapshot{}); len(got) != 0 {
		t.Errorf("groups = %v", got)
	}
}

func TestGroupingTreatsAnUntaggedRowAsClaude(t *testing.T) {
	groups := GroupByProvider(reporting.AccountsSnapshot{Accounts: []reporting.AccountSnapshot{{Number: "1"}}})
	if len(groups) != 1 || groups[0].Provider != "claude" {
		t.Errorf("groups = %v", groups)
	}
}

func TestMergeSnapshotsOfTwoTakenSnapshots(t *testing.T) {
	claude := reporting.AccountsSnapshot{ActiveNumber: "2", Accounts: []reporting.AccountSnapshot{{Number: "1"}, {Number: "2", IsActive: true}}}
	codex := reporting.AccountsSnapshot{ActiveNumber: "1", Provider: "codex", Accounts: []reporting.AccountSnapshot{{Number: "1", IsActive: true, Provider: "codex"}}}
	snap := MergeSnapshots(claude, codex)
	if got := keys(snap); !reflect.DeepEqual(got, []string{"claude:1", "claude:2", "codex:1"}) {
		t.Errorf("keys = %v", got)
	}
	if snap.ActiveNumber != "2" || snap.Accounts[0].Provider != "claude" {
		t.Errorf("snap = %+v", snap)
	}
}

func TestProviderLabelsAreShortEnoughForARowBadge(t *testing.T) {
	for in, want := range map[string]string{"claude": "claude", "codex": "codex", "unknown": "unknown"} {
		if got := ProviderLabel(in); got != want {
			t.Errorf("ProviderLabel(%q) = %q", in, got)
		}
	}
}

// ---- MultiSnapshotSource -----------------------------------------------------

type claudeSource struct{ fetches []map[string]bool }

func (c *claudeSource) AccountsSnapshot(fetch map[string]bool) *reporting.AccountsSnapshot {
	c.fetches = append(c.fetches, fetch)
	return &reporting.AccountsSnapshot{ActiveNumber: "1", Accounts: []reporting.AccountSnapshot{{Number: "1", IsActive: true}}}
}

func TestMultiSnapshotSourceFollowsTheStoreOnlyRule(t *testing.T) {
	claude, codex := fake("claude", "1"), fake("codex", "1")
	m := NewMultiSnapshotSourceFrom(claude, codex)
	snap, owners := m.Take(false, true)
	if len(claude.fetchCalls[0]) != 0 || claude.fetchCalls[0] == nil || codex.fetchCalls[0] == nil {
		t.Errorf("store-only pass fetch sets: %v %v", claude.fetchCalls, codex.fetchCalls)
	}
	m.Take(true, false)
	if claude.fetchCalls[1] != nil || codex.fetchCalls[1] != nil {
		t.Errorf("a plain pass must make every account eligible: %v %v", claude.fetchCalls, codex.fetchCalls)
	}
	if !reflect.DeepEqual(keys(snap), []string{"claude:1", "codex:1"}) || len(owners) != 2 {
		t.Errorf("snap %v owners %v", keys(snap), owners)
	}
	if len(m.Providers()) != 2 {
		t.Errorf("providers = %d", len(m.Providers()))
	}
}

func TestMultiSnapshotSourceWithoutCodexIsClaudeOnly(t *testing.T) {
	src := &claudeSource{}
	m := NewMultiSnapshotSource(context.Background(), src, nil)
	snap := m.AccountsSnapshot(set("claude:1", "codex:9"))
	if !reflect.DeepEqual(keys(*snap), []string{"claude:1"}) || snap.ActiveNumber != "1" {
		t.Errorf("snap = %+v", snap)
	}
	if !reflect.DeepEqual(src.fetches, []map[string]bool{set("1")}) {
		t.Errorf("claude fetch = %v", src.fetches)
	}
	if Switcher(m.Providers()[0]) != nil {
		t.Error("Switcher of the Claude provider is not nil")
	}
}

// ---- registry ----------------------------------------------------------------

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testutil.Setenv(t, "HOME", home)
	testutil.Setenv(t, "CODEX_HOME", filepath.Join(home, ".codex"))
	testutil.Setenv(t, "XDG_DATA_HOME", filepath.Join(home, "xdg"))
	return home
}

func addCodexSlot(t *testing.T) {
	t.Helper()
	st := store.New(store.Options{Keychain: keychain.NewFake(), Platform: platform.Linux})
	if _, err := st.UpsertSlot(authfile.AccountKey("u", "a"), store.Upsert{Email: "a@x"}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexIsAbsentOnACleanMachine(t *testing.T) {
	isolate(t)
	if CodexIsPresent() {
		t.Error("CodexIsPresent on a clean machine")
	}
}

func TestCodexIsPresentOnceItHasASlot(t *testing.T) {
	isolate(t)
	addCodexSlot(t)
	if !CodexIsPresent() {
		t.Error("CodexIsPresent = false with a slot")
	}
}

func TestCodexIsPresentWhenOnlyAnUnimportedRegistryExists(t *testing.T) {
	isolate(t)
	if err := os.MkdirAll(authfile.AuthAccountsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authfile.AuthRegistryPath(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !CodexIsPresent() {
		t.Error("CodexIsPresent = false with a registry")
	}
}

func TestAClaudeOnlyMachineGetsExactlyOneProvider(t *testing.T) {
	isolate(t)
	claude := fake("claude", "1")
	if got := AvailableProviders(context.Background(), claude); len(got) != 1 || got[0] != Provider(claude) {
		t.Errorf("providers = %v", got)
	}
}

func TestCodexJoinsTheListOncePresent(t *testing.T) {
	isolate(t)
	addCodexSlot(t)
	got := AvailableProviders(context.Background(), fake("claude", "1"))
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID())
	}
	if !reflect.DeepEqual(ids, []string{"claude", "codex"}) {
		t.Fatalf("ids = %v", ids)
	}
	if Switcher(got[1]) == nil {
		t.Error("the Codex provider carries no switcher")
	}
	var _ *switcher.Switcher = Switcher(got[1])
}
