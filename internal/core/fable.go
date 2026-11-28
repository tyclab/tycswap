package core

import (
	"fmt"
	"os"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
)

// ForFable is the switcher over the store's Fable scope (DESIGN A100).
func (sw *Switcher) ForFable() (*Switcher, error) {
	f, err := sw.Store.ForFable()
	if err != nil {
		return nil, err
	}
	return &Switcher{Store: f}, nil
}

// SwitchFable switches the Fable store by hand: under the Fable state lock,
// and a switch holds the Fable engine for its cooldown in the Fable state
// file (A94 per store). byHand's roster mark cannot see it: the roster does
// not change.
func (sw *Switcher) SwitchFable(id string, jsonOut bool) (map[string]any, error) {
	return sw.SwitchFableBy(id, jsonOut, "hand")
}

// SwitchFableBy is SwitchFable with the log line's actor ("" writes none, for
// a caller that logs the switch itself).
func (sw *Switcher) SwitchFableBy(id string, jsonOut bool, actor string) (map[string]any, error) {
	return sw.switchFableWith(id, switching.FableSwitchOptions{Actor: actor})
}

// SelectIdleFable picks an enrolled login for a future native session.
func (sw *Switcher) SelectIdleFable(id, actor string) (map[string]any, error) {
	return sw.switchFableWith(id, switching.FableSwitchOptions{Actor: actor, BeforeChange: fableStoreIdle})
}

func fableStoreIdle(f *store.Store, _ *store.SequenceData) error {
	busy, err := lifecycle.SelectedStoreBusy(f)
	if err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("the Fable store is in use or a Claude session has no recorded store")
	}
	return nil
}

func (sw *Switcher) switchFableWith(id string, opts switching.FableSwitchOptions) (map[string]any, error) {
	f, err := sw.Store.ForFable()
	if err != nil {
		return nil, err
	}
	run := func() (map[string]any, error) {
		return switching.SwitchFable(f, id, opts)
	}
	if !f.HasFableLogins() {
		return run() // refused before the Fable scope exists, so nothing creates it
	}
	var result map[string]any
	warn := func(err error) {
		if sw.Log != nil {
			sw.Log.Warningf("Fable store hold after the switch by hand not recorded: %v", err)
		}
	}
	err = autoswitch.HandSwitch(f.StatePath(), sw.Clk, warn, func() (string, error) {
		var err error
		if result, err = run(); err != nil {
			return "", err
		}
		if num, switched := jsonout.SwitchedTo(result); switched {
			return num, nil
		}
		return "", nil
	})
	return result, err
}

// EngineSwitchFable is the Fable engine's switch: it holds the Fable state
// lock already and stamps the switch itself.
func (sw *Switcher) EngineSwitchFable(id string, jsonOut bool) (map[string]any, error) {
	f, err := sw.Store.ForFable()
	if err != nil {
		return nil, err
	}
	return switching.SwitchFable(f, id, switching.FableSwitchOptions{})
}

// EmptyFable turns the Fable store off, under the Fable state lock while it
// holds a login so no Fable tick installs one meanwhile. It stamps no hold.
func (sw *Switcher) EmptyFable(jsonOut bool) (map[string]any, error) {
	return sw.EmptyFableBy(jsonOut, "hand")
}

// EmptyFableBy is EmptyFable with the log line's actor ("" writes none).
func (sw *Switcher) EmptyFableBy(jsonOut bool, actor string) (map[string]any, error) {
	return sw.emptyFableWithinBy(nil, actor)
}

// emptyFableWithinBy is EmptyFableWithin that logs the emptying as actor's
// action once the store was emptied, also when fn fails after that.
func (sw *Switcher) emptyFableWithinBy(fn func(empty func() error) error, actor string) (map[string]any, error) {
	result, err := sw.EmptyFableWithin(fn)
	if result["switched"] == true && sw.Log != nil && actor != "" {
		sw.Log.Action("Emptied the Fable store", actor)
	}
	return result, err
}

// EmptyFableWithin is EmptyFable around fn (switching.EmptyFableWithin),
// under the Fable state lock.
func (sw *Switcher) EmptyFableWithin(fn func(empty func() error) error) (map[string]any, error) {
	f, err := sw.Store.ForFable()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(paths.FableScopeDir(f.BackupDir())); os.IsNotExist(err) {
		return switching.EmptyFableWithin(f, fn)
	} else if err != nil {
		return nil, err
	}
	var result map[string]any
	err = autoswitch.HandSwitch(f.StatePath(), sw.Clk, nil, func() (string, error) {
		var err error
		result, err = switching.EmptyFableWithin(f, fn)
		if err == nil {
			err = f.SetFableAllocationParked(false)
		}
		return "", err
	})
	return result, err
}

// SetFableMark writes or removes the account's Fable mark (DESIGN A107).
func (sw *Switcher) SetFableMark(id string, allowed bool) error {
	return lifecycle.SetFableMark(sw.Store, id, allowed)
}
