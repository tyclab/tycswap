# Engine cross-tick state, skip visibility, credential-led identity: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `tycswap auto --once` a true engine tick, make every skipped active-account usage fetch say why on every surface, and make the live slot follow the credential Claude Code will send.

**Architecture:** The engine's per-active-account counters move from `Engine` fields into the existing `autoswitch_state.json` under one `active` object, read at tick start and written through `mutateState`. The usage cache row gains `skipReason`/`skipAt`, written by `usage.Store.Record` for sentinel records that today are dropped, and read back into `UsageEntry.SkipReason` for JSON, tray, dashboard and engine events. A single `store.ResolveLiveSlot` replaces the config-only lookup in the five places that decide which slot is live.

**Tech Stack:** Go 1.26 (per go.mod), standard library, existing fakes (`internal/autoswitch/fakes_test.go`, `internal/testutil`), vanilla JS dashboard (`internal/web/static/app.js`).

**Spec:** `docs/superpowers/specs/2026-10-25-engine-cross-tick-state-design.md`

## Global Constraints

- Validate with `make fmt`, `go test ./...`, `go vet ./...`, `GOOS=windows go vet ./...`, `GOOS=darwin CGO_ENABLED=0 go vet ./...` (AGENTS.md).
- Code-file comments at or below 20 percent of nonblank lines; one-line plain "why" comments.
- `autoswitch_state.json` stays `schemaVersion: 1`; the new `active` key is optional and a malformed value reads as absent.
- JSON output is additive: no existing `usageStatus` value or field changes meaning; new fields appear only when set.
- Tests use temporary stores, fake clocks and fake providers; never the operator's logins.
- `testdata/**` golden files are byte-exact; do not reformat them.
- Each part ends with a `docs/DESIGN.md` A-section (next free number after the last one present; check with `grep -n '^## A' docs/DESIGN.md | tail -1`) and a `CHANGELOG.md` Unreleased line.
- Commit after each task with a conventional `feat:`/`fix:`/`test:` message, no attribution lines.

## Review Focus

1. Two engines ticking concurrently over the same state file (app loop engine plus a stray `--once`): the `active` object must not lose a count or double-count. Test in Task 1 step 6 (sequential engines share the file; the lock serialises writes).
2. A dry-run tick must never write the `active` object even when it would increment. Test in Task 1.
3. A sentinel record for an account that has never been fetched (no row yet) must still create a row carrying `skipReason` without inventing `fetchedAt`, `lastGood` or failures. Test in Task 2.
4. A slot whose stored backup is unreadable or empty must not match by lineage; the resolver must fall back to the config slot. Test in Task 5.
5. `switch` to the lineage-resolved slot must rewrite only the config file, never the credential file, and must say so in JSON and human output. Test in Task 6.

---

### Task 1: Persist the active account's decision memory

**Files:**
- Modify: `internal/autoswitch/state.go` (add the `active` object helpers)
- Modify: `internal/autoswitch/tick.go:118-170` (load at tick start, persist on unknown, clear on known and on switch inside the existing lock mutation at ~`tick.go:520`)
- Modify: `internal/autoswitch/engine.go:58-63` (fields stay as the tick's working copy; add a one-line comment that they are loaded from state each tick)
- Test: `internal/autoswitch/crosstick_test.go` (new)
- Docs: `docs/DESIGN.md` new A-section "Decision memory survives a process"; `CHANGELOG.md` Unreleased.

**Interfaces:**
- Produces in `state.go`:
  - `type activeMemory struct { Number string; UnhealthyTicks int; IdleHoldSince *float64 }`
  - `func activeMemoryOf(state map[string]any, number string) activeMemory` — zero value unless `state["active"]` is an object whose `number` equals `number`; non-numeric or negative counts read as zero.
  - `func (e *Engine) saveActiveMemory(m activeMemory) error` — `mutateState` writing `state["active"] = {"number","unhealthyTicks","idleHoldSince"}` (`idleHoldSince` null when nil). No-op when `e.dryRun`.
  - `func clearActiveMemory(state map[string]any)` — `delete(state, "active")`; used both from a `mutateState` call on the known-headroom path and inside the switch-path lock mutation.
- Consumes: existing `mutateState`, `readState`, `NoSwitchEvent`, `IdleHoldMaxS`, `settings.UnhealthyTicks`.

- [ ] **Step 1: Write the failing tests** in `internal/autoswitch/crosstick_test.go`, using `build(...)` from `engine_test.go` with one shared `statePath` via `WithStatePath`, `newFake()` with the active account's entry `nilEntry()` (usage unknown) and `settings.Default()` (`UnhealthyTicks` = 3):

```go
func TestOnceTicksAccumulateFailoverAcrossProcesses(t *testing.T)
// three separate engines over one state file; recorder details are
// "1/3 before failover", "2/3 before failover", then the third tick emits a
// "switch" event with Trigger "failover".
func TestKnownHeadroomClearsPersistedCount(t *testing.T)
// two unknown ticks, one tick with dictEntry(usageOf(10, 10)), then one unknown
// tick reports "1/3 before failover" and the state file has no "active" key
// after the known tick.
func TestActiveChangeResetsPersistedCount(t *testing.T)
// two unknown ticks on current "1"; f.current = strp("2") (also unknown); the
// next tick reports "1/3 before failover".
func TestIdleHoldSpansProcesses(t *testing.T)
// active entry sentinelEntry(jsonout.UsageTokenExpired) with
// f.liveSessions["1"] = []int{1}; engine A ticks at T0 → "active-idle";
// engine B at T0+IdleHoldMaxS+1 → "active-usage-unknown" "1/3 before failover".
func TestMalformedActiveMemoryReadsAsAbsent(t *testing.T)
// write {"schemaVersion":1,"active":"junk"} then tick → "1/3 before failover".
func TestDryRunReadsButNeverWritesActiveMemory(t *testing.T)
// real engine ticks twice (count 2); a dry-run engine over the same file
// ticks once: it reaches the failover decision (perform's dry-run path emits
// the would-switch event), and the file still says unhealthyTicks 2 afterwards.
```

- [ ] **Step 2: Run** `go test ./internal/autoswitch/ -run 'Once|Persisted|IdleHoldSpans|Malformed|DryRunReads' -v` — expected: FAIL (undefined helpers / wrong details).

- [ ] **Step 3: Implement** the helpers in `state.go` and wire `tick.go`: after `cur` is known, `m := activeMemoryOf(state, cur)`; set `e.unhealthyTicks, e.idleHoldSince = m.UnhealthyTicks, m.IdleHoldSince`. On the known-headroom branch, when the state had an `active` object, call `mutateState` with `clearActiveMemory` (skip in dry-run). On the unknown branch, after incrementing or starting the hold, call `saveActiveMemory` before `emit`. In the switch lock mutation, `clearActiveMemory(state)` before `writeState`. Loop mode changes nothing else: the fields are reloaded every tick.

- [ ] **Step 4: Run** the same tests — expected: PASS. Then `go test ./internal/autoswitch/ ./internal/cli/` — expected: PASS (existing failover tests in `engine_test.go` still pass because a single engine over one file behaves as before).

- [ ] **Step 5: Docs.** Add the DESIGN A-section (what is persisted, when cleared, dry-run rule, why: timer deployments) and the CHANGELOG line: "**Fixed:** `auto --once` counts failover ticks and the idle hold across runs, so a timer-driven engine fails over instead of reporting 1/3 forever."

- [ ] **Step 6: Concurrency test** `TestConcurrentEnginesShareOneCount` in the same file: two engines, `sync.WaitGroup`, each ticks once in a goroutine with unknown usage; afterwards `activeMemoryOf(readStateFile(statePath), "1").UnhealthyTicks == 2`. Run with `go test -race ./internal/autoswitch/ -run Concurrent`.

- [ ] **Step 7: Commit** `fix(autoswitch): persist failover count and idle hold across ticks`.

---

### Task 2: Record why an active fetch was skipped

**Files:**
- Modify: `internal/usage/entry.go` (`FetchRecord.SkipReason string`, `UsageEntry.SkipReason string`, `UsageEntry.SkipAt *float64`)
- Modify: `internal/usage/store.go:241-283` (`Record` persists sentinel records' reason), `:122-170` (`Entries` reads it back)
- Modify: `internal/reporting/fetch.go:84-190` (each early return sets `SkipReason`), `internal/reporting/collect.go:170-215` (static sentinels carry a reason) and `:100-110` (log on change)
- Test: `internal/usage/store_test.go` (add), `internal/reporting/fetch_test.go` or the existing reporting test file for `fetchActiveUsage` (add)

**Interfaces:**
- Produces: `FetchRecord{Sentinel, SkipReason}`; row keys `skipReason` (string), `skipAt` (float seconds); `UsageEntry.SkipReason`, `UsageEntry.SkipAt`.
- Reason texts, exact (spec Part 2 table):
  - `owner holds an expired token; Claude Code refreshes it on next use`
  - `live credential does not match slot N's backup; refresh skipped` (N = slot number)
  - `refresh discarded: an owner appeared mid-refresh`
  - `refresh discarded: live credential changed mid-refresh`
  - `refresh discarded: <error text>`
  - `no credential in the live store`
- Consumes: `store.Store.Log` (`*logging.Logger`, may be nil).

- [ ] **Step 1: Failing store tests** in `internal/usage/store_test.go`:

```go
func TestRecordPersistsSkipReasonForSentinel(t *testing.T)
// Record({"5": {Sentinel: "token expired", SkipReason: "owner holds ..."}}):
// row "5" exists with skipReason and skipAt == now, and has no fetchedAt,
// lastGood, consecutiveFailures or lastError.
func TestRecordSuccessClearsSkipReason(t *testing.T)
// after the above, Record({"5": {Usage: {...}}}) removes skipReason/skipAt.
func TestEntriesExposeSkipReason(t *testing.T)
```

- [ ] **Step 2: Run** `go test ./internal/usage/ -run SkipReason -v` — expected: FAIL.

- [ ] **Step 3: Implement** in `store.go`: `Record` splits outcomes into fetched (as today) and skipped (`Sentinel != "" || SkipReason != ""`); skipped rows get `skipReason`/`skipAt` set under the same `mutate`, nothing else touched; fetched rows delete both keys. `Entries` copies the keys.

- [ ] **Step 4: Run** the store tests — expected: PASS.

- [ ] **Step 5: Failing reporting tests**: for `fetchActiveUsage` with a temp store and `fakeOAuth`-style client, one test per path in the Interfaces list asserting `rec.SkipReason` equals the exact text; plus `TestStaticSentinelNoCredentialsCarriesReason`. Follow the existing test construction in the reporting package (search for `fetchActiveUsage(` in `*_test.go`).

- [ ] **Step 6: Run** `go test ./internal/reporting/ -run SkipReason -v` — expected: FAIL.

- [ ] **Step 7: Implement** in `fetch.go`/`collect.go`: set `SkipReason` on every `FetchRecord{Sentinel: ...}` return and in the `persistSkipped` branch (keep the distinct texts for owner-appeared, token-changed and write-error by recording which branch marked skipped). Static sentinels: `staticUsageSentinel` returns `(sentinel, reason string)`; callers pass both into the record. In `collect.go` after `st.Record(records, identities)`: for each num whose new `SkipReason` differs from `entries[num].SkipReason` (before the record), `s.Log.Warningf("Usage for account %s not fetched: %s", num, reason)` when set, and `Infof("Usage for account %s fetches again", num)` when it cleared.

- [ ] **Step 8: Run** `go test ./internal/reporting/ ./internal/usage/` — expected: PASS. `go vet ./...` clean.

- [ ] **Step 9: Commit** `feat(usage): record why an active fetch was skipped`.

---

### Task 3: Surface the skip reason in JSON and engine events

**Files:**
- Modify: `internal/web/state.go:287-302` (`usageRefresh.skipReason`, `usageRefresh.skipAt` RFC3339)
- Modify: `internal/jsonout/jsonout.go:~230-250` (the `list --json` row builder: add `SkipReason`/`SkipAt` to its opts struct and emit `usageRefresh.skipReason` the same way `state.go` does; if `list --json` has no `usageRefresh` today, add the object with only the keys that are set)
- Modify: `internal/autoswitch/tick.go` (`active-usage-unknown` and `active-idle` details append ` · <reason>`), `internal/autoswitch/collect.go` or the switcher facade so the engine sees `UsageEntry.SkipReason` (the fake's `UsageEntriesByAccount` already returns `UsageEntry`).
- Modify: `docs/reference.md` (document `usageRefresh.skipReason` next to `usageRefresh.error` where the JSON row is described)
- Test: `internal/web/state_test.go`, `internal/jsonout/*_test.go`, `internal/autoswitch/crosstick_test.go` (add `TestUnknownDetailCarriesSkipReason`)

**Interfaces:**
- Consumes: `UsageEntry.SkipReason`, `UsageEntry.SkipAt` from Task 2.
- Produces: JSON `usageRefresh: {"skipReason": "...", "skipAt": "2026-10-25T17:00:19Z"}` (keys only when set); event detail format `"1/3 before failover · owner holds an expired token; Claude Code refreshes it on next use"`.

- [ ] **Step 1: Failing tests**: `TestStateRowCarriesSkipReason` (state.go: an account snapshot whose `Usage.SkipReason` is set yields `usageRefresh.skipReason`; unset yields no key); the `jsonout` equivalent for `list --json`; the engine test asserting the detail string.
- [ ] **Step 2: Run** the three test packages — expected: FAIL.
- [ ] **Step 3: Implement** the three surfaces.
- [ ] **Step 4: Run** `go test ./internal/web/ ./internal/jsonout/ ./internal/autoswitch/ ./internal/cli/` — expected: PASS (golden fixtures under `testdata/` must be untouched; if a golden `list --json` fixture has no skip reason it must still match because the keys are omitted when unset).
- [ ] **Step 5: Commit** `feat: expose usage skip reason in JSON and engine events`.

---

### Task 4: Show the skip reason in the tray and the dashboard, close Part 2

**Files:**
- Modify: `internal/cli/traypanel.go:70-72` (details line becomes `Usage: <status> · <skipReason>` when `row["usageRefresh"].skipReason` is set)
- Modify: `internal/web/static/app.js:1136-1152` (`usageRefreshNote`: when `refresh.skipReason` is set, push it into `parts` in place of the generic `Waiting for Claude to refresh its login` text; keep the tooltip)
- Test: `internal/cli/traypanel_test.go` (add), the existing dashboard JS test harness if one exists (`grep -rn "usageRefreshNote" internal/web/*_test.go`), otherwise `internal/web/onboarding_test.go`-style string presence test on `app.js`
- Docs: `docs/DESIGN.md` A-section "A skipped usage fetch says why"; `CHANGELOG.md` line "**Fixed:** when the active account's usage is not fetched, the tray, dashboard, `list --json` and the engine's events say why, and the log records each change of reason."

**Interfaces:**
- Consumes: `usageRefresh.skipReason` from Task 3's state document.

- [ ] **Step 1: Failing tray test**: a `web.State` with one account row carrying `usageStatus: "token_expired"` and `usageRefresh.skipReason` → `panel(st).Rows[0].Details` contains `"Usage: token_expired · owner holds an expired token; Claude Code refreshes it on next use"`.
- [ ] **Step 2: Run** `go test ./internal/cli/ -run TrayPanel -v` — expected: FAIL.
- [ ] **Step 3: Implement** tray and dashboard changes.
- [ ] **Step 4: Run** `go test ./internal/cli/ ./internal/web/` — expected: PASS.
- [ ] **Step 5: Docs** as listed.
- [ ] **Step 6: Commit** `feat(tray,web): show why usage was not fetched`.

---

### Task 5: Resolve the live slot by credential lineage

**Files:**
- Modify: `internal/store/identity.go:286-299` (`CurrentAccountNumber` delegates to the new resolver)
- Create: `internal/store/liveslot.go`
- Modify: `internal/reporting/info.go:23-24`, `internal/reporting/status.go` (two call sites), `internal/switching/switch.go` (the `currentNum` derivation), `internal/switching/switchto.go` (the `curSlot` derivation) — all use `ResolveLiveSlot`.
- Test: `internal/store/liveslot_test.go` (new), `internal/reporting/info_test.go` (add)

**Interfaces:**
- Produces in `liveslot.go`:
  - `type LiveSlot struct { Slot string; ConfigSlot string; ByLineage bool }`
  - `func (s *Store) ResolveLiveSlot(data *SequenceData) LiveSlot` — `ConfigSlot` from `GetCurrentAccount` + `FindAccountSlot` as today. When `ConfigSlot != ""`, read the live credential (`s.Creds.ReadActive()`), and for each managed slot whose `AccountKindFor` is `"oauth"` compare `oauth.CredentialFingerprint(live)` with the fingerprint of `ReadAccountCredentials(num, email)` or `oauth.SameAccountBlock(live, backup)`. If exactly one slot matches and it is not `ConfigSlot`, `Slot = that`, `ByLineage = true`; otherwise `Slot = ConfigSlot`. An unreadable/empty backup never matches. When `ConfigSlot == ""`, `Slot == ""` regardless of lineage.
  - Warning once per process per `(ConfigSlot, Slot)` pair via `s.Log.Warningf("live config names slot %s (%s, %s) but the live credential is slot %s's; using %s. A running Claude Code rewrote ~/.claude.json")` — remember the last warned pair in a `sync.Map`/mutex-guarded field on `Store`.
- `CurrentAccountNumber()` returns `&ls.Slot` or nil.

- [ ] **Step 1: Failing tests** in `liveslot_test.go` with `testutil.IsolateHome` or the store package's own temp-store helper (find with `grep -n "func newStore\|func tempStore" internal/store/*_test.go`): write a sequence with slots 5 and 6 (same email, different orgs), stored backups with distinct refresh tokens, a live credential equal to slot 5's backup, and a global config naming slot 6's org:

```go
func TestResolveLiveSlotPrefersCredentialLineage(t *testing.T) // Slot "5", ConfigSlot "6", ByLineage true, one warning logged
func TestResolveLiveSlotAgreesWithoutWarning(t *testing.T)
func TestResolveLiveSlotUnmanagedConfigStaysNil(t *testing.T)
func TestResolveLiveSlotSkipsAPIKeySlots(t *testing.T)
func TestResolveLiveSlotUnreadableBackupFallsBack(t *testing.T)
func TestResolveLiveSlotWarnsOncePerPair(t *testing.T)
```

- [ ] **Step 2: Run** `go test ./internal/store/ -run ResolveLiveSlot -v` — expected: FAIL.
- [ ] **Step 3: Implement** `liveslot.go` and the delegation.
- [ ] **Step 4: Run** store tests — PASS. Then route the four other call sites through the resolver and run `go test ./internal/reporting/ ./internal/switching/ ./internal/core/ ./internal/cli/` — expected: PASS (existing tests construct config and credentials consistently, so lineage agrees with config).
- [ ] **Step 5: Add** `TestBuildAccountsInfoActiveFollowsLineage` in the reporting tests: with the Task 5 fixture, the info row for slot 5 has `IsActive` true and slot 6 false.
- [ ] **Step 6: Commit** `fix(store): resolve the live slot by credential lineage when the config disagrees`.

---

### Task 6: `switch` repairs the config under a lineage mismatch, close Part 3

**Files:**
- Modify: `internal/switching/switch.go:263-280`, `internal/switching/switchto.go:85-104` (when `ResolveLiveSlot` gives `ByLineage && Slot == target`, skip the already-active short-circuit and run the existing config-only write path that `performSwitch` uses for "config only; credentials unchanged"; result reason `config-repaired`, human line `Repaired the live config for Account-N (<email>)`)
- Modify: `internal/switching/result.go` (the noop/result reasons list gains `config-repaired`)
- Test: `internal/switching/*_test.go` (add `TestSwitchRepairsConfigUnderLineageMismatch` for both the strategy path and `switchto`)
- Docs: `docs/DESIGN.md` A-section "The live slot follows the credential"; `CHANGELOG.md` line "**Fixed:** two slots of one login in different organisations: the live slot is the one whose credential Claude Code holds, even after a running Claude Code rewrote `~/.claude.json`; `switch` to that slot repairs the config."; `docs/reference.md` if switch JSON reasons are listed there.

**Interfaces:**
- Consumes: `store.LiveSlot` from Task 5; existing `performSwitch(s, nextAccount, verbose, force, prov)` and the "config only" backup step already logged as `Backed up account N (config only; credentials unchanged)`.
- Produces: switch JSON `{"switched": true, "reason": "config-repaired", ...}` with `from` and `to` both the target ref; the credential file's bytes unchanged (assert by hashing before/after in the test).

- [ ] **Step 1: Failing tests** as named; the test fixture is Task 5's.
- [ ] **Step 2: Run** `go test ./internal/switching/ -run LineageMismatch -v` — expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./...`, `go vet ./...`, `GOOS=windows go vet ./...`, `GOOS=darwin CGO_ENABLED=0 go vet ./...`, `make fmt` — all clean.
- [ ] **Step 5: Docs** as listed.
- [ ] **Step 6: Commit** `fix(switch): repair the live config when the credential belongs to the target slot`.
