<p align="center">
  <img src="docs/logo.svg" width="160" alt="tycswap: an octopus holding two login cards, arrows swapping them">
</p>

# tycswap

**One machine, several Claude Code and Codex logins, never a logout.**

tycswap keeps every login you own for an agent CLI in a numbered slot, makes one
of them live, switches to another in place, and does that by itself before the
live one runs into its rate limit. It shows every account's usage windows in a
list and in a full-screen dashboard, and runs a second account in its own
terminal beside the live one. It works with the Claude Code CLI and the VS Code
extension, and ships as a single static Go binary for Linux, macOS and Windows.

It began as a fork of [dpemmons/cswap](https://github.com/dpemmons/cswap), the
Go port of [claude-swap](https://github.com/realiti4/claude-swap), and keeps
that command grammar, store layout and JSON contract. What it adds is a
second provider: **Codex (ChatGPT)** accounts get the same slots, in-place
switching, usage windows, auto-switch, dashboard rows and export/import as
Claude accounts, under `tycswap codex …` — see
[Codex accounts](#codex-chatgpt-accounts). tycswap is its own product with its
own binary, module and store; an existing store from the parent tools is copied
over once by [`tycswap migrate`](#move-over-from-claude-swap).

| | Claude Code | Codex (ChatGPT) |
|---|---|---|
| store a login, switch in place, alias, disable | yes | yes |
| usage windows in list and dashboard | 5h, 7d, per model | 5h, weekly |
| auto-switch before a limit | yes | yes, same `tycswap auto` loop |
| second account in its own terminal | `run`, `env` | no: a switch lists the `codex` processes still on the old token |
| take over an existing switcher's registry | — | yes, from codex-auth |
| export, import, purge | yes | yes |

This document is the entry-point guide: concepts, installation, and the tasks a
user performs in order. The complete per-command contract — every argument,
default, exit status, JSON schema, and error condition — is in
[`docs/reference.md`](docs/reference.md). The architecture is in
[`docs/DESIGN.md`](docs/DESIGN.md).

## Concepts

**Account.** An account is a saved Claude Code login: an OAuth credential blob
(or an API key) together with a snapshot of the login's configuration. tycswap
holds accounts in its backup store and never alters the underlying Claude
subscription; it only moves credentials into and out of Claude Code's own
credential store.

**Slot.** Each account occupies a numbered slot. Slots start at 1 and may be
sparse — an account can sit at slot 5 while slot 4 is empty. Every command that
names an account accepts the slot number, the account email, or the account
alias interchangeably.

**Active login.** Exactly one account is active at a time. The active account is
the login Claude Code uses: its credentials are the ones written into Claude
Code's credential store and its identity fields into `~/.claude.json`. Switching
replaces the active login with another account's stored credentials.

**Backup store.** The backup store is a directory holding every managed
account's saved credentials and configuration, the account roster
(`sequence.json`), settings, directory mappings, the usage cache, session
profiles, and the switch log. Its location is per-platform (see
[Data locations](#data-locations)). Switching copies credentials between the
backup store and the active login; it removes nothing.

**Usage windows.** Claude enforces rate limits over rolling time windows: a
5-hour window and a 7-day window, and — for accounts that use them — per-model
weekly windows. tycswap fetches each account's usage and reports the remaining
headroom as a percentage. An account is *at limit* when a relevant window has
reached or exceeded its limit. Auto-switch and the `best` switch strategy use
this headroom to choose a target; by default (`autoswitch.strategy`
`soonest-reset`) auto-switch orders the targets that qualify by earliest weekly
renewal, and `best` orders them by most headroom instead.

**Session profile.** A session profile is a private `CLAUDE_CONFIG_DIR` under
the backup store's `sessions/` directory. It lets one account run in a single
terminal without changing the machine-wide active login. `tycswap run` and
`tycswap env` prepare and use session profiles; this is how two accounts run in
parallel.

**Relationship to claude-swap (Python).** tycswap is a Go port of claude-swap
(Python, MIT, by Onur Cetinkol). The two implementations share their on-disk
formats and `--json` schemas: an export file written by either implementation
is read by the other, and the layout inside the backup store is the same. The
store itself lives at its own path (see [Data locations](#data-locations)), so
the two never share one; `tycswap migrate` copies an existing claude-swap store
once. The command grammar,
exit codes, and lock protocol are identical. The macOS menu bar application is
not part of tycswap. The update mechanism, the `tycswap env` command, the
at-limit markers, and the Codex provider are Go-only; where the Go binary and the Python reference
diverge, the divergences are enumerated in [`docs/DESIGN.md`](docs/DESIGN.md)
§6 and its Amendments.

## Installation

**Prerequisites.**

- Go 1.25.5 or newer to build or `go install`. The CLI builds without cgo.
- The Claude Code CLI, for the accounts to run against.
- macOS only: the `security` command, used to read and write the login Keychain.

**From a checkout:**

```bash
git clone https://github.com/tyclab/tycswap
cd tycswap
make build      # builds ./tycswap with the version embedded
make install    # go install with the version embedded
```

The binary is named `tycswap` and `make install` places it in the Go install
directory (`$GOBIN`, or `$GOPATH/bin`, or `~/go/bin`). Add that directory to
`PATH`. `make help` lists every target.

**With `go install`:**

```bash
go install github.com/tyclab/tycswap/cmd/tycswap@latest
```

## Tasks

The tasks below follow the order in which a user meets them. Each transcript is
complete. Prompt lines begin with `$`; all other lines are program output.

### Add the first account

Log into Claude Code as usual, then snapshot the current login into slot 1:

```
$ tycswap add
Added Account 1: alice@example.com [personal]
```

`tycswap add` reads the active Claude Code login, copies its credentials and
configuration into the backup store, and makes it the active managed account. If
no account is yet managed, it lands in slot 1.

### Add a second account

There are two ways to register another account, and neither touches the
current login.

To add an interactive (subscription) login, run `tycswap add --login`. It runs
Claude Code's own `claude auth login` in a private scratch profile, so the
browser flow signs the second account in beside the live one, and stores the
result in the next free slot:

```
$ tycswap add --login
Added Account 2: bob@example.com [personal] (from login)
```

The live login stays as it was and remains the active account; add `--switch`
to make the new account live straight away, through the same switch as
`tycswap switch 2`. Arguments after `--` go to `claude auth login`, for example
`tycswap add --login -- --email bob@example.com` or `-- --sso`. `--slot` and
`--alias` work as for `tycswap add`. If the account is already managed, its
stored credentials are refreshed in place. The scratch profile is deleted
whether the login succeeds or not. On macOS, where Claude Code keeps the new login in
the Keychain, tycswap reads it from the item made for the scratch profile and
deletes that item along with the profile.

To register an account from a setup token or API key, use `tycswap add-token`. The token is read interactively (not echoed) or
from `-` (standard input). Avoid passing it as an argument: the command line is visible to other local users through `ps`
and stays in your shell history, and tycswap warns when you do.

```
$ tycswap add-token --email bob@example.com --slot 2
Token:
Added Account 2: bob@example.com [personal] (from token)
$ pass show claude/bob-token | tycswap add-token - --email bob@example.com
```

`--slot` is optional; without it the account lands in the next free slot.
`--email` labels the account.

### Switch accounts

Rotate to the next account in slot order:

```
$ tycswap switch
Switched to Account-2 (bob@example.com)
Accounts:
  1: alice@example.com [personal]
     usage unavailable (http-429)

  2: dev (bob@example.com) [personal] (active)
     usage unavailable (http-429)

  3: key@example.com [personal]
     API key (no quota)

  5: carol@example.com [personal] (disabled)
     usage unavailable (http-429)

New account is active on your next message — no restart needed.
```

Switch to a specific account by slot number, email, or alias:

```
$ tycswap switch 1
$ tycswap switch bob@example.com
$ tycswap switch dev
```

Switch by remaining quota instead of slot order:

```
$ tycswap switch --strategy best            # the account with the most headroom
$ tycswap switch --strategy next-available  # rotate, skipping at-limit accounts
```

**Restart semantics.** The follow-up line states whether Claude Code needs a
restart, keyed to where the credential write landed:

- On Linux, WSL, and Windows the active credentials are file-based. The line
  reads `New account is active on your next message — no restart needed.` The
  running Claude Code session picks up the new account on its next message.
- On macOS the active credentials are stored in the login Keychain. The line
  reads `Restart Claude Code to apply immediately — otherwise the session can
  take up to ~30 seconds to pick up the new account.`

### Read the list dashboard

`tycswap list` (alias `ls`) prints every managed account and its usage:

```
$ tycswap list
Accounts:
  1: alice@example.com [personal] (active)
     usage unavailable (http-429)

  2: dev (bob@example.com) [personal]
     usage unavailable (http-429)

  3: key@example.com [personal]
     API key (no quota)

  5: carol@example.com [personal] (disabled)
     usage unavailable (http-429)
```

Markers, per line:

- The leading number is the slot.
- `alias (email)` — when an account has an alias, the alias precedes the email
  in parentheses (slot 2 above).
- `[personal]` or `[Organization Name]` — the account's organization label.
- `(active)` — the currently active login.
- `(disabled)` — held out of auto-rotation by `tycswap disable`.
- `at limit: <windows>` — a relevant usage window is at or over its limit; the
  suffix names the limiting windows (for example `7d` or `Fable 5`).

The second line per account is usage. For an OAuth account with a healthy fetch
it shows the 5-hour and 7-day percentages and reset times; `usage unavailable
(http-NNN)` reports a failed fetch and its HTTP status; `API key (no quota)`
marks an account authenticated by API key, which has no measured quota.

Add `--token-status` for each OAuth account's token expiry and refresh state:

```
$ tycswap list --token-status
Accounts:
  1: alice@example.com [personal] (active)
     usage unavailable (http-429)
     • oauth: unknown expiry, refresh token no
  ...
```

`tycswap list --json` and `tycswap status --json` emit one JSON document
(schemaVersion 1) instead of the human table; the schemas are in
[`docs/reference.md`](docs/reference.md). To extract the active account's email:

```
$ tycswap status --json | jq -r '.active.email'
alice@example.com
```

The full-screen interactive dashboard is `tycswap` with no arguments (also
`tycswap tui` and `tycswap watch`). It shows the same accounts with live usage and
refreshes on a timer. Its Settings screen (`c`, or the menu's "Settings…" row)
lists every `settings.json` key with its value and help text and edits it in
place — a bool toggles, a choice cycles, a number or string is typed and
validated like `tycswap config set` — and `u` resets a key to its default; the
auto-switch screen's own threshold adjustment stays session-only.

### Alias accounts

An alias is a short name usable anywhere an account is named:

```
$ tycswap alias 3 ops
Set alias 'ops' for Account 3

$ tycswap alias
Aliases:
  2: dev (bob@example.com)
  3: ops (key@example.com)

$ tycswap alias 3 --unset
Removed alias for Account 3
```

### Disable and enable accounts

A disabled account stays managed and switchable by hand but is skipped by
auto-switch:

```
$ tycswap disable 2
Disabled Account-2 (bob@example.com).

$ tycswap enable 2
Enabled Account-2 (bob@example.com).
  It is back in the rotation.
```

### Auto-switch before a limit

`tycswap auto` runs a foreground loop that polls usage and switches to another
account before the active one reaches its rate limit:

```
$ tycswap auto --threshold 80
Auto-switch running: threshold 80%, every 60s — Ctrl-C to stop
13:53:36  Account-1 (alice@example.com): usage unknown (http-429) (switch at 80%) | others: #2: ? (http-429), #3: ?, #5: ? (http-429)
13:53:36  no switch: active-usage-unknown (1/3 before failover)
```

`--threshold` is the headroom percentage at which a switch triggers; the polling
interval defaults to 60 seconds. Ctrl-C stops the loop and exits 130.

**Single tick, for cron.** `tycswap auto --once` performs exactly one evaluation
and exits; `--json` prints the tick as JSON event lines:

```
$ tycswap auto --once --json
{"active":{"email":"alice@example.com","number":1},"event":"poll","fetchErrors":{"1":"http-429","2":"http-429","5":"http-429"},"headroomPct":{"1":null,"2":null,"3":null,"5":null},"schemaVersion":1,"threshold":80.0,"ts":"2026-07-17T20:49:40Z"}
{"detail":"1/3 before failover","event":"no-switch","reason":"active-usage-unknown","schemaVersion":1,"ts":"2026-07-17T20:49:40Z"}
```

The process exit code reports the outcome, so a scheduler can branch on it:

| Exit | Meaning  | Condition                                                    |
|------|----------|-------------------------------------------------------------|
| 0    | Switched | A switch happened.                                          |
| 1    | Error    | Network trouble, lock contention, or a transient failure.  |
| 2    | NoAction | Nothing to do — below threshold, in cooldown, or idle.     |
| 3    | Blocked  | A switch was wanted but no viable target remained.         |

A crontab entry that ticks every five minutes and logs the result:

```cron
*/5 * * * * tycswap auto --once --json >> tycswap-auto.log 2>&1
```

cron runs with a minimal `PATH`; the entry finds `tycswap` only if its install
directory is on cron's `PATH` (set `PATH=` at the top of the crontab, or place
`tycswap` in a directory cron already searches).

**Per-model weekly limits.** By default auto-switch weighs only the account-wide
5-hour and 7-day windows. To also count a model's per-model weekly window, set
the model's display name in `autoswitch.model`:

```
$ tycswap config set autoswitch.model Fable
autoswitch.model = Fable
```

The value is matched, case-insensitively, against the display name of the
account's scoped weekly window. An exhausted per-model weekly window then reads
as at-limit even when the account-wide 7-day window has headroom. A name that
matches no window is a silent no-op — use the display name exactly as it appears
in the account's per-model usage rows.

**Renewal-ordered switching.** By default auto-switch tries the qualifying
target with the earliest weekly renewal first (`autoswitch.strategy
soonest-reset`): the account whose 7-day window — and any `autoswitch.model`
weekly windows — refills earliest is tried first, so quota is spent where it
returns soonest and no account sits idle on a window that is about to renew.
`best` instead tries the target with the most headroom first. Qualification
itself is the same under both. On a proactive switch, a target
must still land under the threshold and beat the active account by the
hysteresis margin. On an at-limit or failover switch neither check applies,
but `soonest-reset` still never lets an early renewal beat the threshold: an
account at or above the threshold is tried only after every account below
it, regardless of how soon it renews.

```
$ tycswap config set autoswitch.strategy best
autoswitch.strategy = best
```

A settings file that does not name a strategy uses `soonest-reset`; one that
sets `best` keeps it.

**Auto-switch never changes how Claude Code authenticates.** It rotates
between subscription accounts only. It does not move onto an API-key account
when every subscription account is at its limit (it reports the wall
instead), and it does not move off one that you made active. An API-key
account replaces the subscription login with a key billed per token, and a
Claude Code session that is already running keeps the login it started with
until you restart it; an automatic switch into that state would leave every
open session on the old login while tycswap reports the new one. Switching to
an API-key account by hand asks first and says how many sessions you will have
to restart:

```
$ tycswap switch 3
An API-key account authenticates with a key instead of a subscription login, and its usage is billed per token.
2 Claude Code sessions are running; they keep their current login until each one is restarted.
Switch to API-key account #3? [y/N] y
Switched to Account-3 (key@example.com)
```

`--yes` (`-y`) answers the question for scripts. Without a terminal and
without `--yes` the switch is refused at once, and any answer but `y` cancels
it; both exit 1. The bare `switch` and `switch --strategy` skip API-key
accounts. The browser dashboard's *Switch* asks before switching to one; the
TUI and the dashboard's *Force switch* do not ask and refuse, pointing at
`tycswap switch <n>`.

### Run accounts in parallel

`tycswap run` launches Claude Code as a chosen account in the current terminal
only, using a session profile, without changing the machine-wide active login.
A second terminal can run `tycswap run` for a different account at the same time.

```
$ tycswap run 2 -- --version
Launching Account-2 (bob@example.com) [session mode]
2.1.212 (Claude Code)
```

Arguments after `--` are forwarded to `claude`. When the requested account is
already the active default login, `tycswap run` launches `claude` directly rather
than preparing a session profile.

**Directory mappings.** Map a directory to an account so a bare `tycswap run` in
that directory resolves to it:

```
$ tycswap map 2 ~/work/client-app
Mapped ~/work/client-app → Account-2 (bob@example.com)

$ tycswap map
Directory mappings:
  ~/work/client-app → 2: bob@example.com [personal]

$ cd ~/work/client-app && tycswap run     # runs as account 2
```

`tycswap unmap [path]` removes a mapping.

**Pinning a shell.** `tycswap env` prepares the same session profile `tycswap run`
does, but instead of launching `claude` it prints an eval-able export that pins
the current shell's `CLAUDE_CONFIG_DIR` to the account. Every subsequent
`claude` in that shell runs as the pinned account, and no separate process is
launched:

```
$ eval "$(tycswap env 2)"                 # pin this shell to account 2
$ eval "$(tycswap env)"                   # resolve from this directory's mapping
$ eval "$(tycswap env --unset)"           # drop the pin (back to the default login)
```

`tycswap env` writes only the eval lines to standard output; every notice goes to
standard error:

```
$ tycswap env 2
Prepared Account-2 (bob@example.com) [session mode]      # (stderr)
export CLAUDE_CONFIG_DIR='~/.local/share/tycswap/sessions/2-bob_example.com'
```

When the chosen account is already the active default login, `tycswap env` exports
nothing and writes a note to standard error, since an unpinned shell already uses
that account; the `eval` is a safe no-op.

Other shells take `--shell`:

```fish
tycswap env 2 --shell fish | source
```
```powershell
tycswap env 2 --shell pwsh | Invoke-Expression
```

A pinned shell keeps the profile it eval'd until it eval's again. After
switching accounts or a credential change, re-run the `eval` — or use
`tycswap run`, which re-prepares the profile on every launch.

In a pinned shell, every tycswap command except `tycswap env` and `tycswap run`
ignores the pin and operates on the default login, printing `This shell is
pinned via tycswap env; operating on the default login.` to standard error.

### Export and import

`tycswap export` writes accounts to a portable file for transfer to another
machine:

```
$ tycswap export backup.tycswap
Exported 4 account(s) to backup.tycswap

$ tycswap export bob-only.tycswap --account 2
Exported 1 account(s) to bob-only.tycswap
```

`--account` limits the export to one account. The export file is JSON with
`encrypted: false`; the format is identical to claude-swap's, so either
implementation imports the other's exports.

`tycswap import` reads accounts back:

```
$ tycswap import backup.tycswap
Imported alice@example.com → slot 1
Imported bob@example.com → slot 2
Imported key@example.com → slot 3
Imported carol@example.com → slot 5
Done: 4 imported, 0 overwritten, 0 skipped
Note: alice@example.com is your current live login — activate the imported credentials with: tycswap --switch-to 1 --force
```

Import skips a slot already holding a different account unless `--force` is
given, which overwrites it. Import rejects any file marked `encrypted: true`.

### Configure settings

`tycswap config` prints the settings and their values, marking each unchanged one
`(default)`:

```
$ tycswap config
autoswitch.threshold        80
autoswitch.intervalSeconds  60             (default)
autoswitch.codexEnabled     true           (default)
autoswitch.codexThreshold   0              (default)
autoswitch.cooldownSeconds  300            (default)
autoswitch.hysteresisPct    10             (default)
autoswitch.strategy         soonest-reset  (default)
autoswitch.unhealthyTicks   3              (default)
autoswitch.model            Fable
```

`tycswap config set KEY VALUE` validates and stores one setting. An out-of-range
value is rejected and nothing is written:

```
$ tycswap config set autoswitch.threshold 85
autoswitch.threshold = 85

$ tycswap config set autoswitch.threshold 40
Error: autoswitch.threshold must be between 50 and 99.9
```

Each key's type, default, and range are listed in
[`docs/reference.md`](docs/reference.md).

### Upgrade

`tycswap upgrade` (alias `update`) updates the binary in place. When tycswap was
installed with `go install github.com/tyclab/tycswap/cmd/tycswap@…`, it re-runs
that for `@latest`. A binary built from a checkout is never replaced from the
remote; it says so and leaves the update to you:

```
$ tycswap upgrade
tycswap was built from a checkout: git pull && make install
```

When it cannot detect a `go install` layout, it prints the manual command and
the releases URL (`https://github.com/tyclab/tycswap/releases`) instead.

On Windows, `tycswap upgrade` never self-replaces; it prints the upgrade command
for the user to run.

### Move over from claude-swap

tycswap keeps its data in its own store and never reads or writes the store of
claude-swap (or of the fork tycswap came from). To bring an existing store
over, copy it once:

```
$ tycswap migrate --dry-run
Would copy ~/.local/share/claude-swap
  to ~/.local/share/tycswap
  ...
$ tycswap migrate
Copied ~/.local/share/claude-swap
  to ~/.local/share/tycswap
  6 directories, 14 files, 3 symlinks
  ...
  renamed claude-swap.log -> tycswap.log
The old store was only read; remove it yourself once nothing else uses it.
```

`migrate` copies only into an empty tycswap store and refuses otherwise; it
never moves, changes or deletes the old store or its Keychain items, because
another tool may still use them. Until the copy is made, every other command
prints a one-line reminder on standard error. The full contract is in
[`docs/reference.md`](docs/reference.md#tycswap-migrate).

### Remove and purge

`tycswap remove <num|email>` unmanages one account, deleting its stored
credentials and configuration from the backup store. It prompts for
confirmation first; any answer other than `y` cancels. `tycswap disable` (above)
keeps the account but drops it from auto-rotation.

`tycswap purge` removes all tycswap data. It states what it will delete and prompts
for confirmation; any answer other than `y` cancels:

```
$ tycswap purge
This will remove ALL tycswap data from your system:
  - Backup directory: ~/.local/share/tycswap
  - All stored account credential files

Note: This does NOT affect your current Claude Code login.

Are you sure you want to purge all data? [y/N] n
Cancelled
```

### Dashboard

`tycswap web` serves a dashboard in the browser on `127.0.0.1` and opens it:

```
$ tycswap web
Dashboard: http://127.0.0.1:52117/?token=<one-time token>
Press Ctrl-C to stop.
```

*Dashboard* lists the accounts with their 5h, 7d and model windows and every
account action (switch, add the current login, add a token, enable/disable,
alias, move, swap, remove), says when Claude Code is signed in with an
account that is not stored yet, warns when an `ANTHROPIC_*` variable or a
`settings.json` key makes Claude Code ignore the stored login, and shows what
can be updated: a newer tycswap release and a newer Claude Code from the
installer it was set up with, each installed from the page after a
confirmation (the header carries the count on every tab; the check runs at
start and every six hours). *Auto* runs an auto-switch engine in the `web`
process with its *Next best* ranking, quarantine and event log; *Sessions*
lists the running Claude Code sessions (also those started with `tycswap
run`) and can stop one; *Settings* edits every `settings.json` key with its
type, range and default, the same keys as `tycswap config`; *Guide* explains
the tool. The Accounts and Updates cards fold, and the choice is remembered.
Export and import stay on the command line.

Only the browser tab that opens the printed URL can use the page: the token
in it works once, the server listens on loopback only, and every API call
needs the session cookie and a per-launch CSRF token the tab received from
the launch URL alone (a new tab needs a fresh URL). On WSL the browser opens on
the Windows side through `wslview` or an `xdg-open` that translates Linux
paths. The dashboard drives Claude accounts; Codex accounts stay with
`tycswap codex`. See `docs/reference.md`, `tycswap web`, for the API and the
security model.

### Codex (ChatGPT) accounts

tycswap also switches [Codex](https://github.com/openai/codex) accounts, under a
`codex` namespace. Bare `tycswap list` and `tycswap switch` keep meaning Claude, so
no existing command changes.

```bash
tycswap codex list                     # accounts + 5h/weekly usage
tycswap codex list --skip-api          # cached usage only, no network
tycswap codex list --token-status      # token expiry (never prints the token)
tycswap codex list --json              # machine-readable
tycswap codex status                   # the account codex is running as
tycswap codex switch                   # rotate to the next account
tycswap codex switch 2                 # or by number / email / alias
tycswap codex switch --strategy best   # jump to the most quota left
tycswap codex add                      # store the account you are logged in as
tycswap codex login                    # run `codex login`, then store it
tycswap codex alias 2 work
tycswap codex disable 2                # hold it out of auto-rotation
tycswap codex swap 1 2                 # exchange slot numbers
tycswap codex move 3 1                 # assign a slot (swaps if taken)
tycswap codex export ~/codex.json      # back up (contains live tokens)
tycswap codex import ~/codex.json
tycswap codex purge                    # drop tycswap's copies; your login stays
```

Each verb mirrors its Claude counterpart, and each accepts `-h` / `--help` and
`--debug`. `tycswap codex status` prints the same block as `tycswap status`, rendered
by the same code. The `--json` payloads of `list` and `status` carry
`schemaVersion`, a `provider: "codex"` field and the same camelCase usage
encoding as the Claude verbs, with Codex field names (`workspace` rather than
`organizationName`); an error under `--json` is the usual error envelope on
stdout. Their shapes are in [`docs/reference.md`](docs/reference.md).

A listing marks the active account with `*`, shows the workspace tag, the alias
and a `[disabled]` marker, then the usage summary:

```text
$ tycswap codex list
* 1. alice@example.com [personal]  5h 12%  7d 40%
  2. alice@example.com [Example Team] (work)  7d 3%
  3. bob@example.com [personal] [disabled]  http 429
```

Switching prints the new account and, when a codex session is running, names
its PIDs:

```text
$ tycswap codex switch work
Switched to Codex account 2: alice@example.com
codex is running (pid 4242) — restart it for the new account to take effect.
```

**Coming from `codex-auth`?** Its accounts are imported automatically the first
time any `tycswap codex` command runs while tycswap holds no Codex account.
`~/.codex/accounts/` is read, never written, so that tool keeps working. Re-run
the import by hand with `tycswap codex import-codex-auth`.

> [!IMPORTANT]
> Switching rewrites `~/.codex/auth.json`. A codex session that is **already
> running** keeps its old account until it is restarted — tycswap warns and names
> the running PIDs. This applies to automatic switching too: a switch affects
> only the next session started. For switching without a restart, see the
> [`codext`](https://github.com/Loongphy/codext) fork of the Codex CLI.

Codex accounts appear in the dashboard after the Claude ones, each tagged
`⟨codex⟩`. Switch, disable / enable and remove act on the selected row's own
provider; adding an account from the dashboard stays Claude-only. After a Codex
switch the dashboard warns with the PIDs of any codex sessions still running.

`tycswap auto` rotates both providers in one process. With `--once` the Codex tick
runs after the Claude one; in the loop the Codex engine ticks at once and then
every interval. It prints a line only when it switched or failed (every tick
under `--dry-run`), with the same timestamp prefix as the Claude events, or a
JSON line with `"event": "codex"` under `--json`. It uses the Claude
`autoswitch.hysteresisPct`, and two settings tune the rest:

```bash
tycswap config set autoswitch.codexThreshold 85   # 0 = use autoswitch.threshold
tycswap config set autoswitch.codexEnabled false  # leave Codex out of `tycswap auto`
```

A Codex API-key login is never a rotation target either: it reports no usage, so
a threshold has nothing to compare.

The Codex provider is a Go-side extension relative to the Python reference that
`docs/port-spec/` pins; it ports claude-swap PR #252 so that the command
grammar, the on-disk store and the export format stay compatible across the two
implementations. Where the PR disagrees with the rest of tycswap (JSON envelope,
`--debug`, the `tycswap auto` event line, a few messages and safety checks),
tycswap follows its own conventions; [`docs/DESIGN.md`](docs/DESIGN.md)
Amendment A22 lists each deviation.

<details>
<summary>Where Codex data is stored</summary>

Accounts live in tycswap's own store, a sibling of the Claude one, not in
`~/.codex/accounts/`:

| What                       | Where                                             |
|----------------------------|---------------------------------------------------|
| Slot registry (no secrets) | `<backup store>/codex/sequence.json`              |
| Credentials, macOS         | Keychain, service `tycswap-codex`                 |
| Credentials, Linux/Windows | `<backup store>/codex/credentials/`, mode 0600    |
| Usage cache                | `<backup store>/codex/cache/`                     |
| Lock                       | `<backup store>/codex/.lock`                      |

The lock is the Codex store's own, so a Codex switch never waits on a Claude
switch; `export` and `purge` take no lock, like their Claude counterparts,
while `import` and `import-codex-auth` write under it, and every roster write
takes it on its own. The codex CLI's own directory is `$CODEX_HOME`, or
`~/.codex` when it is unset. tycswap writes exactly one file there, `auth.json`,
mode 0600.

Credentials are keyed by account identity rather than slot number, so `swap` and
`move` rewrite only the registry and never move a secret.

`tycswap codex export` writes real OAuth tokens — an export that cannot log in is
not a backup. The file is created private (0600), an existing file is narrowed
to 0600 before it is overwritten, and the document says so in a `warning`
field. Treat it like a password. `tycswap codex purge` refuses to run when the
store root is, or contains, `$CODEX_HOME`, so it can never delete your live
login.

</details>

## Data locations

The backup store is the root of everything tycswap persists.

| Platform    | Active credentials             | Backup store                                        |
|-------------|--------------------------------|-----------------------------------------------------|
| Linux / WSL | file, in Claude Code's store   | `${XDG_DATA_HOME:-~/.local/share}/tycswap/`         |
| macOS       | login Keychain; backups under service `tycswap` (Codex: `tycswap-codex`) | `~/.tycswap/` |
| Windows     | file, in Claude Code's store   | `~/.tycswap/`                                       |

The layout inside the backup store is claude-swap's, unchanged; only the root
is tycswap's own, and the log is named `tycswap.log`. tycswap never uses an old
claude-swap store in place; `tycswap migrate` copies one in once.

Inside the backup store:

| Path                      | Contents                                                       |
|---------------------------|---------------------------------------------------------------|
| `sequence.json`           | The account roster: slot, email, alias, kind, disabled state. |
| `credentials/`            | Each account's stored credential blob (`.enc`).               |
| `configs/`                | Each account's configuration snapshot.                        |
| `settings.json`           | Auto-switch settings (`tycswap config`).                        |
| `mappings.json`           | Directory-to-account mappings (`tycswap map`).                  |
| `cache/usage.json`        | Cached usage fetches.                                         |
| `cache/update_check.json` | Cached update-check result.                                  |
| `sessions/`               | Session profiles for `tycswap run` and `tycswap env`.            |
| `tycswap.log`             | The switch log; rotates at 1 MB, keeping 3 backups.          |
| `codex/`                  | The Codex store; see Codex accounts under Tasks.             |

The full per-command contract — arguments, defaults, exit codes, JSON schemas,
environment variables, and error conditions — is in
[`docs/reference.md`](docs/reference.md).

## NOTES

**Restart timing.** File-based platforms (Linux, WSL, Windows) apply a switch on
Claude Code's next message with no restart. macOS reads credentials from the
Keychain and a running session may take up to about 30 seconds to notice a
switch; restarting Claude Code applies it at once.

**Same-account drift.** tycswap and Claude Code share the same credential store.
While an account is active, Claude Code may refresh its own token; tycswap folds
such refreshes back into the account's backup on the next operation, so the
backup does not go stale against the live login.

**API-key accounts.** An account authenticated by API key has no measured usage
quota. It shows `API key (no quota)` in the list, never carries usage
percentages, and is never a target for auto-switch or the rotation: moving onto
it changes how Claude Code authenticates, which a running session does not pick
up. Switch to one by hand, confirm the prompt, then restart your sessions.

**macOS Keychain.** On macOS, active and session-profile credentials live in the
login Keychain. The first credential read or write in a session may prompt for
Keychain access. `tycswap purge` deletes the session-profile Keychain entries
along with the backup store.

## License

MIT. Behavioral design and CLI surface derived from claude-swap, © Onur
Cetinkol, MIT.
</content>
</invoke>
