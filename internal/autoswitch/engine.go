package autoswitch

import (
	"math"
	"math/rand/v2"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/settings"
)

type TickOutcome int

const (
	Switched TickOutcome = 0 // a switch happened (or would, in dry-run)
	Error    TickOutcome = 1 // network trouble, lock contention, transient freshen failure
	NoAction TickOutcome = 2 // nothing to do (below threshold, cooldown, idle, ...)
	Blocked  TickOutcome = 3 // wanted to switch but no viable target / all exhausted
)

// Sleeper is the timer seam for the inter-tick wait. clock.System and clock.Fake
// both satisfy it, but the loop needs a truly blocking After, so tests inject
// their own controllable sleeper.
type Sleeper interface {
	After(d time.Duration) <-chan time.Time
}

// realSleeper is the production timer (a truly blocking time.After).
type realSleeper struct{}

func (realSleeper) After(d time.Duration) <-chan time.Time { return time.After(d) }

type Engine struct {
	sw            Switcher
	settings      atomic.Pointer[settings.AutoSwitchSettings]
	models        []string
	pendingModels atomic.Pointer[[]string]
	onEvent       func(Event)
	dryRun        bool

	clk       clock.Clock
	sleeper   Sleeper
	rng       func() float64
	oauth     oauth.Client
	log       *logging.Logger
	statePath string
	lockPath  string

	stopCh   chan struct{}
	stopOnce sync.Once
	wakeCh   chan struct{}

	// Touched only from the single tick goroutine.
	unhealthyTicks  int
	sleepUntilTS    *float64
	blockedWaitLong bool
	idleHoldSince   *float64
	idleHoldSlow    bool
	modelCheckDone  bool
}

// Option customizes engine construction (clock, sleeper, rng, oauth client,
// logger, state path). The DESIGN §2.18 NewEngine positional prefix
// (sw, settings, onEvent, dryRun) is preserved; these options carry the
// injectable seams Python passed as __init__ keyword args (state_path, clock)
// plus the oauth client and jitter rng the tests need.
type Option func(*Engine)

// WithClock injects the wall clock used for persisted lastSwitchAt/cooldown,
// near-expiry math, reset math, and event timestamps (default clock.System).
func WithClock(c clock.Clock) Option { return func(e *Engine) { e.clk = c } }

// WithSleeper injects the inter-tick timer seam (default a blocking time.After).
func WithSleeper(s Sleeper) Option { return func(e *Engine) { e.sleeper = s } }

func WithRNG(rng func() float64) Option { return func(e *Engine) { e.rng = rng } }

func WithOAuthClient(c oauth.Client) Option { return func(e *Engine) { e.oauth = c } }

// WithLogger injects the logger for the idle-hold-exceeded warning and the
// uuid-backfill debug line (default nil = no-op).
func WithLogger(l *logging.Logger) Option { return func(e *Engine) { e.log = l } }

func WithStatePath(path string) Option { return func(e *Engine) { e.statePath = path } }

// NewEngine builds an engine. settings is the frozen policy value; models are
// parsed once here (05§8) and pinned via SetPollPolicyInputs so the collector
// plans on the same threshold/models the engine decides with.
func NewEngine(sw Switcher, s settings.AutoSwitchSettings, onEvent func(Event), dryRun bool, opts ...Option) *Engine {
	e := &Engine{
		sw:      sw,
		models:  settings.ParseModelNames(s.Model),
		onEvent: onEvent,
		dryRun:  dryRun,
		clk:     clock.System{},
		sleeper: realSleeper{},
		rng:     rand.Float64,
		stopCh:  make(chan struct{}),
		wakeCh:  make(chan struct{}, 1),
	}
	sv := s
	e.settings.Store(&sv)
	for _, o := range opts {
		o(e)
	}
	if e.oauth == nil {
		e.oauth = oauth.NewHTTPClient()
	}
	if e.statePath == "" {
		e.statePath = StatePath(sw.BackupDir())
	}
	e.lockPath = filepath.Join(filepath.Dir(e.statePath), ".autoswitch_state.lock")
	// Poll plans must key on the same bars/models the engine decides with.
	sw.SetPollPolicyInputs(pollThreshold(s, e.models), e.models)
	e.modelCheckDone = len(e.models) == 0
	return e
}

func (e *Engine) currentSettings() settings.AutoSwitchSettings {
	return *e.settings.Load()
}

// ApplyThreshold moves only the 7d bar (DESIGN A34); it pins models from settings, never e.models (tick goroutine only).
func (e *Engine) ApplyThreshold(threshold float64) {
	s := e.currentSettings()
	s.SevenDayThreshold = threshold
	e.settings.Store(&s)
	models := settings.ParseModelNames(s.Model)
	e.sw.SetPollPolicyInputs(pollThreshold(s, models), models)
}

// pollThreshold is the lowest bar in force: the planner compares it against the binding headroom.
func pollThreshold(s settings.AutoSwitchSettings, models []string) float64 {
	lowest := math.Min(s.SevenDayThreshold, s.FiveHourThreshold)
	if len(models) > 0 {
		lowest = math.Min(lowest, s.ModelThreshold)
	}
	return lowest
}

// Stop is latching and idempotent: safe before the loop starts and more than once (DESIGN §4 row 2).
func (e *Engine) Stop() {
	e.stopOnce.Do(func() { close(e.stopCh) })
	// Non-blocking wake so a sleeping loop returns immediately.
	select {
	case e.wakeCh <- struct{}{}:
	default:
	}
}

func (e *Engine) Wake() {
	select {
	case e.wakeCh <- struct{}{}:
	default:
	}
}

// emit does not recover callback panics: a broken frontend should fail loudly.
func (e *Engine) emit(ev Event) { e.onEvent(ev) }

// nowSeconds returns wall time as fractional Unix seconds (Python self.clock()).
func (e *Engine) nowSeconds() float64 { return clock.Seconds(e.clk) }

// nowISO returns _now_iso(): the injected wall clock as RFC3339 seconds with a
// Z suffix (deterministic under clock.Fake; Python uses datetime.now(utc)).
func (e *Engine) nowISO() string {
	return e.clk.Now().UTC().Format("2006-01-02T15:04:05") + "Z"
}
