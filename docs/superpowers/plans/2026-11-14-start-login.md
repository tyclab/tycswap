# Start login: plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every session starts on a marked start login when one is usable,
the engine then rotates as before, and `run` / `env` / `map` go.

**Spec:** `docs/superpowers/specs/2026-11-14-start-login-design.md` (S1 to
S6). Findings closed here are RECLAIM-1 to RECLAIM-6; delete each row from
`docs/superpowers/plans/2026-11-10-review-findings.md` in the commit that
closes it.

**Not started.** The operator's go on the spec starts Task 1. The ruling on
a start login at 100 % (spec, "Ruling wanted") does not block Tasks 1 to 5.

**Architecture:** `tycswap start` is a launcher, not an engine: it reads the
store, switches at most once through the existing guarded switch, stamps the
engine state and replaces itself with the command. It holds no state after
`exec`. The engine learns nothing new; it reads one more writer's stamp.

## Global Constraints

- Test-first: each test named below fails on the parent commit for the
  reason given. If it cannot be made to fail, stop and report.
- Every commit builds and its packages' tests pass on Linux, Windows and
  macOS. The gate per task: `gofmt -l .` prints nothing, the touched
  packages' tests, `go vet ./...`, `GOOS=windows go vet ./...`,
  `GOOS=darwin CGO_ENABLED=0 go vet ./...`.
- No `go build`, `go test` or `go vet` on a laptop: the dev VM.
- Tests use temporary stores and the packages' fakes, never a real login.
  Never log or print a credential or token.
- `start` makes no network request on the path that does not switch. A test
  asserts it with a fake client that fails the test when called.
- Lock order as in the one-engine plan: the store lock, then Claude Code's
  locks, then `.autoswitch_state.lock`; never the store lock while
  `.autoswitch_state.lock` is held.
- Public repository: no private addresses, hostnames, account names or
  employer details in code, tests, docs or commit messages.
- One commit per task, conventional-commit subject, a body that says why.
  Every changed contract is in `CHANGELOG.md` in the same commit, removals
  under **Breaking**.

## Review Focus

1. Auto-switch off, a marked account available, no `--account`: the login
   does not move (Task 2, `TestStartFallsBackAndStillRuns/auto-off`).
2. The command's arguments and environment reach it byte for byte, also
   arguments that look like tycswap flags after `--` (Task 2).
3. A start switch followed by a tick with the new account over a proactive
   bar: `cooldown`, no switch. The same with the account at 100 %: the
   engine moves, as the spec says (Task 2).
4. Two starts at once: one switch, both commands run, one stamp (Task 2,
   `TestStartConcurrent`).
5. A leftover profile with a live process is never released, and a release
   never leaves the login in both the profile and the slot (Task 4).
6. `start` with no marked account does nothing but run the command: no store
   write, no lock file left, no line printed (Task 2).

---

### Task 1: the `startLogin` flag

**Files:**
- Modify: `internal/lifecycle/disable.go` (add `SetAccountStartLogin`, same
  shape as `SetAccountDisabled`), `internal/store/identity.go`
  (`StartLoginNumbers() []string`, in sequence order)
- Modify: `internal/reporting/snapshot.go` (`AccountSnapshot.StartLogin`),
  `internal/web/state.go` (`startLogin` in the row, omitted when false)
- Test: `internal/lifecycle/disable_test.go`, `internal/store/identity_test.go`,
  `internal/web/state_test.go`

- [ ] Failing tests: `TestSetAccountStartLogin` (on, off, unknown account,
  survives `swap` of two slots), `TestStartLoginNumbers` (order, absent
  field is false, a record of the wrong type is false),
  `TestAccountRowStartLogin`.
- [ ] Implement; the write goes through the store lock like `disabled`.
- [ ] Gate, commit `feat(accounts): mark start logins`.

### Task 2: `tycswap start`

**Files:**
- Create: `internal/launch/launch.go` (+ `exec_posix.go`, `exec_windows.go`,
  moved from `internal/session`; `session.ExecDefault` calls it until Task 4)
- Create: `internal/switching/start.go`, `internal/cli/startcmd.go`
- Modify: `internal/autoswitch/state.go` (exported `StampSwitch`),
  `internal/cli/cli.go` (pre-dispatch `start`), `internal/cli/help.go`
- Test: `internal/switching/start_test.go`, `internal/cli/startcmd_test.go`,
  `internal/autoswitch/state_test.go`, `internal/core/start_test.go`

**Interfaces:**
```go
// internal/switching
type StartOutcome struct {
    Switched bool
    From, To string // slot numbers; To is "" when the login stayed
    Reason   string // "", "no-start-login", "auto-off", "already-start-login",
                    // "none-usable", "switch-failed", "not-default-login"
    Detail   string // the one line for the person, "" when silent
}
// Start decides and switches under the store lock; it never returns an error
// the caller must act on: a failure is a StartOutcome with Reason set.
func Start(s *store.Store, set settings.AutoSwitchSettings, account string, stamp func(to string) error) StartOutcome
// StartTarget is the pure choice of S3 over cached usage.
func StartTarget(candidates []string, usage map[string]usage.UsageEntry, set settings.AutoSwitchSettings, models []string) (slot string, tier string)

// internal/autoswitch
// StampSwitch records a switch another writer made: lastSwitchAt, lastSwitchTo,
// the active memory cleared, under .autoswitch_state.lock.
func StampSwitch(statePath string, clk clock.Clock, to string) error
```

- [ ] Failing tests, each named for the spec goal:
  `TestStartTarget` (tier A before B, C never, unknown usage is B, lowest
  binding utilisation, tie by sequence, disabled / API-key / held /
  quarantined accounts are no candidates);
  `TestStartSwitchesToStartLogin`; `TestStartFallsBackAndStillRuns` with
  cases `no-start-login`, `auto-off`, `already-start-login`, `none-usable`,
  `switch-failed`, `not-default-login` (`CLAUDE_CONFIG_DIR` set, `--bare`,
  an API key in the environment); `TestStartSwitchHoldsTheProactiveCooldown`
  and its 100 % twin; `TestStartLeavesNoClaim`; `TestStartConcurrent`;
  `TestStartMakesNoRequestWithoutASwitch`; `TestStartAccountFlag` (named
  target, works with auto-switch off, unknown account is a usage error
  before anything runs); `TestStartPassesArgvAndEnv`.
- [ ] `StartTarget`, then `Start` (freshen with `RefreshBackupGuarded`,
  switch with `SwitchTo(s, target, true, false)`, stamp), then the command:
  `--allow`, `--disallow`, `--list`, `--account`, `--`, default command
  `claude`. A notice goes to standard error and to the log.
- [ ] Exit codes: the command's own on Windows; on POSIX `start` is replaced.
  A usage error is 2 and runs nothing.
- [ ] Gate, commit `feat(start): start a session on a start login`.

### Task 3: screens and documents for the start login

**Files:**
- Modify: `internal/web/handlers.go` (`POST /api/accounts/{id}/start-login`
  and `…/no-start-login`, the `handleDisable` shape),
  `internal/web/static/app.js` (chip `start`, menu item),
  `internal/cli/traypanel.go` (tag), the `list` renderer
- Modify: `README.md`, `docs/reference.md`, `docs/DESIGN.md` (A90),
  `docs/port-spec/05-autoswitch.md` (reason list), `CHANGELOG.md`
- Test: `internal/web/handlers_test.go`, `internal/cli/traypanel_test.go`

- [ ] Failing tests: `TestStartLoginEndpoints`, `TestTrayPanelStartLogin`,
  the `list` golden with a marked account.
- [ ] Implement. README gets one section, "Start every session on a chosen
  login", with the launcher line for a shell without a provisioner:
  `alias claude='tycswap start --'`.
- [ ] Reference and port spec list `accounts-in-use` and
  `account-unavailable` (closes RECLAIM-6).
- [ ] Gate, commit `feat(start): show and toggle start logins`.

### Task 4: retire `run`, `env`, `map`, `unmap`

Closes RECLAIM-1 to RECLAIM-5. Ships one release after Tasks 1 to 3.

**Files:**
- Delete: `internal/cli/run.go`, `envcmd.go`, `mapcmd.go`, their tests, the
  dispatch cases in `internal/cli/cli.go`, `internal/mappings`,
  `internal/session` except what moved to `internal/launch`
- Modify: `internal/sessprofile` (keep the Keychain-name and credential-path
  helpers `add --login`, the updater, purge and migrate use; delete the
  rest), `internal/store/legacyclaims.go` (`ClaimLegacyProfile` and
  `EnsureSessionAccountAvailable` go), `internal/store/legacyrelease.go`,
  `internal/switching/legacyrelease.go`, `internal/autoswitch/tick.go`,
  `freshen.go`, `ownership.go`, `internal/core/autoswitch_adapters.go`,
  `internal/store/identity.go`, `internal/groups/registry.go` (claim gains
  `releaseError`)
- Test: `internal/core/legacyrotation_test.go` (rewritten as
  `TestLeftoverProfileRelease`), `internal/autoswitch/tick_test.go`

- [ ] Before deleting: `grep` each helper's callers and list them in the
  commit body (D10: "no remaining caller, or the operator's instruction").
- [ ] Failing tests: `TestLeftoverProfileRelease` with cases `idle`, `live`
  (never released, claim kept), `rotated`, `rotated-expired` (refreshed with
  the profile's refresh token, identity from the refresh, login lands in the
  slot, profile cleared), `invalid-grant` (dead login dropped with its
  claim, backup kept), `offline` (claim kept, `releaseError` set, the
  snapshot row carries it, the engine reports the same reason);
  `TestHandoverWithHeldAccountEmitsOneLine`. The repro tests of the review
  (`TestReviewRotatedThenExpiredProfile`,
  `TestReviewDashboardAgreesWhenReleaseCannotVerify`,
  `TestReviewHandoverWithHeldAccountEmitsOneLine`) are the starting point.
- [ ] The release runs on scheduled ticks that are not dry runs, as
  `retireGroups` does, not before every poll; `PrepareRotationCandidates`
  and `PrepareRotationTarget` go.
- [ ] `CHANGELOG.md` **Breaking**: the four commands; README and reference
  lose their sections; DESIGN A91 says what replaced them.
- [ ] Gate, commit `refactor!: retire run, env and map`.

### Task 5: fleet wiring and acceptance (provisioner repository)

- [ ] A `claude` launcher ahead of the real binary on `PATH` that runs
  `tycswap start -- <real claude> "$@"`, and the real binary directly when
  `tycswap` is missing or exits before `exec`. A provisioner check asserts
  the order on `PATH` and the fallback.
- [ ] Mark the start logins on each box (`tycswap start --allow`).
  **Operator-only:** which accounts.
- [ ] Acceptance A1 and A2 of the spec, by an agent; the results go into the
  pull request. **Needs the operator's go** if the login must be moved onto
  a denying organization's login for A1.
- [ ] After A1 passes on both boxes: Task 4's release.
