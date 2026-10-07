package core

import "github.com/tyclab/tycswap/internal/switching"

func (sw *Switcher) Switch(strategy *string, jsonOut bool, models []string, modelSrc *string) (map[string]any, error) {
	result, err := switching.Switch(sw.Store, strategy, jsonOut, models, modelSrc)
	if err != nil {
		return nil, err
	}
	m, _ := result.(map[string]any)
	return m, nil
}

func (sw *Switcher) ApproveAPIKeySwitch(id string) {
	num, _, _, err := sw.Store.ResolveAccount(id)
	if err != nil || num == "" {
		return
	}
	switching.ApproveAPIKeySwitch(num)
}

// SwitchTo delegates to switching.SwitchTo with force=false (spec 02§6). The
// frozen autoswitch.Switcher (§2.18) and tui.Facade (§2.20) pin exactly this
// two-argument shape.
func (sw *Switcher) SwitchTo(id string, jsonOut bool) (map[string]any, error) {
	return sw.SwitchToForce(id, jsonOut, false)
}

// SwitchToForce delegates to switching.SwitchTo with the `force` flag DESIGN
// §2.15 specifies but no frozen interface exposes (cli's `--switch-to --force`
// wires it here).
func (sw *Switcher) SwitchToForce(id string, jsonOut, force bool) (map[string]any, error) {
	result, err := switching.SwitchTo(sw.Store, id, jsonOut, force)
	if err != nil {
		return nil, err
	}
	m, _ := result.(map[string]any)
	return m, nil
}
