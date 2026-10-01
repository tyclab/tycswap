// webfacades.go — the façades `tycswap web` hands the dashboard (DESIGN A25):
// settings, the hosted auto-switch engine, transfer, and the account
// operations beyond the frozen Facade.
//
// internal/web owns the consumer interfaces and plain view structs; this file
// binds them to the same packages the CLI commands use (settings, autoswitch
// via autoswitchAdapter, transfer via transferAdapter), so the dashboard and
// `tycswap config|auto|export|import` cannot drift apart.
package cli

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/transfer"
	"github.com/tyclab/tycswap/internal/web"
)

// ---- account operations ----

// webAccounts is web.AccountOps over *core.Switcher. Everything is promoted
// except ApproveAPIKeySwitch: tycswap's switch layer switches onto an API-key
// account without asking, so there is nothing to approve.
type webAccounts struct{ *core.Switcher }

// ApproveAPIKeySwitch is a no-op in tycswap (see webAccounts).
func (webAccounts) ApproveAPIKeySwitch(string) {}

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
// tick in flight finishes first).
const autoStopWait = 30 * time.Second

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
	q := autoswitch.ReadQuarantine(autoswitch.StatePath(a.sw.BackupDir()))
	quarantine := map[string]any{}
	for k, v := range q {
		quarantine[k] = v
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
	}()
	return nil
}

// Stop stops the engine and waits (up to autoStopWait) for its loop to
// return, so a Start right after it can never overlap two engines.
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
		return errors.New("auto-switch was asked to stop but its current tick has not finished yet")
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

func (a *autoFacade) ApplyThreshold(t float64) error {
	if t < 0 || t > 100 {
		return cerr.Validation("threshold must be between 0 and 100")
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

// ---- transfer ----

// transferMu serialises the dashboard's transfer calls: transfer reports
// through its package-level Stderr, which each call borrows.
var transferMu sync.Mutex

// transferFacade is `tycswap export` / `tycswap import` over a file on this
// machine; the lines the CLI would print come back as the result's messages.
type transferFacade struct{ sw *core.Switcher }

func (t transferFacade) Export(path, account string, full bool) (web.TransferResult, error) {
	msgs, err := captureTransfer(func() error {
		return transfer.Export(transferAdapter{t.sw}, path, account, full)
	})
	return web.TransferResult{Path: path, Messages: msgs}, err
}

func (t transferFacade) Import(path string, force bool) (web.TransferResult, error) {
	msgs, err := captureTransfer(func() error {
		return transfer.Import(transferAdapter{t.sw}, path, force)
	})
	return web.TransferResult{Path: path, Messages: msgs}, err
}

func captureTransfer(fn func() error) ([]string, error) {
	transferMu.Lock()
	defer transferMu.Unlock()
	var buf bytes.Buffer
	prev := transfer.Stderr
	transfer.Stderr = &buf
	defer func() { transfer.Stderr = prev }()
	err := fn()
	var msgs []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if line = strings.TrimRight(line, "\r"); strings.TrimSpace(line) != "" {
			msgs = append(msgs, line)
		}
	}
	return msgs, err
}

var (
	_ web.Facade         = (*core.Switcher)(nil)
	_ web.AccountOps     = webAccounts{}
	_ web.SettingsFacade = settingsFacade{}
	_ web.AutoFacade     = (*autoFacade)(nil)
	_ web.TransferFacade = transferFacade{}
	_ autoEngine         = (*autoswitch.Engine)(nil)
)
