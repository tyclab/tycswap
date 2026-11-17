package core

import (
	"strconv"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/switching"
)

// Switch rotates by strategy: a switch by hand (byHand).
func (sw *Switcher) Switch(strategy *string, jsonOut bool, models []string, modelSrc *string) (map[string]any, error) {
	return sw.byHand(func() (any, error) { return switching.Switch(sw.Store, strategy, jsonOut, models, modelSrc) })
}

func (sw *Switcher) ApproveAPIKeySwitch(id string) {
	num, _, _, err := sw.Store.ResolveAccount(id)
	if err != nil || num == "" {
		return
	}
	switching.ApproveAPIKeySwitch(num)
}

// SwitchTo delegates to switching.SwitchTo with force=false (spec 02§6): a
// switch by hand (byHand). The frozen autoswitch.Switcher (§2.18) and
// tui.Facade (§2.20) pin exactly this two-argument shape; the engine reaches
// EngineSwitchTo through its adapter instead.
func (sw *Switcher) SwitchTo(id string, jsonOut bool) (map[string]any, error) {
	return sw.SwitchToForce(id, jsonOut, false)
}

// SwitchToForce delegates to switching.SwitchTo with the `force` flag DESIGN
// §2.15 specifies but no frozen interface exposes (cli's `--switch-to --force`
// wires it here). A switch by hand (byHand).
func (sw *Switcher) SwitchToForce(id string, jsonOut, force bool) (map[string]any, error) {
	return sw.byHand(func() (any, error) { return switching.SwitchTo(sw.Store, id, jsonOut, force) })
}

// EngineSwitchTo is the switch the engine and `tycswap start` make: they hold
// the state lock already and stamp the switch themselves (DESIGN A94).
func (sw *Switcher) EngineSwitchTo(id string, jsonOut bool) (map[string]any, error) {
	result, err := switching.SwitchTo(sw.Store, id, jsonOut, false)
	if err != nil {
		return nil, err
	}
	m, _ := result.(map[string]any)
	return m, nil
}

// byHand is the one call every switch by hand goes through (command line,
// dashboard, trays, TUI): a switch that moved the roster holds the engine for
// its cooldown, and auto-switch stays as it is (DESIGN A94).
func (sw *Switcher) byHand(run func() (any, error)) (map[string]any, error) {
	var result any
	warn := func(err error) {
		if sw.Log != nil {
			sw.Log.Warningf("Auto-switch hold after the switch by hand not recorded: %v", err)
		}
	}
	err := autoswitch.HandSwitch(sw.StatePath(), sw.Clk, warn, func() (string, error) {
		before := sw.rosterMark()
		var err error
		if result, err = run(); err != nil {
			return "", err
		}
		if after := sw.rosterMark(); after != before {
			return after.active, nil
		}
		return "", nil
	})
	if err != nil {
		return nil, err
	}
	m, _ := result.(map[string]any)
	return m, nil
}

// rosterMark is the roster's live slot and when the roster was written: a
// switch changes it, one onto the live account does not.
type rosterMark struct{ active, stamp string }

func (sw *Switcher) rosterMark() rosterMark {
	data, err := sw.ReadSequence()
	if err != nil || data == nil {
		return rosterMark{}
	}
	m := rosterMark{stamp: data.LastUpdated}
	if data.ActiveAccountNumber != nil {
		m.active = strconv.Itoa(*data.ActiveAccountNumber)
	}
	return m
}
