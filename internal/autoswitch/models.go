// models.go — retarget the per-model weekly windows a running engine counts.
//
// The models were fixed at construction (05§8). The dashboard's "Count model
// limits" switch saves autoswitch.model and must also change what the engine
// RUNNING in the same process counts, or headroom, the at-limit verdict and
// the ranking keep counting a window the user just excluded until a restart.
// The model slice is read throughout a tick without a lock, so ApplyModels
// never writes it: it queues the new set, and the tick goroutine adopts it at
// the start of its next tick. Nothing but the tick goroutine reads the slice
// either: ApplyThreshold re-pins the poll plan from the settings' Model.
package autoswitch

import "github.com/tyclab/tycswap/internal/settings"

// ApplyModels retargets the counted per-model windows mid-run: "all", a
// comma-separated list of window names, or "" for the 5h and 7d windows only.
// The poll plan is re-pinned at once and the engine is woken, so the next
// tick decides with the new set. Safe to call from any goroutine.
func (e *Engine) ApplyModels(model string) {
	var mp *string
	if model != "" {
		m := model
		mp = &m
	}
	models := settings.ParseModelNames(mp)
	s := e.currentSettings()
	s.Model = mp
	e.settings.Store(&s)
	e.pendingModels.Store(&models)
	e.sw.SetPollPolicyInputs(pollThreshold(s, models), models)
	e.Wake()
}

// adoptPendingModels installs a model set ApplyModels queued. Tick goroutine
// only.
func (e *Engine) adoptPendingModels() {
	p := e.pendingModels.Swap(nil)
	if p == nil {
		return
	}
	e.models = *p
	// Re-run the one-shot unknown-model-name check for the new set.
	e.modelCheckDone = len(e.models) == 0
}
