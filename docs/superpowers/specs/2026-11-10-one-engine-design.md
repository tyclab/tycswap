# One engine, an off switch, truthful screens

Date: 2026-11-10. Status: operator said go on the plan this design details; the
calls marked "our call" are the author's and open to reversal.

## Why

The operator cannot turn auto-switch off. A provisioner timer runs
`tycswap auto --once`, a Claude Code hook runs the same tick, the status line
runs `tycswap auto --observe`, and because the timer exists the dashboard
service carries `TYCSWAP_AUTO_MANAGED_BY`, which makes the tray and dashboard
toggle refuse. The only off switch is a rebuild of the box.

History: the engine this tool replaced had `auto on|off` at run time
(flakelab 94c87ad, 2026-09-30). The migration to tycswap kept the timer and
dropped the switch ("tycswap has no such switch", flakelab bb3c9ba). When the
app gained its own Start/Stop engine, the conflict was resolved by locking the
toggle (014f894, DESIGN A51) instead of choosing one engine. The 2026-10-25
design kept the timer as given and made ticks stateless for it (A66); every
engine feature since (A75 to A77) lives only on the `--once` path.

A nine-lane review on 2026-11-10 reported 128 defects. Most trace to six
causes:

1. On/off is "does a scheduler exist", a provisioning fact, although A62
   states the provisioner owns scheduling and not policy. Nothing on disk says
   off, so no tick path can honour it, and a manual switch records nothing.
2. Four engine constructions (one-shot, `auto` loop, app-hosted, TUI) with
   different policy: only `--once` applies the session-model policy.
3. Ticks from independent processes share one decision state without a
   single-flight rule: strikes are counted per tick, and a decision is applied
   after the live account changed.
4. The status line observation is used as a trigger for a usage request, not
   as the reading it is: one forced request per 15 s, then 429s and a blind
   engine. On the reviewed box 429s went from none in the 11 days before the
   observe rollout to 121 in the 6 days after.
5. tycswap refreshes and rewrites credentials it does not own (the live
   login) and files a credential under the account the config names when it
   cannot attribute it.
6. Screens derive what the engine does from settings instead of from the
   engine, so they state the opposite of what the tick in charge does.

## Goals

1. One persisted on/off, set from the tray, the dashboard, the TUI and the
   CLI, honoured by every path that can move a login.
2. Choosing an account by hand is respected: the engine does not move it.
3. One tick implementation with one policy, whoever calls it, safe when
   several callers tick at once.
4. Decisions that cannot alternate between two accounts, never act on a
   reading that belongs to another account or a past window, and never fail
   over from a login that is merely idle.
5. No path discards a rotated token, files a credential under another
   account, or leaves a half-made switch unrepaired.
6. The Windows tray panel shows every account's windows, resets and reading
   age at once, and carries two switches (auto-switch, start at login) and no
   settings editor.
7. Every screen states what the engine does, from the engine's own record.
8. Less code: the engines, modes and features this supersedes are removed in
   the same series.

## Non-goals

- Upstream features tycswap does not have (priority tiers, per-account
  thresholds, new strategies).
- A redesign of the macOS and Linux menus beyond the defects listed.
- Codex engine changes beyond the off switch and the listed defects.

## Anti-goals

- A second fact that decides on/off (a scheduler's presence, an environment
  variable, an app-local preference).
- Hiding or disabling a control instead of making it work.
- A screen-side workaround for an engine decision.
- A constant that is not taken from a measurement or from existing precedent.

## Design

### D1. `autoswitch.enabled`

A bool in tycswap's `settings.json` beside the other `autoswitch.*` keys:
typed, validated, edited by `config set/unset`, carried by backup and
export like the rest. Default false (auto-switching is opt-in; upstream's menu
bar is "off until you turn it on"). When the key is absent and
`ui_prefs.json` holds `"autoSwitch": true` (A43), the first load seeds true and
removes that key from `ui_prefs.json`; A43's remember/resume is deleted.

`tycswap auto on|off|status [--json]` set and report it. `status` prints
enabled, the last tick (time, host, outcome, reason, counted models), the last
switch (from, to, trigger, window, origin) and whether anything has ticked
within three intervals ("enabled, but nothing is running the engine").

Every tick starts by loading settings. Disabled: one `no-switch` event with
reason `disabled`, exit status 2, no usage request, no state write, no Codex
tick, no group retirement. `--dry-run` still decides and says the engine is
off. Codex follows the same key (`codexEnabled` remains a narrower switch).

### D2. A manual switch turns auto-switch off (our call)

A switch onto an account the caller named (`switch <id>`, the tray's and
dashboard's row action, the TUI's) sets `autoswitch.enabled` to false as soon
as the switch has committed, when it was true and the switch moved the login.
A tick that decided before that re-reads the key under the state lock (D3
step 5), so it cannot undo the choice.
The result, the notification and the CLI line say so and name
`tycswap auto on`. Strategy switches (`switch` without a target,
`--strategy`, the dashboard's Switch now) do not: they ask the engine's
choice. The engine's own switches do not.

Rejected: a timed hold (needs a duration nobody measured) and a third "held"
state (one more state on every screen for the same two clicks).

### D3. One tick

`autoswitch.Engine` is built by one constructor. Per tick it

1. takes `.autoswitch_tick.lock` without waiting; held means `no-switch`
   `busy`, exit 2;
2. loads settings (a caller's CLI overrides are applied by the settings source
   it passes) and stops when disabled (D1);
3. derives the session policy (A76, A77) through a policy function the caller
   passes; `internal/cli` passes `reporting.DefaultSessionPolicy` for every
   host;
4. decides as today, with D4's rules;
5. before switching, under the state lock, re-reads the live slot and the
   enabled key and stops with `no-switch` `active-changed` or `disabled` when
   either moved;
6. records `lastTick` (time, host, outcome, reason, counted models) in
   `autoswitch_state.json`.

Removed with this: `ApplyModels`, `ApplyThreshold`, `adoptPendingModels`, the
pinned poll inputs, the dashboard's session-only 7d slider (the slider writes
the setting), the "running engine differs" chip, and `--once`-only wiring in
`autoCommand`. Lock order: tick lock, then the existing chain (state lock,
store lock, Claude Code's locks, usage lock, status line lock).

### D4. Decision rules

Each rule names its precedent.

- **Proactive landing.** A proactive move lands only on an account that is
  below every line it reports, and better than the active one by the
  hysteresis on the deciding window. Tier B (over another line) remains for
  `at-limit` and `failover` only. Upstream: "a proactive switch only lands on
  an account that's below the threshold and better by the margin"; A34 had
  narrowed both tests to one window, which lets two accounts hot on different
  windows trade places every cooldown (five switches in 17 minutes observed).
  Upstream's extra move when every account is over its lines is not taken: it
  needs two constants we have not measured.
- **A window the candidate does not report** cannot bind it: the 5-hour gate
  and hysteresis are skipped for an account without a 5-hour window
  (upstream "Accounts with no 5-hour window"; flakelab 400901c). A counted
  model window must still be reported (flakelab a755eb0, A61).
- **A window whose `resets_at` passed** before now, on a reading older than
  that reset, is unknown, not full, for the engine and for every at-limit
  marker.
- **Strikes** count a failed fetch attempt, not a tick: one strike per new
  `lastAttemptAt`, and only for an error that means the login is dead
  (401, 403, no credential, refused refresh). A 429, a timeout, a 5xx or the
  network is the endpoint failing (flakelab 6f2ccd0, 0cdade5).
- **An idle login whose access token expired** is held, never failed over
  (upstream "When something fails"); see D6.
- **A switch's warnings** reach the event; the `switch` event is emitted once
  the switch happened even when the state write then fails.
- **Stamps in the future** (`fetchedAt`, `lastSwitchAt`) read as absent.
- `tycswap auto` usage errors exit 64, not 2; non-finite numeric flags are
  refused; a Codex 429 is spelled like a Claude one so the post-429 floor
  applies; the one-time Codex import records that it ran.

### D5. The loop and its hosts

The loop is: tick, wait one interval (±10 %) or a wake, repeat. The 6-hour
sleep, `SleepUntil`, `WaitLong`, `IdleSlow` and the sleep event go: a tick
that has nothing to fetch costs a store read, and the usage store already
parks polls (A31).

Every long-lived tycswap process runs the loop while it lives: `app`
(tray or `--headless`), `web`, and `auto`. D3's lock makes several hosts
harmless. Start and Stop on every surface set D1's key; there is no engine
goroutine to start, stop, remember or resume, so the "stopping, try again"
error and its inverted retry go. A host that exits waits for the tick in
flight (no 2 s bound). `SIGTERM` and `SIGHUP` are handled like `SIGINT` in
every command: finish or roll back, release Claude Code's locks.

`--headless` prints each tick's JSON events on stdout, so a service's journal
holds the record the timer's journal held. It mints no launch token at
start and prints the address without one; a launch URL is minted on request
(`/api/launch`, `web --reuse`).

Removed: `TYCSWAP_AUTO_MANAGED_BY` and every branch on it, the dry-run engine
mode in the app and dashboard (a "Check now" action runs one dry tick and
shows its events), the TUI's own engine (its Auto screen shows D7's record
and toggles D1's key), the nil-engine Codex tick.

Deployment (flakelab, separate change): the timer, its oneshot, the
`quota_auto_resume_fired` hook and the option `tycswapAutoSwitchInterval` are
deleted; the app service hosts the loop on every box that wants it;
`autoswitch.intervalSeconds` is the one cadence. Our call, with the base
correct under any scheduler; the alternative is listed below.

### D6. Observation is a reading

`tycswap auto --observe` reads the status line JSON. Claude Code documents
`rate_limits.five_hour` and `.seven_day`, each with `used_percentage` and
`resets_at` in epoch seconds, no account identity and no per-model window.

- It records the session's model (A76) as today.
- It files the two windows as an observed reading of the live account when
  every window it carries matches that account's stored `resets_at` within
  600 s (flakelab 363bf4b's tolerance for the same misattribution), or
  follows a stored window whose reset has passed. Otherwise it files
  nothing: a session still on the previous account, a gateway, API-key or
  provider session (flakelab d69a1e8), a status line from a profile other
  than the default.
- The observed reading overlays the 5h and 7d windows for decisions while it
  is newer than the endpoint's; per-model windows and the poll plan stay the
  endpoint's.
- It runs one tick only when the filed reading moved a counted window across
  its line or to 100 %, or the session's model is news. That tick decides on
  the store; it forces no request.

Removed: `MarkUsageDue` from this path, the 15 s slot and
`observe_state.json`. A `StopFailure` hook with matcher `rate_limit` (the
documented event for a turn that ends on a limit) runs `tycswap auto --once`;
it replaces the resume notification, which fires after the wait.

### D7. The engine's record

`autoswitch_events.jsonl` in the store root, appended under the tick lock (a
manual switch appends under the store lock), trimmed to the newest 200 lines,
the number the dashboard's log shows today. Lines: every switch with trigger,
window, origin (`auto` or `manual`) and host; auto on and off with origin;
a `no-switch` whose reason differs from the previous line; errors;
quarantines. Polls are not recorded; `lastTick` (D3) is the heartbeat.
`tycswap.log` gets the same switch line. The dashboard's Event log, the tray's
last-switch line, the TUI and `auto status` read this file; the in-memory ring
goes.

### D8. Credentials

- tycswap never refreshes the live login. An expired live access token means
  "waiting for Claude Code": no request, the engine holds (D4). The unlocked
  refresh in `reporting.fetchActiveUsage` and its discard branches are
  deleted. Claude Code documents that its sessions coordinate the renewal of
  the saved login; a second refresher outside that protocol is the defect.
- A live credential that cannot be attributed is never written into a slot:
  it goes to the unclaimed stash and the switch says so. The same when the
  config has no account block but a credential is live.
- `add` and `export` take the live slot from the resolver inside the store
  lock and refuse when credential and config disagree.
- A slot's dead mark is lifted wherever its stored refresh-token lineage
  changes.
- A guarded refresh proves the slot is writable before the grant, and never
  sends one refresh token twice in a pass.
- The four writers that rename without retry on Windows go through
  `atomicfile`.
- An interrupted switch is repaired forward by the next command that takes
  the store lock: config and active pointer follow the slot whose lineage the
  live credential has.

### D9. Screens

The state document's `auto` object is `enabled`, `lastTick`, `lastSwitch`,
the counted models the last tick used, and D7's events. Tray, dashboard, TUI,
`list` and `status` render from it; at-limit markers use the same policy
function as the tick.

Windows panel: one page. Per account: slot and name, plan, status, each
window as share and reset (absolute when the engine is unreachable), and the
reading's age. No details box, no Settings page, no scope selector, no
model-limits switch, no "Used by" column. Footer: Auto-switch, Start at
login, Open dashboard, Add account, Quit, and the update row when one waits.
Double-click and Enter open the confirmation, which names the one
destination and says auto-switch will turn off. Countdowns are recomputed at
paint on a one-minute timer. Before the first state: "No data yet".

Start at login has one owner per box. Where a logon task with the app's
identifier exists, the switch enables and disables that task and reads its
state; otherwise it writes the Run value, with the flags the process was
started with. Status compares the entry's command with the one it would
write.

Notifications are queued, name the account, and are not raised for a dry
tick. Text menus show a switch row's second line. Linux menu ids and macOS
row tags identify the item, not its position; the Linux tray registers again
when the watcher changes owner.

Dashboard: add-token asks before replacing an occupied slot; one event
stream per browser, closed while the tab is hidden; buttons take their
enabled state from the state document; a switch that did not switch is not
reported as done; a session's Stop identifies the process by its start ticks
and executable, not by a start time rebuilt from boot time; the token field
is cleared when the dialog closes; a lost session points back to the app.

### D10. Removed

With the evidence the series must show for each: no remaining caller, or the
operator's instruction.

- `TYCSWAP_AUTO_MANAGED_BY`, the app's remembered engine choice, the dry-run
  engine mode, the TUI's engine (D5).
- The Claude/Codex handover: `internal/recovery`, the `recovery` verbs, the
  dashboard and tray handover views, the rotation inside the state build.
  Broken for Claude since the groups retired (554762d); nothing installs its
  hooks; operator's instruction 2026-11-10.
- Group leftovers: `groups guard`, the group branches of observe, the
  `TYCSWAP_DEFAULT_*` readers, the panel's scope selector and destination
  list, `routing.lock`.
- The dead code each lane listed, each with its grep.
- Open, the operator's: `tycswap run` / `env` session profiles and the
  ownership registry that exists for them.

## Alternatives considered

- **Keep the provisioner timer as the scheduler and add only the flag.**
  The 2026-10-25 design called the timer "the operator's chosen model
  (declarative on/off, journal lines, per-tick timeout)". Declarative on/off
  is the complaint; the journal record and bounded ticks are kept by D5. The
  hosted loop must exist anyway for boxes without systemd, so keeping the
  timer keeps two schedulers and two cadence settings. D1 to D4 hold under
  either, so this stays a small, reversible deployment change.
- **One configuration directory per account** (Claude Code's documented way
  to use several accounts) with no credential copying at all. A running
  session cannot change directory, so a session at its limit would have to
  be restarted and resumed elsewhere; this is the session-group design that
  was retired on 2026-11-04 as not worth its complexity. The in-place
  hand-over tycswap exists for depends on running sessions picking up a
  changed credential, which Claude Code does not document.
- **Return to upstream** (v1.0.0, maintained) with thin glue. Upstream has no
  Windows or Linux tray, no browser dashboard and no Codex side; the
  operator's boxes use all three. D4 and D5 re-align with upstream's rules
  where this port had diverged.

## Validation of the findings

`docs/superpowers/plans/2026-11-10-review-findings.md` lists every finding
with its verdict. A finding is fixed test-first: the regression test must
fail on the parent commit for the reason the finding gives. A test that
cannot be made to fail closes the finding as not real, with that evidence.
Findings on Windows and macOS are proven by the CI jobs for those systems.

## Rollout

1. Engine series (D1 to D7, the D10 items they retire), release.
2. Provisioner change, pin, update of each box; `tycswap auto on` where the
   timer ran, so the visible state does not change by itself.
3. Credential series (D8), tray and dashboard series (D9), remaining
   removals and findings, each with its release.
