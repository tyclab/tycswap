# Start on a marked login

Status: implementation design for the first release (S1–S4 and S6). S5 is a
separate, later release, gated on fleet acceptance of its replacement. Plan:
`docs/superpowers/plans/2026-11-14-start-login.md`.

## Purpose and boundaries

Allow an operator to mark accounts suitable for starting a command, select a
usable marked account before launch, and then let the ordinary engine rotate
the shared default login. A start creates no profile or claim. Account choice
is generic: this feature does not change, bypass, test the boundaries of, or
promise an outcome under organization-managed settings or model policy. The
operator chooses marks; account names, subscription names and policy outcomes
do not imply a mark. No settings tampering is part of this design.

The command receives the inherited environment and arguments unchanged. The
selection is best effort: stale cached usage, a concurrent engine or manual
switch, or the child reading credentials later can mean the child sees a
different login. Existing sessions share that login and are affected by a
switch. No child-read handshake or hold is claimed. No new credentials are
copied outside existing slots and the default profile.

## S1. Marking accounts

Account records gain `startLogin`, absent or non-boolean meaning false.
`tycswap start --allow ID`, `--disallow ID` and `--list` manage/list marks;
ID resolves using existing unambiguous account resolution. Marks persist on
the same account through swap, move, in-place re-add/re-login, re-add into a
new slot, add-token and import where the existing identity is retained. They
must not leak to a different identity reusing a slot. New identities default
to false; define matching using each lifecycle operation's existing identity
and conflict checks. Preserve existing independent metadata as well; do not
silently drop `disabled` or alias when refreshing the same identity. Writes
use the existing store lock and atomic sequence write.

## S2. Launch and failure behavior

`tycswap start [--account ID] [--] [command [args...]]` defaults to the real
Claude executable. Management options are exclusive with launching and each
other. Parse only the prefix before `--` or the first command; all remaining
arguments, including flags, pass unchanged. Malformed tycswap syntax is a
usage error (2), runs no child. An unknown or ambiguous explicit ID, or an
unavailable target, is a selection failure: report it and run the command on
the current login. This includes inaccessible/corrupt store or settings,
permission/private-root failure, lock failure, refresh/switch/stamp failure,
and unavailable usage or ownership information. Never continue to read or
write an unsafe store after a private-root failure. A failure after a completed
switch reports the switch and the failed stamp; it does not switch back or
retry the child. Failures print one sanitized diagnostic to stderr and, when
safe and possible, the log; absence of marks and an already-marked current
login are silent. Do not emit credentials. No stdout diagnostics pollute the
child's output. Unexpected process crashes and inability to execute the
requested command cannot be made fail-open; a launch failure is reported once
and exits nonzero (127 when executable is not found), never reruns a child.

Before store initialization or profile neutralization, preserve the incoming
environment. If `CLAUDE_CONFIG_DIR` is set (including empty), the command has
`--bare`, or any of the existing Claude auth override variables is present,
skip selection and run unchanged. Define the variable list by reusing the
existing `session.AuthOverrideEnvVars` contract, not a second guessed list.
This protects existing pinned shells throughout the first release. Do not
scrub or add environment variables, including for arbitrary commands. Check
arguments without consuming or rewriting them. An override skips even an
explicit account request with a diagnostic.

Without `--account`, no marks or a current marked account cause no switch or
stamp; auto-switch off also skips selection. With `--account`, the requested
account is the sole candidate, need not be marked, and is the explicit
exception permitted while auto-switch is off. Both forms apply all safety,
ownership, model and cached-usage eligibility checks in S3, including not
selecting a known 100% account. No implicit force, API-key approval, or prompt
is allowed. A no-op switch is not stamped. An explicit current account may
be reported as already active. Selection failures never toggle auto-switch.

Candidate preparation follows existing engine semantics where practical,
including refresh when near expiry with guarded credential ownership, token
identity verification and legacy release. No network call occurs when selection is skipped or when the
current account already satisfies the request. Retry the next ordered candidate
on per-account transient refresh, identity, or ownership/release failures;
quarantine invalid grant/no refresh token and identity conflict with the
engine's existing semantics. Other failures report a clear reason and end selection. An
explicit ID has no alternate. After a successful mutation do not attempt a
second target, even if stamping fails.

### Serialization in v0.11.6

The existing engine holds `.autoswitch_state.lock` across its enabled recheck,
`SwitchTo`, and stamp; `SwitchTo` takes the store and Claude locks. Use this
existing state → store → Claude order. Never hold the store lock while calling
`RefreshBackupGuarded` or `SwitchTo`: both acquire it themselves. Prepare and
freshen a candidate before acquiring the state lock. Under the state lock,
re-read the current login and auto-enabled setting immediately before switching,
then use the existing guarded switch and stamp only a confirmed mutation.
Concurrent starts serialize here; the second rechecks and does not switch or
stamp if the desired account is already live. Preserve unrelated state fields.
Do not acquire the state lock recursively while stamping or quarantining. A
switch or stamp error produces a clear diagnostic and launches the child once.

Enabled-setting writers and manual switches do not currently take this state
lock. Consequently, an off/manual operation racing just after the enabled
recheck can still lose to a start; similarly, another account or roster change
can race selection. The guarded switch retains its existing ownership checks.
This narrow race is accepted in the first release, and stronger cross-command
serialization may be added later if needed. Direct settings-file edits also sit
outside this protocol. Filelock's in-process mutex wait has no hard timeout;
do not claim a strict upper bound on startup latency.

When E1 lands, migrate these participants together to E1's lock order and
one-switch operation. Add a `start` origin and explicit preserve-auto
semantics to E1; do not use a separately locked stamp helper inside E1 or mix
orders. Reconcile E1's actual interfaces when it is implemented.

### Handoff

POSIX replaces tycswap with the resolved child. Windows spawns and waits,
returns the actual child exit code, and coordinates SIGINT so the wrapper does
not exit on the child's first Ctrl+C. A real launch failure is distinct from a
child exit; do not map every spawn error to 130. Preserve terminal handles and
standard streams. Test signal behavior on Windows; cross-compilation alone
cannot establish console behavior.

The provisioner launcher resolves a real executable that cannot resolve to
itself (including symlinks and path/case normalization). It invokes
`tycswap start -- <real executable> "$@"` if tycswap exists, otherwise executes
the real executable once. It cannot infer whether a nonzero status came from
tycswap or the child, so it must never retry after invoking tycswap. Direct
`tycswap start` without an explicit command must resolve the real executable
without selecting the launcher recursively; if that cannot be established,
report a launch error once. The launcher must not alter the child's environment
or arguments. `alias claude='tycswap start -- claude'` alone is unsafe when
`claude` resolves to the launcher; document an explicit verified real path.

## S3. Candidate ordering

Use cached usage only; no usage request is made by start. An automatic
candidate must be marked, pass `RotationEligible`, not be quarantined, and
pass the engine's counted-model guard derived from the same default session
policy and model-expansion rules. `RotationEligible` can provisionally accept
an idle legacy claim for which a release candidate exists; the guarded release
still has to succeed. An explicit candidate has the same gates except the
mark and auto-on requirements. No eligible candidates means fallback.

Rank in sequence order by these tiers, then lowest binding utilization and
sequence order for ties: A, all known counted windows below their configured
bars; B, at least one at or above a bar but none known at 100%, or unknown
usage; C, any known counted window at 100%, excluded. Resolve unknown binding
utilization deterministically after known values within B. If required model
coverage cannot be established from the same cached data, exclude the
candidate; do not invent model availability. Empty model requirements follow
the engine's own semantics. Cache may be stale: an apparently viable account
can in fact be exhausted, and this is not a guarantee of access to a model.
A fresh launch from an unmarked current login may switch back to a marked
account each time the engine moves away, including nested launches; each
actual start switch stamps the cooldown. An already-marked login does not
stamp, so frequent launches do not extend cooldown in that case.

## S4. Engine state

A confirmed start switch records the engine's `lastSwitchAt`, `lastSwitchTo`
and clears active memory, under the protocol above. `proactive` and `reserved`
respect cooldown; `at-limit`, `failover`, and `model-missing` do not. Model
coverage reduces but cannot eliminate a model-missing move: cached state and
session policy can change. There is no startup hold, and the stamp is not a
guarantee that the launched process reads that login. A stamp-write error is
visible and may allow an immediate engine move.

## S5. Later release: retire legacy profiles

`run`, `env`, `map`, and `unmap` remain available in the first release. Only
a subsequent release, after the fleet replacement is accepted, may remove
them and retire their claims. The later implementation must preserve login
ownership and migration: do not duplicate refresh-token lineages; leave live
or unverifiable claims intact; release idle claims only with verified identity,
including rotated/expired profile credentials under their own lock; retain the
slot backup on invalid grant. Scheduled non-dry ticks perform retirement,
including while auto-switch is off; observe and dry-run ticks do not. Surface
release failures consistently. Audit every caller before deleting helpers,
and retain conversation history. RECLAIM-1 through RECLAIM-5 remain open
until that behavior and command retirement ship. The first release documents
the existing reclaim limitations and event reasons (RECLAIM-6). The later plan
requires fresh code review before implementation.

## S6. Surfaces and rollout

`list`, dashboard and tray show the mark; the dashboard can toggle it. The
tray has no mark setting. The toggle applies to Claude accounts only; Codex
rows do not offer it. Update reference, design, changelog and help with the
actual behavior, including failure and stale-cache limits.

Ship S1–S4/S6 first, then deploy the provisioner launcher. Test with fake
providers and temporary stores; acceptance on each participating box verifies
launcher routing, one child invocation, account choice using operator-selected
authorized accounts, fallback and cooldown without inspecting private
transcripts. Do not alter account marks or real logins without the operator's
instruction. No acceptance seeks a model denied by an organization's policy.
S5 ships only after the operator accepts the replacement across the fleet.
