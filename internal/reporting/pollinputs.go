// pollinputs.go — the threshold/model poll-planning keys that steer usage
// cadence (spec 02§13 _poll_policy_inputs / set_poll_policy_inputs /
// clear_poll_policy_inputs, feeding _persist_poll_plans).
//
// Python holds this state on the switcher instance: an optional override pinned
// by a hosting auto engine (so cadence follows its effective, CLI-merged
// settings) plus an mtime-cached read of settings.json otherwise. In the Go
// decomposition the collectors are free functions over *store.Store that cannot
// carry per-instance state, and the frozen autoswitch.Switcher / tui.Facade
// interfaces (DESIGN A13) put SetPollPolicyInputs/ClearPollPolicyInputs on
// *core.Switcher. This file is the package-level seam those methods drive,
// mirroring the established jsonout.ResetStrings / oauth.Log package seams: a
// single process ever hosts one switcher, so a package-level override is
// faithful. The Python mtime cache is dropped — settings.Load is a forgiving
// read run once per collect pass; the extra stat is not observable (only cadence
// jitter and the urgent-escalation band depend on these inputs, and only after a
// successful fetch).
package reporting

import (
	"math"
	"sync"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

type pollInputs struct {
	threshold float64
	models    []string
}

var (
	pollInputsMu       sync.Mutex
	pollInputsOverride *pollInputs
)

// SetPollPolicyInputs pins the (threshold, models) poll-planning keys, so a
// hosting auto engine's effective settings steer usage cadence instead of the
// settings file (spec 02§13 set_poll_policy_inputs). *core.Switcher's frozen
// SetPollPolicyInputs delegates here.
func SetPollPolicyInputs(threshold float64, models []string) {
	pollInputsMu.Lock()
	defer pollInputsMu.Unlock()
	cp := append([]string(nil), models...)
	pollInputsOverride = &pollInputs{threshold: threshold, models: cp}
}

// ClearPollPolicyInputs drops the hosted engine's pin so poll planning falls
// back to the settings file (spec 02§13 clear_poll_policy_inputs). Called when
// the engine's screen closes so a TUI session threshold override cannot keep
// steering cadence after the engine it belonged to is gone.
func ClearPollPolicyInputs() {
	pollInputsMu.Lock()
	defer pollInputsMu.Unlock()
	pollInputsOverride = nil
}

// PollPolicyInputs reports the current pin: the threshold and models a hosted
// engine set, and whether one is set at all. The read side of
// SetPollPolicyInputs / ClearPollPolicyInputs, for callers that must know
// whether a stopped engine still steers the poll plan.
func PollPolicyInputs() (threshold float64, models []string, pinned bool) {
	pollInputsMu.Lock()
	defer pollInputsMu.Unlock()
	if pollInputsOverride == nil {
		return 0, nil, false
	}
	return pollInputsOverride.threshold, append([]string(nil), pollInputsOverride.models...), true
}

// resolvePollInputs returns the pinned override when present, else the settings
// file's threshold and parsed model names (spec 02§13 _poll_policy_inputs).
func resolvePollInputs(s *store.Store) (float64, []string) {
	pollInputsMu.Lock()
	o := pollInputsOverride
	pollInputsMu.Unlock()
	if o != nil {
		return o.threshold, append([]string(nil), o.models...)
	}
	loaded := settings.Load(s.BackupDir())
	models := settings.ParseModelNames(loaded.Model)
	return lowestBar(loaded, models), models
}

// lowestBar is the single figure poll planning escalates on now that each
// window has a threshold of its own (DESIGN A34). The planner compares it
// against the BINDING headroom, so the lowest bar in force is the honest one:
// whichever window is closest to making the engine act decides how often an
// account is looked at. The per-model bar is in force only while models
// counts something. It is the same rule the engine pins (its pollThreshold).
func lowestBar(s settings.AutoSwitchSettings, models []string) float64 {
	lowest := math.Min(s.SevenDayThreshold, s.FiveHourThreshold)
	if len(models) > 0 {
		lowest = math.Min(lowest, s.ModelThreshold)
	}
	return lowest
}
