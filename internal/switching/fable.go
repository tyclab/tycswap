package switching

import (
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

// FableSwitchOptions configures a switch of the Fable store. Actor names who
// switched in the log line ("hand"); "" writes none: the engine logs its own.
type FableSwitchOptions struct {
	Actor string
}

// fableBeforeInstall is a test seam: it runs under the locks just before the
// incoming login is installed.
var fableBeforeInstall func()

// SwitchFable makes account id's Fable login the Fable store's live login
// (DESIGN A100). Under the store lock and the Fable store's own Claude Code
// locks, the outgoing login goes back into its account's Fable login (or the
// unclaimed stash), the incoming one is installed with the store's seat-wide
// keys by atomic rename, and the Fable chain record names it. The roster,
// ~/.claude.json, the default credential, credentials/ and configs/ are never
// written.
func SwitchFable(fable *store.Store, id string, opts FableSwitchOptions) (map[string]any, error) {
	f, err := fable.ForFable()
	if err != nil {
		return nil, err
	}
	data, err := f.ReadSequence()
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, cerr.Config("No accounts are managed yet")
	}
	num, email, _, err := f.ResolveAccountFrom(data, id)
	if err != nil {
		return nil, err
	}
	if err := fableTargetRefusal(f, num); err != nil {
		return nil, err
	}
	to := numRef(num, email)
	live, _, err := f.Creds.ReadActive()
	if err != nil {
		return nil, err
	}
	ls := f.ResolveFableLiveSlot(data)
	if ls.Slot == num {
		return fableResult(false, to, to, "already-active"), nil
	}
	if err := f.SetupFableDirectories(); err != nil {
		return nil, err
	}
	var from map[string]any
	err = f.WithFableLocks(func() error {
		var err error
		if from, err = backUpFableOutgoing(f, live, ls); err != nil {
			return err
		}
		incoming, _ := f.ReadAccountCredentials(num, email)
		if incoming == "" {
			return cerr.Switch("Account-%s's Fable login is gone", num)
		}
		if fableBeforeInstall != nil {
			fableBeforeInstall()
		}
		if err := f.Creds.WriteActiveAccount(incoming); err != nil {
			return err
		}
		if err := f.RecordFableLive(num, incoming); err != nil && f.Log != nil {
			f.Log.Debugf("Could not record the Fable store's live lineage: %v", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if opts.Actor != "" {
		f.Log.Action("Switched the Fable store to account "+num, opts.Actor)
	}
	return fableResult(true, from, to, "switched"), nil
}

// EmptyFable turns the Fable store off: under the same locks the live login
// goes back into its account's Fable login, and the store keeps its seat-wide
// keys only, or no file when it has none.
func EmptyFable(fable *store.Store) (map[string]any, error) {
	f, err := fable.ForFable()
	if err != nil {
		return nil, err
	}
	live, _, err := f.Creds.ReadActive()
	if err != nil {
		return nil, err
	}
	if live == "" {
		return fableResult(false, nil, nil, "emptied"), nil
	}
	data, _ := f.ReadSequence()
	ls := f.ResolveFableLiveSlot(data)
	var from map[string]any
	err = f.WithFableLocks(func() error {
		var err error
		if from, err = backUpFableOutgoing(f, live, ls); err != nil {
			return err
		}
		return f.Creds.ClearActive()
	})
	if err != nil {
		return nil, err
	}
	return fableResult(true, from, nil, "emptied"), nil
}

func fableTargetRefusal(f *store.Store, num string) error {
	switch {
	case f.AccountKindFor(num) == "api_key":
		return cerr.Validation("Account-%s is an API-key account; the Fable store holds OAuth logins only", num)
	case !f.HasFableLogin(num):
		return cerr.Validation("Account-%s has no Fable login: tycswap add --login --lineage fable --slot %s", num, num)
	case !f.FableCapable(num):
		return cerr.Validation("Account-%s's plan offers no Fable", num)
	}
	return nil
}

// backUpFableOutgoing runs under the locks: it re-reads the live login and,
// when it changed since ls was resolved, resolves it again (backup and chain
// only: the store lock keeps the profile endpoint out). It returns the
// outgoing account's ref, nil for an empty store or an unknown login.
func backUpFableOutgoing(f *store.Store, live string, ls store.LiveSlot) (map[string]any, error) {
	now, _, err := f.Creds.ReadActive()
	if err != nil {
		return nil, err
	}
	data, _ := f.ReadSequence()
	if now != live {
		ls = f.ResolveFableLiveSlot(data)
	}
	if now == "" {
		return nil, nil
	}
	if ls.Slot == "" {
		if _, err := f.Creds.WriteUnclaimed(now, map[string]any{"lineage": "fable"}); err != nil {
			return nil, err
		}
		return nil, nil
	}
	rec, _ := accountRec(data, ls.Slot)
	email := recStr(rec, "email")
	if err := f.WriteAccountCredentials(ls.Slot, email, oauth.AccountOnly(now)); err != nil {
		return nil, err
	}
	return numRef(ls.Slot, email), nil
}

func fableResult(switched bool, from, to map[string]any, reason string) map[string]any {
	return map[string]any{
		"schemaVersion": jsonout.SchemaVersion,
		"switched":      switched,
		"from":          from,
		"to":            to,
		"reason":        reason,
		"store":         "fable",
	}
}
