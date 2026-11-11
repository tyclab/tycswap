# One engine, an off switch, truthful screens

Date: 2026-11-10. Revision 2, after an independent design review the same
day (4 blockers, 10 majors; each was checked against the code and is folded in
below). Status: the operator said go on the plan this design details; calls
marked "our call" are the author's and open to reversal.

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

A nine-lane review on 2026-11-10 reported 128 defects
(`docs/superpowers/plans/2026-11-10-review-findings.md`): 93 to fix, 17 that go
with a removal, 13 duplicates, 4 in the provisioner, 1 by design. The causes
behind the ones this design removes:

1. On/off is "does a scheduler exist", a provisioning fact, although A62
   states the provisioner owns scheduling and not policy. Nothing on disk says
   off, so no tick path can honour it.
2. Four engine constructions (one-shot, `auto` loop, app-hosted, TUI) with
   different policy: only `--once` applies the session-model policy.
3. A switch is not one guarded operation. The engine decides, then calls a
   switch that takes other locks; a manual switch takes only the store lock
   and records nothing. So a decision is applied after the live account
   changed, a manual choice is undone, and no cooldown follows it.
4. The status line observation is used as a trigger for a usage request
   although it carries neither an account nor a measurement time: one forced
   request per 15 s, then 429s and a blind engine (none in the 11 days before
   the observe rollout on the reviewed box, 121 in the 6 days after).
5. Credential ownership is not a rule. The live credential is Claude Code's,
   yet tycswap refreshes it outside Claude Code's lock and throws the result
   away on a race; a stored slot is tycswap's, yet a switch overwrites it with
   bytes it could not attribute.
6. Screens derive what the engine does from settings instead of from the
   engine, so they state the opposite of what the tick in charge does.
7. An account is identified by an email string and a reusable slot number
   (STORE-9, STORE-16, STORE-19, WEB-9, CLI-11).
8. Results travel as untyped maps (ENG-10, ENG-14; a switch returns nothing at
   all in human-output mode).

## Goals

1. One persisted on/off, set from the tray, the dashboard, the TUI and the
   CLI, honoured by every path that can move a login.
2. Choosing an account by hand is respected: the engine does not move it.
3. One tick implementation with one policy, whoever calls it, safe when
   several callers tick at once; one switch operation that checks, switches
   and records under one lock.
4. Decisions that cannot alternate between two accounts, never act on a
   reading that belongs to another account or a past window, and never fail
   over from a login that is merely idle.
5. No path discards a rotated token, replaces a slot's only copy, or leaves a
   half-made switch unrepaired.
6. The Windows tray panel shows every account's windows, resets and reading
   age at once, and carries two switches (auto-switch, start at login) and no
   settings editor.
7. Every screen states what the engine does, from the engine's own record.
8. Less code: the engines, modes and features this supersedes are removed in
   the same series.

## Non-goals

- A redesign of the macOS and Linux menus beyond the defects listed.
- Upstream features with no finding behind them (priority tiers, per-account
  thresholds, `consume-first`).

## Anti-goals

- A second fact that decides on/off (a scheduler's presence, an environment
  variable, an app-local preference).
- Hiding or disabling a control instead of making it work.
- A screen-side workaround for an engine decision.
- A constant that is not taken from a measurement or from existing precedent
  (this code base, upstream claude-swap, or the provisioner's history).

## Design

### D1. `autoswitch.enabled`

A bool in tycswap's `settings.json` beside the other `autoswitch.*` keys
(typed, validated, listed by `tycswap config`), default false. When the key
is absent and `ui_prefs.json` holds `"autoSwitch": true` (A43), the first run
seeds true; A43's remember/resume is deleted. One switch covers both
providers: `codexEnabled` stays the standing choice "rotate Codex too".

- `tycswap auto on|off|status [--json]`; `auto on --if-unset` writes only an
  absent key (a provisioner's seed, so a fresh box needs no hand step and a
  user's later choice is never overwritten).
- Every change goes through one function, which also stamps
  `enabledChange: {at, by, to}` in `autoswitch_state.json` and appends an
  `auto-on` / `auto-off` line to the record (D7). `by` is the surface or
  `manual-switch`.
- A disabled tick does nothing but stamp the heartbeat (D5) and retire a
  former session group (that never moves the default login, and `add` tells
  its user the next tick does it): no usage request, no switch, no Codex
  tick, exit status 2 with one `no-switch` `disabled` event. A foreground `tycswap auto` (loop or
  `--once`) that finds it off says so on stderr and names `tycswap auto on`.
  `--dry-run` still decides.
- Unreadable `settings.json`: defaults apply, so auto-switch is off; the tick
  stores one `config-warning` naming the file (D7), and the screens show it.

Kept as a setting, not engine state (the review proposed
`autoswitch_state.json`): it is the same kind of thing as `codexEnabled`, the
user reads and edits it where the other choices are, and the check that
matters (D3) is race-free wherever the bit lives. Consequence stated: a
backup that carries `settings.json` carries the choice to the box it is
restored on.

### D2. One switch operation

Every switch (engine, named target, strategy, Codex) runs through one
operation that, under the store lock and Claude Code's locks:

1. evaluates the caller's guard: the live slot is the one the caller decided
   for, auto-switch is still enabled, the cooldown has elapsed (each only when
   the caller asks for it); a failed guard changes nothing and is reported as
   a typed refusal (`active-changed`, `disabled`, `cooldown`);
2. switches;
3. when the login moved (from differs from to): stamps `lastSwitch` (at, from,
   to, origin, trigger, window, host, provider) in the state file and appends
   the record line (D7), before releasing the lock.

It returns a typed outcome whatever the output mode. The cooldown reads
`lastSwitch.at` for every origin, so a strategy or manual switch is not
followed by an immediate engine move.

A switch onto an account the caller named (`switch <id>`, a row action in the
tray, dashboard or TUI, `add --login --switch`, a Codex row) turns
auto-switch off in step 3 when it was on and the login moved; the result
carries `autoSwitchDisabled`, and the CLI line, the notification and the
confirmation say so and name `tycswap auto on`. `switch <id> --keep-auto` (a
field in the API) leaves it on. Strategy switches (`switch`, `--strategy`,
Switch now) leave it on. (Our call. Rejected: a hold on the chosen account
that yields at a limit, which the incidents show would have been undone, and
a timed hold, whose duration nobody measured. Upstream has no rule here; its
hold is `disable`, which does not hold the active account.)

Lock order, one for every path: tick lock, store lock, Claude Code's locks,
then the short inner ones (state, settings, usage, record, status line). The
engine no longer holds the state lock around a switch.

### D3. One tick

`autoswitch.Engine` is built by one constructor. Per tick it

1. takes `.autoswitch_tick.lock` without waiting (a real try-lock; held means
   `no-switch` `busy`, exit 2, nothing stored); a dry tick takes no lock and
   writes nothing;
2. loads settings (a caller's CLI overrides are applied by the settings source
   it passes) and stops when disabled (D1), clearing the poll-policy pin;
3. derives the session policy (A76, A77) through a policy function the caller
   passes; `internal/cli` passes `reporting.DefaultSessionPolicy` for every
   host;
4. decides (D4) and, to switch, calls D2's operation with the guard "live slot
   as decided, enabled, cooldown where the trigger has one";
5. runs the Codex tick and the group retirement with the same settings,
   inside the same lock (not for a status-line tick);
6. records `lastTick` (time, host, outcome, reason, counted models).

A caller that only wants a fetch soon (the status line, a limit hook) marks
the active row due before it tries the lock, so a `busy` request is served by
the lock holder's next tick.

Removed: `ApplyModels`, `ApplyThreshold`, `adoptPendingModels`, the pinned
poll inputs, the dashboard's session-only 7d slider (the slider saves the
setting), the "running engine differs" chip, the separate Codex goroutine and
its start-time settings.

### D4. Decision rules: upstream's, ported

The port froze upstream's engine at its July state and then diverged. Upstream
v1.0.0 has since fixed the same failures with measured cases in its source
(`autoswitch.py`, `lines.py`, `poll_policy.py`). Its rules are ported, with
its constants; ours stay only where upstream has no equivalent (three
per-window lines, A34; reservation, A71 and A77; model-missing, A73; the
session-model policy, A76).

- **Headroom is measured against an account's own lines.** A proactive move
  lands only on a candidate that sits below every one of its lines and beats
  the active account by the hysteresis. (A34 judged both on the deciding
  window alone: five switches in 17 minutes on 2026-11-06.) A window an
  account does not report has no line for it (upstream "Accounts with no
  5-hour window"); a counted model window must be reported (A61).
- **Every account over its lines:** move to the one with the most raw
  headroom once it has `min(2 x, x + 3)` points where the active has `x`
  (`HORIZON_HEADROOM_RATIO`, `SPENT_HEADROOM_PCT`), and never back to the
  account just left until it has recovered by that margin (upstream's
  no-return bar).
- **Exhausted:** keep checking at the exhausted-account poll cadence; no long
  sleep (upstream `MAX_SLEEP_S`).
- **A window whose `resets_at` passed** on a reading older than that reset is
  unknown, not full, for the engine and for every at-limit marker.
- **Strikes:** at most one per interval, and only for evidence that the login
  is dead (401, 403, a refused refresh, no credential in the live store while
  the roster names a live slot). A 429, a timeout, a 5xx or the network is the
  endpoint failing (flakelab 6f2ccd0, 0cdade5). A login Claude Code cleared
  after a rejected refresh is therefore failed over, and a `StopFailure` hook
  with matcher `authentication_failed` may bring that tick forward.
- **An idle login whose access token expired** is held, never failed over
  (upstream "When something fails").
- A switch's warnings reach the event; stamps in the future read as absent;
  `tycswap auto` usage errors exit 64, not 2; non-finite numeric flags are
  refused; a Codex 429 is spelled like a Claude one; the one-time Codex import
  records that it ran.

The series that implements D4 starts with a written delta between the port's
engine and upstream v1.0.0's, rule by rule, and ports the rule with its test
cases.

### D5. The loop and its hosts

The loop is: tick, wait one interval (±10 %) or a wake, repeat. `SleepUntil`,
`WaitLong`, `IdleSlow`, the 6-hour cap and the sleep event go.

Every long-lived tycswap process that serves an engine view runs the loop
while it lives: `app` (tray or `--headless`), `web`, and `auto`. `app
--remote` hosts nothing. D3's lock makes several hosts harmless. Start and
Stop on every surface set D1's key; there is no engine goroutine to start,
stop, remember or resume, so the "stopping, try again" error and its inverted
retry go.

- A host stamps a heartbeat (host, pid, time) in the state file every round,
  enabled or not; "running" on every screen is a loop host's heartbeat within
  two intervals (one may be in flight), not the last tick.
- A tick that runs longer than 180 s (the bound the provisioner's unit and
  hook have used since the start) ends the host with status 1: a service is
  restarted, the tray app restarts itself the way it does after an update. A
  host that exits otherwise waits for the tick in flight. `SIGTERM` and
  `SIGHUP` are handled like `SIGINT` in every command: finish or roll back,
  release Claude Code's locks.
- A long-lived host must not keep a missed identity lookup for good: the
  resolver's memo of a miss expires like `ReaskProfile`'s
  (`profileMissReuse`).
- `--headless` prints every tick's events as JSON lines on stdout and follows
  the record (D7) for lines other processes appended, so a service's journal
  holds every decision on the box. It mints no launch token at start and
  prints the address without one.

Removed: `TYCSWAP_AUTO_MANAGED_BY` and every branch on it, the dry-run engine
mode in the app and dashboard (a "Check now" action runs one dry tick and
shows its events), the TUI's own engine (its Auto screen shows D7's record
and toggles D1's key).

Deployment (the provisioner, a separate change, after the release that has
the tick deadline): exactly one host per box, the app service where the
dashboard port is set, otherwise a service running `tycswap auto --json`. The
timer, its oneshot and the interval option go; an option for the initial
state replaces it and is applied with `tycswap auto on --if-unset` at
activation. The status-line wrapper and the limit hook are gated on tycswap
being installed. Until then the timer stays: with D1 and D3 it is correct
beside a host.

### D6. The status line is a hint

Claude Code documents `rate_limits.five_hour` and `.seven_day` in the status
line's input, each with `used_percentage` and `resets_at`; there is no
account, no measurement time and no per-model window, and an idle session
redraws its last figures on every refresh. So the payload is never filed as a
reading.

`tycswap auto --observe`:

- records, per session (the file that already holds its model), the last
  payload and when it changed; an unchanged payload is not news; a session
  with a gateway, API-key, provider or `CLAUDE_CODE_OAUTH_TOKEN` login, or in
  a profile other than the default, is ignored (flakelab d69a1e8);
- when a changed payload puts a window at or over its line, marks the active
  account's row due, at most once per `UrgentIntervalS` and inside the 429
  floor and backoff the store already keeps, then ticks. The tick decides on
  the endpoint's reading;
- ticks at once when the session's model is news (A77), on stored usage.

`MarkUsageDue` on every near-bar redraw, the 15 s slot and
`observe_state.json` go. A `StopFailure` hook with matcher `rate_limit` (the
documented event for a turn that ends on a limit) marks due and ticks; it
replaces the resume notification, which fires after the wait.

### D7. The engine's record

`autoswitch_events.jsonl` in the store root, its own lock taken last, trimmed
to the newest 200 lines (the dashboard's log length). Lines carry an id, the
time of the event (a switch's commit time), host, provider, kind, origin and
fields.

- A switch line is written inside the switch (D2) and carries what justified
  it: trigger, window, the deciding readings of both accounts, the lines, the
  counted models. A switch killed before its line leaves `lastSwitch` to
  repair from (D8).
- `auto-on` / `auto-off` with `by`.
- A `no-switch` or an error only when its reason or message differs from the
  newest stored line; `busy` never; polls never.
- Quarantines and `config-warning`.

An append first completes a truncated last line with a newline; a reader
skips a line that does not parse. `tycswap.log` gets the same switch line.
The dashboard's Event log, the tray's last-switch line, the TUI and `auto
status` read this file; the in-memory ring goes.

### D8. Credentials: who owns what

The live credential is Claude Code's. tycswap writes it in a switch, and
refreshes it only when no session owns it, the way Claude Code's own sessions
do: store lock and Claude Code's credential lock first, re-read under them,
stop when the token is no longer expired or changed, grant, write the live
store and the slot. (Upstream relies on the same protocol: Claude Code
re-reads after the lock and aborts its refresh when the token is fresh.) A
stored slot is tycswap's.

- No path discards a rotation. A grant whose write fails is kept in the
  unclaimed stash and adopted by the next pass.
- A live credential the switch cannot attribute is written into the resolved
  slot as today, and the backup it replaces goes to the stash, so both
  survive. "The bytes moved since the pre-lock read" is retried, not treated
  as unattributable. The stash gets a reader, `tycswap unclaimed
  [list|claim]`, and an entry is promoted or dropped once a profile answer or
  an `invalid_grant` settles the owner. The same when the config has no
  account block but a credential is live.
- `add` and `export` take the live slot from the resolver inside the store
  lock and refuse when credential and config disagree.
- A slot's dead mark is lifted wherever its stored refresh-token lineage
  changes.
- A guarded refresh proves the slot is writable before the grant, and never
  sends one refresh token twice in a pass.
- The four writers that rename without retry on Windows go through
  `atomicfile`.
- An interrupted switch is repaired forward by the next command that takes
  the store lock: config, active pointer and `lastSwitch` follow the slot
  whose lineage the live credential has.

Like D4, this series starts with a delta against upstream v1.0.0's
credential handling (its stash "successors" and `unclaimed` verb) and ports a
rule where upstream has one.

### D9. Screens

The state document's `auto` object is `enabled`, `enabledChange`, `running`
(D5), `lastTick`, `lastSwitch`, the counted models the last tick used, and
D7's events. Tray, dashboard, TUI, `list` and `status` render from it;
at-limit markers use the same policy function as the tick.

Windows panel: one page, the daily view. A title; one row per account
with its name, the plan under it and a warning when its reading failed;
one column per limit (5-hour, weekly, each model window) showing the
share, a bar and, small under the bar, that limit's own reset as clock
time and time left; a centred dash where the account has no such limit
or its plan cannot run the model; a Switch button per row, "In use" on
the active one. Under the list, one line for the last action's result.
Footer: Quit on the left (and the update buttons while an update
waits), Auto-switch, Start at login and Open dashboard on the right. No
tabs, no settings, no details box, no "Used by" column, no sorting, no
confirmation: Switch acts at once and turns auto-switch off when it was
on, so the login stays where the user put it; the line under the list
says so. Before the first state: "No data yet".

Start at login has one owner per box. Where a logon task with the app's
identifier exists, the switch enables and disables that task and reads its
state; otherwise it writes the Run value, with the flags the process was
started with. Status compares the entry's command with the one it would
write.

Notifications are queued, name the account, and are not raised for a dry
tick. Text menus show a switch row's second line. Linux menu ids and macOS
row tags identify the item, not its position; the Linux tray registers again
when the watcher changes owner.

Dashboard: add-token asks before replacing an occupied slot; one event stream
per browser, closed while the tab is hidden; buttons take their enabled state
from the state document; a switch that did not switch is not reported as
done; a session's Stop identifies the process by its start ticks and
executable, not by a start time rebuilt from boot time; the token field is
cleared when the dialog closes; a lost session points back to the app.

### D10. Removed

With the evidence the series must show for each: no remaining caller, or the
operator's instruction.

- `TYCSWAP_AUTO_MANAGED_BY`, the app's remembered engine choice, the dry-run
  engine mode, the TUI's engine (D5).
- The Claude/Codex handover: `internal/recovery`, the `recovery` verbs, the
  dashboard and tray handover views, the rotation inside the state build,
  `autoswitch.handoverWaitMinutes`. Broken for Claude since the groups
  retired (554762d); nothing installs its hooks; operator's instruction
  2026-11-10.
- `tycswap run` / `env` session profiles and the parts of the ownership
  registry that exist only for them: operator's decision 2026-11-10. First
  the helpers other features use move out of `internal/session` and
  `internal/sessprofile` (the launch and Keychain-name helpers behind `add
  --login`, `codex login`, the updater, purge and migrate); then a one-time
  step takes each profile's credential into its slot when it is the slot's
  lineage and newer, as A78 did for groups.
- Group leftovers: `groups guard`, the group branches of observe, the
  `TYCSWAP_DEFAULT_*` readers, the panel's scope selector and destination
  list, `routing.lock`.
- The dead code each lane listed, each with its grep.

## Alternatives considered

- **Keep the provisioner timer as the only scheduler and delete the hosted
  loop.** Simpler process model on a systemd box: a fresh process per tick, a
  hard per-tick bound, crash isolation; in 11,891 recorded ticks none
  overlapped and the bound was never needed. But the loop has to stay for
  boxes without systemd and `--once` for the hook, so no code goes, two
  cadence settings remain, and the provisioner stays in a loop the operator
  wants out of. D5 takes the timer's three guarantees into the host (tick
  deadline, expiring lookup memo, journal lines). D1 to D4 hold under either,
  so this stays a small, reversible deployment choice.
- **One configuration directory per account** (Claude Code's documented way
  to use several accounts), no credential copying. It removes the whole
  credential class, but a running session cannot change directory, so a
  session at its limit must be restarted and resumed elsewhere. The tool
  built that twice (session groups, run profiles); both are retired, and 15
  of the 17 rows that go with a removal come from them.
- **Return to upstream** (v1.0.0, maintained) with thin glue. Upstream has no
  Windows or Linux tray, no browser dashboard, no Codex side and no
  session-model policy; the operator's boxes use all four. D4 and D8 take
  upstream's rules instead.
- **Engine and CLI only**, dropping dashboard and tray (about 37 findings
  go). It gives up the at-a-glance view and a tray off switch, which are the
  operator's two requests.
- **Untested, worth a pilot:** a setup token as the live credential. Claude
  Code never rotates it, so tycswap alone would own every rotating login and
  cause 5 would be gone by construction. Unknown: what the usage endpoint
  returns for it and whether every feature in use works on it.

Evidence that would change the choice: a measurement that running sessions
often keep or write back the previous login after a switch (the log has 66
config-versus-credential warnings and 5 unattributable backups in about 200
switches); Claude Code documenting or changing how sessions re-read
credentials; upstream gaining a Windows surface and the session policy.

## Risks outside the code

- The hand-over a switch relies on, a running session picking up a changed
  credential, is not documented by Claude Code for a normal session, and its
  refresh lock is not documented either.
- The published terms say nothing explicit about rotating between one's own
  subscriptions. The Claude Code legal page says developers "may not collect,
  store, or intermediate Claude.ai credentials or session tokens", which
  describes what this tool does, and also that this does not "prevent an end
  user from signing in to the unmodified Claude Code binary with their own
  Claude subscription". Whether to run the tool is the user's decision.

## Validation of the findings

`docs/superpowers/plans/2026-11-10-review-findings.md` lists every finding
with its verdict. A finding is fixed test-first: the regression test must
fail on the parent commit for the reason the finding gives. A test that
cannot be made to fail closes the finding as not real, with that evidence. A
defect a review finds in a change is fixed in that change. Findings on
Windows and macOS are proven by the CI jobs for those systems.

## Rollout

1. E1: the off switch (D1), the switch operation (D2), one tick (D3), hosts
   (D5), the record (D7), the removals they retire. Release. The provisioner
   pins it and seeds `auto on` where the timer ran, so nothing changes by
   itself; the timer stays.
2. E2: decision rules (D4) and the status line (D6). Release; the provisioner
   moves to one host per box and deletes the timer.
3. C: credentials (D8). U: tray and dashboard (D9). R: removals (D10) and the
   remaining findings. Each with its release.
