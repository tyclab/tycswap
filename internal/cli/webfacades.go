// webfacades.go — the façades `tycswap web` hands the dashboard (DESIGN A26):
// settings, the hosted auto-switch engines, the account operations beyond
// the frozen Facade, and the Codex account operations (A47).
//
// internal/web owns the consumer interfaces and plain view structs; this file
// binds them to the same packages the CLI commands use (settings, autoswitch
// via autoswitchAdapter, the Codex switcher and engine constructor), so the
// dashboard and `tycswap config|auto|codex` cannot drift apart.
package cli

import (
	"context"
	"sync"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
	codexauto "github.com/tyclab/tycswap/internal/codex/autoswitch"
	codexswitcher "github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/reporting"
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

// ---- Codex accounts ----

// codexOps is web.CodexOps over the Codex switcher: the verbs `tycswap codex`
// and the terminal dashboard run, each holding the Codex store's lock from
// start to end (the switcher takes it, DESIGN A48; a store that stays busy is
// a lock error, 409 on the API).
type codexOps struct{ sw *codexswitcher.Switcher }

// newCodexOps is the dashboard's Codex façade; nil without a Codex switcher
// (no Codex accounts at launch), so the Codex routes answer 503.
func newCodexOps(sw *codexswitcher.Switcher) web.CodexOps {
	if sw == nil {
		return nil
	}
	return codexOps{sw}
}

func (o codexOps) SwitchTo(id string) (map[string]any, error) {
	res, err := o.sw.SwitchTo(context.Background(), id)
	if err != nil {
		return nil, err
	}
	pids := res.RunningPIDs
	if pids == nil {
		pids = []int{}
	}
	return map[string]any{"number": res.Number, "email": res.Email, "runningPids": pids, "alreadyActive": res.AlreadyActive}, nil
}

func (o codexOps) SetAccountDisabled(id string, disabled bool) error {
	_, err := o.sw.SetAccountDisabled(id, disabled)
	return err
}

// RemoveAccount is `tycswap codex remove -y`. The switcher's warning about an
// active slot goes to its discarded stdout: the page's modal has said it.
func (o codexOps) RemoveAccount(id string) error {
	removed, err := o.sw.Remove(id, true)
	if err != nil {
		return err
	}
	if !removed {
		return cerr.AccountNotFound("Codex account %s was not removed: it is no longer stored", id)
	}
	return nil
}

func (o codexOps) AddCurrent() (map[string]any, error) {
	slot, err := o.sw.Add(context.Background(), "")
	if err != nil {
		return nil, err
	}
	return map[string]any{"number": slot.Number, "email": slot.Email}, nil
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
// way the TUI's Auto screen does, and streams its events. Beside it runs the
// Codex engine when this machine has Codex accounts, as in `tycswap auto`
// (DESIGN A47): one Start and one Stop drive both, and both feed one event
// log.
type autoFacade struct {
	sw  *core.Switcher
	clk clock.Clock
	// codexSw is the Codex switcher; nil on an install that had no Codex
	// accounts at launch, which then hosts the Claude engine alone.
	codexSw *codexswitcher.Switcher
	// newEngine builds the engine; tests substitute one.
	newEngine func(s settings.AutoSwitchSettings, onEvent func(autoswitch.Event), dryRun bool) autoEngine
	// newCodexEngine builds the Codex engine at Start, nil for none: the
	// constructor `tycswap auto` uses. Tests substitute one.
	newCodexEngine func(s settings.AutoSwitchSettings) *codexauto.AutoSwitcher
	// statePath is where the app records the user's on/off choice (DESIGN
	// A43); "" in `web`, which neither records nor resumes.
	statePath string

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

	codexEngine *codexauto.AutoSwitcher // this run's Codex engine; nil when none runs
	stopCodex   func()                  // cancels the Codex loop and waits for it
	codexDone   chan struct{}           // closed when a stopped Codex loop has returned
	codexLast   *web.CodexTickView      // the Codex engine's last tick, kept across runs
}

// newAutoFacade builds the host over sw and codexSw (nil without Codex
// accounts). The engine comes from newAutoEngine, the seam the TUI's Auto
// screen uses too (tuiwire.go), so both hosts wire the switcher's OAuth
// client, logger and clock the same way; the Codex engine from
// newCodexAutoEngineFor, as `tycswap auto` builds it.
func newAutoFacade(sw *core.Switcher, codexSw *codexswitcher.Switcher) *autoFacade {
	a := &autoFacade{sw: sw, clk: sw.Clk, codexSw: codexSw, stream: make(chan web.AutoEventView, 64)}
	a.newEngine = func(s settings.AutoSwitchSettings, onEvent func(autoswitch.Event), dryRun bool) autoEngine {
		return newAutoEngine(sw, s, onEvent, dryRun, sw.OAuth)
	}
	a.newCodexEngine = func(s settings.AutoSwitchSettings) *codexauto.AutoSwitcher {
		return newCodexAutoEngineFor(codexSw, s)
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
		threshold = s.SevenDayThreshold
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
	v := web.AutoView{
		Available:  true,
		Running:    a.running,
		DryRun:     a.dryRun,
		StartedAt:  a.startedAt,
		Threshold:  threshold,
		Settings:   settings.ValuesOf(s),
		Events:     events,
		Quarantine: quarantine,
	}
	if a.codexSw != nil {
		// The Codex bar is the one the engine started with (or would start
		// with): the slider moves the Claude 7d bar only.
		v.Codex = &web.CodexAutoView{
			Enabled:   s.CodexEnabled,
			Running:   a.running && a.codexEngine != nil,
			Threshold: codexThreshold(s),
			LastTick:  a.codexLast,
		}
	}
	return v
}

func (a *autoFacade) Start(dryRun bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return cerr.Validation("auto-switch is already running")
	}
	if stillRunning(a.done) || stillRunning(a.codexDone) {
		// A Stop timed out waiting: never run two engines side by side.
		return cerr.Validation("auto-switch is still stopping; try again in a moment")
	}
	s := settings.Load(a.sw.BackupDir())
	engine := a.newEngine(s, a.onEvent, dryRun)
	now := clock.Seconds(a.clk)
	done := make(chan struct{})
	a.engine, a.done, a.running, a.dryRun, a.startedAt, a.threshold, a.settings = engine, done, true, dryRun, &now, s.SevenDayThreshold, s
	if !dryRun {
		a.remember(true)
	}
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
	// The Codex engine rides beside it as in `tycswap auto`: the same
	// constructor, a tick at once and then every interval on its own
	// goroutine, its bar fixed for this run.
	codexEngine := a.newCodexEngine(s)
	a.codexEngine, a.codexDone = codexEngine, nil
	a.stopCodex = startCodexLoop(codexEngine != nil, time.Duration(s.IntervalSeconds*float64(time.Second)), func(ctx context.Context) {
		a.codexTick(ctx, codexEngine, dryRun)
	})
	return nil
}

// stillRunning reports whether done is a loop's channel not yet closed.
func stillRunning(done chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
		return true
	}
}

// Stop asks the engine to stop, cancels the Codex loop, and waits (up to
// autoStopWait for both) for the loops to return, so a Start right after it
// can never overlap two engines. When a tick in flight outlasts the wait,
// the engines are stopping all the same: Stop reports it with a lock-kind
// error (409) and Start refuses until both loops have returned.
func (a *autoFacade) Stop() error {
	a.mu.Lock()
	if !a.running || a.engine == nil {
		a.mu.Unlock()
		return cerr.Validation("auto-switch is not running")
	}
	engine, done, stopCodex := a.engine, a.done, a.stopCodex
	codexDone := make(chan struct{})
	a.running, a.engine, a.startedAt = false, nil, nil
	a.codexEngine, a.stopCodex, a.codexDone = nil, nil, codexDone
	if !a.dryRun {
		a.remember(false)
	}
	a.mu.Unlock()
	engine.Stop()
	go func() {
		defer close(codexDone)
		stopCodex()
	}()
	wait := time.After(autoStopWait)
	for _, loop := range []chan struct{}{done, codexDone} {
		select {
		case <-loop:
		case <-wait:
			return cerr.Lock("auto-switch is stopping; its current tick has not finished yet — try again in a moment")
		}
	}
	return nil
}

// remember records the user's choice for the next app start (A43). Called
// with a.mu held, from Start and Stop only: an engine that ends by itself, or
// a process that quits, leaves the choice as the user made it.
func (a *autoFacade) remember(on bool) {
	if a.statePath == "" {
		return
	}
	if err := updateAppState(a.statePath, func(st *appState) { st.AutoSwitch = on }); err != nil && a.sw.Store != nil && a.sw.Log != nil {
		a.sw.Log.Warning("could not record the auto-switch choice: " + err.Error())
	}
}

// stopKeepingChoice ends a running engine as the app exits, the way `tycswap
// web` does, without recording "off": quitting the app is not turning
// auto-switch off, and the next start resumes it (A43).
func (a *autoFacade) stopKeepingChoice() {
	a.mu.Lock()
	a.statePath = ""
	a.mu.Unlock()
	if a.View().Running {
		_ = a.Stop()
	}
}

// resume starts the engine when the user left it on (A43). started is false
// when there was nothing to resume.
func (a *autoFacade) resume() (started bool, err error) {
	if a.statePath == "" || !loadAppState(a.statePath).AutoSwitch {
		return false, nil
	}
	if err := a.Start(false); err != nil {
		return false, err
	}
	return true, nil
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

// ApplyThreshold retargets the running engine's 7d bar, the one the slider
// moves (DESIGN A34). The bounds are the autoswitch.sevenDayThreshold spec's
// (50–100), as for `tycswap config` and the TUI.
func (a *autoFacade) ApplyThreshold(t float64) error {
	spec, err := settings.SpecFor("autoswitch.sevenDayThreshold")
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
	a.publish(view)
}

// codexTick runs one Codex tick and records it as the engine's last. Like
// `tycswap auto`, only a switch or an error reaches the event log, and every
// tick under dry-run (codexTickShown).
func (a *autoFacade) codexTick(ctx context.Context, eng *codexauto.AutoSwitcher, dryRun bool) {
	tick := eng.Tick(ctx, dryRun)
	at := clock.Seconds(a.clk)
	last := &web.CodexTickView{At: at, Outcome: tick.Outcome, Detail: tick.Detail, RunningPIDs: tick.RunningPIDs}
	if last.RunningPIDs == nil {
		last.RunningPIDs = []int{}
	}
	if tick.SwitchedTo != "" {
		to := tick.SwitchedTo
		last.SwitchedTo = &to
	}
	a.mu.Lock()
	a.codexLast = last
	a.mu.Unlock()
	if codexTickShown(tick, dryRun) {
		a.publish(codexEvent(at, tick))
	}
}

// codexEvent is a Codex tick as an engine event: the Claude engine's kind
// for the outcome, so the page colours and filters it as it does those
// (switched → switch, error → error, blocked → all-exhausted, else
// no-switch), Tick.Human as the message, and the `auto --json` fields.
func codexEvent(at float64, tick codexauto.Tick) web.AutoEventView {
	kind := "no-switch"
	switch tick.Outcome {
	case codexauto.OutcomeSwitched:
		kind = "switch"
	case codexauto.OutcomeError:
		kind = "error"
	case codexauto.OutcomeBlocked:
		kind = "all-exhausted"
	}
	return web.AutoEventView{
		At: at, Kind: kind, Message: tick.Human(), Account: tick.SwitchedTo,
		Fields: codexTickFields(tick), Provider: reporting.ProviderCodex,
	}
}

// publish ring-buffers an event and offers it to the stream without ever
// blocking the engine goroutine that produced it.
func (a *autoFacade) publish(view web.AutoEventView) {
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

var (
	_ web.Facade         = (*core.Switcher)(nil)
	_ web.AccountOps     = (*core.Switcher)(nil)
	_ web.CodexOps       = codexOps{}
	_ web.SettingsFacade = settingsFacade{}
	_ web.AutoFacade     = (*autoFacade)(nil)
	_ autoEngine         = (*autoswitch.Engine)(nil)
)
