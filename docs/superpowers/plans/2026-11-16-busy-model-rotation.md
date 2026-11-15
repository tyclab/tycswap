# Busy-model rotation and the end of run/env — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The default login counts a model only for sessions that work and only while some account can serve it, Opus fills accounts whose Fable week is spent, a Fable prompt moves the login before its request, `run`/`env`/`map`/`unmap` are gone with their leftovers retiring, and every manual change names its actor.

**Architecture:** The default-session policy (`internal/reporting/defaultmodels.go`) reads each live session's Claude Code record status and the cached Fable room; the engine drops a derived model no eligible account can serve, per tick; a new `auto --prompt` entry runs one cached-usage tick from a synchronous `UserPromptSubmit` hook that flakelab installs. The legacy profile commands and their session manager go; the existing release path retires what is left.

**Tech Stack:** Go (go.mod's version and toolchain), flakelab (Nix home-manager module `nix/home/claude.nix`).

**Spec:** `docs/superpowers/specs/2026-11-16-busy-model-rotation-design.md`

## Global Constraints

- Follow AGENTS.md: temporary stores and fake providers in tests, never the operator's logins; preserve file locks, atomic writes, credential ownership, JSON/exit-code and migration contracts; `testdata/**` byte-exact; comments ≤ 20 % of nonblank lines.
- Validate with `make fmt`, `go test ./...`, `go vet ./...`, `GOOS=windows go vet ./...`, `GOOS=darwin CGO_ENABLED=0 go vet ./...`. These run on tycdev (`ssh tycdev`), never on the WSL boxes: push the branch, then `git -C ~/git/tyclab/tycswap fetch && git -C ~/git/tyclab/tycswap worktree add ~/git/worktrees/tycswap-busy <branch>` there (or `git pull` in that worktree).
- B1–B3 change only *derived* models (`autoswitch.model` unset). An explicit `autoswitch.model` or `--model` keeps today's semantics exactly.
- "Working" = a live session whose record `status` is anything but `"idle"` (`busy`, `shell`, `waiting`, missing, unknown), or the session the prompt hook names.
- No commit carries a Co-authored-by or model trailer. English everywhere. CHANGELOG keeps `## Unreleased` as its only heading; this release is breaking (commands removed).
- Every operator ruling in the spec's coverage table ends in a test named in a task below; nothing is gated on an acceptance the operator did not set.

## Review Focus

1. A Fable session waiting on background work (`status: "shell"`) or a dialog (`waiting`) counts as working, or its wake-up request lands on a Fable-spent login and stops for days. Test in Task 1.
2. A record without `status` (older Claude Code) or a live session without a model record behaves as today (counted / clears the reservation). Test in Task 1.
3. The prompt hook never blocks or pollutes a prompt: malformed stdin, a busy lock, no network, auto-switch off → exit 0, nothing on stdout without `--json`, returns within its budget. Test in Task 3.
4. No ping-pong: a Fable session flipping busy/idle, or Fable becoming servable/unservable around a 5h reset, causes at most one `reserved` move per cooldown; a model-driven at-limit move happens only while the model is servable. Test in Task 2.
5. Disabled accounts, quarantined accounts and accounts held by a live legacy claim are not in the servable pool; an explicit `autoswitch.model` never drops a model. Tests in Task 2.

---

### Task 1: busy-aware default-session policy (B1, B3)

**Files:**
- Modify: `internal/reporting/defaultmodels.go` (`DefaultSessionPolicy`)
- Modify: `internal/cli/auto.go:173-177`, `internal/cli/startcmd.go:241` (callers)
- Test: `internal/reporting/defaultmodels_test.go`

**Interfaces:**
- Produces: `type PolicyInput struct { WorkingSession string }` and
  `func DefaultSessionPolicy(s *store.Store, base settings.AutoSwitchSettings, in PolicyInput) (Policy)` with
  `type Policy struct { Settings settings.AutoSwitchSettings; Reserved func(num string) bool; Derived bool }`.
  `Derived` is true when `Settings.Model` came from session records, false when `base.Model` was set or nothing was counted.
- Consumes: `procdetect.ClaudeSession.Status *string`, `store.FableCapable`, `UsageEntriesByAccount`.

- [ ] **Step 1: failing tests** in `defaultmodels_test.go`, table-driven over fake session records and cached usage:
  - `TestPolicyIgnoresIdleSessions`: 8 Fable records `status:"idle"`, 14 Opus `status:"busy"` → `Settings.Model == nil`, `Reserved != nil`, `Derived == false`.
  - `TestPolicyCountsShellAndWaiting`: one Fable `status:"shell"` → `*Settings.Model == "Fable"`, `Reserved == nil`, `Derived`; same for `"waiting"`, `"busy"`, and a record with no `status`.
  - `TestPolicyWorkingSessionOverridesIdle`: Fable record idle, `PolicyInput{WorkingSession: thatID}` → Fable counted.
  - `TestPolicyUnknownWorkingSessionClearsReservation`: an Opus busy record plus a busy session without a model record → `Reserved == nil` (as today); the same session idle → reservation on.
  - `TestReservationNeedsFableRoom`: no working Fable; accounts 4, 7 capable with Fable 100 %, 11 capable with Fable 78 % → `Reserved("11")`, `!Reserved("4")`, `!Reserved("7")`.
  - `TestExplicitModelWins`: `base.Model = "Fable"` with all sessions idle → returned unchanged, `Derived == false`, `Reserved == nil`.
  - Update `TestDefaultSessionPolicyReservesFableForFableSessions` and `TestDefaultSessionSettingsFollowLiveSessions` to give their records `status:"busy"`.
- [ ] **Step 2:** run `go test ./internal/reporting/ -run 'Policy|Reservation|ExplicitModel|DefaultSession'` on tycdev → FAIL (signature).
- [ ] **Step 3:** implement. Working = `Status == nil || *Status != "idle" || SessionID == in.WorkingSession`; only working sessions contribute families and the all-known check. Reserved = `s.FableCapable(n) && fableRoom(n)` where `fableRoom` is "the account's cached usage reports a scoped window whose name contains `fable` with pct < 100". Callers take `.Settings` / `.Reserved`; `auto.go` passes `.Derived` on as `autoswitch.WithDerivedModels(true)` (Task 2).
- [ ] **Step 4:** tests pass; whole package passes.
- [ ] **Step 5:** commit `feat(auto): count the models of working sessions only; reserve accounts with Fable room`.

### Task 2: a derived model nobody can serve does not block (B2)

**Files:**
- Create: `internal/autoswitch/servable.go`
- Modify: `internal/autoswitch/engine.go` (option), `internal/autoswitch/tick.go` (per-tick models), `internal/autoswitch/collect.go` if headroom is computed there
- Test: `internal/autoswitch/servable_test.go`, `internal/autoswitch/engine_test.go`

**Interfaces:**
- Consumes: `Policy.Derived` (Task 1).
- Produces: `func WithDerivedModels(derived bool) Option`;
  `func servableModels(usage map[string]map[string]any, pool []string, models []string) []string` — keeps a model when some account in `pool` reports that window with pct < 100 and has Session and Week room > 0 (an absent 5h window is room).
  The tick computes `models := e.models` and, when derived, `models = servableModels(usage, pool, e.models)` once, with `pool` = rotation-eligible, non-quarantined, enabled candidates plus the current account; every later use in that tick (trigger, model-missing, candidates, ranking, `trulyExhausted`, ownership report, wake time) uses `models`, never `e.models`.

- [ ] **Step 1: failing tests:**
  - `TestServableModels` (unit): tonight's usage (3: 5h 100/7d 27/Fable 28; 11: 5h 100/7d 80/Fable 78; 4: 5/78/100; 7: 4/88/100; 8: –/93/100) → `[]`; 11 with 5h 0 → `["Fable"]`; 11 disabled (not in pool) → `[]`.
  - `TestDerivedUnservableFableDoesNotExhaust` (engine): derived `Fable`, active 3 at 5h 100 → outcome switched to 4 or 7 (soonest-reset order), not `all-exhausted`.
  - `TestDerivedServableFableStillMoves`: derived `Fable`, active 7 (Fable 100), 11 has 5h 0 → at-limit move to 11.
  - `TestExplicitModelKeepsExhaustion`: same usage as the first engine test with `s.Model = "Fable"` and not derived → `all-exhausted` (rename/keep `TestScopedOnlyExhaustionDrivesWakeTime` as this case).
  - `TestNoReservedPingPong`: reserved move, then the next tick within the cooldown with the reservation cleared and re-set → no second move.
  - `TestHeldAccountNotServable`: the only Fable-room account held by a live legacy claim → model dropped.
- [ ] **Step 2:** run `go test ./internal/autoswitch/` → the new tests FAIL.
- [ ] **Step 3:** implement as in Interfaces; keep `e.models` for explicit settings and for poll cadence (`engine.go:141-147`).
- [ ] **Step 4:** whole package green; the tests listed in the research (`model_missing_test.go`, `reserved_test.go`, `classes_test.go`) unchanged or adjusted only where they built derived models.
- [ ] **Step 5:** commit `fix(auto): a derived model no eligible account can serve stops blocking the login`.

### Task 3: the prompt hook moves the login before a Fable request (B4)

**Files:**
- Create: `internal/cli/prompt.go`
- Modify: `internal/cli/auto.go` (flag `--prompt`, help text at `:395-410`), `docs/reference.md` (`tycswap auto`)
- Test: `internal/cli/prompt_test.go`

**Interfaces:**
- Consumes: `reporting.DefaultSessionPolicy(..., PolicyInput{WorkingSession})`, the observe path's cached-usage tick (`observeTicks`'s tick on news).
- Produces: `tycswap auto --prompt [--json]`: reads Claude Code's `UserPromptSubmit` hook JSON on stdin (`session_id`), looks up the session's family in `default_models.json`; when the family maps to no scoped window the engine counts (e.g. opus), exits 0 at once without a tick; otherwise runs one tick on cached usage with that session working. Honours `autoswitch.enabled`. Always exit 0; stdout only with `--json` (one line per event).

- [ ] **Step 1: failing tests:**
  - `TestPromptSwitchesForIdleFableSession`: Fable session idle, live account Fable 100 %, account with Fable room → switch event, trigger `model-missing` or `at-limit`.
  - `TestPromptIgnoresOpusSession`: Opus session → no tick (fake switcher records zero calls).
  - `TestPromptNeverFails`: malformed stdin, unknown session, auto-switch off, lock busy → exit 0, empty stdout without `--json`.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** implement; reuse observe's tick construction (derived models, reservation, `WithDerivedModels`).
- [ ] **Step 4:** pass.
- [ ] **Step 5:** commit `feat(auto): --prompt moves the login for a working Fable session before its request`.

### Task 4: remove run, env, map and unmap (B5, part 1)

**Files:**
- Delete: `internal/cli/run.go`, `internal/cli/envcmd.go`, `internal/cli/mapcmd.go` and their tests; `internal/mappings/` (drop `PruneMappings` call at `lifecycle/lifecycle.go:325` and `core/session_adapters.go:41`); session-manager files in `internal/session/` used only by run/env (`manager.go`, `bootstrap.go`, `mcp.go`, `sharing.go`, `history.go`, `runner.go`, `env.go`, `exec_*.go`) after moving the helpers that must stay.
- Keep (move if their file goes): `session.AuthOverrideEnvVars`, `ScrubAuthOverrides`, `CLICommand`, `CheckCmdShimArgs`; `sessprofile.KeychainServiceName`, `CredentialsFileName`, `ReadSessionIdentity`; the ownership scan; `groups` claims registry; legacy release code (Task 5); `LiveSessionPidsFor`.
- Modify: `internal/cli/help.go:47-52,105-106`, `internal/cli/cli.go:145-158` (pinned-shell guard: replace with one stderr notice that profiles are retired when `CLAUDE_CONFIG_DIR` points into the sessions dir), dispatch/parse entries, `docs/reference.md` sections run/env/map/unmap, README (27 mentions), DESIGN new section `A91. run, env and map retire` (cause: the 2026-11-10 ruling; what stays until no profile remains), CHANGELOG **Breaking/Removed**, `docs/superpowers/plans/2026-11-10-review-findings.md` RECLAIM-1/2 rows closed, start-login plan's S5 section replaced by a pointer to this plan.
- Test: `internal/cli/removed_test.go`

- [ ] **Step 1: failing test** `TestLegacyProfileCommandsAreGone`: `run`, `env`, `map`, `unmap` → exit 64 with the unknown-command message; `tycswap help` output contains none of them.
- [ ] **Step 2:** FAIL. **Step 3:** delete and move as listed; `git grep -n -E 'sessprofile|mappings\.|session\.New'` shows only kept callers. **Step 4:** `go test ./...`, the three `go vet` lines, `make fmt` green. **Step 5:** commit `feat!: remove run, env, map and unmap (operator's ruling 2026-11-10)`.

### Task 5: leftover profiles retire on their own (B5, part 2)

**Files:**
- Modify: `internal/store/legacyrelease.go`, `internal/switching/legacyrelease.go`, `internal/autoswitch/tick.go:95-97,225`, `internal/autoswitch/ownership.go`, `internal/store/identity.go:443-480`
- Test: `internal/store/legacyrelease_test.go`, `internal/switching/legacyrelease_test.go`, `internal/autoswitch/ownership_test.go`

**Interfaces:**
- Consumes: existing `LegacyReleaseCandidate`, `ReleaseIdleLegacy`, `PrepareRotationCandidates`.
- Produces: real ticks release every idle legacy profile, including one whose account is the live default login; observe, prompt and dry-run ticks never release.

- [ ] **Step 1: failing tests:**
  - `TestStaleClaimOnLiveAccountIsReleased`: claim `legacy:7`, no live pid, account 7 live default, profile credential stale-marked or same/older lineage → claim dropped on a real tick; `CredentialOwner("7")` no longer conflicts; dry-run and observe ticks leave it.
  - `TestRotatedExpiredProfileVerifiedByRefresh` (RECLAIM-3): idle profile whose access token expired after rotation → refresh with the profile's own token (fake provider), same identity → newer credential taken into the slot, claim dropped; `invalid_grant` → slot backup kept, claim dropped.
  - `TestLiveProfileKeepsClaim`: a live pid → nothing released.
  - `TestSnapshotMatchesEngineOnUnverifiableRelease` (RECLAIM-4) and `TestNoDoubleEventsWithHeldAccount` (RECLAIM-5) as the findings rows describe.
- [ ] **Steps 2–4:** FAIL, implement, pass. **Step 5:** commit `fix(auto): retire idle legacy profiles, the live account's too; verify expired ones by refresh`; close RECLAIM-3/4/5 rows.

### Task 6: actors, external login changes, one policy for every reader (B6)

**Files:**
- Modify: `internal/lifecycle/disable.go:19-81` (actor parameter), `internal/switching/perform.go` (actor in the "Switched from" line), settings writes in `internal/cli/config*.go` and `internal/web/handlers.go:367-375,569-586`, `internal/tui/app.go:528-540`, `internal/cli/menu.go:255-257`; `internal/autoswitch/tick.go` (event `live-login-changed` when the live credential's slot differs from the last recorded active without a tycswap switch); `internal/reporting/atlimit.go:15-17`, `internal/cli/appshell.go:1155-1170`, dashboard state (`internal/web/state.go`) read the counted models from `DefaultSessionPolicy`.
- Test: `internal/lifecycle/disable_test.go`, `internal/web/handlers_test.go`, `internal/autoswitch/engine_test.go`, `internal/reporting/atlimit_test.go`

**Interfaces:**
- Produces: `type Actor string` in `internal/lifecycle` (values `cli(pid=N,parent=<comm>)`, `tui`, `dashboard(<cookie|bearer>,<remote addr>)`, `engine:<trigger>`, `start`); log lines end `by <actor>`.

- [ ] **Step 1: failing tests:** `TestDisableLogsActor` (CLI and HTTP handler), `TestSwitchLogsActor`, `TestSettingsWriteLogsActor`, `TestExternalLoginChangeLoggedOnce`, `TestAtLimitUsesDerivedPolicy` (idle Fable only → 4 and 7 not at limit; busy Fable with no servable Fable → not at limit).
- [ ] **Steps 2–4:** FAIL, implement, pass. **Step 5:** commit `feat: every enable, disable, switch and settings write names its actor`.

### Task 7: flakelab installs the prompt hook and drops run/env

**Files (repo `tyclab/flakelab`, own worktree):**
- Modify: `nix/home/claude.nix` — a `UserPromptSubmit` hook owned by its command, beside the quota hook (`:134-153`): `tycswap auto --prompt --json 2>&1 | systemd-cat -t flakelab-tycswap-prompt || true`, `timeout: 20`, only when `tycswapHook`; prune regex like the Notification hook's. `README.md:596-597`, `remote-sessions.md:92`, `files/scripts/lib/prompt.zsh:6`: drop run/env. Version + source hash + Windows exe hash pin bump to the new tycswap release.
- Test: the module's existing checks (`nix flake check` on tycdev), `files/scripts/test-claude-sessions` still passing; a jq test in the module's test set that the hook is added once and pruned when `tycswapHook` is false.

- [ ] Steps: failing jq/settings test → implement → `nix flake check` green on tycdev → drvPath comparison of `default` and `proxmox-vm` against main → PR → commit `feat(claude): tycswap moves the login before a Fable prompt; run/env retired`.

### Task 8: release, rollout, live acceptance

- [ ] Whole-branch review of tycswap (fresh Opus at max effort, unprimed; builder is Opus: check standing rules without tests, footprint, refusal and idempotence) → fix → PR CI green on Linux, Windows, macOS → merge (merge commit) → `git ls-remote --tags origin`, tag next minor (`v0.13.0` unless taken) per the release runbook → publish notes → flakelab pin PR (Task 7) green → merge.
- [ ] Rollout: `flakelab update` on GamerTyc; `ssh tycdev flakelab update`; verify `tycswap --version`, the app service ExecStart path, `systemctl --user --failed`, the `UserPromptSubmit` hook in `~/.claude/settings.json`. The EliteBook takes it on its next update.
- [ ] Live acceptance on real accounts, each with the command and the output kept:
  - L1 `tycswap auto --once --dry-run --json`: with no busy Fable session, accounts with Fable 100 % show headroom > 0 and the outcome is not `all-exhausted` while any has 5h/7d room.
  - L2 the journal (`flakelab-tycswap-autoswitch`, `-observe`, `-prompt`) shows a real switch onto a Fable-spent account (reserved or at-limit) and Opus lanes keep answering.
  - L3 a throwaway Fable session in a scratch dir, idle while the login sits on a Fable-spent account; one typed prompt → `flakelab-tycswap-prompt` shows the switch before the reply, the reply comes from Fable (transcript `"model"`), no limit message.
  - L4 `tycswap run` exits 64; `groups/claims.json` holds no claim without a live pid; `tycswap list --json` shows account 7's usage fetched.
  - L5 the next manual or engine switch line in `tycswap.log` ends `by <actor>`.
- [ ] Update the memory notes that teach `tycswap run` (`note:lane-on-another-account`, `claude-memory:lanes-in-new-shells`, `claude-memory:fable-sessions-start-on-ultra`, `claude-memory:tycswap-one-engine-program`) and flakelab docs in the same go.
