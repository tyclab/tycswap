// webfacades.go — the façades `tycswap web` hands the dashboard (DESIGN A25):
// settings, the hosted auto-switch engine, and the account operations beyond
// the frozen Facade.
//
// internal/web owns the consumer interfaces and plain view structs; this file
// binds them to the same packages the CLI commands use (settings, autoswitch
// via autoswitchAdapter), so the dashboard and `tycswap config|auto` cannot
// drift apart.
package cli

import (
	"sync"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

// ---- settings ----

// settingsFacade is `tycswap config` over the backup root's settings.json.
type settingsFacade struct{ root string }

func (f settingsFacade) Effective() []web.SettingView {
	eff := settings.EffectiveSettings(f.root)
	out := make([]web.SettingView, 0, len(eff))
	for _, e := range eff {
		v := web.SettingView{
			Key:         e.Spec.Dotted(),
			Kind:        string(e.Spec.Kind),
			Value:       e.Value,
			Default:     e.Spec.Default,
			IsDefault:   !e.IsSet,
			Choices:     e.Spec.Choices,
			Description: e.Spec.Help,
		}
		if e.Spec.Kind == settings.KindFloat || e.Spec.Kind == settings.KindInt {
			lo, hi := e.Spec.Lo, e.Spec.Hi
			v.Min, v.Max = &lo, &hi
		}
		out = append(out, v)
	}
	return out
}

// Set validates before it writes, so an unknown key or a value out of range
// is the caller's mistake (a validation error, 400 on the API), not a config
// failure (500).
func (f settingsFacade) Set(dotted, raw string) (any, error) {
	spec, err := settings.SpecFor(dotted)
	if err != nil {
		return nil, cerr.Validation("%s", err.Error())
	}
	if _, err := settings.ParseSettingValue(spec, raw); err != nil {
		return nil, cerr.Validation("%s", err.Error())
	}
	return settings.SetSetting(f.root, dotted, raw)
}

func (f settingsFacade) Unset(dotted string) (bool, error) {
	if _, err := settings.SpecFor(dotted); err != nil {
		return false, cerr.Validation("%s", err.Error())
	}
	return settings.UnsetSetting(f.root, dotted)
}

// ---- auto-switch engine host ----

// autoEventRing caps the retained event log.
const autoEventRing = 200

// autoStopWait bounds how long Stop waits for the engine's loop to return (a
// tick in flight finishes first). It is short because the dashboard calls
// Stop while holding its mutation lock, which every other action waits on.
// Past it Stop reports the engine as still stopping (a 409 on the API, with
// the engine already marked not running), and Start keeps refusing until the
// loop has returned, so two engines still never overlap. A var so tests can
// shorten it.
var autoStopWait = 2 * time.Second

// autoEngine is what autoFacade drives; *autoswitch.Engine satisfies it.
type autoEngine interface {
	RunLoop() int
	Stop()
	Wake()
	ApplyThreshold(float64)
	ApplyModels(string)
}

// autoFacade hosts one autoswitch engine inside the dashboard process, the
// way the TUI's Auto screen does, and streams its events.
type autoFacade struct {
	sw  *core.Switcher
	clk clock.Clock
	// newEngine builds the engine; tests substitute one.
	newEngine func(s settings.AutoSwitchSettings, onEvent func(autoswitch.Event), dryRun bool) autoEngine

	mu        sync.Mutex
	engine    autoEngine
	done      chan struct{} // closed when the current engine's RunLoop returns
	running   bool
	dryRun    bool
	startedAt *float64
	threshold float64
	settings  settings.AutoSwitchSettings
	events    []web.AutoEventView
	stream    chan web.AutoEventView
}

func newAutoFacade(sw *core.Switcher) *autoFacade {
	a := &autoFacade{sw: sw, clk: sw.Clk, stream: make(chan web.AutoEventView, 64)}
	a.newEngine = func(s settings.AutoSwitchSettings, onEvent func(autoswitch.Event), dryRun bool) autoEngine {
		return autoswitch.NewEngine(autoswitchAdapter{sw}, s, onEvent, dryRun,
			autoswitch.WithOAuthClient(sw.OAuth),
			autoswitch.WithLogger(sw.Log),
			autoswitch.WithClock(sw.Clk),
		)
	}
	return a
}

// Events is the channel the web server fans out as `event: auto`.
func (a *autoFacade) Events() <-chan web.AutoEventView { return a.stream }

func (a *autoFacade) View() web.AutoView {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.settings
	threshold := a.threshold
	if !a.running {
		s = settings.Load(a.sw.BackupDir())
		threshold = s.Threshold
	}
	// Each quarantine entry carries its reason and the engine's RFC3339 "at"
	// stamp, so the page can show since when a slot has been held out.
	quarantine := map[string]any{}
	for k, e := range autoswitch.ReadQuarantineEntries(autoswitch.StatePath(a.sw.BackupDir())) {
		entry := map[string]any{"reason": e.Reason}
		if e.At != "" {
			entry["at"] = e.At
		}
		quarantine[k] = entry
	}
	events := make([]web.AutoEventView, len(a.events)) // never nil: the UI wants []
	copy(events, a.events)
	return web.AutoView{
		Available:  true,
		Running:    a.running,
		DryRun:     a.dryRun,
		StartedAt:  a.startedAt,
		Threshold:  threshold,
		Settings:   settingsMap(s),
		Events:     events,
		Quarantine: quarantine,
	}
}

// settingsMap renders the settings the way `tycswap config` names them.
func settingsMap(s settings.AutoSwitchSettings) map[string]any {
	m := map[string]any{
		"autoswitch.threshold":             s.Threshold,
		"autoswitch.intervalSeconds":       s.IntervalSeconds,
		"autoswitch.codexEnabled":          s.CodexEnabled,
		"autoswitch.codexThreshold":        s.CodexThreshold,
		"autoswitch.cooldownSeconds":       s.CooldownSeconds,
		"autoswitch.hysteresisPct":         s.HysteresisPct,
		"autoswitch.strategy":              s.Strategy,
		"autoswitch.includeApiKeyAccounts": s.IncludeAPIKeyAccounts,
		"autoswitch.unhealthyTicks":        s.UnhealthyTicks,
		"autoswitch.model":                 nil,
	}
	if s.Model != nil {
		m["autoswitch.model"] = *s.Model
	}
	return m
}

func (a *autoFacade) Start(dryRun bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return cerr.Validation("auto-switch is already running")
	}
	if a.done != nil {
		select {
		case <-a.done:
		default:
			// A Stop timed out waiting: never run two engines side by side.
			return cerr.Validation("auto-switch is still stopping; try again in a moment")
		}
	}
	s := settings.Load(a.sw.BackupDir())
	engine := a.newEngine(s, a.onEvent, dryRun)
	now := clock.Seconds(a.clk)
	done := make(chan struct{})
	a.engine, a.done, a.running, a.dryRun, a.startedAt, a.threshold, a.settings = engine, done, true, dryRun, &now, s.Threshold, s
	go func() {
		defer close(done)
		engine.RunLoop()
		a.mu.Lock()
		if a.engine == engine {
			a.running, a.engine, a.startedAt = false, nil, nil
		}
		a.mu.Unlock()
		// The engine pinned the usage poll plan to its threshold and models
		// (NewEngine, ApplyThreshold, ApplyModels). Un-pin once its loop has
		// returned, as the TUI's Auto screen does on exit, or the stopped
		// engine's policy would keep steering the dashboard's polling. No
		// newer engine can have pinned its own yet: Start refuses until this
		// goroutine closes done.
		a.sw.ClearPollPolicyInputs()
	}()
	return nil
}

// Stop asks the engine to stop and waits (up to autoStopWait) for its loop
// to return, so a Start right after it can never overlap two engines. When
// a tick in flight outlasts the wait, the engine is stopping all the same:
// Stop reports it with a lock-kind error (409) and Start refuses until the
// loop has returned.
func (a *autoFacade) Stop() error {
	a.mu.Lock()
	if !a.running || a.engine == nil {
		a.mu.Unlock()
		return cerr.Validation("auto-switch is not running")
	}
	engine, done := a.engine, a.done
	a.running, a.engine, a.startedAt = false, nil, nil
	a.mu.Unlock()
	engine.Stop()
	select {
	case <-done:
		return nil
	case <-time.After(autoStopWait):
		return cerr.Lock("auto-switch is stopping; its current tick has not finished yet — try again in a moment")
	}
}

func (a *autoFacade) Wake() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running || a.engine == nil {
		return cerr.Validation("auto-switch is not running")
	}
	a.engine.Wake()
	return nil
}

// ApplyThreshold retargets the running engine's threshold. The bounds are the
// autoswitch.threshold spec's (50–99.9), as for `tycswap config` and the TUI.
func (a *autoFacade) ApplyThreshold(t float64) error {
	spec, err := settings.SpecFor("autoswitch.threshold")
	if err != nil {
		return err
	}
	if t != t || t < spec.Lo || t > spec.Hi {
		return cerr.Validation("threshold must be between %g and %g", spec.Lo, spec.Hi)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running || a.engine == nil {
		return cerr.Validation("auto-switch is not running")
	}
	a.engine.ApplyThreshold(t)
	a.threshold = t
	return nil
}

// ApplyModels retargets the running engine's per-model windows. The
// dashboard's "Count model limits" switch saves the setting AND calls this, so
// headroom, the at-limit verdict and the ranking change at once instead of
// waiting for a restart.
func (a *autoFacade) ApplyModels(model string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running || a.engine == nil {
		return cerr.Validation("auto-switch is not running")
	}
	a.engine.ApplyModels(model)
	if model == "" {
		a.settings.Model = nil
	} else {
		m := model
		a.settings.Model = &m
	}
	return nil
}

// onEvent is the engine's sink: ring-buffer the event and offer it to the
// stream without ever blocking the engine goroutine.
func (a *autoFacade) onEvent(ev autoswitch.Event) {
	fields := ev.JSON()
	view := web.AutoEventView{At: clock.Seconds(a.clk), Kind: ev.Kind(), Message: ev.Human(), Fields: fields}
	for _, k := range []string{"to", "account", "email", "from"} {
		if s, ok := fields[k].(string); ok && s != "" {
			view.Account = s
			break
		}
	}
	a.mu.Lock()
	a.events = append(a.events, view)
	if len(a.events) > autoEventRing {
		a.events = a.events[len(a.events)-autoEventRing:]
	}
	a.mu.Unlock()
	select {
	case a.stream <- view:
	default:
	}
}

// waitStopped blocks until the most recently started engine goroutine has
// returned, or the timeout passes. Tests use it so no tick can outlive the
// test's temp dir.
func (a *autoFacade) waitStopped(timeout time.Duration) bool {
	a.mu.Lock()
	done := a.done
	a.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

var (
	_ web.Facade         = (*core.Switcher)(nil)
	_ web.AccountOps     = (*core.Switcher)(nil)
	_ web.SettingsFacade = settingsFacade{}
	_ web.AutoFacade     = (*autoFacade)(nil)
	_ autoEngine         = (*autoswitch.Engine)(nil)
)
