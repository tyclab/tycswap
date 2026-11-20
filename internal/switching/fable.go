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
	recoverIdentity string
	Actor           string
	BeforeChange    func(*store.Store, *store.SequenceData) error
}

// fableBeforeInstall is a test seam: it runs under the locks just before the
// incoming login is installed.
var fableBeforeInstall func()

// RecoverFable starts a new, identity-pinned session on an empty store.
// Eligibility is checked under the native lock and the future process is
// registered before unlocking. A failed install never registers a reader.
func RecoverFable(f *store.Store, id, identity string, check func(*store.Store, *store.SequenceData) error) error {
	r, err := SwitchFable(f, id, FableSwitchOptions{recoverIdentity: identity, BeforeChange: check})
	if err == nil && r["switched"] != true {
		return cerr.Switch("The Fable login changed during recovery; retry the new start")
	}
	return err
}

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
	num, email, org, err := f.ResolveAccountFrom(data, id)
	if err != nil {
		return nil, err
	}
	if err := fableTargetRefusal(f, data, num); err != nil {
		return nil, err
	}
	to := numRef(num, email)
	if f.ResolveOwnerSlot(data).Slot == num {
		return fableResult(false, to, to, "already-active"), nil
	}
	if err := f.SetupFableDirectories(); err != nil {
		return nil, err
	}
	var from map[string]any
	already := false
	err = f.WithFableLocks(func() error {
		current, err := f.ReadSequence()
		if err != nil {
			return err
		}
		if f.FindAccountSlot(current, email, org) != num {
			return cerr.Switch("Account-%s changed while the switch waited for the store lock; nothing switched. Retry the command.", num)
		}
		if err := fableTargetRefusal(f, current, num); err != nil {
			return err
		}
		ls := f.ResolveOwnerSlot(current)
		if already = ls.Slot == num; already {
			return nil
		}
		incoming, _ := f.ReadAccountCredentials(num, email)
		if incoming == "" {
			return cerr.Switch("Account-%s's Fable login is gone", num)
		}
		parked, err := store.FableAllocationParked(f.BackupDir())
		if err != nil {
			return err
		}
		if opts.recoverIdentity != "" && !parked {
			return cerr.Switch("The Fable store was turned off during recovery")
		}
		if parked && opts.recoverIdentity == "" {
			if opts.Actor == "" {
				return cerr.Switch("The Fable login is parked for a new native session")
			}
			if err := f.CheckFableIdle(); err != nil {
				return err
			}
		}
		if err := f.ReleaseIdleFableReaders(); err != nil {
			return err
		}
		if opts.BeforeChange != nil {
			if err := opts.BeforeChange(f, current); err != nil {
				return err
			}
		}
		if from, err = backUpFableOutgoing(f, current, ls); err != nil {
			return err
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
		if opts.recoverIdentity != "" {
			return f.PinFableReader(opts.recoverIdentity)
		}
		if parked && opts.recoverIdentity == "" {
			return f.SetFableAllocationParked(false)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if already {
		return fableResult(false, to, to, "already-active"), nil
	}
	if opts.Actor != "" {
		f.Log.Action("Switched the Fable store to account "+num, opts.Actor)
	}
	return fableResult(true, from, to, "switched"), nil
}

// EmptyFable turns the Fable store off: under the same locks the live login
// goes back into its account's Fable login, the store keeps its seat-wide
// keys only, or no file when it has none, and its chain record goes.
func EmptyFable(fable *store.Store) (map[string]any, error) {
	return emptyFableWhen(fable, nil)
}

func emptyFableWhen(fable *store.Store, check func(*store.SequenceData) error) (map[string]any, error) {
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
	f.ResolveFableLiveSlot(data) // asks the profile endpoint, if at all, before the locks
	var from map[string]any
	err = f.WithFableLocks(func() error {
		current, err := f.ReadSequence()
		if err != nil {
			return err
		}
		parked, err := store.FableAllocationParked(f.BackupDir())
		if err != nil {
			return err
		}
		if parked && check == nil {
			if err := f.CheckFableIdle(); err != nil {
				return err
			}
		}
		if err := f.ReleaseIdleFableReaders(); err != nil {
			return err
		}
		if check != nil {
			if err := check(current); err != nil {
				return err
			}
		}
		if from, err = backUpFableOutgoing(f, current, f.ResolveOwnerSlot(current)); err != nil {
			return err
		}
		if err := f.Creds.ClearActive(); err != nil {
			return err
		}
		return f.ForgetFableLive()
	})
	if err != nil {
		return nil, err
	}
	return fableResult(true, from, nil, "emptied"), nil
}

func fableTargetRefusal(f *store.Store, data *store.SequenceData, num string) error {
	if err := checkSwitchEnabled(data, num); err != nil {
		return err
	}
	switch {
	case f.AccountKindFor(num) == "api_key":
		return cerr.Validation("Account-%s is an API-key account; the Fable store holds OAuth logins only", num)
	case !f.HasFableLogin(num):
		return cerr.Validation("Account-%s has no Fable login: tycswap add --login --lineage fable --slot %s", num, num)
	case !f.FableEntitlement(data, num).Capable:
		return cerr.Validation("Account-%s's plan offers no Fable", num)
	}
	return nil
}

// backUpFableOutgoing runs under the locks with the roster read there and ls,
// the live login's owner resolved from it (backup and chain, a profile answer
// asked before the locks, else the last install: the store lock keeps the
// endpoint out). A login taken for the last install's is stashed as well. It
// returns the outgoing account's ref, nil for an empty store or an unknown
// login.
func backUpFableOutgoing(f *store.Store, data *store.SequenceData, ls store.LiveSlot) (map[string]any, error) {
	now, _, err := f.Creds.ReadActive()
	if err != nil || now == "" {
		return nil, err
	}
	if ls.Slot == "" {
		if _, err := f.Creds.WriteUnclaimed(now, map[string]any{"lineage": "fable"}); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if ls.Source == store.SourceLastInstall {
		id, err := f.Creds.WriteUnclaimed(now, map[string]any{"lineage": "fable", "reason": store.SourceLastInstall, "slot": ls.Slot})
		if err != nil {
			return nil, err
		}
		if f.Log != nil {
			f.Log.Infof("Backed up the Fable store's login into account %s's Fable login as its last install (nothing else named its account); a copy is stashed as %s", ls.Slot, id)
		}
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
