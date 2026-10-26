# Engine cross-tick state, silent-skip visibility, credential-led identity

Date: 2026-10-25. Status: approved by the operator ("proper long-term fix").

## Why

On 2026-10-25 the auto-switch engine moved the default login onto account 5
at 12:53. From 12:56 until a manual re-login at 19:00 account 5 reported no
usage. Nothing was logged: no fetch failure, no sentinel, no reason. The engine
first decided on stale numbers for four hours, then reported
`active-usage-unknown 1/3 before failover` on every tick for 95 minutes and
never failed over, because flakelab runs the engine as a systemd timer firing
`tycswap auto --once` every two minutes and the strike counter lives in
process memory. At 15:02 the app resolved the live slot as account 6 although
the switch to 5 had happened at 12:53: a running Claude Code had rewritten
`~/.claude.json` from memory while the credentials were still account 5's.

Three defects, one per section below. The timer deployment stays: it is the
operator's chosen model (declarative on/off, journal lines, per-tick timeout).
The engine has to be correct under it.

## Goals

1. `tycswap auto --once` behaves exactly like one tick of the loop engine,
   including everything that depends on consecutive ticks.
2. When the active account's usage is not fetched, every consumer (journal
   events, `list --json`, `/api/state`, tray row details, dashboard) can say why.
3. The live slot is the account whose credential Claude Code will actually
   send, even when Claude Code's config file disagrees.

## Non-goals

- Moving rotation into the app service or changing the flakelab timer.
- New dashboard or tray layout. Existing status surfaces carry the new text.
- Repairing `~/.claude.json` automatically. Resolution and a warning only.
- Codex engine changes beyond what the shared tick code forces.

## Anti-goals

- A fix that only works in loop mode, or only in `--once` mode.
- Any skip path that still returns without recording a reason.
- Network refreshes of a credential an owner holds (the ownership guard is
  correct and stays).

## Constraints

- `docs/DESIGN.md` is the contract. Each change gets an entry there (new
  numbered A-section at the end, same style as A43 to A56) and a
  `CHANGELOG.md` Unreleased line.
- `AGENTS.md`: gofmt, `go test ./...`, `go vet ./...`, `GOOS=windows go vet`,
  `GOOS=darwin CGO_ENABLED=0 go vet`. Comments at or below 20 percent of
  nonblank lines per code file. Tests use temporary stores and fakes.
- State file schema stays `schemaVersion: 1`; new keys are additive and
  tolerated when absent or malformed (the file's existing tolerant-read rule).
- JSON output is additive: no existing `usageStatus` value changes meaning.
- The usage cache row is read by every process (engine, app, `list --json`),
  so a reason recorded by one is visible to all.

## Part 1: cross-tick decision state persists

### Behaviour

The engine's per-active-account decision memory moves into
`autoswitch_state.json` under one object:

```json
"active": {
  "number": "5",
  "unhealthyTicks": 2,
  "idleHoldSince": 1792950000.0
}
```

- At tick start the engine reads this object. If `number` differs from the
  current live slot, or the object is absent or malformed, the memory is zero.
- When the active account's headroom is known, the object is cleared (removed
  or written with zero counts). Today's in-memory resets at the same places.
- When the active account's usage is unknown, the tick increments
  `unhealthyTicks` (or starts/keeps `idleHoldSince` on the token-expired
  branch) and persists before emitting the `no-switch` event, so the event's
  `n/3` detail reflects the persisted count.
- Failover fires when the persisted count reaches `unhealthyTicks` from
  settings. After any switch the object is cleared.
- Dry-run ticks never write state (existing rule) but still read it, so a dry
  run reports the real count.
- Writes go through `mutateState` (under the state lock). The tick reads state
  lock-free at start as today. The switch path already holds the lock across
  recheck → switch → record; clear the object inside that same mutation.
- Loop mode uses the same persisted object, and the engine keeps no other
  decision state between ticks: a tick is a function of the store on disk and
  the clock. It returns the next-sleep hints (`SleepUntil`, `WaitLong`,
  `IdleSlow`) with its outcome, and the model-name check records its list in
  the state file (`modelNamesChecked`). An engine restart mid-outage continues
  counting instead of starting over. A dry run reads the file, reports what a
  real tick would do now and writes nothing; its only memory is the model list
  it last checked, for presentation (DESIGN A66).

Superseded by Task 7 on 2026-10-26: the engine keeps nothing between ticks.

### Tests (engine package, fake clock, temp state path)

- Three separate `Engine` values over one state file, each ticking once with
  the active account's usage unknown: the third emits a failover switch; the
  first two emit `active-usage-unknown` with `1/3` and `2/3`.
- A tick with known headroom after two unknown ticks resets the count; the
  next unknown tick reports `1/3`.
- The active slot changing between processes resets the count.
- Token-expired with an owner: the idle hold spans processes; after
  `IdleHoldMaxS` the next process falls through to unhealthy counting.
- A malformed `active` object is treated as absent.
- Dry run reads the count but does not change the file.

## Part 2: a skipped active fetch records why

### Behaviour

Every path in the active-account usage collection that returns without a
network request, and every path that discards a refresh result, writes a
short reason into the account's usage cache row:

| Path | Reason text |
|---|---|
| owner present, token expired | `owner holds an expired token; Claude Code refreshes it on next use` |
| live credential does not match this slot's backup | `live credential does not match slot N's backup; refresh skipped` |
| refresh discarded: owner appeared | `refresh discarded: an owner appeared mid-refresh` |
| refresh discarded: live refresh token changed | `refresh discarded: live credential changed mid-refresh` |
| persist failed under lock | `refresh discarded: <error>` |
| no credentials / no access token | `no credential in the live store` |
| keychain unavailable | existing sentinel text |
| quarantined (dead token) | existing sentinel text |

The row gains `skipReason` (string) and `skipAt` (seconds). A successful fetch
clears both. The reason is a sentence for humans, not a code.

Surfaces, all additive:

- `list --json` and `/api/state` account rows: `usageRefresh.skipReason` next
  to the existing `usageRefresh.error` and `nextAt`.
- Tray row details (`internal/cli/traypanel.go`): one line `Usage: <status> ·
  <skipReason>` when a reason is set. Today the line is `Usage: <status>`.
- Dashboard: the existing refresh chip (`usageRefreshNote` in `app.js`) shows
  the reason and its tooltip keeps the full text.
- Engine `no-switch` event with reason `active-usage-unknown` and the
  `active-idle` event carry the skip reason in `detail` after the count, for
  example `1/3 before failover · owner holds an expired token; …`.
- Logging: a WARNING when the reason for a slot changes (set, changed or
  cleared after being set), never on every tick. The usage store already
  remembers the previous reason in the row, so the comparison is local.

### Tests

- Each path above produces its reason in the cache row, with a temp store and
  fake oauth client; a following successful fetch clears it.
- `jsonout` serialises `skipReason` only when set.
- Tray panel detail line and the engine event detail include the reason.
- Logging fires once per change, not per tick (fake logger).

## Part 3: the live slot follows the credential

### Behaviour

`Store.CurrentAccountNumber()` today maps the identity in `~/.claude.json`
(`oauthAccount.emailAddress` + `organizationUuid`) to a slot and nothing else.
Two slots with the same email and different organisations (accounts 5 and 6,
the OBI pair) make this fragile: a running Claude Code rewrites that file
from memory, so after a switch the file can name the old organisation while
the credential file holds the new slot's token.

New rule, applied only when both candidates are managed OAuth slots:

1. Resolve `X` from the config identity as today.
2. Read the live credential and compute its lineage fingerprint
   (`oauth.CredentialFingerprint`, the refresh-token hash). Find the managed
   slot `Y` whose stored backup shares that fingerprint, or whose account
   block matches (`oauth.SameAccountBlock`).
3. If `Y` exists and `Y != X`: return `Y`. Log one WARNING per change:
   `live config names slot X (<email>, <org>) but the live credential is slot
   Y's; using Y. A running Claude Code rewrote ~/.claude.json`.
4. If no `Y`, or `Y == X`, behaviour is unchanged. If the config identity maps
   to no slot (unmanaged live login) behaviour is unchanged: the unclaimed and
   displaced-login machinery depends on that path and is out of scope.

Consumers that must agree: the engine's `CurrentAccountNumber`, the state
document's `activeNumber`, `status`, `list`, the tray's active row, the
provenance guard in `fetchActiveUsage` (it must compare against the resolved
slot's backup, which the new rule guarantees matches).

`tycswap switch Y` while the mismatch stands is "already active" today and
leaves the config wrong. Add: when the resolved slot is `Y` but the config
names `X`, `switch Y` (and `switch --force Y`) rewrites the config file for
`Y` under the usual triple lock and reports `config repaired`. This is the
only write this part adds and it goes through the existing switch code path.

### Tests (temp store)

- Config names slot 6, credential fingerprint matches slot 5's backup:
  `CurrentAccountNumber` returns 5 and a warning is logged once.
- Config and credential agree: no warning, unchanged result.
- Config names an unmanaged identity: nil as today, even if the credential
  matches a managed slot.
- API-key slots are excluded from lineage matching.
- `switch 5` under the mismatch rewrites the config and reports the repair.

## Order of work

Part 1, then Part 2, then Part 3. Each part is its own commit with tests,
DESIGN entry and CHANGELOG line. Parts 1 and 2 are independent of Part 3.

## Verification on the operator's box

After release and the flakelab pin bump (out of scope here, operator's call):
while the active account's usage is unknown, the journal lines of
`flakelab-tycswap-autoswitch.service` must show `1/3` and `2/3 before
failover` on consecutive ticks and then a failover switch, or a `Blocked`
reason when no target qualifies, and the `detail` must name the skip reason.
