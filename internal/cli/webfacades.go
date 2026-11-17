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
	"strconv"
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
			Label:       e.Spec.Label(),
			Kind:        string(e.Spec.Kind),
			Value:       e.Value,
			Default:     e.Spec.Default,
			IsDefault:   !e.IsSet,
			Choices:     e.Spec.Choices,
			Description: e.Spec.Help,
			Notice:      e.Notice(),
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
}

// autoFacade hosts one autoswitch engine, and the Codex engine beside it on a
// machine with Codex accounts (DESIGN A47), in the dashboard process; both
// feed the tray's event stream. Start and Stop turn autoswitch.enabled on and
// off and the engine idles while it is off (A83); no dry run (A93).
type autoFacade struct {
	// managedBy names an external tick scheduler (TYCSWAP_AUTO_MANAGED_BY): this
	// host then runs no real engine, and Start and Stop only write the setting (A83).
	managedBy string
	sw        *core.Switcher
	clk       clock.Clock
	// codexSw is the Codex switcher; nil on an install that had no Codex
	// accounts at launch, which then hosts the Claude engine alone.
	codexSw *codexswitcher.Switcher
	// newEngine builds the engine; tests substitute one.
	newEngine func(s settings.AutoSwitchSettings, onEvent func(autoswitch.Event)) autoEngine
	// newCodexEngine builds the Codex engine at Start, nil for none: the
	// constructor `tycswap auto` uses. Tests substitute one.
	newCodexEngine func(s settings.AutoSwitchSettings) *codexauto.AutoSwitcher

	mu        sync.Mutex
	engine    autoEngine
	done      chan struct{} // closed when the current engine's RunLoop returns
	running   bool
	startedAt *float64
	settings  settings.AutoSwitchSettings
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
	a.newEngine = func(s settings.AutoSwitchSettings, onEvent func(autoswitch.Event)) autoEngine {
		return newAutoEngine(sw, s, onEvent, false, sw.OAuth)
	}
	a.newCodexEngine = func(s settings.AutoSwitchSettings) *codexauto.AutoSwitcher {
		return newCodexAutoEngineFor(codexSw, s)
	}
	return a
}

// Events is the channel the web server fans out as `event: auto`.
func (a *autoFacade) Events() <-chan web.AutoEventView { return a.stream }

// View is the engine's state and what it counts now; the policy behind the
// count scans processes, so it runs outside the host's lock.
func (a *autoFacade) View() web.AutoView {
	v, s, hosted := a.view()
	v.LastTickAt = autoswitch.ReadLastTick(autoswitch.StatePath(a.sw.BackupDir()))
	v.Counting = a.counting(s, hosted)
	return v
}

// counting is what the engine counts now (DESIGN A91): a set autoswitch.model,
// the configured models of an engine hosted here (ENG-8), or what the
// default-session policy derives for the scheduled ticks.
func (a *autoFacade) counting(s settings.AutoSwitchSettings, hosted bool) *web.CountingView {
	c := &web.CountingView{Mode: "sessions", Models: settings.ParseModelNames(s.Model)}
	switch {
	case s.Model != nil:
		c.Mode, c.Setting = "setting", *s.Model
	case hosted:
		c.Mode = "hosted"
	default:
		r := reporting.DefaultSessionPolicyResult(a.sw.Store, s, a.clk.Now())
		c.Models, c.Sessions, c.Unservable = settings.ParseModelNames(r.Settings.Model), r.Sessions, r.Unservable
		if data, _ := a.sw.ReadSequence(); data != nil && r.Reserved != nil {
			for _, n := range data.Sequence {
				if num := strconv.Itoa(n); r.Reserved(num) {
					c.Reserved = append(c.Reserved, num)
				}
			}
		}
	}
	for _, list := range []*[]string{&c.Sessions, &c.Unservable, &c.Reserved} {
		if *list == nil {
			*list = []string{}
		}
	}
	return c
}

// view is the engine host's own state, the settings its engine decides with
// and whether one is hosted here.
func (a *autoFacade) view() (web.AutoView, settings.AutoSwitchSettings, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fresh := settings.Load(a.sw.BackupDir())
	s := a.settings
	if !a.running {
		s = fresh
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
	// An engine whose ticks all report disabled is not running to the user
	// (DESIGN A83).
	live := a.running && fresh.Enabled
	owned := a.managedBy != "" && fresh.Enabled
	v := web.AutoView{
		Available:  true,
		Owner:      a.managedBy,
		Hosted:     a.running,
		Running:    live || owned,
		StartedAt:  a.startedAt,
		Threshold:  s.SevenDayThreshold,
		Settings:   settings.ValuesOf(s),
		Quarantine: quarantine,
	}
	if a.codexSw != nil && len(a.codexSw.Store().Slots()) > 0 {
		// The Codex bar is the one the engine started (or would start) with.
		v.Codex = &web.CodexAutoView{
			Enabled:   s.CodexEnabled,
			Running:   (live && a.codexEngine != nil) || (owned && fresh.CodexEnabled),
			Threshold: codexThreshold(s),
			LastTick:  a.codexLast,
		}
	}
	return v, s, a.running
}

// Start hosts the engine unless a scheduler runs the ticks, then turns it on.
func (a *autoFacade) Start() error {
	if a.managedBy == "" {
		if err := a.startHosted(); err != nil {
			return err
		}
	}
	if err := a.setEnabled(true); err != nil {
		return err
	}
	// Tick now, not an interval later; the Codex loop follows at its next round.
	a.mu.Lock()
	if a.running && a.engine != nil {
		a.engine.Wake()
	}
	a.mu.Unlock()
	return nil
}

// startHosted runs the engines in this process; it never writes the setting.
func (a *autoFacade) startHosted() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return nil // hosted already
	}
	if stillRunning(a.done) || stillRunning(a.codexDone) {
		// A Stop timed out waiting: never run two engines side by side.
		return cerr.Validation("auto-switch is still stopping; try again in a moment")
	}
	s := settings.Load(a.sw.BackupDir())
	engine := a.newEngine(s, a.onEvent)
	now := clock.Seconds(a.clk)
	done := make(chan struct{})
	a.engine, a.done, a.running, a.startedAt, a.settings = engine, done, true, &now, s
	go func() {
		defer close(done)
		engine.RunLoop()
		a.mu.Lock()
		if a.engine == engine {
			a.running, a.engine, a.startedAt = false, nil, nil
		}
		a.mu.Unlock()
		// Un-pin once the loop returned, or the stopped engine keeps steering polling; Start refuses until done closes.
		a.sw.ClearPollPolicyInputs()
	}()
	// The Codex engine rides beside it as in `tycswap auto`: the same
	// constructor, a tick at once and then every interval on its own
	// goroutine, its bar fixed for this run.
	codexEngine := a.newCodexEngine(s)
	a.codexEngine, a.codexDone = codexEngine, nil
	a.stopCodex = startCodexLoop(true, time.Duration(s.IntervalSeconds*float64(time.Second)), func(ctx context.Context) {
		a.codexRound(ctx, codexEngine)
	})
	return nil
}

// codexRound is one round of the hosted loop. Retiring a former session group
// or profile never moves the default login, so it runs whatever the setting; the Codex
// tick reads the setting, as the Claude tick does (DESIGN A83).
func (a *autoFacade) codexRound(ctx context.Context, eng *codexauto.AutoSwitcher) {
	retireGroups(a.sw, a.onEvent, false)
	retireProfiles(a.sw, false)
	if eng != nil && autoSwitchOn(a.sw.BackupDir()) {
		a.codexTick(ctx, eng)
	}
}

// setEnabled writes and logs autoswitch.enabled, the on/off every tick reads.
func (a *autoFacade) setEnabled(on bool) error {
	if _, err := settings.SetSetting(a.sw.BackupDir(), "autoswitch.enabled", strconv.FormatBool(on)); err != nil {
		return err
	}
	if a.sw.Log != nil {
		a.sw.Log.Info("Auto-switch turned " + onOffWord(on) + " (app)")
	}
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

// Stop turns auto-switch off at once and leaves the engine hosted, idling;
// with nothing hosted, no owner and the setting off there is nothing to stop.
func (a *autoFacade) Stop() error {
	a.mu.Lock()
	hosted := a.running && a.engine != nil
	a.mu.Unlock()
	if !hosted && a.managedBy == "" && !autoSwitchOn(a.sw.BackupDir()) {
		return cerr.Validation("auto-switch is not running")
	}
	return a.setEnabled(false)
}

// stopLocked, called with a.mu held on a running engine, releases it, stops
// both loops and waits (up to autoStopWait) for them, so a later start never
// overlaps two engines. A tick that outlasts the wait is a
// lock-kind error (409); Start refuses until both loops have returned.
func (a *autoFacade) stopLocked() error {
	engine, done, stopCodex := a.engine, a.done, a.stopCodex
	codexDone := make(chan struct{})
	a.running, a.engine, a.startedAt = false, nil, nil
	a.codexEngine, a.stopCodex, a.codexDone = nil, nil, codexDone
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

// stopKeepingChoice ends the hosted engines as the app or `tycswap web`
// exits, without writing "off": quitting is not turning auto-switch off, and
// the next app start resumes it (A43, A83).
func (a *autoFacade) stopKeepingChoice() { _ = a.stopHosted() }

// stopHosted ends the hosted engines without writing the setting.
func (a *autoFacade) stopHosted() error {
	a.mu.Lock()
	if !a.running || a.engine == nil {
		a.mu.Unlock()
		return cerr.Validation("auto-switch is not running")
	}
	return a.stopLocked()
}

// resume hosts the engines from the app's start whatever the setting, so a
// shell's `tycswap auto on` takes effect at once (A43, A83); an external
// scheduler's ticks need no host here.
func (a *autoFacade) resume() (started bool, err error) {
	if a.managedBy != "" {
		return false, nil
	}
	if err := a.startHosted(); err != nil {
		return false, err
	}
	return true, nil
}

// onEvent is the engine's sink.
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

// codexTick runs one Codex tick, records it as the last, and streams it when
// codexTickShown, as `tycswap auto` prints it.
func (a *autoFacade) codexTick(ctx context.Context, eng *codexauto.AutoSwitcher) {
	tick := eng.Tick(ctx, false)
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
	if codexTickShown(tick, false) {
		a.publish(codexEvent(at, tick))
	}
}

// codexEvent is a Codex tick as an engine event with the Claude engine's kind
// for its outcome (switched → switch, error → error, blocked → all-exhausted,
// else no-switch), Tick.Human and the `auto --json` fields.
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

// publish offers an event to the stream without blocking its producer.
func (a *autoFacade) publish(view web.AutoEventView) {
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
