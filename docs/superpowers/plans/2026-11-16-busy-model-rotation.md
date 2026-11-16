# Servable-model rotation and the end of run/env — Implementation Plan (rev 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans for your stream. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A derived model no usable account can serve stops blocking the shared login, Opus fills the accounts whose Fable week is spent, `run`/`env`/`map`/`unmap` are gone and their leftovers retire (the live account's stale claim too), and every manual change names its actor.

**Architecture:** Servability is decided once, in the default-session policy (`internal/reporting/defaultmodels.go`), from cached usage; the engine, `list`, the dashboard and the tray read that one result and the engine's own `e.models` code paths stay as they are. The legacy profile commands go in two commits (move the kept helpers, then delete); the existing release path retires what is left.

**Tech Stack:** Go (go.mod's version/toolchain); flakelab (Nix) afterwards.

**Spec:** `docs/superpowers/specs/2026-11-16-busy-model-rotation-design.md` (rev 2; read "Why not count only working sessions" first).

## Global Constraints

- AGENTS.md: temporary stores and fake providers in tests, never the operator's logins; preserve file locks, atomic writes, credential ownership, JSON/exit-code and migration contracts; `testdata/**` byte-exact; comments ≤ 20 % of nonblank lines.
- Builds and tests run on tycdev only (`ssh tycdev`; Go 1.26 with go.mod's toolchain): push your branch, then on tycdev `git -C ~/git/tyclab/tycswap fetch -q && git -C ~/git/tyclab/tycswap worktree add ~/git/worktrees/<your-branch> origin/<your-branch>` (later `git -C … pull`). Run package tests while working (`go test -p 4 ./internal/<pkg>/...`) and the full gate once per task: `make fmt && go test -p 4 ./... && go vet ./... && GOOS=windows go vet ./... && GOOS=darwin CGO_ENABLED=0 go vet ./...`. Never build or test on the WSL box. Remove your tycdev worktree when done.
- Only derived models (`autoswitch.model` unset) change. An explicit `autoswitch.model` / `--model` keeps today's semantics exactly.
- The `Switched from account X to Y` line (DESIGN A4, `logging_test.go:55-57`) stays byte-exact; no exported signature of `lifecycle.SetAccountDisabled` or the `autoswitch.Switcher` interface changes.
- Unknown commands exit 2 (`subError`); the removed commands keep that code with their own message.
- No Co-authored-by or model trailer. English. CHANGELOG keeps `## Unreleased` as its only heading. Never `git stash`. Commit with the repo-local identity.
- Each test named "failing" below must fail before its code change; a guard test that already passes is labelled "guard".

## Review Focus

1. A cached window at ≥ 100 % whose reset time has passed is room (ENG-7), or B2 drops a model that is servable again. Stream A, Task A1.
2. Disabled, quarantined and live-claim-held accounts are not in the servable pool; the current account is. Stream A, Task A1.
3. The live account's stale claim is released without writing the profile's credential into the slot backup or the live store. Stream B, Task B3.
4. A reused PID never makes a dead session or profile live. Stream B, Task B4.
5. Former profile conversations survive retirement and `claude --resume <id>` finds them on the default login. Stream B, Task B3.

---

## Stream A — branch `fix/servable-models-20261116`

### Task A1: servable derived models and Fable-room reservation (B2, B3)

**Files:** modify `internal/reporting/defaultmodels.go`, `internal/cli/auto.go:173-194`, `internal/cli/startcmd.go:241`, `internal/reporting/atlimit.go:15-17`, `internal/cli/appshell.go:1155-1170`, `internal/web/state.go` (dashboard counted models), `internal/autoswitch/engine.go` (option + poll field), `internal/cli/auto.go:405-407` (help text: per-model windows also count when derived); tests in `internal/reporting/defaultmodels_test.go`, `internal/autoswitch/engine_test.go`, `internal/reporting/atlimit_test.go`.

**Interfaces (produces):**
- `type PolicyResult struct { Settings settings.AutoSwitchSettings; Reserved func(num string) bool; Unservable []string }`
- `func DefaultSessionPolicyResult(s *store.Store, base settings.AutoSwitchSettings, now time.Time) PolicyResult`; `DefaultSessionPolicy` keeps its signature and returns `(r.Settings, r.Reserved)`.
- `func autoswitch.WithUnservable(models []string) Option`; the poll event gains `"unservable":[...]` when non-empty.
- Room rule (one helper, used for every window here): a window has room when `pct < 100`, or `pct >= 100` and its `resets_at` is not after `now`. An absent 5h window is room.
- Servable: derived model M stays counted when an account in the pool reports M's window with room and has room on 5h and 7d. Pool = enabled accounts that are `RotationEligible`, not quarantined in the engine state file (when readable), plus the live account.
- Reserved: `s.FableCapable(n)` and the account's Fable window below `base.ModelThreshold` (room rule applied to the reset).

- [ ] **Step 1: failing tests:** `TestPolicyDropsUnservableModel` (tonight's cached usage: 3 5h 100/7d 27/Fable 28; 11 5h 100/7d 80/Fable 78; 4 5/78/100; 7 4/88/100; 8 –/93/100; one live Fable session → `Settings.Model == nil`, `Unservable == ["Fable"]`); `TestPolicyKeepsModelAfterReset` (11's 5h at 100 % with `resets_at` in the past → `"Fable"` counted); `TestPolicyPoolExcludesDisabledAndHeld` (the only Fable-room account disabled, then held by a live legacy claim → dropped); `TestReservationNeedsFableRoom` (no live Fable; 4, 7 Fable 100, 11 Fable 78, 3 Fable 96 with threshold 95 → only 11 reserved); `TestDerivedUnservableDoesNotExhaust` (engine built from the policy result; active 3 at 5h 100 → switch to 4 or 7, not `all-exhausted`); `TestAtLimitUsesDerivedPolicy` (`list` at-limit marks follow the result). Guards: `TestExplicitModelKeepsExhaustion` (rename of `TestScopedOnlyExhaustionDrivesWakeTime`, explicit `Model` → still `all-exhausted`), `TestDerivedServableFableStillMoves`.
- [ ] **Step 2:** run on tycdev → the failing ones FAIL.
- [ ] **Step 3:** implement; `auto.go` and `startcmd.go` use the result; `auto.go` passes `WithUnservable`.
- [ ] **Step 4:** package tests, then the full gate.
- [ ] **Step 5:** commit `fix(auto): a derived model no usable account can serve stops blocking; reserve only accounts with Fable room`. DESIGN: new section `A91. Servable models` (cause: 2026-11-15 incident; A77's accepted limit narrowed); CHANGELOG **Fixed**.

### Task A2: actors and external login changes (B6)

**Files:** `internal/cli/` entry points of enable/disable/switch/config set, `internal/tui/app.go:528-540`, `internal/cli/menu.go:255-257`, `internal/web/handlers.go:367-375,569-586` (and the switch/settings handlers), `internal/autoswitch/tick.go` + state (`live-login-changed`), `internal/cli/startcmd.go`; tests beside each.

**Interfaces (produces):** one INFO line in `tycswap.log` per manual action, after the shared call succeeded: `<Enabled|Disabled|Switched to|Set> … by <actor>` with actor `cli(pid=N,parent=<comm>)` (`parent` only where `/proc` exists), `tui`, `dashboard(cookie|bearer,<remote addr>)`, `start`; engine switches add `by engine:<trigger>` on their own line. Engine state gains `lastSeenLive` (slot) and `lastSeenAt`; a tick whose live slot differs from `lastSeenLive` with no tycswap switch recorded since `lastSeenAt` emits `{"event":"live-login-changed","from":"N","to":"M"}` once and logs it.

- [ ] **Step 1: failing tests:** `TestDisableOverHTTPLogsActor`, `TestCLIDisableLogsActor`, `TestSwitchLogsActorBesideA4Line` (A4 line unchanged), `TestSettingsWriteLogsActor`, `TestExternalLoginChangeLoggedOnce` (two ticks and a new engine instance → one event).
- [ ] **Steps 2–4:** FAIL, implement, pass, full gate.
- [ ] **Step 5:** commit `feat: every enable, disable, switch and settings write names its actor`; CHANGELOG **Added**; reference.md logging section.

## Stream B — branch `feat/retire-run-env-20261116`

### Task B1: move the helpers that stay (no behaviour change)

Move out of `internal/session` / `internal/sessprofile` the pieces other features use: `AuthOverrideEnvVars`, `ScrubAuthOverrides`, `CLICommand`, `CheckCmdShimArgs`, `KeychainServiceName`, `CredentialsFileName`, `ReadSessionIdentity` (callers: `ccversion.go:506`, `cli/codex.go:68`, `cli/loginwait.go:38`, `cli/addlogin.go:147,152`, `cli/startcmd.go:139`, `store/store.go:105-115`, `store/scope.go:69-129`, `lifecycle/addsource.go`, `lifecycle/purge.go`, `storemigrate`). Full gate green. Commit `refactor: move the helpers that outlive run and env`.

### Task B2: remove run, env, map, unmap (B5)

**Files:** delete `internal/cli/run.go`, `envcmd.go`, `mapcmd.go` and tests; `internal/mappings/` (drop `lifecycle/lifecycle.go:325`, `core/session_adapters.go:41`, `store/credproxy.go:140` callers); the session-manager files left after B1. Modify dispatch/parse/help (`help.go:47-52,105-106`), `cli/cli.go:45` (drop run/env from the exemption), `cli/cli.go:145-158` (keep `os.Unsetenv("CLAUDE_CONFIG_DIR")`, print one stderr notice that profiles are retired), `internal/cli/promptzsh/prompt.zsh:6`, `web/web.go:197-262` (no profile sessions view beyond what B3 needs), `cli/traypanel.go:58`, `docs/reference.md` (run/env/map/unmap sections), README (29 mentions), DESIGN `A92. run, env and map retire` (the 2026-11-10 ruling; what stays until no profile remains), CHANGELOG **Breaking/Removed**, review-findings RECLAIM-1/2 closed, start-login plan's S5 section replaced by a pointer here.

- [ ] **Step 1: failing tests:** `TestLegacyProfileCommandsAreGone` (`run`, `env`, `map`, `unmap` → exit 2, stderr names the removal and points to the shared login; `tycswap help` lists none); `TestPinnedShellStillUsesDefaultProfile` (guard: `CLAUDE_CONFIG_DIR` set to a profile → commands operate on `~/.claude`, one notice).
- [ ] **Steps 2–4:** FAIL, delete/modify, `git grep -n -E 'mappings\.|session\.New|sessprofile\.' ` shows only kept callers, full gate. **Step 5:** commit `feat!: remove run, env, map and unmap (operator's ruling 2026-11-10)`.

### Task B3: leftovers retire on their own (B5)

**Files:** `internal/store/ownership.go:27-39` (`CredentialOwner`), `internal/store/legacyrelease.go`, `internal/store/credproxy.go:76-110`, `internal/switching/legacyrelease.go`, `internal/core/autoswitch_adapters.go:5-19` (walk every stored account, the live one and disabled ones included), `internal/autoswitch/tick.go:94-98`, `internal/autoswitch/freshen.go:22-27`, `internal/autoswitch/ownership.go`, `internal/store/identity.go:443-480`; reuse `switching/groupretire.go` `copyHistory`; tests in the matching `_test.go` files.

**Rules:** real ticks only (never observe, dry-run or `start`); release stays outside perform's state lock. For a profile with no live process: copy its `projects/` into `~/.claude/projects` where missing (once; rerun-safe), then — live default account: clear the profile's login and drop the claim (the non-live branch of `postBackupWrite`), never touch the backup or the live credential; other accounts: today's `ReleaseLegacyAccount`. A profile login of a different lineage without `.tycswap-stale-credentials` keeps its claim on the live account. RECLAIM-3: an expired rotated idle profile is verified by refreshing its own token (fake provider in tests); same identity → today's release; `invalid_grant` → slot backup kept, claim dropped. RECLAIM-4/5 as their rows say (any schema change to `claims.json` documented in reference.md).

- [ ] **Step 1: failing tests:** `TestStaleClaimOnLiveAccountIsReleased` (+ observe and dry-run leave it); `TestLiveAccountReleaseNeverWritesBackup`; `TestDifferentLineageKeepsClaim`; `TestProfileHistoryCopiedOnce` (copy, rerun → no duplicates, existing default transcript untouched); `TestDisabledAccountProfileRetires`; `TestRotatedExpiredProfileVerifiedByRefresh`; `TestInterruptedReleaseReruns`; RECLAIM-4/5 tests. Guard: `TestLiveProfileKeepsClaim`.
- [ ] **Steps 2–4:** FAIL, implement, pass, full gate. **Step 5:** commit `fix(auto): retire idle legacy profiles, the live account's too, with their conversations`; close RECLAIM-3/4/5.

### Task B4: a reused PID is not a live session (SES-6)

**Files:** `internal/procdetect/procdetect.go` (record `procStart` vs `/proc/<pid>/stat` field 22 when both exist; otherwise today's check), `store.profilePIDs` / `LiveSessionPidsFor`; tests with a fake proc root.

- [ ] **Step 1: failing tests:** `TestReusedPIDIsNotLive` (record procStart 100, `/proc/<pid>/stat` field 22 = 200 → not live), `TestRecordWithoutProcStartFallsBack` (guard). **Steps 2–5:** FAIL, implement, pass, full gate, commit `fix: a reused PID no longer keeps a dead session or profile alive`; close SES-6.

## After both streams

- [ ] Whole-branch review of each PR by a fresh Opus at max effort (builder: Opus xhigh — check standing rules without tests, footprint, refusal/idempotence, invariants held only by a type); fix; PR CI green on Linux, Windows, macOS; merge A, then B (update branch), merge commits.
- [ ] Release per the runbook: `git ls-remote --tags origin`, tag the next minor (`v0.13.0` unless taken), notes, publish.
- [ ] flakelab PR: pin bump (version, source hash, Windows exe hash); `files/scripts/claude-sessions:802` resumes former profile sessions on the default login (their transcripts are now in `~/.claude/projects`); README `:596-597`, `remote-sessions.md:92`, `files/scripts/lib/prompt.zsh:6` drop run/env; `nix flake check` and drvPath comparison on tycdev; merge.
- [ ] Rollout: `flakelab update` on GamerTyc, `ssh tycdev flakelab update`; verify `tycswap --version`, the app service's ExecStart, `systemctl --user --failed`.
- [ ] Live acceptance, output kept: L1 `auto --once --dry-run --json` shows no `all-exhausted` while a Fable-spent account has 5h/7d room and Fable is unservable, with `unservable` named; L2 the journal shows the real move onto a Fable-spent account and Opus lanes keep answering; L3 `tycswap run` → exit 2 with the message; L4 `groups/claims.json` holds no claim without a live process, account 7's usage is fetched again, former profile transcripts resume on the default login; L5 the next switch line ends `by <actor>`; L6 accounts 9/10 match the operator's answer.
- [ ] Update the memory notes that teach `tycswap run` (`note:lane-on-another-account`, `claude-memory:lanes-in-new-shells`, `claude-memory:fable-sessions-start-on-ultra`, `claude-memory:tycswap-one-engine-program`).
