# Busy-model rotation, and the end of `run`/`env`

Status: rev 2 after an independent review; one build (B2, B3, B5, B6), go given 2026-11-16 ("fix it merge it roll
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

From 23:53 every scheduled tick logged `all-exhausted` with headroom 0 on all
seven accounts (before that 3, 9 and 10 still had 1–10 %). Accounts 4 and 7 stood
at 5h 4–5 %, 7d 78/88 %, Fable 100 %; 3 and 11 had Fable room but 5h at 100 %;
9 and 10 were enabled and live only for minutes (23:43–23:47, 23:48–00:00). Opus lanes stopped on
3's and 11's 5-hour limits while 4 and 7 had Opus room.

Two independent blocks, both reproduced by `auto --once --dry-run --json`:

1. **The Fable window decided for every session.** `reporting/defaultmodels.go:103-132`
   counts the per-model windows of the families the default login's *live*
   sessions run. Live is `IsPIDAlive` on `~/.claude/sessions/<pid>.json`
   (`procdetect.go:53-120`); the record's `status` (`busy`, `idle`, `shell`,
   `waiting`) is read and ignored. 8 of 22 live sessions ran Fable at 00:2x, so
   Fable was counted; `oauth/windows.go:215-228` takes
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
   recipe) and was scheduled in that rollout's last series ("R: removals"), behind
   E1, E2, C and U. E1 never shipped (`TYCSWAP_AUTO_MANAGED_BY`, which its Task 3
   deletes, is still in `cli/webfacades.go`), so the removal waited behind
   unbuilt work. No test asserted the commands gone.
2. Lanes kept using `tycswap run` (and Fable starts on Ultra); the pins caused
   ownership blocks; v0.11.6 (f82e645, ddfc82a, 782040f, 2026-11-13/14) added
   reclaim machinery to the feature ruled out.
3. The start-login spec (0ae1cb0) deferred the removal to S5 "only after the
   operator accepts the replacement across the fleet", a gate the ruling did not
   have; its acceptance step has no owner, was never raised, no account was ever
   marked, so S5 could not start.
4. The command stayed available and an agent note (2026-11-15) documented it;
   block 1 made 4 and 7 unreachable, so lanes were pinned again and caused block 2.

Process fix: an operator's removal ruling ships in the next release, never behind
a series or an acceptance gate the operator did not set; it is pinned by a test
that the command is gone; every ruling in this spec maps to a task and a test.

## What a running session does (Claude Code 2.1.325, measured)

- Before every API request a session compares the credential store's mtime and
  re-reads it, so a switch reaches every default session on its next request.
- Org model entitlements are fetched once at session start; a refused model stays
  refused until `/login`. Account switches never re-promote it.
- Peer discovery and transcripts live in the config dir; a second config dir
  splits them. Two config dirs cannot hold one refresh-token lineage.

## Why not "count only working sessions" (review of rev 1)

Rev 1 counted a Fable session only while its Claude Code record was not `idle`.
Claude Code 2.1.325 reports `busy` while any background agent runs and `shell`
while any background Bash runs, so long-lived coordinators never read `idle`
(live: 23 sessions, 4 busy, 8 shell, 2 waiting, 9 idle); and a session parked on
auto-resume after a limit, or waiting on an MCP monitor, a peer message or a
scheduled wake-up, reads `idle` and would have been stranded on a Fable-spent
login. That rule, the prompt hook built on it and the start race it was to close
are dropped. The full ask needs Phase 2.

## Goals, each with the test that proves it

B2. **A model nobody can serve does not block anybody.** The default-session
policy counts a derived model only while some account the tick may use
(enabled, rotation-eligible, not quarantined, plus the current account) reports
that model's window with room and has room on its 5h and 7d windows, judged on
cached usage where a window at ≥ 100 % whose reset has passed counts as room
(ENG-7). It returns the counted models after that filter and the dropped ones;
the engine, `list`, the dashboard and the tray all call it; the engine keeps its
`e.models` code paths unchanged. Test: tonight's usage with a live Fable session
→ no model counted, active 3 at its 5h limit moves to 4 or 7, not
`all-exhausted`; 11's 5h reset passed → Fable counted, 7 → 11 at-limit.

B3. **Opus fills the accounts with no Fable left whenever Fable is not counted.**
The reservation covers only accounts that can run Fable *and* sit below the model
bar (`autoswitch.modelThreshold`) on their Fable window; an account whose Fable
week is spent is never reserved. Test: no live Fable session, active 11 (Fable
78 %) → `reserved` move to 4 or 7 after the cooldown; 4 and 7 are never ranked
last for Opus.

B5. **`run`, `env`, `map`, `unmap` are gone; leftovers retire on their own.**
The commands, their help and docs, the session manager and the mappings go; the
commands answer exit 2 with a removal message. Real ticks retire every idle legacy
profile of every stored account (disabled ones too): conversations in its
`projects/` are copied into `~/.claude/projects` where missing (A78's history
copy), then the claim is released. For the live default account (tonight's 7)
release means clearing the profile's login and dropping the claim, never touching
the backup or the live credential; a different lineage without the stale marker
keeps its claim. An expired rotated idle profile is verified by refreshing its own
token (RECLAIM-3); RECLAIM-4 and -5 are fixed. Liveness compares `procStart` with
`/proc/<pid>/stat` field 22 (SES-6), so a reused PID is not a live session.
flakelab's session restore resumes former profile sessions on the default login.
Tests: removal message and exit 2; stale `legacy:7` on the live account released
on a real tick only; transcripts copied once; reused PID not live; refusals and a
rerun after an interrupted release.

B6. **Every manual change says who made it.** Each entry point (CLI, TUI,
dashboard HTTP, engine, start) writes one `<action> by <actor>` line next to the
shared call; the `Switched from account X to Y` line (DESIGN A4) stays
byte-exact; no shared signature changes. A live login that changed without a
tycswap switch logs one `live-login-changed` event, remembered in the engine
state. The poll event names models dropped as `unservable`. Tests: disable over
HTTP logs `by dashboard`; an external credential write logs once across two
ticks and a restart.

## Ruling-to-test coverage

| Ruling | Goal | Test |
| --- | --- | --- |
| "Remove them" (run/env, 2026-11-10), "FIX IT" | B5 | removal message; retirement tests |
| no account pins (2026-11-14/15) | B5 | same; flakelab restore on the default login |
| Opus fills accounts with no Fable left, kept in main rotation (2026-11-16) | B2, B3 | unservable-Fable replay; reservation needs Fable room |
| accounts get blocked, want auto (2026-11-16) | B2, B5 | tonight's usage replayed; account 7 claim release |
| Keep OBI | B6 + operator question | toggles attributed; 9/10 put to the operator |

## Non-goals and accepted limits

- **One login serves everyone.** While any Fable session is alive and some
  account can serve Fable, the login stays on a Fable-capable account and the
  Opus lanes run there too (today's rule, unchanged). Opus fills the accounts
  without Fable while no account can serve Fable or no Fable session is alive.
  Running both on different accounts at the same time needs two credential
  stores: Phase 2, put to the operator now.
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

- **Servable-model single login (chosen).** Smallest change at the cause:
  unservable models stop blocking, the reservation follows real Fable room,
  leftover pins retire. Activity-based counting was rev 1 and is dropped (above).
- **Ignore model windows for the default login.** Unblocks Opus but strands Fable
  sessions on Fable-spent accounts with no move back; contradicts A73/A77's
  reason to exist.
- **Two stores now.** Meets the operator's ask in full but rebuilds the retired
  group machinery under time pressure; Phase 2.

## Left to the builder

Names of events and helpers; the exact removal message.
