# One engine, series E1: the off switch and one tick

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `autoswitch.enabled` on disk, honoured by every tick; one engine
constructor and one tick for every host; a manual switch turns auto-switch
off; the engine's record on disk.

**Architecture:** The engine stops holding settings: each tick loads them
through a source function, derives the session policy through a policy
function, and runs under a non-blocking tick lock. Hosts (`auto`, `app`,
`web`) only run the loop; Start and Stop write the setting. Decision events
go to a trimmed JSONL file every surface reads.

**Tech Stack:** Go (version from `go.mod`), standard library, the repo's
`filelock`, `atomicfile`, `settings`; vanilla JS in `internal/web/static`.

**Spec:** `docs/superpowers/specs/2026-11-10-one-engine-design.md` (D1, D2,
D3, D5, D7; D4, D6 and D5's signal handling are series E2). Findings closed here are named per
task; delete their rows from
`docs/superpowers/plans/2026-11-10-review-findings.md` in the task's commit.

## Global Constraints

- Test-first: each regression test must fail on the parent commit for the
  reason its finding gives. If it cannot be made to fail, stop and report.
- No `go build`, `go test` or `go vet` on this machine. Run them on the dev
  VM with the wrapper the dispatch prompt names (it syncs the worktree and
  runs the command there). Package-scoped runs while working; the full gate
  is `make fmt`, `go test ./...`, `go vet ./...`, `GOOS=windows go vet ./...`,
  `GOOS=darwin CGO_ENABLED=0 go vet ./...` at the end of each task.
- Comments at or below 20 % of nonblank lines; they say why. Cite the DESIGN
  amendment (A83 to A87, Task 7 writes them) where the old code cited one.
- Tests use temporary stores and the packages' fakes, never a real login.
  Never log or print a credential or token.
- Preserve file locks, atomic writes and the JSON and exit-code contracts
  except where a task states the change; every stated change is recorded in
  `CHANGELOG.md` under **Breaking** in the same commit.
- This is a public repository: no private addresses, hostnames, account
  names or employer details in code, tests, docs or commit messages.
- One commit per task, conventional-commit subject, a body that says why.
  No pushes.

## Review Focus

1. `settings.json` unreadable or corrupt while a host runs: the tick reads
   defaults, so auto-switch is off; one `config-warning` says so (Task 2).
2. Two hosts at once (`app` and `tycswap auto`): one tick runs, the other
   reports `busy`; strikes and switches are not doubled (Task 2).
3. A manual "switch" onto the account that is already live moves nothing and
   must leave auto-switch on (Task 5).
4. An older page or tray posting `{"dryRun": true}` to `/api/auto/start`
   gets a 400 that names Check, not a silent enable (Task 4).
5. A truncated or non-JSON line in `autoswitch_events.jsonl` is skipped; the
   file stays appendable and the screens keep working (Task 5).

---

### Task 1: `autoswitch.enabled`

Closes: part of HOST-1, HOST-4.

**Files:**
- Modify: `internal/settings/settings.go` (struct, `SettingSpecs`, defaults)
- Modify: `internal/cli/appstate.go`, `internal/cli/cli.go` (one-time seed)
- Test: `internal/settings/settings_test.go`, `internal/cli/appstate_test.go`

**Interfaces:**
- Produces: `settings.AutoSwitchSettings.Enabled bool`, key
  `autoswitch.enabled` (`KindBool`, default `false`, first in
  `SettingSpecs`, help "Switch accounts automatically before a limit
  (tycswap auto on|off)").
- Produces: `func migrateAutoSwitchChoice(root string) error` in
  `internal/cli/appstate.go`: when `settings.json` has no
  `autoswitch.enabled` key and `ui_prefs.json` holds `"autoSwitch": true`,
  it writes `autoswitch.enabled=true` through `settings.SetSetting`; in every
  case it then rewrites `ui_prefs.json` without `autoSwitch` when that key
  was present. `run()` calls it once per process after the store-root guard;
  its error is logged at debug and never fails the command.
- `appState.AutoSwitch` and its JSON key are deleted with this task; the
  callers that read or wrote it (`remember`, `resume`) are stubbed to no-ops
  here and removed in Task 4.

- [ ] **Step 1: failing tests**
  - `TestEnabledDefaultsFalse`: `settings.Load(emptyRoot).Enabled == false`.
  - `TestEnabledSetUnset`: `SetSetting(root, "autoswitch.enabled", "true")`
    then `Load(root).Enabled == true`; `"maybe"` is refused with the bool
    error the other bool key gives; `UnsetSetting` returns to false.
  - `TestMigrateAutoSwitchChoice`: (a) `ui_prefs.json`
    `{"version":1,"autoSwitch":true,"folded":{"x":true}}` and no key →
    `Load(root).Enabled == true`, `ui_prefs.json` has no `autoSwitch` and
    still has `folded.x`; (b) key already `false` → stays false, `autoSwitch`
    removed; (c) no `ui_prefs.json` → nothing written, no error; (d) run
    twice → same result.
- [ ] **Step 2: run them, see them fail** (`./internal/settings/`,
  `./internal/cli/ -run 'Enabled|MigrateAutoSwitch'`)
- [ ] **Step 3: implement**
- [ ] **Step 4: package tests green, then the full gate**
- [ ] **Step 5: commit** `feat(settings): autoswitch.enabled, seeded from the app's remembered choice`

### Task 2: the tick loads settings, applies the policy, and runs alone

Closes: ENG-4, ENG-8, HOST-2, WEB-8, part of HOST-1.

**Files:**
- Modify: `internal/autoswitch/engine.go`, `tick.go`, `state.go`,
  `events.go`, `models.go` (delete), `fakes_test.go`,
  `internal/settings/settings.go` (`LoadError`)
- Create: `internal/autoswitch/lasttick.go`
- Test: `internal/autoswitch/onetick_test.go` (new), existing tests adapted

**Interfaces:**
- Produces:
  ```go
  type SettingsSource func() settings.AutoSwitchSettings
  type Policy func(base settings.AutoSwitchSettings) (settings.AutoSwitchSettings, func(num string) bool)
  func NewEngine(sw Switcher, src SettingsSource, onEvent func(Event), dryRun bool, opts ...Option) *Engine
  func WithPolicy(p Policy) Option // nil: settings as loaded, nothing reserved
  func WithHost(name string) Option // "once", "loop", "app", "web", "observe", "tui"; default "once"
  type LastTick struct {
      At      float64  `json:"at"`
      Host    string   `json:"host"`
      Outcome string   `json:"outcome"` // "switched", "error", "no-action", "blocked"
      Reason  string   `json:"reason"`  // the no-switch reason or the switch trigger
      Models  []string `json:"models"`  // counted model windows, [] for none
  }
  func ReadLastTick(statePath string) *LastTick // nil when absent or malformed
  ```
- Removed: `WithReserved`, `ApplyThreshold`, `ApplyModels`,
  `adoptPendingModels`, `Engine.settings`, `Engine.models`,
  `Engine.pendingModels`. `currentSettings()` is replaced by the per-tick
  value passed down; `sw.SetPollPolicyInputs` is called at each tick start
  with that tick's values.
- New `NoSwitchEvent.Reason` values: `disabled`, `busy`, `active-changed`.
- Tick order (spec D3): take `.autoswitch_tick.lock` beside the state file
  with `filelock.New(path, 0)` and `Acquire(0)`; load settings; when
  `!Enabled && !dryRun` emit `no-switch disabled` and return `NoAction`
  before any switcher call; when `!Enabled && dryRun` emit one
  `ConfigWarningEvent{Message: "auto-switch is off; a dry run shows what it would do"}`
  and continue; apply the policy; decide; in `perform`, inside the existing
  state-lock span and before `SwitchTo`, re-read `CurrentAccountNumber()` and
  the settings source and return `no-switch active-changed` / `disabled`
  when the live slot differs from the one decided for or the key went false;
  write `lastTick` through `mutateState` at the end of every real tick that
  got past the disabled gate.
- A settings source whose file is unreadable returns defaults (that is
  `settings.Load`'s behaviour); the tick then also emits one
  `ConfigWarningEvent` naming the file when `settings.LoadError(root)` is
  non-nil. Produces `func LoadError(root string) error` in
  `internal/settings` (nil for a missing file).

- [ ] **Step 1: failing tests** in `onetick_test.go`, on the package's fake
  switcher:
  - `TestTickDisabledDoesNothing`: source `Enabled=false`; events are exactly
    one `no-switch` with reason `disabled`; outcome `NoAction`; the fake saw
    no `UsageEntriesByAccount` and no `SwitchTo`; no state file exists.
  - `TestDryTickWhenDisabledDecides`: same store, `dryRun=true`: a
    `config-warning` and then the decision the enabled dry tick gives.
  - `TestTickBusyWhenLockHeld`: hold the tick lock; outcome `NoAction`,
    reason `busy`, no switcher call.
  - `TestTickLoadsSettingsEveryTick`: source returns 7d bar 97 then 80 with
    the active account at 7d 90: first tick `below-threshold`, second switches.
  - `TestPolicyAppliesOnEveryHost`: `WithHost("loop")`, policy reserves the
    active account, every window under its bar: the tick switches with
    trigger `reserved` (ENG-8).
  - `TestPerformStopsWhenActiveChanged`: the fake changes its live account
    from 1 to 3 when `UsageEntriesByAccount` returns; active 1 at its 5h
    limit, candidate 2 free: reason `active-changed`, `SwitchTo` not called,
    live stays 3 (ENG-4).
  - `TestPerformStopsWhenDisabledMeanwhile`: the source flips to
    `Enabled=false` after the first call: reason `disabled`, no `SwitchTo`.
  - `TestLastTickRecorded`: after a `below-threshold` tick with model list
    `["Fable"]` and `WithHost("app")`: `ReadLastTick` gives host `app`,
    outcome `no-action`, reason `below-threshold`, models `["Fable"]`.
  - `TestCorruptSettingsWarnOnce`: `LoadError` non-nil → one
    `config-warning` naming `settings.json`, then `no-switch disabled`.
- [ ] **Step 2: run, see them fail** (`./internal/autoswitch/ -run 'Tick|Perform|Policy|LastTick|CorruptSettings'`)
- [ ] **Step 3: implement; adapt the existing tests to the new constructor
  (`func() settings.AutoSwitchSettings { return s }` with `s.Enabled = true`)**
- [ ] **Step 4: `./internal/autoswitch/` green; the packages that call
  `NewEngine` are fixed in Task 3, so the full gate is run there**
- [ ] **Step 5: commit** `refactor(autoswitch)!: a tick loads its settings and policy and runs alone`

### Task 3: one constructor, `auto on|off|status`

Closes: ENG-12, CLI-6, ENG-17, HOST-1.

**Files:**
- Create: `internal/cli/engine.go`
- Modify: `internal/cli/auto.go`, `tuiwire.go`, `webfacades.go` (constructor
  call only), `config.go`, `help.go`
- Test: `internal/cli/auto_test.go`, `internal/cli/engine_test.go`

**Interfaces:**
- Consumes: Task 2's `NewEngine`, `WithPolicy`, `WithHost`, `ReadLastTick`.
- Produces:
  ```go
  // newEngine is the only place an engine is built.
  func newEngine(sw *core.Switcher, o settings.CLIOverrides, onEvent func(autoswitch.Event), dryRun bool, host string) *autoswitch.Engine
  ```
  Source: `settings.MergedWithCLI(settings.Load(sw.BackupDir()), o)`; policy:
  `reporting.DefaultSessionPolicy(sw.Store, base)`; the adapter, OAuth
  client, logger and clock as `autoCommand` passes them today.
  `newAutoEngine` (tuiwire.go) and the inline construction in `auto.go` are
  deleted; `autoFacade.newEngine` calls this with host `"app"` or `"web"`.
- `tycswap auto on` / `off`: `settings.SetSetting(root, "autoswitch.enabled", …)`,
  prints `Auto-switch on.` / `Auto-switch off.`, exit 0; with `--json`
  `{"schemaVersion":1,"enabled":true}`.
- `tycswap auto status [--json]`: JSON
  `{"schemaVersion":1,"enabled":bool,"running":bool,"lastTick":LastTick|null,"lastSwitch":{"at":…,"to":"…"}|null}`
  with `running` true when `lastTick.at` is within three
  `autoswitch.intervalSeconds` of now. Text: one line per fact; when enabled
  and not running: `enabled, but nothing is running the engine (tycswap app or tycswap auto)`.
- `auto` usage errors exit 64: `subErrorCode(prog, stderr, msg, 64)` beside
  `subError`; a numeric flag whose value is NaN or ±Inf is a usage error.
- `auto --once` when disabled exits 2 after the tick's `no-switch disabled`,
  and runs neither group retirement nor the Codex tick.
- Loop mode passes host `"loop"`, `--once` `"once"`, `--observe` `"observe"`.

- [ ] **Step 1: failing tests**
  - `TestAutoOnOffStatus`: `auto off` → exit 0, `Load().Enabled == false`;
    `auto on` → true; `auto status --json` parses and has `enabled:true`,
    `running:false`, `lastTick:null` on a fresh store.
  - `TestAutoUsageErrorExits64`: `auto --once --threshold 90` → 64, stderr
    names the flag, stdout empty.
  - `TestAutoRefusesNonFinite`: `auto --interval nan` → 64.
  - `TestAutoOnceDisabledExits2`: fresh store, one account: exit 2, stdout
    (with `--json`) is one `no-switch` line with reason `disabled`.
  - `TestLoopEngineAppliesSessionPolicy`: store where the default-session
    records name only Opus sessions and the active account is
    Fable-capable: `newEngine(sw, nil-overrides, …, "loop").Tick()` emits a
    switch with trigger `reserved`.
- [ ] **Step 2: run, see them fail** (`./internal/cli/ -run 'AutoOn|AutoUsage|AutoRefuses|AutoOnce|LoopEngine'`)
- [ ] **Step 3: implement**
- [ ] **Step 4: `./internal/cli/ ./internal/tui/ ./internal/autoswitch/`, then the full gate**
- [ ] **Step 5: commit** `feat(auto): one engine constructor; tycswap auto on|off|status`

### Task 4: hosts run the loop; Start and Stop write the setting

Closes: ENG-9, HOST-3, HOST-4, HOST-5, HOST-6, HOST-7, HOST-9, WEB-5, WEB-6,
FL-7, TRAY-7 (its managed row).

**Files:**
- Modify: `internal/autoswitch/loop.go`, `consts.go`, `events.go`, `tick.go`
  (`tickResult` keeps `Outcome` only)
- Modify: `internal/cli/webfacades.go`, `webcmd.go`, `appcmd.go`,
  `appshell.go`, `traypanel.go`, `remote.go`, `appstate.go`
- Modify: `internal/web/web.go`, `handlers.go`, `state.go`,
  `static/app.js`, `static/index.html`
- Test: `internal/autoswitch/loop_test.go`, `internal/cli/webfacades_test.go`,
  `appcmd_test.go`, `internal/web/auto_test.go`, `internal/web/testdata/autobuttons.cjs`

**Interfaces:**
- Loop: `RunLoop` ticks, then waits `interval*(0.9+0.2*rng)` or a wake or
  stop, every time. Deleted: `nextDelay`'s branches, `MaxSleepS`,
  `NoResetFallbackS`, `SleepEvent`, `SleepUntil`, `WaitLong`, `IdleSlow`.
- Produces in `internal/web`:
  ```go
  type AutoView struct {
      Enabled    bool            `json:"enabled"`
      Running    bool            `json:"running"`    // a tick within three intervals
      LastTick   *autoswitch.LastTick `json:"lastTick"`
      Settings   map[string]any  `json:"settings"`   // settings on disk now
      Events     []AutoEventView `json:"events"`
      Quarantine map[string]any  `json:"quarantine"`
      Codex      *CodexAutoView  `json:"codex"`
  }
  type AutoFacade interface {
      View() AutoView
      SetEnabled(on bool) error
      Wake() error
      Check() ([]AutoEventView, error) // one dry tick, its events
  }
  ```
  Deleted from the view: `managedBy`, `available`, `dryRun`, `startedAt`,
  `threshold`. Routes: `POST /api/auto/start` → `SetEnabled(true)`, with
  `{"dryRun":true}` → 400 `"dry runs moved to Check (/api/auto/check)"`;
  `POST /api/auto/stop` → `SetEnabled(false)`; `POST /api/auto/check` →
  `Check()`; `POST /api/auto/wake` as today. `/api/auto/threshold` and
  `/api/auto/model` are deleted; the page's 7d slider and model picker save
  through the settings route.
- `autoFacade`: `newAutoFacade(sw, codexSw, host string)` starts the Claude
  loop and the Codex loop at once and keeps them until `Close()`, which stops
  both and waits for both to return with no time bound. No `Start`, `Stop`,
  `remember`, `resume`, `stopKeepingChoice`, `managedBy`, `autoStopWait`.
  The Codex loop's tick returns after group retirement when the Codex engine
  is nil.
- `TYCSWAP_AUTO_MANAGED_BY` is read nowhere.
- `app --headless`: prints `Dashboard: http://127.0.0.1:<port>/` with no
  token, mints no launch token at start (`web.Server` gains
  `Deps.NoStartToken bool`; `/api/launch` and `web --reuse` mint on
  request), and prints every engine event as a JSON line on stdout
  (`jsonlEmit`).
- Shell and panel: the `auto` item is always present and enabled; its check
  mark is `AutoView.Enabled`; a click calls `SetEnabled(!enabled)` and
  notifies `Auto-switch on` / `Auto-switch off`. Remote: `AutoStart` /
  `AutoStop` keep their routes; `SetModel` saves the setting.
- Page, Settings tab: `SETTING_LABELS` gets `autoswitch.enabled`
  ("Auto-switch"), and the per-key "applies" note reads "Applies at the next
  tick." for every `autoswitch.*` key (a tick loads settings; nothing
  restarts).
- Page: `run()` clears `aria-busy` and calls the section's render instead of
  forcing `disabled = false`; the Auto tab shows one On/Off control from
  `auto.enabled`, a "Check now" button, the last tick line, and when enabled
  and not running the sentence from Task 3's status.

- [ ] **Step 1: failing tests**
  - `TestLoopKeepsIntervalWhenBlocked` (autoswitch): every candidate at its
    limit with a reset 4 h away; three loop rounds on the fake sleeper; every
    requested wait is within `[0.9, 1.1] × interval` and no `sleep` event
    exists (ENG-9).
  - `TestFacadeSetEnabledWritesSetting` (cli): `SetEnabled(false)` →
    `settings.Load().Enabled == false`, `View().Enabled == false`; with
    `TYCSWAP_AUTO_MANAGED_BY=x` in the environment the result is the same.
  - `TestFacadeCloseWaitsForTick`: `fakeEngine` held in its tick; `Close()`
    returns only after the tick is released (HOST-5).
  - `TestFacadeNilCodexQuiet`: Codex engine nil, three loop rounds: no event
    with provider `codex` (HOST-7).
  - `TestFacadeCodexEngineOverDashboardSwitcher`: the hosted Codex engine is
    built over the Codex switcher the facade was given (the assertion the
    deleted `TestAppResumeStartsBothEngines` carried), through the production
    constructor closure, not a replaced seam.
  - `TestHeadlessPrintsNoToken` (cli): the headless start output matches
    `Dashboard: http://127.0.0.1:\d+/\n` and holds no `token=` (HOST-6).
  - `TestAutoRoutesSetEnabled` (web): start → facade saw `SetEnabled(true)`;
    stop → `false`; start with `{"dryRun":true}` → 400 naming `/api/auto/check`;
    `/api/auto/threshold` → 404.
  - `autobuttons.cjs` (node, the pattern of `testdata/modelcontrols.cjs`):
    after a resolved Start request with a state frame painted first, the
    control shows On and no button is enabled in the opposite state (WEB-6).
- [ ] **Step 2: run, see them fail**
- [ ] **Step 3: implement**
- [ ] **Step 4: `./internal/autoswitch/ ./internal/cli/ ./internal/web/ ./internal/tray/`, then the full gate**
- [ ] **Step 5: commit** `refactor!: hosts run the loop; Start and Stop write autoswitch.enabled`

### Task 5: a manual switch turns auto off; the engine's record

Closes: ENG-3, FL-1, FL-4, WEB-11, WEB-14, TRAY-3.

**Files:**
- Create: `internal/autoswitch/eventlog.go`
- Modify: `internal/core/switching.go`, `internal/cli/dispatch.go` (the
  `switch <id>` path), `internal/web/handlers.go` (row switch),
  `internal/tui/app.go` (row switch), `internal/cli/engine.go`,
  `webfacades.go`, `appshell.go`, `auto.go` (`on`/`off` append an event),
  `internal/web/static/app.js`
- Test: `internal/autoswitch/eventlog_test.go`, `internal/core/switching_test.go`,
  `internal/cli/appshell_test.go`, `internal/web/testdata/autoevents.cjs`

**Interfaces:**
- Produces in `internal/autoswitch`:
  ```go
  type LoggedEvent struct {
      ID      int64          `json:"id"`      // previous line's ID + 1
      At      float64        `json:"at"`
      Host    string         `json:"host"`    // WithHost value, or the surface for a manual line
      Kind    string         `json:"kind"`    // "switch", "no-switch", "auto-on", "auto-off", "error", "account-quarantined", "account-unquarantined"
      Origin  string         `json:"origin"`  // "auto" or "manual"
      Message string         `json:"message"`
      Fields  map[string]any `json:"fields,omitempty"`
  }
  const EventLogLines = 200 // the dashboard's log length
  func EventLogPath(backupDir string) string            // <dir>/autoswitch_events.jsonl
  func AppendEvent(backupDir string, ev LoggedEvent) error // assigns ID; own lock `.autoswitch_events.lock`, waited 1 s, taken last
  func ReadEvents(backupDir string) []LoggedEvent        // skips lines that do not parse
  func Recorder(backupDir, host string, next func(Event)) func(Event)
  ```
  `Recorder` forwards every event to `next` and appends: every `switch`
  that is not a dry run (fields: from, to, trigger, axis, warnings), every
  `error`, `account-quarantined`, `account-unquarantined`, and a `no-switch`
  only when its reason differs from the newest stored line's reason. Polls
  are not stored. `newEngine` wraps `onEvent` with it unless `dryRun`.
- Produces in `internal/core`:
  ```go
  // SwitchToManual is a switch onto an account the caller named.
  func (sw *Switcher) SwitchToManual(id string, jsonOut, force bool, surface string) (map[string]any, error)
  ```
  It calls `SwitchToForce`; when the result has `switched == true` and
  `settings.Load(...).Enabled`, it sets `autoswitch.enabled=false`, appends
  `LoggedEvent{Kind:"auto-off", Origin:"manual", Host:surface, Message:"auto-switch off: switched to account N by hand"}`,
  adds `"autoSwitchDisabled": true` to the result and logs one INFO line.
  A manual switch that moved the login always appends a `switch` line with
  origin `manual`. Surfaces: `"cli"`, `"dashboard"`, `"tray"`, `"tui"`.
  Strategy switches and the engine keep calling `Switch` / `SwitchTo`.
- CLI text after a manual switch that disabled it:
  `Auto-switch is now off (you chose an account); tycswap auto on turns it back on.`
  Tray and dashboard show the same sentence as their notice.
- `autoFacade.View().Events` is `ReadEvents` mapped to `AutoEventView`
  (which gains `ID int64` and `Origin string`); the in-memory ring is
  deleted. The page keys its log by `id`.
- Shell notifications: none for an event whose fields carry `dryRun`; the
  switch title is `Auto-switched to account #N` with N from
  `fields.to.number`; the quarantine title uses `fields.number`.
- The engine logs `Auto-switch: account A to B (trigger, window, host)` at
  INFO after a switch.

- [ ] **Step 1: failing tests**
  - `TestAppendEventNumbersAndTrims`: 205 appends → `ReadEvents` has 200,
    IDs 6..205 in order.
  - `TestReadEventsSkipsBadLines`: a file with a truncated last line and a
    non-JSON middle line → the valid lines, and a following `AppendEvent`
    succeeds with the next ID.
  - `TestRecorderStoresDecisionsNotPolls`: poll, no-switch(below-threshold),
    poll, no-switch(below-threshold), no-switch(cooldown), switch → stored
    kinds/reasons: below-threshold, cooldown, switch.
  - `TestManualSwitchTurnsAutoOff` (core): enabled store, live 1,
    `SwitchToManual("2", true, false, "cli")` → `Enabled == false`, result
    `autoSwitchDisabled:true`, events end with a manual `switch` and
    `auto-off`; a following engine tick returns `disabled` (ENG-3).
  - `TestManualSwitchOntoActiveKeepsAuto`: `SwitchToManual("1", …)` with 1
    live → `Enabled` stays true, no `auto-off` line.
  - `TestStrategySwitchKeepsAuto`: `Switch(strategy best)` → `Enabled` true.
  - `TestTrayNotificationNamesSlotSkipsDry` (cli): a real engine `switch`
    event through the facade → title `Auto-switched to account #2`; the same
    event with `dryRun` → no notification (TRAY-3).
  - `autoevents.cjs`: a state document carrying events 7 and 8 painted, then
    frames 7 and 8 → two log lines (WEB-14); with `auto.enabled` true, no
    events and `running` false the empty state names `tycswap app`.
- [ ] **Step 2: run, see them fail**
- [ ] **Step 3: implement**
- [ ] **Step 4: touched packages, then the full gate**
- [ ] **Step 5: commit** `feat(auto): a manual switch turns auto-switch off; decisions are recorded on disk`

### Task 6: the TUI's Auto screen shows the engine instead of running one

Closes: CLI-1, CLI-2.

**Files:**
- Modify: `internal/tui/autoview.go`, `app.go`, `snapshot.go`,
  `internal/cli/tuiwire.go`
- Test: `internal/tui/autoview_test.go`

**Interfaces:**
- Consumes: `autoswitch.ReadEvents`, `ReadLastTick`, `settings.SetSetting`,
  Task 3's `newEngine` through the existing factory seam, used only for the
  dry check.
- The screen shows: On/Off, the last tick line, the counted models, the
  Next best panel as today, and the stored events (newest last). Keys: `a`
  toggles `autoswitch.enabled`; `d` runs one dry tick and lists its events
  under a "Dry check" heading; `esc` leaves. Deleted: the engine started on
  mount, `l` (go live) and its confirmation, `t` (session threshold), the
  generation counter, `engineStoppedMsg`.

- [ ] **Step 1: failing tests**
  - `TestAutoScreenStartsNoEngine`: mounting the screen calls the engine
    factory zero times (CLI-1).
  - `TestAutoScreenTogglesSetting`: `a` → `settings.Load().Enabled` flips and
    the header reads `Auto-switch: on`.
  - `TestAutoScreenDryCheck`: `d` → the factory is called once with
    `dryRun=true`, and the returned events are listed.
- [ ] **Step 2: run, see them fail** (`./internal/tui/ -run AutoScreen`)
- [ ] **Step 3: implement; delete the tests of the removed keys**
- [ ] **Step 4: `./internal/tui/ ./internal/cli/`, then the full gate**
- [ ] **Step 5: commit** `refactor(tui)!: the Auto screen shows the engine's record and toggles it`

### Task 7: documents

**Files:**
- Modify: `docs/DESIGN.md` (append A83 to A87; mark A43, A51 and the loop
  paragraphs of A66, A76, A77 as superseded with a pointer, do not rewrite
  them), `docs/reference.md` (`auto`, the dashboard API, FILES, exit codes,
  the SETTINGS table, every mention of `autoSwitch` in `ui_prefs.json`),
  `README.md` (auto-switch, tray, the WSL service paragraph, the
  `tycswap config` sample, the FILES row for `ui_prefs.json`), `CHANGELOG.md`,
  `docs/port-spec/05-autoswitch.md` and `09-tui-menubar.md` (deviation notes)

- [ ] **Step 1: write** A83 "Auto-switch is a setting" (D1), A84 "Choosing an
  account turns auto-switch off" (D2), A85 "One tick" (D3), A86 "Hosts run
  the loop" (D5, with the removed pieces and why), A87 "The engine's record"
  (D7). Each states the rule, the precedent it follows, and the findings it
  closed by ID.
- [ ] **Step 2: reference and README** match the code: every command, route,
  JSON field, file and exit status this series added, changed or removed.
- [ ] **Step 3: CHANGELOG** under the next version: Added, Changed, Removed,
  and **Breaking** (engine API in `auto --json` consumers: new reasons
  `disabled`, `busy`, `active-changed`; exit 64; removed routes and state
  fields; `TYCSWAP_AUTO_MANAGED_BY` ignored; the TUI keys).
- [ ] **Step 4:** `grep -rn "MANAGED_BY\|ApplyThreshold\|ApplyModels\|autoSwitch\"" --include='*.go' --include='*.js' --include='*.md' .`
  shows only DESIGN's superseded sections and the CHANGELOG.
- [ ] **Step 5: commit** `docs: auto-switch is a setting, one tick, hosts run the loop (DESIGN A83-A87)`
