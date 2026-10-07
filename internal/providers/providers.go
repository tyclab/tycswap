// providers.go — the provider seam, which providers an installation has, and
// the merged provider-major view the dashboard and menu bar render.
// Port of claude-swap PR #252 providers/base.py, providers/registry.py and
// providers/aggregate.py.
//
// The seam is deliberately narrow (base.py): the Claude switcher's
// provenance machinery has no Codex analogue, so the providers agree only on
// the read model. Claude is always present; Codex appears only once the user
// has Codex accounts (or an un-imported codex-auth registry), so a Claude-only
// install looks and behaves exactly as before (registry.py). Merged rows are
// grouped provider-major and never interleaved, so the row under the cursor
// does not move between refreshes; rows are addressed by "provider:number"
// because slot numbers are per-provider; and one provider's failure never
// blanks the others (aggregate.py). ActiveNumber on a merged snapshot is the
// Claude active slot, because every existing consumer of that field means
// Claude by it; per-provider active state is each row's IsActive.
package providers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/reporting"
)

type Provider interface {
	ID() string
	Snapshot(fetch map[string]bool) (*reporting.AccountsSnapshot, error)
}

type SnapshotSource interface {
	AccountsSnapshot(fetch map[string]bool) *reporting.AccountsSnapshot
}

type claudeProvider struct{ src SnapshotSource }

// Claude adapts the Claude switcher's read model to a Provider.
func Claude(src SnapshotSource) Provider { return claudeProvider{src} }

func (p claudeProvider) ID() string { return reporting.ProviderClaude }

func (p claudeProvider) Snapshot(fetch map[string]bool) (*reporting.AccountsSnapshot, error) {
	snap := p.src.AccountsSnapshot(fetch)
	if snap == nil {
		return nil, fmt.Errorf("claude snapshot unavailable")
	}
	return snap, nil
}

type codexProvider struct {
	ctx context.Context
	sw  *switcher.Switcher
}

// Codex adapts a Codex switcher to a Provider; ctx bounds its usage requests.
func Codex(ctx context.Context, sw *switcher.Switcher) Provider { return codexProvider{ctx, sw} }

func (p codexProvider) ID() string { return reporting.ProviderCodex }

func (p codexProvider) Snapshot(fetch map[string]bool) (*reporting.AccountsSnapshot, error) {
	snap := p.sw.AccountsSnapshot(p.ctx, fetch)
	return &snap, nil
}

func Switcher(p Provider) *switcher.Switcher {
	if c, ok := p.(codexProvider); ok {
		return c.sw
	}
	return nil
}

// ---- registry ----------------------------------------------------------------

// CodexIsPresent reports whether this machine has any Codex accounts tycswap
// knows or could import: a slot in the Codex store, or a codex-auth registry
// not yet imported, so the provider shows up on the first run rather than only
// after a `tycswap codex` command. It never panics.
func CodexIsPresent() (present bool) {
	defer func() {
		if recover() != nil {
			present = false
		}
	}()
	if len(store.New(store.Options{}).Slots()) > 0 {
		return true
	}
	_, err := os.Stat(authfile.AuthRegistryPath())
	return err == nil
}

func AvailableProviders(ctx context.Context, claude Provider) []Provider {
	out := []Provider{claude}
	if CodexIsPresent() {
		out = append(out, Codex(ctx, switcher.New(switcher.Options{})))
	}
	return out
}

// ---- aggregate ---------------------------------------------------------------

// nowSeconds stamps merged snapshots; tests may replace it.
var nowSeconds = func() float64 { return clock.Seconds(clock.System{}) }

// safeSnapshot runs one provider's pass, turning a panic into an error.
func safeSnapshot(p Provider, fetch map[string]bool) (snap *reporting.AccountsSnapshot, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("provider %s snapshot panicked: %v", p.ID(), r)
		}
	}()
	return p.Snapshot(fetch)
}

// splitFetch is one provider's share of a merged fetch set keyed like rows
// ("codex:2"). nil stays nil (every account eligible); a provider named by no
// key gets an empty, non-nil set (no network) — never nil, which would mean
// the opposite.
func splitFetch(fetch map[string]bool, id string) map[string]bool {
	if fetch == nil {
		return nil
	}
	out := map[string]bool{}
	prefix := id + ":"
	for k, v := range fetch {
		if v && strings.HasPrefix(k, prefix) {
			out[k[len(prefix):]] = true
		}
	}
	return out
}

func merge(snaps []*reporting.AccountsSnapshot, owners []Provider) (reporting.AccountsSnapshot, map[string]Provider) {
	out := reporting.AccountsSnapshot{Accounts: []reporting.AccountSnapshot{}, TakenAt: nowSeconds(), Provider: "multi"}
	byKey := map[string]Provider{}
	for i, snap := range snaps {
		if snap == nil {
			continue
		}
		if snap.ProviderName() == reporting.ProviderClaude {
			out.ActiveNumber = snap.ActiveNumber
		}
		for _, a := range snap.Accounts {
			if a.Provider == "" {
				a.Provider = snap.ProviderName()
			}
			out.Accounts = append(out.Accounts, a)
			if owners != nil {
				byKey[a.Key()] = owners[i]
			}
		}
	}
	return out, byKey
}

func MergedSnapshot(providers []Provider, fetch map[string]bool) (reporting.AccountsSnapshot, map[string]Provider) {
	snaps := make([]*reporting.AccountsSnapshot, len(providers))
	for i, p := range providers {
		snap, err := safeSnapshot(p, splitFetch(fetch, p.ID()))
		if err != nil {
			continue
		}
		if snap.Provider == "" {
			snap.Provider = p.ID()
		}
		snaps[i] = snap
	}
	return merge(snaps, providers)
}

// MergeSnapshots merges already-taken snapshots (Claude first, by
// convention) in the order given, with MergedSnapshot's rules.
func MergeSnapshots(snaps ...reporting.AccountsSnapshot) reporting.AccountsSnapshot {
	ptrs := make([]*reporting.AccountsSnapshot, len(snaps))
	for i := range snaps {
		ptrs[i] = &snaps[i]
	}
	out, _ := merge(ptrs, nil)
	return out
}

// Group is one provider's rows within a merged snapshot.
type Group struct {
	Provider string
	Accounts []reporting.AccountSnapshot
}

// GroupByProvider groups rows by provider in first-seen order rather than
// sorting, so the grouping matches what the shell renders row for row.
func GroupByProvider(snap reporting.AccountsSnapshot) []Group {
	var groups []Group
	index := map[string]int{}
	for _, a := range snap.Accounts {
		id := a.ProviderName()
		i, ok := index[id]
		if !ok {
			i = len(groups)
			index[id] = i
			groups = append(groups, Group{Provider: id})
		}
		groups[i].Accounts = append(groups[i].Accounts, a)
	}
	return groups
}

// providerLabels are the short row badges that keep two providers' accounts
// from being confused.
var providerLabels = map[string]string{reporting.ProviderClaude: "claude", reporting.ProviderCodex: "codex"}

// ProviderLabel is the row badge for a provider id; an unknown id is its own
// label, never blank.
func ProviderLabel(id string) string {
	if l, ok := providerLabels[id]; ok {
		return l
	}
	return id
}

type MultiSnapshotSource struct {
	providers []Provider
}

// NewMultiSnapshotSource returns a source over a Claude source plus an
// optional Codex switcher (nil for a Claude-only install).
func NewMultiSnapshotSource(ctx context.Context, claude SnapshotSource, codex *switcher.Switcher) *MultiSnapshotSource {
	ps := []Provider{Claude(claude)}
	if codex != nil {
		ps = append(ps, Codex(ctx, codex))
	}
	return &MultiSnapshotSource{providers: ps}
}

// NewMultiSnapshotSourceFrom returns a source over the given providers.
func NewMultiSnapshotSourceFrom(providers ...Provider) *MultiSnapshotSource {
	return &MultiSnapshotSource{providers: append([]Provider(nil), providers...)}
}

// Providers returns the providers in render order.
func (m *MultiSnapshotSource) Providers() []Provider { return append([]Provider(nil), m.providers...) }

// Take runs one blocking pass across every provider; call it from a worker.
// It follows the TUI's snapshotSource rule: storeOnly reads with an empty
// fetch set (no network), otherwise every stale account is eligible; full is
// accepted for API stability and changes nothing.
func (m *MultiSnapshotSource) Take(full, storeOnly bool) (reporting.AccountsSnapshot, map[string]Provider) {
	var fetch map[string]bool
	if storeOnly {
		fetch = map[string]bool{}
	}
	snaps := make([]*reporting.AccountsSnapshot, len(m.providers))
	for i, p := range m.providers {
		var pf map[string]bool
		if fetch != nil {
			pf = map[string]bool{}
		}
		snap, err := safeSnapshot(p, pf)
		if err != nil {
			continue
		}
		if snap.Provider == "" {
			snap.Provider = p.ID()
		}
		snaps[i] = snap
	}
	return merge(snaps, m.providers)
}

// AccountsSnapshot makes the source usable wherever the Facade's read model
// is: fetch is keyed by row key ("codex:2") and split per provider.
func (m *MultiSnapshotSource) AccountsSnapshot(fetch map[string]bool) *reporting.AccountsSnapshot {
	snap, _ := MergedSnapshot(m.providers, fetch)
	return &snap
}
