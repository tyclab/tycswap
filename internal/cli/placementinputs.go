// What placing a session reads that does not depend on the session (DESIGN
// A117): the Fable store's state, its start snapshot's answer, the Mythos
// selection, each login's verified owner and each link target's owner label.
// A decision that moves a session reads them afresh on every question; one
// dashboard state build reads each once and judges every session it previews
// against them with that session's own link record.
package cli

import (
	"context"
	"sync"

	codexswitcher "github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/providers"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
	"github.com/tyclab/tycswap/internal/web"
)

type placementInputs struct {
	view   *store.Store // without lineage writes, OAuth or log
	shared bool

	fableState readOnce[store.FableState]
	fableStart readOnce[fableStartAnswer]
	mythos     readOnce[mythosAnswer]
	logins     readOnceBy[loginAnswer]
	labels     readOnceBy[[2]string]
}

// placementFor is s's placement inputs, read afresh on every question; shared
// reads each once, for a read operation that judges many sessions.
func placementFor(s *store.Store, shared bool) *placementInputs {
	view := s.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	return &placementInputs{view: view, shared: shared}
}

// readOnce is an input read at most once while shared.
type readOnce[T any] struct {
	once sync.Once
	v    T
}

func (o *readOnce[T]) get(shared bool, read func() T) T {
	if !shared {
		return read()
	}
	o.once.Do(func() { o.v = read() })
	return o.v
}

// readOnceBy is readOnce per key.
type readOnceBy[T any] struct {
	mu sync.Mutex
	m  map[string]*readOnce[T]
}

func (k *readOnceBy[T]) get(shared bool, key string, read func() T) T {
	if !shared {
		return read()
	}
	k.mu.Lock()
	o := k.m[key]
	if o == nil {
		if k.m == nil {
			k.m = map[string]*readOnce[T]{}
		}
		o = &readOnce[T]{}
		k.m[key] = o
	}
	k.mu.Unlock()
	return o.get(true, read)
}

func (p *placementInputs) fableStore() store.FableState {
	return p.fableState.get(p.shared, p.view.FableStore)
}

type fableStartAnswer struct {
	toDefault bool
	err       error
}

// startsOnDefault is fableStartsOnDefault for a session without an account.
func (p *placementInputs) startsOnDefault() (bool, error) {
	a := p.fableStart.get(p.shared, func() fableStartAnswer {
		_, toDefault, err := fableStartsOnDefault(&core.Switcher{Store: p.view}, "")
		return fableStartAnswer{toDefault, err}
	})
	return a.toDefault, a.err
}

// roomOnDefault is the Fable room rule's answer (fableSessionOnDefault). It
// judges an automatic placement only, never a preview, so it is not kept.
func (p *placementInputs) roomOnDefault() bool {
	toDefault, _, _ := fableRoomRule(p)
	return toDefault
}

type mythosAnswer struct {
	target mythosPlacement
	err    error
}

// mythosMove is mythosMovePlacement: where a move to Mythos goes.
func (p *placementInputs) mythosMove() (mythosPlacement, error) {
	a := p.mythos.get(p.shared, func() mythosAnswer {
		target, err := mythosMovePlacement(p.view)
		return mythosAnswer{target, err}
	})
	return a.target, a.err
}

// loginAnswer is a login as placement judges it: the roster, the account its
// credential names and that account's cached usage.
type loginAnswer struct {
	data  *store.SequenceData
	err   error
	owner loginOwner
	entry usage.UsageEntry
}

// login is the login whose store view is, by key: "" the default login,
// "fable" the Fable store.
func (p *placementInputs) login(key string, view *store.Store) loginAnswer {
	return p.logins.get(p.shared, key, func() loginAnswer {
		data, err := view.SequenceForUpdate()
		if err != nil {
			return loginAnswer{err: err}
		}
		a := loginAnswer{data: data, owner: verifiedLoginOwner(view, data)}
		if a.owner.verified() {
			id := usage.Identity{Email: a.owner.email, OrgUUID: a.owner.org}
			a.entry = view.Usage.Entries(map[string]usage.Identity{a.owner.num: id})[a.owner.num]
		}
		return a
	})
}

// loginLabel is reporting.SessionLoginOwner of a link target.
func (p *placementInputs) loginLabel(target string) (label, number string) {
	a := p.labels.get(p.shared, target, func() [2]string {
		label, number := reporting.SessionLoginOwner(p.view, target)
		return [2]string{label, number}
	})
	return a[0], a[1]
}

// buildReads gives each dashboard state build its own reads (DESIGN A117):
// one read operation over the store (A116) and placement inputs read once
// for every session the build previews. They hold no write and end with the
// build; the mutation routes keep the dashboard's own façades.
func (d *dashboard) buildReads(ctx context.Context, codexSw *codexswitcher.Switcher) func() web.StateReads {
	return func() web.StateReads {
		read := &core.Switcher{Store: d.sw.Store.OneRead()}
		place := placementFor(read.Store, true)
		return web.StateReads{
			Snapshot:       providers.NewMultiSnapshotSource(ctx, read, codexSw).AccountsSnapshot,
			ListAccounts:   read.ListAccounts,
			CurrentLogin:   func() (string, bool) { return currentLogin(read) },
			AutoView:       func() web.AutoView { return d.auto.viewIn(read.Store) },
			NextStartModel: d.NextStartModel,
			SessionLogin:   place.sessionLogin,
			SessionMoves:   place.moveChoices,
		}
	}
}
