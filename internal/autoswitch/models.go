package autoswitch

import "github.com/tyclab/tycswap/internal/settings"

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

func (e *Engine) adoptPendingModels() {
	p := e.pendingModels.Swap(nil)
	if p == nil {
		return
	}
	e.models = *p
	e.modelCheckDone = len(e.models) == 0
}
