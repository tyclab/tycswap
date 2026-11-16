# Phase 2: a Fable login beside the default login

Status: design and pilot plan, nothing built. The operator asked for it on
2026-11-16 ("Spec phase 2"): "we can have 2 logins into the same account so i
think that is the best idea. no need for handover and stuff". The build waits
for the pilot's results and the operator's go.

## The problem it solves

One shared login serves every Claude Code session on the box. While a Fable
session is alive and some account has Fable room, the login sits on that account
and the Opus lanes burn its 5-hour window there too; while no Fable session is
alive, Opus fills the Fable-spent accounts. The operator wants both at once:
"opus session with opus limit auto swich to fill the accounts with no fable
left", Fable sessions on the accounts that still have Fable.

## The design in one paragraph

Each account gets a second, independent sign-in, its own OAuth grant and refresh-
token family, kept in a second slot ("fable lineage"). A second credential store,
the Fable store, holds whichever fable-lineage login tycswap makes live there.
Sessions started on a Fable model run with Claude Code's
`CLAUDE_SECURESTORAGE_CONFIG_DIR` pointing at the Fable store and share
`~/.claude` for everything else (transcripts, `--resume`, session messaging,
rules, plugins, settings). tycswap rotates the two stores independently: the
default store over all enabled accounts, putting Fable-room accounts last (Opus
fills the rest); the Fable store over accounts with Fable, 5h and 7d room. Because
each store has its own grant per account, both may sit on one account at once,
and no login ever moves between stores: no hand-over, no claim, no two owners.

## What Claude Code does (2.1.325, read from the binary; the pilot confirms)

- The credential store path is `CLAUDE_SECURESTORAGE_CONFIG_DIR` if set, else the
  config dir; a session re-checks the store's mtime before every request, so a
  switch inside either store reaches its sessions on their next request.
- The refresh lock lives in the store's directory; two stores never serialise
  against each other, which is fine for two independent grants.
- Org model entitlements are fetched at session start with the live credential:
  a Fable session starting on the Fable store starts on an account that offers
  Fable (the Fable store never holds an OBI login), so start marks are not needed
  for Fable sessions.
- `.claude.json` stays shared: every session writes its `oauthAccount` from
  memory, so that field no longer names the live account of either store, and
  `modelAccessCache` is shared and not keyed by org.
- MCP OAuth sign-ins (`mcpOAuth`) live in the credential store.

## Pilot (before any design detail is final)

All steps on GamerTyc with one personal Ultra account (e.g. slot 8,
tycho@tyclab.ai), a scratch Fable store under the scratch dir, `claude -p` probes
in a scratch folder. Operator-only: the second browser sign-in.

| # | Question | How | Pass |
| --- | --- | --- | --- |
| P1 | Do two independent grants of one account coexist? | `tycswap add --login` a second time for the account into a scratch slot; refresh each grant 20 times alternating (fake-free, real token endpoint) | both still answer the profile and usage endpoints; neither gets `invalid_grant` |
| P2 | Does a session use only the Fable store for credentials and `~/.claude` for the rest? | start `claude` with the variable set; list where it writes (inotify on both trees) | credentials read/written only in the store; transcript under `~/.claude/projects`; record under `~/.claude/sessions` |
| P3 | Does session messaging work across the two classes? | ListAgents from a default session sees the Fable-store session; one SendMessage each way | both delivered |
| P4 | Does a switch inside the Fable store reach a running session? | swap the store's credential between two fable-lineage logins | next request runs on the new account (usage moves) |
| P5 | What do shared `.claude.json` writes break? | run both classes on different accounts for an hour; record `oauthAccount` flips and tycswap's live-account detection | tycswap identifies each store's live account by credential lineage, never by `.claude.json` |
| P6 | Does a shared `modelAccessCache` leak a denial? | Opus session on an OBI login running; start a Fable session on the Fable store | Fable session runs Fable (transcript `"model"`) |
| P8 | Does a session parked on account A go on after the Fable store moves to B? | park a Fable-store session on a limit, switch the store | it waits until told (as on the default store); `claude-wake-parked` covers Fable-store panes too |
| P9 | Which store does `claude --resume` of a Fable session started without `--model` take? | settings default model Fable, resume without flags | the launcher reads the session's recorded family and picks the Fable store |
| P7 | MCP sign-ins | list the MCP servers that use OAuth; open one from a Fable-store session | works, or the list of servers needing a second sign-in is known |

## Build outline (after the pilot)

- Store: a `lineage` dimension on account credentials (`default`, `fable`);
  `tycswap add --login --lineage fable`; ownership checks per (account, lineage);
  usage fetched once per account and shared.
- Engine: one tick per store; the default store's policy reserves Fable-room
  accounts (A91's reservation, always on, since Fable sessions no longer live on
  it); the Fable store counts Fable plus 5h and 7d and holds only accounts whose
  plan offers Fable and that have a fable-lineage login.
- Launcher: `tycswap start` sets `CLAUDE_SECURESTORAGE_CONFIG_DIR` for a session
  whose model is Fable (`--model fable…`, or the settings' default model when no
  `--model`) and leaves every other session on the default store; tycswap's
  refusal of that variable (`addlogin.go:187-196`, `ScrubAuthOverrides`) becomes
  "set only by start, only to the Fable store".
- Live account per store from the credential lineage; dashboard and tray show two
  rows of "live" (default, Fable).
- flakelab: the launcher already routes every `claude` through `tycswap start`;
  session restore keeps a Fable session on the Fable store.

## Limits to settle with the operator after the pilot

- Fable subagents of an Opus session run on the default store and fail when it
  sits on a Fable-spent account; Fable work belongs in Fable sessions.
- A session that switches to Fable with `/model` stays on its start store.
- Both stores on one account share its 5h and 7d windows; the default store puts
  the Fable store's live account last to keep that rare.
- Each account needs a second browser sign-in (operator-only, once per account
  and box).
