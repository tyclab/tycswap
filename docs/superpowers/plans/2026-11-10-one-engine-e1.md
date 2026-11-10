# One engine, series E1: one switch operation, one tick, hosts

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One switch operation that checks, switches and records under the
store lock; one engine constructor and one tick for every host; a manual
switch turns auto-switch off; the engine's record on disk.

**Already released (v0.10.0), not repeated here:** the setting
`autoswitch.enabled` with its seed, `settings.SeedSetting`,
`autoswitch.WithEnabled` (a gate at the start of a tick), `tycswap auto
on|off|status`, and Start/Stop in the app and dashboard writing the setting,
also under `TYCSWAP_AUTO_MANAGED_BY` (DESIGN A83). Task 2 below replaces the
gate with the per-tick settings load; Task 3 deletes the environment
variable.

**Architecture:** A leaf package holds what every process shares on disk: the
state file, the record and the on/off change. A switch evaluates its caller's
guard, switches, stamps and records inside the store lock and returns a typed
outcome. The engine holds no settings: each tick try-locks, loads settings,
derives the session policy and decides; hosts only run the loop.

**Tech Stack:** Go (version from `go.mod`), standard library, the repo's
`filelock`, `atomicfile`, `settings`; vanilla JS in `internal/web/static`.

**Spec:** `docs/superpowers/specs/2026-11-10-one-engine-design.md`, revision 2
(D2, D3, D5, D7, the rest of D1). D4 and D6 are series E2. Findings closed
here are named per task; delete their rows from
`docs/superpowers/plans/2026-11-10-review-findings.md` in the task's commit.

## Global Constraints

- Test-first: each regression test must fail on the parent commit for the
  reason its finding gives. If it cannot be made to fail, stop and report.
- Every commit builds and its packages' tests pass on all three systems. A
  method a later task removes may stay as a pass-through until that task; the
  plan names each one, and nothing else is kept "for now".
- No `go build`, `go test` or `go vet` on a laptop: run them on the dev VM
  through the wrapper the dispatch prompt names. The task's gate is
  `gofmt -l .` (prints nothing), the touched packages' tests, `go vet ./...`,
  `GOOS=windows go vet ./...`, `GOOS=darwin CGO_ENABLED=0 go vet ./...`.
- Comments at or below 20 % of nonblank lines; they say why. New rules cite
  DESIGN A84 to A88 (Task 5 writes them).
- Tests use temporary stores and the packages' fakes, never a real login.
  Never log or print a credential or token.
- Preserve file locks, atomic writes and the JSON and exit-code contracts
  except where a task states the change; every stated change is recorded in
  `CHANGELOG.md` under **Breaking** in the same commit.
- Lock order, everywhere: `.autoswitch_tick.lock`, the store lock, Claude
  Code's locks, then one at a time and never nested in each other:
  `.settings.lock`, `.autoswitch_state.lock`, the usage lock,
  `.autoswitch_events.lock`, the status line lock. No code may hold
  `.autoswitch_state.lock` while it takes the store lock.
- This is a public repository: no private addresses, hostnames, account
  names or employer details in code, tests, docs or commit messages.
- One commit per task, conventional-commit subject, a body that says why.

## Review Focus

1. A tick decides for account 1, a manual switch to 3 commits, the tick's
   switch arrives: it must be refused under the store lock, the login stays
   on 3 and auto-switch ends off (Task 1).
2. `tycswap switch 3` in human-output mode (no `--json`) must turn
   auto-switch off like the JSON path (Task 1).
3. Two hosts at once (`app` and `tycswap auto`): one tick runs, the other
   reports `busy` at once, without waiting; strikes and switches are not
   doubled (Task 2).
4. `settings.json` unreadable while a host runs: auto-switch is off, one
   stored `config-warning` says why (Task 2).
5. A truncated last line or a non-JSON line in `autoswitch_events.jsonl`:
   readers skip it, the next append is intact (Task 1).

---

### Task 1: one switch operation, the state it stamps, the record

Closes: ENG-3, ENG-4, ENG-10, ENG-13, FL-1, FL-4.

**Files:**
- Create: `internal/autostate/state.go`, `record.go`, `enabled.go`, `types.go`
- Modify: `internal/filelock/filelock.go` (`TryAcquire`)
- Modify: `internal/autoswitch/state.go` (file primitives move to
  `autostate`; the `Engine` methods call them), `tick.go` (`perform`),
  `switcher.go`, `fakes_test.go`
- Modify: `internal/switching/switchto.go`, `switch.go`, `perform.go`,
  `result.go`
- Modify: `internal/core/switching.go`, `autoswitch_adapters.go`
- Modify: `internal/cli/dispatch.go`, `parse.go`, `help.go` (`--keep-auto`),
  `auto.go` and `webfacades.go` (on/off through `autostate.SetEnabled`),
  `webcmd.go`, `internal/web/handlers.go` (the row switch passes the surface
  and `keepAuto`)
- Test: `internal/autostate/*_test.go`, `internal/filelock/filelock_test.go`,
  `internal/switching/run_test.go`, `internal/core/switching_test.go`,
  `internal/autoswitch/perform_test.go`

**Interfaces:**
- `internal/autostate` imports only `settings`, `filelock`, `atomicfile`,
  `clock`. It produces:
  ```go
  type Ref struct{ Number, Email string }
  type SwitchMeta struct {
      Origin   string         // "auto", "manual", "strategy"
      Host     string         // engine host, or the surface: "cli", "dashboard", "tray", "tui"
      Provider string         // "claude", "codex"
      Trigger  string         // the engine's trigger, else ""
      Window   string         // the deciding window, else ""
      Detail   map[string]any // what justified an engine switch; nil otherwise
      KeepAuto bool           // a named-target switch that leaves auto-switch on
  }
  type Guard struct {
      ExpectFrom     string  // the live slot the caller decided for; "" = any
      RequireEnabled bool
      CooldownS      float64 // 0 = no cooldown check
  }
  type Outcome struct {
      Moved              bool // From.Number != To.Number
      From, To           Ref
      Reason             string // switching's reason
      Refused            string // "", "active-changed", "disabled", "cooldown"
      Warnings           []string
      AutoSwitchDisabled bool
  }
  func StatePath(root string) string
  func ReadState(root string) map[string]any                       // lock-free; {} when absent or malformed
  func MutateState(root string, f func(map[string]any)) (map[string]any, error) // under .autoswitch_state.lock
  func StampSwitch(root string, o Outcome, m SwitchMeta, now float64) error
  func LastSwitchAt(state map[string]any) (float64, bool)          // a stamp in the future reads as absent
  func SetEnabled(root string, on bool, by string, now float64) (changed bool, err error)

  type Event struct {
      ID       int64          `json:"id"`
      At       float64        `json:"at"`
      Host     string         `json:"host"`
      Provider string         `json:"provider"`
      Kind     string         `json:"kind"`   // "switch", "no-switch", "auto-on", "auto-off", "error", "account-quarantined", "account-unquarantined", "config-warning"
      Origin   string         `json:"origin"` // "auto", "manual", "strategy"
      Message  string         `json:"message"`
      Fields   map[string]any `json:"fields,omitempty"`
  }
  const EventLogLines = 200 // the dashboard's log length
  func AppendEvent(root string, ev Event) (Event, error) // assigns ID (newest stored + 1); lock .autoswitch_events.lock, waited 1 s
  func ReadEvents(root string) []Event                   // skips lines that do not parse
  ```
- `StampSwitch` does, in one `MutateState`, what `Engine.perform` does to the
  state after a switch today (`lastSwitchAt`, `lastSwitchTo`, the active
  memory cleared), and adds `lastSwitch: {at, from, to, origin, host,
  provider, trigger, window}`. It then appends the `switch` event (message
  `account A -> B`, fields: from, to, trigger, window, detail, warnings).
- `SetEnabled`: `settings.SetSetting`, then `enabledChange: {at, by, to}` in
  the state, then an `auto-on` / `auto-off` event with origin `manual`.
  `changed` is false when the value already was `on`. `tycswap auto on|off`
  and the facade's Start/Stop call it instead of `SetSetting`.
- `AppendEvent` writes `"\n"` first when the file does not end in a newline.
- `filelock.(*FileLock).TryAcquire() (bool, error)`: one non-blocking attempt;
  false when another holder has it; in-process holders included.
- `internal/switching`:
  ```go
  type Request struct {
      Target   string  // identifier; "" with Strategy set
      Strategy *string // as Switch takes it
      Models   []string
      ModelSrc *string
      Force    bool
      JSONOut  bool
      Meta     autostate.SwitchMeta
      Guard    autostate.Guard
  }
  func Run(s *store.Store, req Request) (autostate.Outcome, map[string]any, error)
  ```
  `SwitchTo` and `Switch` keep their signatures and call `Run` with origin
  `manual` (named target) or `strategy`, host `cli`, no guard. Inside
  `withTripleLock`, before anything is written: the guard (`ExpectFrom`
  against the slot read under the lock; `RequireEnabled` against
  `settings.Load`; `CooldownS` against `autostate.LastSwitchAt`). After the
  body, still inside the lock, when the login moved: `StampSwitch`; and for a
  named target with origin `manual`, not `KeepAuto`, while enabled:
  `SetEnabled(false, "manual-switch")` and `Outcome.AutoSwitchDisabled`. A
  failed stamp after a moved switch is a warning in the outcome, never an
  error. The map is nil in human-output mode, as today; the outcome never is.
- `internal/core`: `Switcher.Surface string` (default `cli`; `webcmd.go`
  sets `dashboard` or `tray`, the TUI `tui`);
  `SwitchToForce(id, jsonOut, force)` passes it;
  `SwitchToOpts(id string, jsonOut, force, keepAuto bool) (autostate.Outcome, map[string]any, error)`;
  `SwitchGuarded(num string, g autostate.Guard, m autostate.SwitchMeta) (autostate.Outcome, error)`.
- `autoswitch.Switcher` gains `SwitchGuarded` with that signature; `SwitchTo`
  leaves the interface. `Engine.perform` takes no state lock: it calls
  `SwitchGuarded` with `ExpectFrom` the slot the tick decided for,
  `RequireEnabled` true, `CooldownS` the cooldown for the triggers
  `heldByCooldown` names, and meta origin `auto`, its trigger and window,
  detail = the tick's readings for both accounts. A refusal becomes
  `no-switch` with that reason; a moved outcome becomes the `switch` event
  with the outcome's warnings, whatever a later write does.
- CLI: `tycswap switch <id> --keep-auto`. After a switch that turned it off:
  `Auto-switch is now off (you chose an account); tycswap auto on turns it back on.`
  The dashboard's row switch reads `keepAuto` from its body and returns
  `autoSwitchDisabled`.

- [ ] **Step 1: failing tests**
  - `TestTryAcquireDoesNotWait` (filelock): a second instance on a held path
    returns false in under 50 ms; true after release.
  - `TestAppendEventNumbersAndTrims`: 205 appends leave IDs 6..205.
  - `TestAppendAfterTruncatedLine`: a file ending in half a JSON line and a
    middle non-JSON line: `ReadEvents` returns the valid ones; the next
    `AppendEvent` gets the next ID and is read back intact.
  - `TestSetEnabledStampsAndRecords`: on -> off: `changed`, state
    `enabledChange.to == false` with `by`, newest event `auto-off`; off -> off:
    not changed, no new event.
  - `TestRunGuardActiveChanged` (switching): live slot 3, guard
    `ExpectFrom "1"`: `Refused == "active-changed"`, the live credential and
    config byte-identical, no state file.
  - `TestRunGuardDisabled`, `TestRunGuardCooldown` (stamp 100 s old,
    `CooldownS` 300): refused, nothing written.
  - `TestRunManualTurnsAutoOff`: enabled, live 1, target `2`, human-output
    mode: `Outcome.Moved`, `AutoSwitchDisabled`, `settings.Load().Enabled ==
    false`, state `lastSwitch.origin == "manual"`, events end `switch`,
    `auto-off` (ENG-3 and the human path).
  - `TestRunKeepAuto`, `TestRunOntoActiveKeepsAuto` (already active: not
    moved, still enabled, no events), `TestRunConfigRepairKeepsAuto`
    (repaired with from equal to to), `TestStrategySwitchStampsKeepsAuto`
    (stamp present, enabled stays).
  - `TestPerformRefusedWhenActiveChanged` (autoswitch; the fake's live slot
    moves to 3 when usage is collected): reason `active-changed`, live stays 3
    (ENG-4).
  - `TestPerformHoldsNoStateLock`: the fake's `SwitchGuarded` does
    `TryAcquire` on `.autoswitch_state.lock` and must get it.
  - `TestSwitchWarningsReachEvent` (ENG-10): a `[]string` warning from the
    outcome is in the `switch` event.
  - `TestEngineSwitchThroughRealStore` (core): one tick over a temporary
    store with the real switcher switches within 2 s and leaves the stamp and
    one `switch` event with origin `auto`, trigger and window.
- [ ] **Step 2: run, see them fail**
- [ ] **Step 3: implement**
- [ ] **Step 4: gate** (`./internal/autostate/ ./internal/filelock/ ./internal/switching/ ./internal/core/ ./internal/autoswitch/ ./internal/cli/ ./internal/web/ ./internal/tui/`)
- [ ] **Step 5: commit** `feat(switching)!: one guarded switch that stamps and records; a manual switch turns auto-switch off`

### Task 2: one tick, one constructor

Closes: ENG-8, ENG-12, ENG-17, HOST-2, HOST-7, WEB-8, CLI-6.

**Files:**
- Modify: `internal/autoswitch/engine.go`, `tick.go`, `events.go`,
  `models.go`, `fakes_test.go`; Create: `internal/autoswitch/record.go`
- Modify: `internal/settings/settings.go` (`LoadError`)
- Create: `internal/cli/engine.go`; Modify: `internal/cli/auto.go`,
  `codexauto.go`, `tuiwire.go`, `webfacades.go` (constructor call only),
  `config.go`, `help.go`
- Modify: `internal/codex/switcher/switcher.go` (meta and guard),
  `internal/core/lineage_test.go`, `usagepark_test.go` (constructor)
- Test: `internal/autoswitch/onetick_test.go`, `internal/cli/auto_test.go`,
  `engine_test.go`, `internal/codex/switcher/switcher_test.go`

**Interfaces:**
- ```go
  type SettingsSource func() settings.AutoSwitchSettings
  type Policy func(base settings.AutoSwitchSettings) (settings.AutoSwitchSettings, func(num string) bool)
  func NewEngine(sw Switcher, src SettingsSource, onEvent func(Event), dryRun bool, opts ...Option) *Engine
  func WithPolicy(p Policy) Option
  func WithHost(name string) Option // "once", "loop", "app", "web", "observe", "tui"; default "once"
  func WithAfterTick(f func(s settings.AutoSwitchSettings, dryRun bool)) Option // Codex and retirement, inside the tick lock
  type LastTick struct {
      At      float64  `json:"at"`
      Host    string   `json:"host"`
      Outcome string   `json:"outcome"` // "switched", "error", "no-action", "blocked"
      Reason  string   `json:"reason"`
      Models  []string `json:"models"`
  }
  func ReadLastTick(root string) *LastTick
  func Recorder(root, host string, next func(Event)) func(Event)
  func LoadError(root string) error // settings: nil for a missing file
  ```
- Tick order: `TryAcquire` on `.autoswitch_tick.lock` (held: `no-switch`
  `busy`, `NoAction`, nothing stored); load settings; `LoadError` non-nil:
  one `config-warning` naming the file; `!Enabled && !dryRun`: clear the
  poll-policy pin, emit `no-switch` `disabled`, return `NoAction` before any
  switcher call; policy; decide; `WithAfterTick`'s function; `lastTick`
  written in one `autostate.MutateState`. A dry tick takes no lock, writes
  nothing, and when disabled emits the `config-warning` and then decides.
- `Recorder` forwards every event and appends through
  `autostate.AppendEvent`: an `error` or `no-switch` only when its message or
  reason differs from the newest stored event's; quarantines and
  `config-warning`; never `busy`, polls or a dry run's events. Switch events
  are not appended here (the switch wrote them).
- `WithEnabled`, `WithReserved`, `adoptPendingModels`, `Engine.settings` and
  `Engine.models` go. `Engine.ApplyThreshold` and `Engine.ApplyModels` stay
  as methods that do nothing (a tick loads settings); Tasks 3 and 4 remove
  their callers and the methods.
- `internal/cli`:
  ```go
  // newEngine is the only place an engine is built.
  func newEngine(sw *core.Switcher, codexSw *codexswitcher.Switcher, o settings.CLIOverrides, onEvent func(autoswitch.Event), dryRun bool, host string) *autoswitch.Engine
  ```
  Source: `settings.MergedWithCLI(settings.Load(root), o)`; policy:
  `reporting.DefaultSessionPolicy`; after-tick (not for host `observe`):
  group retirement, then the Codex tick built from that tick's settings,
  skipped when no Codex engine applies; events through `Recorder`.
  `startCodexLoop` and the engine construction in `auto.go` and `tuiwire.go`
  are deleted.
- Codex: `(*codexswitcher.Switcher).SwitchToWith(ctx, identifier string, m autostate.SwitchMeta, g autostate.Guard) (SwitchResult, autostate.Outcome, error)`
  evaluates `RequireEnabled` and stamps and records with provider `codex`
  under the Codex store lock; `SwitchTo` is the manual form and turns
  auto-switch off like a Claude row. The Codex engine calls `SwitchToWith`
  with origin `auto` and `RequireEnabled`.
- CLI: `tycswap auto status --json` gains `enabledChange`, `lastTick` and
  `lastSwitch` (each `null` when absent). A foreground `auto` (loop or
  `--once`) that finds it off prints
  `Auto-switch is off; tycswap auto on turns it on.` on stderr once. `auto`
  usage errors exit 64; a NaN or infinite numeric flag is a usage error.

- [ ] **Step 1: failing tests**
  - `TestDryTickTakesNoLock` (tick lock held by the test: the dry tick still
    decides); `TestTickBusyAtOnce` (lock held: `busy` in under 200 ms,
    nothing stored).
  - `TestTickLoadsSettingsEveryTick` (7d line 97 then 80, active at 90).
  - `TestPolicyAppliesOnEveryHost` (`WithHost("loop")`, policy reserves the
    active account: trigger `reserved`) (ENG-8).
  - `TestAfterTickRunsInsideLock`: the function finds the tick lock held.
  - `TestLastTickRecorded`; `TestRecorderStoresChangesOnly` (below-threshold
    twice, cooldown, error A twice, error B: stored below-threshold,
    cooldown, A, B); `TestCorruptSettingsWarnOnce`.
  - `TestCodexManualSwitchTurnsAutoOff`, `TestCodexEngineSwitchRequiresEnabled`.
  - `TestAutoUsageErrorExits64`, `TestAutoRefusesNonFinite`,
    `TestAutoOnceDisabledStderrHint`, `TestLoopEngineAppliesSessionPolicy`,
    `TestNilCodexQuiet` (no Codex engine: three ticks, no event with provider
    `codex`) (HOST-7).
- [ ] **Step 2: run, see them fail**
- [ ] **Step 3: implement**
- [ ] **Step 4: gate** (`./internal/autoswitch/ ./internal/settings/ ./internal/cli/ ./internal/core/ ./internal/codex/... ./internal/tui/ ./internal/web/`)
- [ ] **Step 5: commit** `feat(auto)!: one tick under one lock for every host`

### Task 3: hosts run the loop

Closes: ENG-9, HOST-3, HOST-5, HOST-6, HOST-9, WEB-5, WEB-6, WEB-11, WEB-14,
FL-7, TRAY-3, TRAY-7 (its managed row).

**Files:**
- Modify: `internal/autoswitch/loop.go`, `consts.go`, `events.go`, `tick.go`
  (`tickResult` keeps `Outcome` only), `engine.go`
- Modify: `internal/autostate/state.go` (heartbeat)
- Modify: `internal/cli/webfacades.go`, `webcmd.go`, `appcmd.go`,
  `appshell.go`, `traypanel.go`, `remote.go`, `appstate.go`, `sigint.go`
- Modify: `internal/web/web.go`, `handlers.go`, `state.go`,
  `static/app.js`, `static/index.html`; every fake of `web.AutoFacade`
  (`internal/web/helpers_test.go`, `internal/cli/remote_test.go`,
  `webfacades_test.go`)
- Test: `internal/autoswitch/loop_test.go`, `internal/cli/webfacades_test.go`,
  `appcmd_test.go`, `internal/web/auto_test.go`,
  `internal/web/testdata/autobuttons.cjs`, `autoevents.cjs`

**Interfaces:**
- Loop: tick, then wait `interval*(0.9+0.2*rng)` or a wake or stop, every
  round. Deleted: `nextDelay`'s branches, `MaxSleepS`, `NoResetFallbackS`,
  `SleepEvent`, `SleepUntil`, `WaitLong`, `IdleSlow`. Each round stamps
  `autostate.Heartbeat{Host, PID, At}` (state key `heartbeat`), enabled or
  not. `autostate.Running(state, intervalS, now) bool`: a heartbeat of host
  `loop`, `app` or `web` within two intervals.
- Deadline: a tick that runs longer than `TickDeadline = 180 * time.Second`
  (the bound the provisioner's unit and hook have always used) makes
  `RunLoop` return an error; `tycswap auto`, `web` and `app --headless` then
  exit 1 after logging it; the tray app calls its existing `restartSelf`.
- ```go
  type AutoView struct {
      Enabled       bool                 `json:"enabled"`
      EnabledChange map[string]any       `json:"enabledChange"`
      Running       bool                 `json:"running"`
      LastTick      *autoswitch.LastTick `json:"lastTick"`
      LastSwitch    map[string]any       `json:"lastSwitch"`
      Settings      map[string]any       `json:"settings"` // on disk now
      Events        []AutoEventView      `json:"events"`   // from the record
      Quarantine    map[string]any       `json:"quarantine"`
      Codex         *CodexAutoView       `json:"codex"`
  }
  type AutoFacade interface {
      View() AutoView
      SetEnabled(on bool) error
      Wake() error
      Check() ([]AutoEventView, error) // one dry tick
  }
  ```
  `AutoEventView` gains `ID`, `Origin`, `Provider`. Routes: `POST
  /api/auto/start` -> `SetEnabled(true)`; with `{"dryRun":true}` -> 400 `"dry
  runs moved to Check (/api/auto/check)"`; `/api/auto/stop` ->
  `SetEnabled(false)`; `/api/auto/check`; `/api/auto/wake`.
  `/api/auto/threshold` and `/api/auto/model` are deleted; the slider and the
  model picker save through the settings route.
- `autoFacade`: `newAutoFacade(sw, codexSw, host string)` starts the loop and
  keeps it until `Close()`, which stops it and waits for it with no time
  bound. No `Start`, `Stop`, `resume`, `stopKeepingChoice`, `managedBy`,
  `autoStopWait`, no event ring. `TYCSWAP_AUTO_MANAGED_BY` is read nowhere.
- `app --headless`: prints `Dashboard: http://127.0.0.1:<port>/` with no
  token and mints none at start (`web.Deps.NoStartToken`; `/api/launch` and
  `web --reuse` mint on request); prints its engine's poll events and every
  new record line (its own and other processes') as JSON lines on stdout.
- `SIGTERM` and `SIGHUP` take the path `SIGINT` takes in `sigint.go`.
- Shell and panel: the `auto` item is always present and enabled; its check
  mark is `AutoView.Enabled`; a click calls `SetEnabled(!enabled)` and
  notifies `Auto-switch on` / `Auto-switch off`. No notification for an event
  whose fields carry `dryRun`; the switch title is `Auto-switched to account
  #N` from the record's `to`.
- Page: `run()` clears `aria-busy` and calls the section's render instead of
  forcing `disabled = false`. The Auto tab shows one On/Off control from
  `auto.enabled`, "Check now", the last tick and last switch lines, and when
  enabled and not running: `enabled, but nothing is running the engine
  (tycswap app or tycswap auto)`. The log is keyed by event `id`.
  `SETTING_LABELS` gets `autoswitch.enabled` ("Auto-switch"); the per-key
  note reads "Applies at the next tick." for every `autoswitch.*` key.

- [ ] **Step 1: failing tests**
  - `TestLoopKeepsIntervalWhenBlocked`: every candidate at its limit, reset
    4 h away, three rounds: every wait within `[0.9, 1.1] x interval`, no
    `sleep` event (ENG-9).
  - `TestLoopStampsHeartbeatWhenDisabled`; `TestRunning` (heartbeat host
    `once` does not count; `app` 90 s old with interval 60 counts; 130 s old
    does not).
  - `TestLoopEndsOnTickDeadline`: a tick blocked past a shortened deadline
    ends `RunLoop` with an error naming the deadline.
  - `TestFacadeSetEnabledWritesSetting` (also with the old environment
    variable set); `TestFacadeCloseWaitsForTick` (HOST-5);
    `TestFacadeCodexEngineOverDashboardSwitcher` (the hosted Codex tick uses
    the Codex switcher the facade was given, through the production
    constructor).
  - `TestHeadlessPrintsNoToken` (HOST-6); `TestHeadlessFollowsRecord`: an
    event appended by another process appears on its stdout.
  - `TestSigtermReleasesLocks`: a command holding Claude Code's locks ends on
    `SIGTERM` with the lock directories gone (unix build tag).
  - `TestAutoRoutesSetEnabled`; `TestTrayNotificationNamesSlotSkipsDry`
    (TRAY-3).
  - `autobuttons.cjs`: after a resolved Start with a state frame painted
    first, the control shows On (WEB-6). `autoevents.cjs`: a state document
    carrying events 7 and 8, then frames 7 and 8: two log lines (WEB-14).
- [ ] **Step 2: run, see them fail**
- [ ] **Step 3: implement**
- [ ] **Step 4: gate** (`./internal/autoswitch/ ./internal/autostate/ ./internal/cli/ ./internal/web/ ./internal/tray/`)
- [ ] **Step 5: commit** `refactor!: hosts run the loop; no engine to start or stop`

### Task 4: the TUI's Auto screen shows the engine instead of running one

Closes: CLI-1, CLI-2.

**Files:**
- Modify: `internal/tui/autoview.go`, `app.go`, `snapshot.go`,
  `helpers_test.go`, `internal/cli/tuiwire.go`,
  `internal/autoswitch/engine.go` (delete `ApplyThreshold`, `ApplyModels`)
- Test: `internal/tui/autoview_test.go`

**Interfaces:**
- The screen shows On/Off, running, the last tick and last switch lines, the
  counted models, the Next best panel as today, and the record (newest
  last). Keys: `a` toggles through `autostate.SetEnabled(by "tui")`; `d` runs
  one dry tick through the factory seam and lists its events under "Dry
  check"; `esc` leaves. Deleted: the engine started on mount, `l` and its
  confirmation, `t`, the generation counter, `engineStoppedMsg`. A row switch
  from the TUI passes surface `tui`.

- [ ] **Step 1: failing tests** `TestAutoScreenStartsNoEngine` (the factory
  is called zero times on mount) (CLI-1), `TestAutoScreenTogglesSetting`,
  `TestAutoScreenDryCheck` (the factory is called once with `dryRun`).
- [ ] **Step 2: run, see them fail** (`./internal/tui/ -run AutoScreen`)
- [ ] **Step 3: implement; delete the tests of the removed keys**
- [ ] **Step 4: gate** (`./internal/tui/ ./internal/cli/ ./internal/autoswitch/`),
  then `grep -rn "ApplyThreshold\|ApplyModels" internal` prints nothing.
- [ ] **Step 5: commit** `refactor(tui)!: the Auto screen shows the engine's record and toggles it`

### Task 5: documents

**Files:**
- Modify: `docs/DESIGN.md` (append A84 to A88; mark A51 and the loop
  paragraphs of A66, A76, A77 as superseded with a pointer, do not rewrite
  them), `docs/reference.md` (`auto`, `switch --keep-auto`, the dashboard API,
  FILES, exit codes), `README.md` (auto-switch, tray, the service paragraph,
  the FILES rows), `CHANGELOG.md`, `docs/port-spec/05-autoswitch.md` and
  `09-tui-menubar.md` (deviation notes)

- [ ] **Step 1: write** A84 "One switch operation; choosing an account turns
  auto-switch off" (D2), A85 "One tick" (D3), A86 "Hosts run the loop" (D5,
  with what went and why), A87 "The engine's record" (D7), A88 "What every
  process shares on disk" (`internal/autostate`, the lock order). Each states
  the rule, the precedent it follows and the findings it closed by ID.
- [ ] **Step 2: reference and README** match the code: every command, flag,
  route, JSON field, file and exit status this series added, changed or
  removed.
- [ ] **Step 3: CHANGELOG**: Added, Changed, Removed and **Breaking**: new
  reasons `busy`, `active-changed`, `cooldown`; exit 64; removed routes and
  state fields; `TYCSWAP_AUTO_MANAGED_BY` ignored; a named-target switch
  turns auto-switch off; the TUI keys.
- [ ] **Step 4:** `grep -rn "MANAGED_BY\|ApplyThreshold\|ApplyModels\|WithEnabled" --include='*.go' --include='*.js' --include='*.md' .`
  shows only DESIGN's superseded sections and the CHANGELOG.
- [ ] **Step 5: commit** `docs: one switch, one tick, hosts run the loop (DESIGN A84-A88)`
