# Busy-model rotation, and the end of `run`/`env`

Status: design for one build (B1–B6), go given 2026-11-16 ("fix it merge it roll
it out. test it make sure it really works"). Phase 2 (parallel Fable and Opus
on two logins) is a decision for the operator, not part of this build. Plan:
`docs/superpowers/plans/2026-11-16-busy-model-rotation.md`.

## The operator's words

- 2026-11-10: `tycswap run` / `env` session profiles: "Remove them".
- 2026-11-14: "Ordinary workers must share the default rotating login … Do not
  create account pins again."
- 2026-11-15: "this is why i told u to not use single accounts, move sessions to
  other accounts that auto swiches"; "Keep OBI" (accounts 9 and 10 stay in
  rotation).
- 2026-11-16: "i want opus session with opus limit auto swich to fill the
  accounts with no fable left"; "right now somehow accounts get blocked, but i
  want that auto and the4se accounts should be still in main rotation"; on the
  run/env removal that never happened: "Why that never happen RCA! FIX IT".

## What happened (2026-11-15 23:30 – 2026-11-16 00:35, GamerTyc, v0.12.0)

Every scheduled tick logged `all-exhausted` with headroom 0 on all seven
accounts. Accounts 4 and 7 stood at 5h 4–5 %, 7d 78/88 %, Fable 100 %; 3 and 11
had Fable room but 5h at 100 %; 9 and 10 were disabled. Opus lanes stopped on
3's and 11's 5-hour limits while 4 and 7 had Opus room.

Two independent blocks, both reproduced by `auto --once --dry-run --json`:

1. **The Fable window decided for every session.** `reporting/defaultmodels.go:103-132`
   counts the per-model windows of the families the default login's *live*
   sessions run. Live is `IsPIDAlive` on `~/.claude/sessions/<pid>.json`
   (`procdetect.go:53-120`); the record's `status` (`busy`, `idle`, `shell`,
   `waiting`) is read and ignored. 8 of 22 live sessions ran Fable, most of them
   idle at a prompt, so Fable was counted; `oauth/windows.go:215-228` takes
   100 − max(5h, 7d, Fable), so every account with Fable spent had headroom 0,
   was never a target (`tick.go:412`, `:436`) and made the tick
   `trulyExhausted` (`:232-239`). With `--model none` the same tick gives 4: 22,
   7: 12, 9: 34 and stays on 7. DESIGN A77 lists "one live Fable session keeps
   every default session on a Fable-capable account" as an accepted limit; the
   operator's 2026-11-16 words overrule it.
2. **Live `run`/`env` profiles held 4 and 7** from 21:02 (pinned lanes
   pv-follow-ls, pv-display, usce-review-a/b, ma-finish), so they were not
   `RotationEligible` (`store/identity.go:443-461`, `ownership.go:236-252`) and
   `tycswap switch 4|7` refused. At 00:08 account 7 was the live default login
   and still claimed `legacy:7` with no live process: "conflicting credential
   owners; hold switching", usage not fetched since.

Also measured: accounts 9/10 were enabled and disabled eight times between
18:38 and 00:00 through the dashboard's HTTP API (the app process logged them;
the tray has no such route), and three switches (23:47, 23:48, 00:00) left no
engine stamp; no log names who did any of it (`lifecycle/disable.go:56-58`,
`web/handlers.go:367-375`).

## RCA: why `run`/`env` survived the 2026-11-10 ruling

1. The ruling went into the one-engine design (1707c73, D10, with a migration
   recipe) but the E1 plan's five tasks never included it, and no test asserted
   the commands gone. No spec-to-plan coverage check caught the omission.
2. Lanes kept using `tycswap run` (and Fable starts on Ultra); the pins caused
   ownership blocks; v0.11.6 (f82e645, ddfc82a, 9544f23, 782040f, Codex sessions
   2026-11-13) added reclaim machinery to the feature ruled out.
3. The start-login spec (0ae1cb0) deferred the removal to S5 "only after the
   operator accepts the replacement across the fleet", a gate the ruling did not
   have; its acceptance step has no owner, was never raised, no account was ever
   marked, so S5 could not start.
4. The command stayed available and an agent note (2026-11-15) documented it;
   block 1 made 4 and 7 unreachable, so lanes were pinned again and caused block 2.

Process fix in this build: every operator ruling in this spec maps to a task and
a test (table below); a removal is pinned by a test that the command is gone; no
release gate exists that the operator did not set.

## What a running session does (Claude Code 2.1.325, measured)

- Before every API request a session compares the credential store's mtime and
  re-reads it, so a switch reaches every default session on its next request.
- Org model entitlements are fetched once at session start; a refused model stays
  refused until `/login`. Account switches never re-promote it.
- Peer discovery and transcripts live in the config dir; a second config dir
  splits them. Two config dirs cannot hold one refresh-token lineage.

## Goals, each with the test that proves it

B1. **A model counts only for sessions that work.** A default session counts
toward the counted models and toward clearing the Fable reservation unless its
record says `status: "idle"`. `busy`, `shell` (waiting on its background work),
`waiting` (a dialog), a missing or unknown status, and a record without a
family stay counted, as today. Test: 8 idle Fable records + 14 busy Opus records
→ no model counted, reservation on; one `shell` Fable record → Fable counted.

B2. **A model nobody can serve does not block anybody.** A counted model is
dropped for the tick when no account that the tick may use (rotation-eligible
candidates and the current account) reports that model's window below 100 %
*and* has room on its 5h and 7d windows. Test: tonight's usage (3, 11: 5h 100;
4, 7, 8, 9, 10: Fable 100) with a busy Fable session → active 3 at its 5h limit
moves to 4 or 7, not `all-exhausted`.

B3. **Opus fills the accounts with no Fable left.** While no counted session runs
Fable, the reservation covers only accounts that can run Fable *and* have Fable
room; accounts whose Fable week is spent rank first and are the targets of the
`reserved` hand-over. Test: idle Fable sessions only, active 11 (Fable 78 %, 5h
low) → `reserved` move to 4 or 7 after the cooldown; 4 and 7 are never disabled
or ranked last for Opus.

B4. **A Fable prompt moves the login before its request.** A synchronous
`UserPromptSubmit` hook runs `tycswap auto --prompt` with the hook's JSON on
stdin; the tick treats that session as working (B1) and, if its family's model
is counted and servable while the live account cannot serve it, switches before
Claude Code sends the request (`model-missing` / at-limit, no cooldown). Cached
usage only; it always exits 0 and never blocks a prompt longer than its timeout.
Test: idle Fable session on a login with Fable 100 % and an account with Fable
room → the prompt hook switches; an Opus session's prompt does not switch.

B5. **`run`, `env`, `map`, `unmap` are gone; leftovers retire on their own.**
The commands, their help and docs, the session manager and the mappings are
removed. Existing profiles keep being released by real ticks once no process
uses them (A89), now also when the claimed account is the live default login
(tonight's account 7) and when the profile's login rotated and then expired
(RECLAIM-3: verify by refreshing the idle profile's own token). A live or
unverifiable claim stays. RECLAIM-4 and -5 are fixed. Tests: `tycswap run` / `env`
/ `map` / `unmap` exit 64 as unknown commands; a stale `legacy:7` claim on the live
account is released on a real tick, never on observe or dry-run; an expired
rotated idle profile is released after a refresh.

B6. **Every manual change says who made it.** Enable, disable, switch and
settings writes log the actor: `cli` (pid, parent command), `tui`, `dashboard`
(cookie or bearer, remote address), `engine:<trigger>`, `start`. A live login
that changed without a tycswap switch is logged once as `live-login-changed`.
`list`, the dashboard and the tray read the counted models from the same policy
the engine uses. Tests: a disable through the HTTP handler logs `by dashboard`;
an external credential write yields one `live-login-changed` event.

## Ruling-to-test coverage

| Ruling | Goal | Test |
| --- | --- | --- |
| "Remove them" (run/env, 2026-11-10), "FIX IT" | B5 | commands unknown; leftover release tests |
| no account pins (2026-11-14/15) | B5 | same |
| Opus fills accounts with no Fable left, kept in main rotation (2026-11-16) | B1–B3 | idle-Fable, unservable-Fable and reservation tests |
| accounts get blocked, want auto (2026-11-16) | B2, B5 | tonight's usage replayed; account 7 claim release |
| Keep OBI | B6 | toggles are attributed; nothing in tycswap disables accounts |

## Non-goals and accepted limits

- **One login serves everyone.** While a Fable session works (busy, shell or
  waiting) and some account can serve Fable, the login stays on a Fable-capable
  account and the Opus lanes run there too. Opus fills the accounts without
  Fable whenever no Fable session works. Running both on different accounts at
  the same time needs two credential stores: Phase 2 below.
- A Fable session's background wake-up (a watcher or task notification, not a
  typed prompt) passes no prompt hook. It counts as working while it waits
  (`shell`), so the login does not leave Fable under it.
- Start marks (`tycswap start --allow`) stay the operator's choice; this build
  sets none.
- The engine never disables or enables accounts.

## Phase 2, for the operator's decision (not built here)

Two rotating credential stores: the default store for Opus/other sessions and a
Fable store that rotates among accounts with Fable room, both sharing `~/.claude`
for transcripts, rules, plugins and session messaging through Claude Code's
`CLAUDE_SECURESTORAGE_CONFIG_DIR` (undocumented; tycswap treats it as hostile
today). It gives parallel Fable and Opus on separate accounts. Costs: an account
may sit in one store at a time, so moving it between stores brings back the
release and two-owner machinery behind 15 of the 17 rows the profile removals
closed; `.claude.json` (`oauthAccount`, `modelAccessCache`) is shared and
rewritten by both classes; MCP OAuth sign-ins are per store. Needs a pilot on
throwaway accounts before any design.

## Approaches considered for this build

- **Busy-aware single login (chosen).** Smallest change at the cause: liveness
  becomes activity, unservable models stop blocking, the reservation follows real
  Fable room, a prompt hook closes the start race.
- **Ignore model windows for the default login.** Unblocks Opus but strands Fable
  sessions on Fable-spent accounts with no move back; contradicts A73/A77's
  reason to exist.
- **Two stores now.** Meets the operator's ask in full but rebuilds the retired
  group machinery under time pressure; Phase 2.

## Left to the builder

Names of flags, events and helpers; how the prompt hook finds the session's
record (session id → `default_models.json` family, pid → `sessions/<pid>.json`);
where the actor travels (a context value or an argument).
