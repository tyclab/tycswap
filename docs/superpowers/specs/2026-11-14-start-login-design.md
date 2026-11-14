# A start login for every session, and the end of `run` / `env`

Status: draft for the operator's go. Nothing in it is built.
Review of v0.11.6 that led here: rows RECLAIM-1 to RECLAIM-6 in
`docs/superpowers/plans/2026-11-10-review-findings.md`.
Plan: `docs/superpowers/plans/2026-11-14-start-login.md`.

## Why

**What a session may run is decided in the second it starts.** Claude Code
fetches an organization's server-managed settings at process start, for the
organization that owns the login at that moment. Those settings can deny a
model (`deniedModels`, `availableModels`); a denied `--model` is replaced with
a notice and a denied `/model` is refused
([model configuration](https://code.claude.com/docs/en/model-config),
[server-managed settings](https://code.claude.com/docs/en/server-managed-settings)).

Measured on one box, transcripts from 2026-11-12 to 2026-11-14 (56 main
sessions, 193 transcripts with their subagents) against the switch log (204
switches):

- 5 sessions carry the notice `Model "…" is restricted by your organization's
  settings`. All 5 have it in their first 8 lines, and all 5 started on a
  login of the denying organization.
- Of 16 main sessions that started on such a login, 15 answered first with
  the fallback model. Of 40 that started on another login, 32 answered first
  with the model the organization denies.
- A process that started on another login keeps the model after rotation:
  such sessions wrote 4,935 answers with the denied model while a login of
  the denying organization was the default login, 1,966 of them more than 60
  minutes into an unbroken stretch on it. Two sessions changed to the denied
  model 11 minutes after their start, while on such a login.
- `--model` does not help: the 5 sessions asked for the model by name.

**The working method is manual and is undone by the engine.** The operator's
sequence is: auto-switch off, switch, start the sessions, auto-switch on. The
log shows the engine moving the login back 10 seconds and 3 seconds after it
was turned on again: a switch by hand stamps no cooldown
(`autoswitch/tick.go` stamps `lastSwitchAt` only in `perform`).

**`run` / `env` were the other way to start on a chosen account, and they
pin it.** A pinned account is out of the rotation for every other session;
on 2026-11-14 three pinned accounts left the default login without a usable
target. v0.11.6 added an automatic reclaim that now breaks the pins
themselves (RECLAIM-1, RECLAIM-2). The removal was decided on 2026-11-10
(`2026-11-10-one-engine-design.md`, D10); what it lacked was the
replacement for "start on this account".

The operator's words:

- 2026-11-13: "i need a way to start a claude session with … ultra account
  but then let it switch after start back to auto mode" (the account names
  are left out here).
- 2026-11-14: "should we include it more general to have the option to have
  session start account default?"
- 2026-11-14: "even if that account is at limit should still work right? and
  if creds are gone we would just use auto one or something like that?"
- 2026-11-14: "might it be easier to just do it for all claude sessions? so i
  can be sure i can switch to fable later?"
- 2026-11-10, on `run` / `env` session profiles: "Remove them".

## Goals

Each goal names the test that proves it (plan task in brackets).

1. **A session starts on a start login when one is usable.** With the
   default login on an unmarked account and a marked account below its bars,
   `tycswap start -- <command>` leaves the default login on the marked
   account and runs the command with its arguments and environment
   unchanged. `TestStartSwitchesToStartLogin` [T2].
2. **A start is never blocked.** No marked account, none usable, auto-switch
   off, or a failed switch: the command still runs on the current login, and
   one line on standard error and in the log says why.
   `TestStartFallsBackAndStillRuns` (one case per reason) [T2].
3. **The engine does not undo a start.** The start switch stamps the
   engine's `lastSwitchAt`; a tick inside the cooldown with the new account
   over a proactive bar reports `cooldown`.
   `TestStartSwitchHoldsTheProactiveCooldown` [T2].
4. **No pin.** A start creates no profile and no claim and sets no
   `CLAUDE_CONFIG_DIR`. `TestStartLeavesNoClaim` [T2].
5. **It covers every session of the fleet.** The provisioner puts a `claude`
   launcher ahead of the real one on `PATH`; a session started by typing
   `claude`, by `flakelab sessions --start claude` or by `claude -p` goes
   through `tycswap start`. Provisioner check plus acceptance A1 [T5].
6. **`run`, `env`, `map` and `unmap` are gone**, and profiles they left
   behind return their login to the account slot once, including a login
   that rotated in its profile and has since expired.
   `TestLeftoverProfileRelease` (cases idle, live, rotated, rotated and
   expired, offline) [T4]. Closes RECLAIM-1 to RECLAIM-5.
7. **The screens say which accounts are start logins** and why a held
   account is held. `TestAccountRowStartLogin`, `TestTrayPanelStartLogin`
   [T3].

## Non-goals

- Two logins in use at once. Sessions share the default login, as since A78.
- Choosing a start login per directory (`map`). It goes with `run`.
- Sessions that do not use the OAuth login (`--bare`, an API key in the
  environment): `start` runs them unchanged.

## Anti-goals

- No path that leaves auto-switch off after a start, and no start that moves
  the login while auto-switch is off: off means nothing moves it (A83).
- No copy of a login outside the default profile and the account slots.
- No wait, timer or hold that ends on a guessed duration.

## Design

### S1. Start logins are marked per account

An account record gains `startLogin` (boolean, absent means false), set like
`disabled`:

```
tycswap start --allow <NUM|EMAIL|ALIAS>
tycswap start --disallow <NUM|EMAIL|ALIAS>
tycswap start --list
```

The dashboard's account menu gets the same toggle. The flag lives in the
record, so it survives a slot renumbering (a slot moved from 2 to 8 on the
measured box). With no account marked, `start` runs the command and does
nothing else: a store that never uses the feature sees no change.

Why a flag and not a rule: which organization denies which model is not
readable from a stored login. The plan name does not say it either (a login
without a top-tier plan started the denied model in the measurements).

### S2. `tycswap start [--account <id>] [--] [command [args…]]`

`command` defaults to `claude`. In this order, each step under the store
lock where it reads or writes the store:

1. `CLAUDE_CONFIG_DIR` is set, `--bare` is among the arguments, or the
   environment carries an API key Claude Code would use: run the command.
2. No account is marked and no `--account` is given: run the command.
3. Auto-switch is off and no `--account` is given: run the command, one line
   (`auto-switch is off; starting on Account-N`).
4. The live login (`ResolveLiveSlot`) is a marked account: run the command.
5. Pick the target (S3). None: run the command, one line with the reason.
6. Freshen the target as the engine does (`RefreshBackupGuarded` when the
   stored token is near expiry), then switch with the ordinary guarded
   switch. A failure: run the command, one line with the error.
7. Stamp the engine state: `lastSwitchAt` now, `lastSwitchTo` the target,
   the active memory cleared, as `perform` does.
8. Run the command: `exec` on POSIX, spawn and wait on Windows, the code
   `run` used for the default login (`session.ExecDefault`), moved to
   `internal/launch`.

Every one-line notice also goes to the log file: a terminal UI that clears
the screen would hide it.

`--account <id>` names the target for this start and skips S3; it is the
explicit form of the operator's manual sequence and works with auto-switch
off, because a person named the account.

### S3. Which marked account

Candidates are the marked accounts that `store.RotationEligible` accepts
(stored login and config, not disabled, not an API key, not held by another
owner) and that the engine has not quarantined. Usage comes from the store's
cached entries only: a start makes no usage request.

- **Tier A:** every counted window is below its bar (`autoswitch.*Threshold`,
  the counted models as the engine counts them).
- **Tier B:** a window is at or over its bar but none is at 100 %, or the
  usage is unknown.
- **Tier C:** a window is at 100 %. Never chosen (see "Ruling wanted").

The first non-empty tier wins; inside a tier the lowest binding utilisation,
ties by sequence order. The engine's strategy is not consulted: its next
proactive move re-ranks with fresh usage.

### S4. What the engine changes

One thing: it reads the stamp of S2 step 7 like its own. `reserved` and
`proactive` moves wait out `autoswitch.cooldownSeconds`; `at-limit`,
`failover` and `model-missing` do not, as today. No new setting.

When E1 Task 1 of the one-engine plan lands ("one switch operation"), the
start switch becomes its origin `start` with `KeepAuto`; until then S2 step
7 calls an exported stamp helper in `internal/autoswitch`.

### S5. `run`, `env`, `map`, `unmap` and their profiles

Removed as D10 describes, in this order: the launch and Keychain-name
helpers other features use move out of `internal/session` and
`internal/sessprofile`; the four commands, their help, README and reference
sections go; the ownership scan keeps reading legacy claims until none is
left.

Leftover profiles retire as session groups did (A78), replacing A89's
release before every poll: a scheduled tick that is not a dry run releases
each claimed profile once no process uses it.

- A profile whose login is the slot's own lineage is released as today.
- A login that rotated in its profile is verified by the profile endpoint
  while its access token lives. Once it expired, the release refreshes it
  with the profile's own refresh token under the profile's credential lock
  and takes the identity the refresh returns (RECLAIM-3). `invalid_grant`
  drops the dead login with its claim and keeps the slot's backup, as A78.
- A release that cannot verify (offline, an identity that does not resolve)
  keeps the claim and stores the reason on the claim. The snapshot shows
  that reason, so the dashboard, the tray and the engine give the same
  answer (RECLAIM-4).
- A hand-over tick with a held account emits one line (RECLAIM-5).

With no command left that creates a claim, RECLAIM-1 and RECLAIM-2 cannot
occur. `sessions/` stays on disk: it holds conversation history.

### S6. Screens

`list`, the dashboard and the tray panel show `start` on a marked account.
The dashboard menu toggles it. The tray gets no setting (operator's ruling:
no settings in the tray). `no-switch` reasons `accounts-in-use` and
`account-unavailable` enter the reference and the port spec's reason list
(RECLAIM-6).

## Ruling wanted: a start login at 100 %

The operator asked whether a start works when the start account is at its
limit. It can be built, at a price:

- For as long as the login sits on the exhausted account, every other
  running session's request fails. On 2026-11-14 ten agents ended that way
  when the login they shared reached a five-hour limit.
- `at-limit` ignores the cooldown, so the engine would move the login away
  before the new process has read it. It needs a hold: `start` records its
  process id, and a tick does nothing while that process lives and has not
  yet written `sessions/<pid>.json`. One traced start wrote that file 1.8 s
  after `exec`, 1.5 s after it read the login. Whether the organization's
  settings are bound by then, and whether an interactive start writes the
  file before its folder-trust question, are not measured.

This design leaves tier C out: with every marked account at 100 % the
session starts on the current login and says so. If the operator wants tier
C, it is a second step with that hold and the two measurements first.

## Limits, accepted

- A tick's `at-limit` or `failover` move can land between `start`'s check
  and the new process reading the login (0.3 s after `exec` in the traced
  start). The session then starts on the engine's target.
- With the live login already a marked account, `start` stamps nothing: a
  stamp on every start would hold proactive moves for as long as sessions
  keep starting, until the account reached 100 %. A proactive move in the
  same second as a start can therefore still win.
- **Not proven: changing to a denied model later.** A session that started
  on a start login and has run more than an hour on a denying
  organization's login may have fetched that organization's settings (they
  are polled hourly). Continuing with the model is measured (1,966
  answers); changing to it then is not. Acceptance A2 measures it. If it
  fails, the remedy is a `PreModelSwitch` hook, designed separately.
- Sessions started while a denying organization's login is live run under
  all of that organization's managed settings (plugins, telemetry
  endpoints, minimum version), not only its model list. Starting on a start
  login avoids that at start; what a running session takes over at the
  hourly poll is not measured.

## Alternatives considered

- **Only for sessions that ask for the denied model.** Needs the launcher to
  read `--model`, the settings' default model and a later `/model`. The
  operator's question settles it: all sessions, so a later change of model
  stays possible.
- **A `SessionStart` hook that switches.** The hooks reference does not say
  whether it runs before the managed settings are bound; a launcher runs
  before the process exists.
- **Keep `run` / `env` and narrow the reclaim.** A shell pinned with `env`
  and no Claude process is the same on disk as an abandoned profile; any
  rule that spares the first keeps the second out of the rotation, which is
  the complaint of 2026-11-14.
- **Prefer start logins in the rotation.** The login would sit on them
  until a bar, which uses them up first and leaves none for a start.

## Acceptance on the fleet

- **A1.** With a denying organization's login live and a marked account in
  tier A: `tycswap start -- claude -p "<one line>" --model <denied model>`.
  The transcript has no restriction notice and its first answer names the
  requested model; the switch log shows one switch and no return inside the
  cooldown. Needs the operator's go when the login must be moved onto the
  denying organization for the test: it moves the login of every running
  session.
- **A2.** A session started through `start` with another model; after more
  than 60 minutes unbroken on a denying organization's login, `/model
  <denied model>` and one prompt. Pass: the answer names that model.

## Rollout

tycswap release with S1 to S4 and S6, then the provisioner's launcher (goal
5) and A1; S5 in the release after, so a box has the replacement before the
commands go. DESIGN A90 records S1 to S4, A91 the retirement.
