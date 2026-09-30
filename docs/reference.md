# cswap — Command Reference

## NAME

cswap — multi-account switcher for Claude Code.

## SYNOPSIS

```
cswap                                       open the interactive dashboard (TTY only)
cswap help                                  print the command list and options
cswap list [--json] [--token-status]        list managed accounts
cswap status [--json]                       show the active account
cswap switch [--json]                       rotate to the next account
cswap switch <NUM|EMAIL|ALIAS> [--json] [--force]
                                            switch to a specific account
cswap switch --strategy {best|next-available} [--model NAMES] [--json]
                                            pick the target by remaining quota
cswap add [--slot NUM] [--alias NAME]       snapshot the current login as an account
cswap add-token [TOKEN|-] [--email EMAIL] [--slot NUM]
                                            register a setup-token or API key
cswap remove <NUM|EMAIL|ALIAS>              remove an account (prompts to confirm)
cswap disable <NUM|EMAIL|ALIAS>             hold an account out of auto-rotation
cswap enable <NUM|EMAIL|ALIAS>              return a disabled account to rotation
cswap alias [<NUM|EMAIL|ALIAS> <NAME>]      set / list account aliases
cswap alias <NUM|EMAIL|ALIAS> --unset       remove an account's alias
cswap move <NUM|EMAIL|ALIAS> <SLOT>         assign an account to a slot (swaps if taken)
cswap swap <A> <B>                          exchange two accounts' slot numbers
cswap run [<NUM|EMAIL|ALIAS>] [--no-share] [--share-history|--no-share-history] [-- ARGS...]
                                            launch Claude Code as an account, this terminal only
cswap env [<NUM|EMAIL|ALIAS>] [--no-share] [--share-history] [--shell {sh|fish|pwsh}] [--unset]
                                            print eval-able env lines that pin this shell
cswap map [<NUM|EMAIL|ALIAS> [PATH]]        map a directory to an account / list mappings
cswap unmap [PATH]                          remove a directory mapping
cswap auto [--once] [--json] [--interval SECONDS] [--threshold PCT] [--cooldown SECONDS]
           [--model NAMES] [--include-api-key-accounts|--no-include-api-key-accounts]
           [--dry-run]                      auto-switch when nearing rate limits
cswap config [list] [--json]                show settings
cswap config get <KEY> [--json]             read one setting
cswap config set <KEY> <VALUE>              change one setting
cswap config unset <KEY>                    clear one setting
cswap config path                           print the settings.json path
cswap export <PATH> [--account NUM|EMAIL] [--full]
                                            export accounts to a file ("-" = stdout)
cswap import <PATH> [--force]               import accounts from a file ("-" = stdin)
cswap tui                                    interactive dashboard
cswap watch                                  interactive dashboard, live watch page
cswap menubar                                macOS menu bar app (not available in this build)
cswap upgrade                                self-upgrade to the latest release
cswap purge                                  remove all claude-swap data (prompts to confirm)
cswap --version                              print the program name and version
cswap codex list [--json] [--skip-api] [--token-status]
                                            list managed Codex accounts and their usage
cswap codex status [--json]                 show the account the codex CLI is using
cswap codex switch [<NUM|EMAIL|ALIAS>] [--strategy best]
                                            rotate, or activate a Codex account
cswap codex add [--alias NAME]              store the current Codex login
cswap codex login [--device-auth] [--alias NAME]
                                            run 'codex login', then store the result
cswap codex remove <NUM|EMAIL|ALIAS> [-y]   forget a Codex account
cswap codex alias <NUM|EMAIL> [NAME] [--unset]
                                            set / clear a Codex alias
cswap codex disable|enable <NUM|EMAIL|ALIAS>
                                            hold out of / return to auto-rotation
cswap codex swap <A> <B>                    exchange two Codex slot numbers
cswap codex move <NUM|EMAIL|ALIAS> <SLOT>   assign a Codex account to a slot
cswap codex export <PATH> [--account NUM|EMAIL]
                                            export Codex accounts ("-" = stdout)
cswap codex import <PATH> [--force]         import Codex accounts ("-" = stdin)
cswap codex purge [-y]                      remove all cswap Codex data
cswap codex import-codex-auth               re-run the codex-auth registry import
```

Every subcommand also accepts `--debug` (enable debug logging) and `-h` / `--help`,
the `cswap codex` namespace and each of its verbs included.

Legacy flag spellings are equivalent to the verbs and remain supported:
`--list`, `--status`, `--switch`, `--switch-to <id>`, `--add-account`,
`--add-token [tok]`, `--remove-account <id>`, `--disable-account <id>`,
`--enable-account <id>`, `--export <path>`, `--import <path>`, `--tui`,
`--watch`, `--menubar`, `--upgrade`, `--purge`. `ls` is an alias for `list`,
`rm` for `remove`, `update` for `upgrade`. A verb, or the leading legacy flag,
must be the first argument; `cswap --debug run 2` is not accepted (place the
verb first).

## Account resolution: NUM | EMAIL | ALIAS

Commands that name an account accept any of three forms, resolved in this order:

- **NUM** — the account's slot number (`2`). Slot numbers are stable and need
  not be contiguous.
- **EMAIL** — the account's exact recorded email address (`bob@example.com`).
- **ALIAS** — a short display name previously set with `cswap alias`
  (`dev`). An all-numeric string is never treated as an alias; it resolves as
  a slot number.

A form that matches no account raises `AccountNotFoundError`
(`No account found with identifier: <id>`), exit status 1.

---

## cswap list

### Synopsis

```
cswap list [--json] [--token-status] [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--json` | flag | off | Mutually exclusive with `--token-status`. |
| `--token-status` | flag | off | Cannot combine with `--json`. |
| `--debug` | flag | off | — |

### Description

Lists every managed account in slot order with its usage summary. For each
account `list` shows the slot number, email (and alias when set), the
organization tag (`[personal]` or the organization name), and status markers:
`(active)` for the current default login, `(disabled)` for an account held out
of auto-rotation, and `at limit: <windows>` — the comma-separated labels of the
limiting windows (for example `5h`, `7d`, or a per-model name such as
`Fable 5`) — when a relevant rate-limit window is at or over its limit.
Accounts registered from an API key show `API key (no quota)`. `--token-status` adds an OAuth token expiry line per account.
When usage cannot be fetched the row reads `usage unavailable (<reason>)` where
`<reason>` is the last fetch error (for example `http-429`, `http-401`).

### Files

Reads `sequence.json`, per-account `configs/` and `credentials/`, and
`cache/usage.json` from the backup root. Writes nothing.

### Exit status

`0` on success. `1` on a handled error. `2` on a usage error (for example
`--token-status` combined with `--json`).

### Output

Human output is a titled `Accounts:` block, one stanza per account.

With `--json`, exactly one indented JSON document is written to stdout:

```
{
  "schemaVersion": 1,
  "activeAccountNumber": <int|null>,
  "accounts": [ <account-row>, ... ]
}
```

An `<account-row>` object contains these keys.

Always present:

| Key | Type | Meaning |
|-----|------|---------|
| `number` | int | Slot number. |
| `email` | string | Recorded email. |
| `organizationName` | string | Organization name, or `""`. |
| `organizationUuid` | string | Organization UUID, or `""`. |
| `isOrganization` | bool | `true` when `organizationUuid` is non-empty. |
| `active` | bool | `true` for the current default login. |
| `usageStatus` | string | One of the seven values below. |
| `usage` | object \| null | Usage windows when `usageStatus` is `ok`; otherwise `null`. |

Present only when applicable (additive, schemaVersion 1):

| Key | Type | Present when |
|-----|------|--------------|
| `alias` | string | An alias is set. |
| `disabled` | `true` | The account is disabled. |
| `atLimit` | `true` | A relevant window is at/over its limit. |
| `limitingWindows` | array of string | Emitted with `atLimit`; the windows at/over limit, in relevant-window order. |
| `usageFetchedAt` | string (ISO-8601 UTC) | `usage` is non-null and the measurement time is known. |
| `usageAgeSeconds` | number | Emitted with `usageFetchedAt`. |

The `usageStatus` enumeration:

| Value | Meaning |
|-------|---------|
| `ok` | Usage was fetched; `usage` is populated. |
| `token_expired` | The OAuth token is expired. |
| `api_key` | The account authenticates with an API key (no OAuth quota). |
| `keychain_unavailable` | The macOS Keychain could not be read. |
| `relogin_required` | The account must be logged in again. |
| `no_credentials` | No stored credentials. |
| `unavailable` | Usage could not be determined for any other reason. |

The `usage` object (present when `usageStatus` is `ok`) holds these keys, each
present only when its data exists in the source: `fiveHour` and `sevenDay`
(each `{pct, resetsAt?, countdown?, clock?}`), `spend`
(`{used, limit, pct, currency, resetsAt?, countdown?, clock?}`), and `scoped`
(an array of per-model weekly windows `{name, pct, resetsAt?, countdown?, clock?}`).
`pct` is a number; `resetsAt` is an ISO-8601 string; `countdown` and `clock`
are human strings recomputed at serialization time.

### Errors

| Message (exit 2) | Condition |
|------------------|-----------|
| `--token-status cannot be combined with --json` | Both flags given. |
| `--json can only be used with 'list', 'status', or 'switch'` | `--json` on a command that does not emit it (caught by the parser, not `list`). |

### Example

```
$ cswap list
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

### See also

`cswap status`, `cswap switch`, [JSON OUTPUT CONTRACT](#json-output-contract).

---

## cswap status

### Synopsis

```
cswap status [--json] [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--json` | flag | off | — |
| `--debug` | flag | off | — |

### Description

Reports the current default login (the account Claude Code will use) and the
number of managed accounts. When the live account is not one cswap manages, the
active record is reported as unmanaged.

### Files

Reads `sequence.json`, the live `~/.claude.json` (or a custom `CLAUDE_CONFIG_DIR`
equivalent; a cswap session pin is ignored — see ENVIRONMENT), and
`cache/usage.json`. Writes nothing.

### Exit status

`0` on success; `1` on a handled error.

### Output

Human output is a `Status:` line plus a managed-account count and a usage
summary. With `--json`:

```
{
  "schemaVersion": 1,
  "totalManagedAccounts": <int>,
  "active": <active-object>
}
```

The `<active-object>` always contains: `number` (int, or `null` for an
unmanaged live account), `email`, `organizationName`, `organizationUuid`,
`isOrganization`, `managed` (bool), `usageStatus` (same enum as `list`), and
`usage` (object or `null`). The same additive keys `list` emits per row —
`alias`, `atLimit`, `limitingWindows`, `usageFetchedAt`, `usageAgeSeconds` —
appear here under the same conditions.

### Errors

Handled errors (exit 1) surface as `Error: <message>` on stderr, or as the JSON
error envelope on stdout under `--json`.

### Example

```
$ cswap status
Status: Account-1 (alice@example.com [personal])
  Total managed accounts: 4
  usage unavailable (http-429)
```

```
$ cswap status --json
{
  "active": {
    "email": "alice@example.com",
    "isOrganization": false,
    "managed": true,
    "number": 1,
    "organizationName": "",
    "organizationUuid": "",
    "usage": null,
    "usageStatus": "unavailable"
  },
  "schemaVersion": 1,
  "totalManagedAccounts": 4
}
```

To extract the active account's email in a script:

```
$ cswap status --json | jq -r '.active.email'
alice@example.com
```

### See also

`cswap list`, `cswap switch`.

---

## cswap switch

### Synopsis

```
cswap switch [--json] [--debug]
cswap switch <NUM|EMAIL|ALIAS> [--json] [--force] [--debug]
cswap switch --strategy {best|next-available} [--model NAMES] [--json] [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--json` | flag | off | — |
| `--force` | flag | off | Only with `switch <NUM\|EMAIL\|ALIAS>`; activates without backing up the current login first. |
| `--strategy` | choice `best` \| `next-available` | unset | Only with bare `switch` (no target). |
| `--model` | string (comma-separated names) | unset | Only with `switch --strategy`. |
| `--debug` | flag | off | — |

### Description

Bare `cswap switch` rotates to the next account in the sequence. `cswap switch
<NUM|EMAIL|ALIAS>` activates a specific account. `--strategy best` picks the
account with the most remaining 5h/7d quota; `--strategy next-available`
rotates, skipping rate-limited accounts. `--model` adds the named models'
per-model weekly limits to the quota computation for the usage-aware
strategies. A switch never requires restarting Claude Code to be correct; the
post-switch note is informational (see NOTES). The switch cooperates with
Claude Code's own credential lock, so it never interleaves with a token
refresh.

### Files

Reads and writes `sequence.json` (the active pointer), the live Claude Code
config/credentials, and per-account `configs/`/`credentials/`. Acquires the
backup-root and Claude Code credential locks.

### Exit status

`0` on success (including a no-op "already active"); `1` on a handled error;
`2` on a usage error (for example `--strategy` without bare `switch`).

### Output

Human output is `Switched to Account-<n> (<email>)` (or `Already on ...`), then
a refreshed account list and the post-switch note. With `--json`:

```
{
  "schemaVersion": 1,
  "switched": <bool>,
  "from": <ref|null>,
  "to": <ref>,
  "strategy": "<string>",
  "reason": "<string>",
  "message": "<string>",
  "warnings": [ <string>, ... ]
}
```

A `<ref>` is `{"number": <int|null>, "email": "<string>"}`. `switched` is
`true` when `from` and `to` differ. `strategy` is `direct` for a named target,
`rotation` for a bare rotate, or `best` / `next-available`. `reason` is one of
`switched`, `already-active`, `activated` (with `--force`), `unmanaged-account`,
`only-one-account`, `candidates-exhausted`, `no-valid-target`,
`usage-unavailable`, or `already-best`. A bare `switch --strategy` with
`--model` additionally emits `models` (array of string) and `modelSource`
(`cli` or `autoswitch.model`).

### Errors

| Message | Type / exit |
|---------|-------------|
| `No account found with identifier: <id>` | `AccountNotFoundError`, exit 1 |
| `argument --strategy: invalid choice: '<v>' (choose from 'best', 'next-available')` | usage, exit 2 |
| `--strategy can only be used with bare 'switch'` | usage, exit 2 |
| `--model can only be used with 'switch --strategy best' or 'switch --strategy next-available'` | usage, exit 2 |
| `--force can only be used with 'import' or 'switch <num\|email>'` | usage, exit 2 |

### Example

```
$ cswap switch 2 --json
{
  "from": {
    "email": "alice@example.com",
    "number": 1
  },
  "message": "Switched to Account-2 (bob@example.com)",
  "reason": "switched",
  "schemaVersion": 1,
  "strategy": "direct",
  "switched": true,
  "to": {
    "email": "bob@example.com",
    "number": 2
  },
  "warnings": []
}
```

### See also

`cswap auto`, `cswap list`, `cswap status`, [SETTINGS](#settings).

---

## cswap add

### Synopsis

```
cswap add [--slot NUM] [--alias NAME] [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--slot` | int | next free slot | Only with `add` or `add-token`. |
| `--alias` | string | none | Only with `add`. Letters/digits/`.`/`-`/`_`; not all-numeric. |
| `--debug` | flag | off | — |

### Description

Snapshots the account currently logged into Claude Code as a new managed
account, copying its credentials and `oauthAccount` config into the backup
root. `--slot` places it in a specific slot (swapping if occupied); `--alias`
sets a short display name at the same time.

When the current login's identity `(email, organizationUuid)` already belongs to
a managed account and no `--slot` is given, `add` refreshes that account's
stored credentials and config in place rather than allocating a second slot, and
reports `Updated credentials for Account <n> (<email> [<tag>]).`. This is the
supported way to repair an account whose refresh token has died (`usageStatus:
relogin_required`): log in with the account in Claude Code, then re-run
`cswap add`. The refresh also clears any `cswap auto` quarantine on that slot.

### Files

Reads the live `~/.claude.json` and credentials store. Writes `sequence.json`,
`configs/`, and `credentials/` (or the macOS Keychain).

### Exit status

`0` on success; `1` on a handled error (for example no current login, or an
invalid alias).

### Output

`Added Account <n>: <email> [<tag>]` on a new account, or `Updated credentials
for Account <n> (<email> [<tag>]).` when refreshing an already-managed identity
in place.

### Errors

An invalid alias raises `ConfigError` (exit 1). A missing or unreadable Claude
config raises `ConfigError` (`Claude config file not found`,
`Permission denied reading Claude config`, or a wrapped read error).

### Example (illustrative — requires a current Claude Code login to snapshot)

```
$ cswap add --slot 3 --alias work
Added Account 3: me@example.com [personal]
```

### See also

`cswap add-token`, `cswap alias`, `cswap remove`.

---

## cswap add-token

### Synopsis

```
cswap add-token [TOKEN|-] [--email EMAIL] [--slot NUM] [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `TOKEN` | positional string | read interactively if omitted | A lone `-` reads the token from stdin. |
| `--email` | string | derived if possible | Only with `add-token`. |
| `--slot` | int | next free slot | Only with `add` or `add-token`. |
| `--debug` | flag | off | — |

### Description

Registers an account from a setup token (`sk-ant-oat01-...`) or an API key
(`sk-ant-api03-...`) without an interactive Claude Code login. API-key accounts
are marked `kind: api_key` and carry no OAuth quota.

### Files

Writes `sequence.json`, `configs/`, and `credentials/` (or the macOS Keychain).

### Exit status

`0` on success; `1` on a handled error.

### Output

`Added Account <n>: <email> [<tag>] (from token)`.

### Errors

Handled errors surface as `Error: <message>` (exit 1).

### Example (illustrative token)

```
$ cswap add-token sk-ant-oat01-EXAMPLE --email me@example.com
Added Account 6: me@example.com [personal] (from token)
```

### See also

`cswap add`, `cswap remove`.

---

## cswap remove

### Synopsis

```
cswap remove <NUM|EMAIL|ALIAS> [--debug]
```

Legacy: `cswap --remove-account <id>`. Alias: `cswap rm <id>`.

### Options

Only `--debug`. The account identifier is required.

### Description

Permanently removes a managed account: its `sequence.json` entry, stored
config, and stored credentials. Prompts for confirmation on an interactive
terminal (`[y/N]`); any answer other than `y` cancels.

### Files

Removes the account's `configs/` and `credentials/` entries and its
`sequence.json` record.

### Exit status

`0` on success or a cancelled prompt; `1` on a handled error.

### Output

`Removed Account-<n> (<email>).` on success; `Cancelled` when declined.

### Errors

`No account found with identifier: <id>` (`AccountNotFoundError`, exit 1).

### Example

```
$ cswap remove 5
Are you sure you want to permanently remove Account-5 (carol@example.com)? [y/N] Cancelled
```

### See also

`cswap disable`, `cswap add`.

---

## cswap disable

### Synopsis

```
cswap disable <NUM|EMAIL|ALIAS> [--debug]
```

Legacy: `cswap --disable-account <id>`.

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `NUM\|EMAIL\|ALIAS` | positional | — | Required. The account to disable. |
| `--debug` | flag | off | — |

### Description

Holds an account out of auto-rotation. `cswap auto` and the usage-aware `switch`
strategies skip a disabled account; a direct `switch <id>` still activates it.
The account and its credentials are retained.

### Files

Writes the `disabled` flag on the account's `sequence.json` record.

### Exit status

`0` on success; `1` on a handled error.

### Output

`Disabled Account-<n> (<email>).` When the disabled account is also the active
login, a second line follows: `  It is the active account — it stays live until
you switch away; it just won't be an automatic switch target.`

### Errors

`No account found with identifier: <id>` (`AccountNotFoundError`, exit 1).

### Example

```
$ cswap disable 1
Disabled Account-1 (alice@example.com).
  It is the active account — it stays live until you switch away; it just won't be an automatic switch target.
```

### See also

`cswap enable`, `cswap auto`.

---

## cswap enable

### Synopsis

```
cswap enable <NUM|EMAIL|ALIAS> [--debug]
```

Legacy: `cswap --enable-account <id>`.

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `NUM\|EMAIL\|ALIAS` | positional | — | Required. The account to enable. |
| `--debug` | flag | off | — |

### Description

Returns a disabled account to auto-rotation, clearing its `disabled` flag.

### Files

Clears the `disabled` flag on the account's `sequence.json` record.

### Exit status

`0` on success; `1` on a handled error.

### Output

For an account that was disabled:

```
Enabled Account-<n> (<email>).
  It is back in the rotation.
```

For an account that is already enabled:
`Account-<n> (<email>) is already enabled.`

### Errors

`No account found with identifier: <id>` (`AccountNotFoundError`, exit 1).

### Example

```
$ cswap enable 5
Enabled Account-5 (carol@example.com).
  It is back in the rotation.
```

### See also

`cswap disable`, `cswap auto`.

---

## cswap alias

### Synopsis

```
cswap alias                                 list all aliases
cswap alias <NUM|EMAIL|ALIAS> <NAME>        set an alias
cswap alias <NUM|EMAIL|ALIAS> --unset       remove an alias
cswap alias [...] [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--unset` | flag | off | Requires an account; takes no `NAME`. |
| `--debug` | flag | off | — |

### Description

With no arguments, lists every account that has an alias. With an account and a
name, sets the alias. With an account and `--unset`, removes it. A valid alias
is letters, digits, `.`, `-`, or `_`, and is not purely numeric (which would be
ambiguous with a slot number).

### Files

Writes the `alias` field on the account's `sequence.json` record.

### Exit status

`0` on success; `1` on a handled error (for example an invalid alias name);
`2` on a usage error.

### Output

- List: `Aliases:` followed by `  <n>: <alias> (<email>)` lines, or
  `No aliases set`.
- Set: `Set alias '<alias>' for Account <n>`.
- Unset: `Removed alias for Account <n>`.

### Errors

| Message | Exit |
|---------|------|
| `--unset does not take a NAME argument` | 2 |
| `NUM\|EMAIL is required with --unset` | 2 |
| `NAME is required (or pass --unset to remove the alias)` | 2 |
| `unrecognized arguments: <tok>` | 2 |
| Invalid alias name (`ConfigError`) | 1 |
| Corrupt or unreadable `sequence.json` (`ConfigError`, names the file and the repair/restart options) | 1 |

A corrupt roster refuses rather than reporting `No aliases set` (list) or
silently writing over it (set/unset): the roster's records may still be
hand-repairable, and every account's stored credential and config backup is
intact even though the roster naming it is not.

### Example

```
$ cswap alias
Aliases:
  2: dev (bob@example.com)
```

### See also

`cswap add`, [Account resolution](#account-resolution-num--email--alias).

---

## cswap move

### Synopsis

```
cswap move <NUM|EMAIL|ALIAS> <SLOT> [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `NUM\|EMAIL\|ALIAS` | positional | — | Required. The account to move. |
| `SLOT` | positional int | — | Required. The destination slot; a positive integer. |
| `--debug` | flag | off | — |

### Description

Assigns an account to a specific slot number. If the target slot is occupied by
another account, the two accounts exchange slots. If the account is already in
the target slot, nothing changes.

### Files

Rewrites the affected `sequence.json` records and renames the per-account
`configs/`, `credentials/`, and `sessions/` entries to match the new slot
numbers.

### Exit status

`0` on success; `1` on a handled error; `2` on a usage error (missing
argument).

### Output

- Moved onto a free slot: `Moved <email> to slot <n>`.
- Swapped with an occupant: `Swapped Account <a> and Account <b>:` then a
  numbered two-line list.
- Already there: `Already in slot <n>: <email>`.

### Errors

| Message | Exit |
|---------|------|
| `the following arguments are required: NUM\|EMAIL\|ALIAS, SLOT` (or `SLOT`) | 2 |
| `unrecognized arguments: <tok>` | 2 |
| `No account found with identifier: <id>` | 1 |
| `Target slot must be a positive slot number, got: '<v>' (use \`swap\` to trade two accounts by identifier)` | 1 |

### Example

```
$ cswap move 1 3
Swapped Account 1 and Account 3:
  1: key@example.com
  3: alice@example.com
```

### See also

`cswap swap`.

---

## cswap swap

### Synopsis

```
cswap swap <A> <B> [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `A` | positional `NUM\|EMAIL\|ALIAS` | — | Required. The first account. |
| `B` | positional `NUM\|EMAIL\|ALIAS` | — | Required. The second account. |
| `--debug` | flag | off | — |

### Description

Exchanges the slot numbers of two accounts. Both accounts must exist.

### Files

Rewrites the two `sequence.json` records and renames the corresponding
`configs/`, `credentials/`, and `sessions/` entries.

### Exit status

`0` on success; `1` on a handled error; `2` on a usage error.

### Output

`Swapped Account <a> and Account <b>:` followed by two `  <n>: <email>` lines
in numeric order.

### Errors

| Message | Exit |
|---------|------|
| `the following arguments are required: NUM\|EMAIL\|ALIAS` | 2 |
| `unrecognized arguments: <tok>` | 2 |
| `No account found with identifier: <id>` | 1 |

### Example

```
$ cswap swap 1 2
Swapped Account 1 and Account 2:
  1: bob@example.com
  2: alice@example.com
```

### See also

`cswap move`.

---

## cswap run

### Synopsis

```
cswap run [<NUM|EMAIL|ALIAS>] [--no-share] [--share-history|--no-share-history] [--debug] [-- ARGS...]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--no-share` | flag | off (session shares the default profile's MCP/config) | — |
| `--share-history` | flag | off | Mutually last-wins with `--no-share-history`. |
| `--no-share-history` | flag | on (history not shared) | — |
| `--debug` | flag | off | — |
| `-- ARGS...` | passthrough | — | Everything after the first `--` is forwarded to `claude` verbatim. |

### Description

Launches Claude Code as a stored account in the current terminal only, without
changing the machine-wide default login. It prepares the account's persistent
session profile (bootstrap, token validate, MCP mirror, credential scrub) and
then execs `claude` with `CLAUDE_CONFIG_DIR` pointed at that profile. With no
account argument, the account is resolved from the current directory's mapping
(see `cswap map`); if the directory has no usable mapping, `claude` launches as
the default login. This is an experimental feature.

### Files

Reads `sequence.json`, `mappings.json`, per-account `configs/`/`credentials/`.
Creates and maintains the account's `sessions/<n>-<email>` profile directory.

### Exit status

The exit status of the launched `claude` process. `1` on a pre-exec failure;
`2` on a usage error. On POSIX the process is replaced by `claude`, so cswap
itself does not return on success.

### Output

Preparation notices (`Launching Account-<n> (<email>) [session mode]`, warnings)
are printed before the handoff. A directory with no mapping prints
`No account mapped for <dir> — launching the default account.`; a mapping to a
removed account prints a warning and launches the default.

### Errors

| Message | Exit |
|---------|------|
| `unrecognized arguments: <tok>` | 2 |
| A `SessionError` / bootstrap failure (`Error: <message>`) | 1 |
| Corrupt or unreadable `sequence.json` (`ConfigError`, names the file and the repair/restart options) | 1 |

An API-key account cannot be run in session mode and is rejected.

Account resolution — an explicit account, or the current directory's
mapping — refuses on a corrupt roster; with no account argument this means
`run` refuses rather than falling back to the default login.

### Example (illustrative — requires a real Claude Code install)

```
$ cswap run 2 -- --resume
Launching Account-2 (bob@example.com) [session mode]
# ... claude starts as account 2 with --resume ...
```

### See also

`cswap env`, `cswap map`.

---

## cswap env

### Synopsis

```
cswap env [<NUM|EMAIL|ALIAS>] [--no-share] [--share-history] [--shell {sh|fish|pwsh}] [--unset] [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--shell` | choice `sh` \| `fish` \| `pwsh` | `sh` | Selects the output syntax. |
| `--no-share` | flag | off | — |
| `--share-history` | flag | off | — |
| `--unset` | flag | off | Takes no account argument; skips bootstrap. |
| `--debug` | flag | off | — |

### Description

Prints shell-evalable lines that pin the current shell to a stored account's
session profile without launching `claude`. It prepares the same profile
`cswap run` does (bootstrap / validate / mirror / `--no-share` /
`--share-history` all apply), then prints a `CLAUDE_CONFIG_DIR` export instead
of exec'ing. Intended for `eval "$(cswap env 2)"`. `--unset` prints only the
`CLAUDE_CONFIG_DIR` unset line for the chosen shell, dropping the pin. With no
account, the account is resolved from the current directory's mapping; unlike
`run`, `env` has no default-login fallback and errors when nothing is mapped.

When the chosen account is already the active default login and
`CLAUDE_CONFIG_DIR` is not already set, `cswap env` does nothing: it prepares no
profile, creates no second credential copy, and exports nothing. It writes one
note to stderr and exits 0, so `eval "$(cswap env <n>)"` is a safe no-op. When
`CLAUDE_CONFIG_DIR` is already set, the preset-override behavior applies instead:
the profile is prepared and exported for this shell, overriding the preset,
identical to `cswap run`.

Output discipline: stdout carries only the eval-able lines. Before the export,
one unset line is emitted for each currently-set authentication-override
variable (see ENVIRONMENT) so it cannot shadow the pinned account. All notices
and warnings go to stderr.

### Files

Same as `cswap run`: reads the registry and mappings, prepares the account's
`sessions/<n>-<email>` profile.

### Exit status

`0` on success; `1` on a handled error (including "nothing to prepare"); `2` on
a usage error.

### Output

The eval stream, per shell:

| Shell | Export | Unset |
|-------|--------|-------|
| `sh` | `export CLAUDE_CONFIG_DIR='<dir>'` | `unset CLAUDE_CONFIG_DIR` |
| `fish` | `set -gx CLAUDE_CONFIG_DIR '<dir>'` | `set -e CLAUDE_CONFIG_DIR` |
| `pwsh` | `$env:CLAUDE_CONFIG_DIR = '<dir>'` | `Remove-Item Env:CLAUDE_CONFIG_DIR -ErrorAction SilentlyContinue` |

When the chosen account is already the active default login and no
`CLAUDE_CONFIG_DIR` preset is set, stdout is empty; only the stderr note is
written (see Description).

### Errors

| Message | Exit |
|---------|------|
| `argument --shell: invalid choice: '<v>' (choose from sh, fish, pwsh)` | 2 |
| `--unset does not take a NUM\|EMAIL\|ALIAS argument` | 2 |
| `argument --shell: expected one argument` | 2 |
| `unrecognized arguments: <tok>` | 2 |
| `Nothing to prepare an environment for (...). Pass an account ..., map this directory ..., or clear a pinned profile with cswap env --unset.` | 1 |
| Corrupt or unreadable `sequence.json` (`ConfigError`, names the file and the repair/restart options) | 1 |

Account resolution (an explicit `NUM\|EMAIL\|ALIAS`, or the current
directory's mapping) refuses on a corrupt roster rather than reporting that
no such account exists.

### Example

```
$ cswap env 2
Prepared Account-2 (bob@example.com) [session mode]      # (stderr)
export CLAUDE_CONFIG_DIR='~/.local/share/claude-swap/sessions/2-bob_example.com'
$ cswap env 1                                            # already the active default login
Account-1 (alice@example.com) is the active default login — an unpinned shell already uses it; nothing exported.      # (stderr; exit 0, nothing exported)
$ cswap env --unset
unset CLAUDE_CONFIG_DIR
$ cswap env --unset --shell fish
set -e CLAUDE_CONFIG_DIR
```

`cswap env` has no counterpart in claude-swap (Python); it is defined by this
implementation.

### See also

`cswap run`, `cswap map`, ENVIRONMENT.

---

## cswap map

### Synopsis

```
cswap map                                   list directory mappings
cswap map <NUM|EMAIL|ALIAS> [PATH]          map a directory to an account
cswap map [...] [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `NUM\|EMAIL\|ALIAS` | positional | — | Optional. Omit (with no `PATH`) to list mappings; supply it to map a directory. |
| `PATH` | positional string | the current directory | The directory to map. A non-existent directory is mapped with a warning. |
| `--debug` | flag | off | — |

### Description

With no arguments, lists directory→account mappings. With an account and an
optional path (defaulting to the current directory), records that a bare
`cswap run` / `cswap env` in that directory resolves to the given account. A
path that is not an existing directory is mapped anyway with a warning.

### Files

Reads and writes `mappings.json` in the backup root.

### Exit status

`0` on success; `1` on a handled error; `2` on a usage error.

### Output

- List: `Directory mappings:` and one `  <path> → <n>: <email> [<tag>]` line
  per mapping (or `... (account removed)` for a dangling mapping); or the empty
  notice `No directory mappings yet.` plus a hint.
- Set: `Mapped <path> → Account-<n> (<email>)`, with `(was <prev-email>)`
  appended when the path was previously mapped to a different account.

### Errors

| Message | Exit |
|---------|------|
| `unrecognized arguments: <tok>` | 2 |
| `No account found with identifier: <id>` | 1 |
| Corrupt or unreadable `sequence.json`, given an account argument (`ConfigError`, names the file and the repair/restart options) | 1 |

A non-directory path prints
`Warning: <path> is not an existing directory (mapping it anyway)` on stdout
and proceeds.

The corrupt-roster refusal applies only when an account is given: account
resolution refuses rather than reporting "no such account." The bare,
no-argument listing does not detect a corrupt roster at all and exits 0 —
every mapping prints as `... (account removed)`, whether or not the account
it names still exists, because the listing cannot tell corruption from
absence.

### Example

```
$ cswap map
Directory mappings:
  ~/work/client-app → 2: bob@example.com [personal]
```

### See also

`cswap unmap`, `cswap run`, `cswap env`.

---

## cswap unmap

### Synopsis

```
cswap unmap [PATH] [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `PATH` | positional string | the current directory | The directory whose mapping is removed. |
| `--debug` | flag | off | — |

### Description

Removes the mapping for a directory (defaulting to the current directory).

### Files

Reads and writes `mappings.json`.

### Exit status

`0` on success (including "no mapping present"); `2` on a usage error.

### Output

`Unmapped <path>` when a mapping was removed, else `No mapping for <path>`.

### Errors

`unrecognized arguments: <tok>` (exit 2).

### Example

```
$ cswap unmap ~/work/client-app
Unmapped ~/work/client-app
```

### See also

`cswap map`.

---

## cswap auto

### Synopsis

```
cswap auto [--once] [--json] [--interval SECONDS] [--threshold PCT] [--cooldown SECONDS]
           [--model NAMES] [--include-api-key-accounts|--no-include-api-key-accounts]
           [--dry-run] [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--once` | flag | off (loop) | Runs a single tick; the exit status is the tick outcome. |
| `--json` | flag | off | Emit JSONL events (compact) instead of human lines. |
| `--interval` | float seconds | `autoswitch.intervalSeconds` (60) | Clamped to 15–3600. |
| `--threshold` | float percent | `autoswitch.threshold` (90) | Clamped to 50–99.9. |
| `--cooldown` | float seconds | `autoswitch.cooldownSeconds` (300) | Clamped to 0–86400. |
| `--model` | string (comma names) | `autoswitch.model` | Adds per-model weekly limits. |
| `--include-api-key-accounts` | flag | `autoswitch.includeApiKeyAccounts` (false) | Allow rotating onto API-key accounts. |
| `--no-include-api-key-accounts` | flag | — | The negated form. |
| `--dry-run` | flag | off | Evaluate and report but never switch. |
| `--debug` | flag | off | — |

Command-line values override the corresponding `settings.json` values for this
run only, then are re-clamped to the ranges above.

### Description

Watches the active account's usage and proactively switches before it reaches
the threshold. In loop mode it polls every interval; in `--once` mode it
evaluates a single tick and exits with the outcome as the status code — the
form intended for cron. Switching proactively (while the old account is still
valid) is what keeps the change safe under the macOS Keychain propagation
latency.

Which accounts qualify as targets is governed by the threshold, hysteresis,
cooldown, and quarantine rules; the order in which qualifying targets are
tried is governed by `autoswitch.strategy` — most headroom first (`best`,
the default) or earliest weekly renewal first (`soonest-reset`). See
[SETTINGS](#settings) for the ordering rules.

**Codex accounts.** When `autoswitch.codexEnabled` is true (the default) and
cswap holds Codex accounts, or an un-imported codex-auth registry exists, `cswap
auto` also runs a Codex engine in the same process. It is a second, small
engine, not a mode of the Claude one: each tick reads every Codex account's
usage and, when the active account's worse window (5h or weekly) is at or above
the Codex threshold, switches to the enabled OAuth account with the lowest
worse window that is below the threshold and at least `autoswitch.hysteresisPct`
below the active account. The Codex threshold is `autoswitch.codexThreshold`,
or the effective `--threshold` / `autoswitch.threshold` when that is 0. An
account with no measurement is never switched away from. Cooldown, quarantine,
`--model`, and `--include-api-key-accounts` apply to Claude only; Codex API-key
accounts are never targets. `--dry-run` applies to both engines.

With `--once`, the Codex tick runs after the Claude tick, and the exit status
is the Claude tick's outcome. In loop mode the Codex engine ticks on its own
goroutine, once at launch and then every interval, so a slow Codex fetch never
delays a Claude switch; a Codex failure never stops the loop.
A Codex switch rewrites `~/.codex/auth.json`, so it affects the next codex
session started, not one already running.

### Files

Reads `sequence.json`, `settings.json`, `cache/usage.json`. Reads and writes
`autoswitch_state.json` (cooldown / quarantine state) under
`.autoswitch_state.lock`. A real switch writes the same files `cswap switch`
does. The Codex engine reads and writes the files `cswap codex switch` does.

### Exit status

Loop mode returns `0` on a clean stop. `--once` returns the tick outcome:

| Code | Outcome |
|------|---------|
| `0` | Switched (or, in `--dry-run`, would have switched). |
| `1` | Error (network trouble, lock contention, transient freshen failure). |
| `2` | No action needed (below threshold, cooldown, idle). |
| `3` | Blocked: a switch was wanted but no viable target exists / all exhausted. |

A construction or configuration error before the first tick exits `1`.

### Output

Human mode prints one timestamped line per event, colored by kind. `--json`
prints one compact JSON object per event on its own line (JSONL). Every event
object carries `schemaVersion` (1), `event` (the kind), `ts` (RFC3339 UTC with
a `Z` suffix), plus per-kind fields:

| `event` | Fields |
|---------|--------|
| `poll` | `active` (`{number,email}` or null), `headroomPct` (`{num: float|null}`), `threshold` (float), `fetchErrors` (`{num: msg}`, optional), `windowsPct` (`{num: {label: float}}`, optional) |
| `switch` | `trigger` (`proactive`\|`at-limit`\|`failover`), `from` (ref), `to` (ref), `warnings` (array), `dryRun` (bool) |
| `no-switch` | `reason` (string), `detail` (string) |
| `account-quarantined` | `number`, `email`, `reason` |
| `account-unquarantined` | `number`, `email`, `reason` |
| `all-exhausted` | `earliestResetAt` (string or null) |
| `sleep` | `seconds` (float), `until` (string) |
| `error` | `message` (string), `transient` (bool) |
| `config-warning` | `message` (string) |

The Codex engine reports a tick only when it switched or failed, or on every
tick under `--dry-run`. Its human line carries the same `HH:MM:SS` prefix as
the Claude events and is coloured the same way (a switch accented, an error in
yellow, anything else dimmed): `<HH:MM:SS>  codex: <detail>` (`codex:
<outcome>` when there is no detail), with ` — restart codex (pid <pids>) for it to take effect`
appended when codex processes are running. `<detail>` is one of `switched <a>
(<pct>%) -> <b> (<pct>%)`, `would switch <a> (<pct>%) -> <b> (<pct>%)`, `account
<n> at <pct>% (below threshold)`, `account <n> at <pct>% and no better
candidate`, `account <n> usage unknown`, `no managed account active`, `no
rotatable accounts`, `snapshot failed (<error>)`, or `switch failed: <message>`.
Under `--json` it is one compact line, `{"schemaVersion": 1, "event": "codex",
"ts": "<RFC3339 UTC, Z suffix>", "outcome": ..., "detail": ..., "switchedTo":
<num>|null, "runningPids": [...]}`; `ts` has the format of the Claude events'
`ts`, and `outcome` is `ok`, `switched`, `blocked`, `no-accounts`, or `error`.

A handled error in `--json` mode is emitted as the compact error envelope
(see JSON OUTPUT CONTRACT) on stdout.

### Errors

| Message | Exit |
|---------|------|
| `argument --interval: invalid float value: '<v>'` (and `--threshold`, `--cooldown`) | 2 |
| `argument --interval: expected one argument` (and the others) | 2 |
| `unrecognized arguments: <tok>` | 2 |
| A `ClaudeSwitchError` before the loop | 1 |

### Example

```
$ cswap auto --once --json
{"active":{"email":"alice@example.com","number":1},"event":"poll","fetchErrors":{"1":"http-429","2":"http-429","5":"http-401"},"headroomPct":{"1":null,"2":null,"3":null,"5":null},"schemaVersion":1,"threshold":80.0,"ts":"2026-07-17T20:52:55Z"}
{"detail":"1/3 before failover","event":"no-switch","reason":"active-usage-unknown","schemaVersion":1,"ts":"2026-07-17T20:52:55Z"}
$ echo $?
2
```

A cron entry that checks every five minutes and appends output to a log:

```
*/5 * * * * cswap auto --once --json >> cswap-auto.log 2>&1
```

cron runs with a minimal `PATH`; the entry finds `cswap` only when its install
directory is on cron's `PATH`. Either set a `PATH=` line at the top of the
crontab that includes that directory, or place `cswap` in a directory cron
already searches.

### See also

`cswap switch`, `cswap codex switch`, [SETTINGS](#settings), [SIGNALS](#signals).

---

## cswap config

### Synopsis

```
cswap config [list] [--json] [--debug]
cswap config get <KEY> [--json] [--debug]
cswap config set <KEY> <VALUE> [--debug]
cswap config unset <KEY> [--debug]
cswap config path [--debug]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--json` | flag | off | Only with `list` or `get`. |
| `--debug` | flag | off | — |

`KEY` is a dotted settings key (see SETTINGS). The default action with no
positional is `list`.

### Description

Reads and edits `settings.json` in the backup root. `list` shows every key with
its effective value and whether it is explicitly set; `get` reads one key; `set`
validates and stores one key (writing only that key, never freezing the other
defaults into the file); `unset` removes one key; `path` prints the settings
file path. Values are validated strictly on `set`: an out-of-range or mistyped
value is rejected rather than silently clamped.

### Files

Reads and writes `settings.json`.

### Exit status

`0` on success; `1` on a handled error (`ConfigError` — unknown key, out of
range, wrong type, corrupt file); `2` on a usage error.

### Output

- `list` (human): aligned `key  value` rows; unset keys are tagged `(default)`.
- `list --json`:
  ```
  {"schemaVersion":1,"path":"<settings.json>","settings":[{"key":"<k>","value":<v>,"isSet":<bool>},...]}
  ```
- `get` (human): the bare value; `get --json`:
  `{"schemaVersion":1,"key":"<k>","value":<v>,"isSet":<bool>}`.
- `set`: `<key> = <value>`.
- `unset`: `<key> unset (default: <default>)`, or the stderr notice
  `<key> is not set; nothing to do` (exit 0) when it was not set.
- `path`: the absolute settings file path.

### Errors

| Message | Exit |
|---------|------|
| `argument {list,get,set,unset,path}: invalid choice: '<a>' (...)` | 2 |
| `the following arguments are required: KEY` (or `KEY, VALUE`, `VALUE`) | 2 |
| `unrecognized arguments: <tok>` | 2 |
| `--json can only be used with list or get` | 2 |
| `unknown setting '<key>'\nValid keys: ...` | 1 |
| `<key> must be between <lo> and <hi>` | 1 |
| `<key> expects an integer, got '<v>'` / `expects a number, got '<v>'` | 1 |
| `<key> expects true or false (or 1/0, yes/no), got '<v>'` | 1 |
| `<key> must be one of: <choices>` | 1 |
| `<key> expects a non-empty value; use 'cswap config unset <key>' to clear it` | 1 |

### Example

```
$ cswap config set autoswitch.threshold 80
autoswitch.threshold = 80
$ cswap config get autoswitch.threshold
80
$ cswap config set autoswitch.threshold 40
Error: autoswitch.threshold must be between 50 and 99.9
$ echo $?
1
```

### See also

[SETTINGS](#settings), `cswap auto`.

---

## cswap export

### Synopsis

```
cswap export <PATH> [--account NUM|EMAIL] [--full] [--debug]
```

Legacy: `cswap --export <path>`.

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `PATH` | positional string | — | `-` writes to stdout. |
| `--account` | string `NUM\|EMAIL` | all accounts | Only with `export`. Limits the export to one account. |
| `--full` | flag | off | Only with `export`. Embeds the entire `~/.claude.json` per account instead of just `oauthAccount`. |
| `--debug` | flag | off | — |

### Description

Serializes managed accounts (with their credentials) to a `.cswap` JSON file
for transfer to another machine. By default each account carries only its
`oauthAccount` config; `--full` embeds the whole per-account config. The active
account is read from the live vault for the freshest tokens. In a bulk export, a
single broken account is skipped with a stderr warning; a named single-account
export treats the same condition as a hard failure. A missing `oauthAccount` is
always fatal.

### Files

Reads `sequence.json`, `configs/`, `credentials/` (or Keychain). Writes the
`.cswap` file (or stdout).

### Exit status

`0` on success; `1` on a handled error.

### Output

`Exported <n> account(s) to <path>`. The file is a JSON envelope:

```
{
  "version": 1,
  "exportedAt": "<ISO-8601 UTC>",
  "exportedFrom": "<platform>",
  "swapVersion": "<version>",
  "encrypted": false,
  "activeAccountNumber": <int|null>,
  "accounts": [ { "number", "email", "uuid", "organizationUuid",
                  "organizationName", "added", "credentials", "config",
                  "kind"?, "alias"? }, ... ]
}
```

`encrypted` is always `false`. `credentials` is an OAuth object or a raw
API-key string; `config` is `{"oauthAccount": ...}` (default) or the full
config (`--full`). `kind` and `alias` are omitted when empty.

### Errors

Handled errors surface as `Error: <message>` (`TransferError` or a credential
error), exit 1. A corrupt or unreadable `sequence.json` surfaces a
`ConfigError` naming the file and the repair/restart options instead of the
`TransferError` an absent or empty roster reports (`no accounts to
export — run cswap --add-account first`).

### Example

```
$ cswap export backup.cswap
Exported 4 account(s) to backup.cswap
```

### See also

`cswap import`, [COMPATIBILITY](#compatibility).

---

## cswap import

### Synopsis

```
cswap import <PATH> [--force] [--debug]
```

Legacy: `cswap --import <path>`.

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `PATH` | positional string | — | `-` reads from stdin. |
| `--force` | flag | off | Only with `import` (or `switch <id>`). Overwrites an existing matching local account in place. |
| `--debug` | flag | off | — |

### Description

Reads a `.cswap` envelope and writes its accounts into the local store. It
validates every account first (path-traversal defence, credential shape,
duplicate-identity and alias-collision checks) with zero writes, so a malformed
account late in the file never half-imports an earlier one. Each account is then
matched on its `(email, organizationUuid)` identity: an existing match is
skipped, or overwritten with `--force`; a new identity gets a freshly allocated
slot. Only a destination with no prior preference inherits the file's
`activeAccountNumber`. Encrypted exports are rejected.

### Files

Reads the `.cswap` file (or stdin). Writes `sequence.json`, `configs/`,
`credentials/` under one lock spanning the write pass.

### Exit status

`0` on success (including an all-skipped import); `1` on a handled error.

### Output

Per-account lines (`Imported ...`, `Overwrote ...`, `Skipped <email> (already
exists, use --force)`) then a summary
`Done: <i> imported, <o> overwritten, <s> skipped`.

### Errors

| Message | Type |
|---------|------|
| `export file is not valid JSON: <detail>` | `TransferError` |
| `export file must be a JSON object` | `TransferError` |
| `unsupported export version: <v> (expected 1)` | `TransferError` |
| `encrypted exports are not supported in this version — decrypt before piping (e.g. gpg -d backup.gpg \| cswap --import -)` | `TransferError` |
| `export file has no accounts to import` | `TransferError` |

All are exit 1.

### Example

```
$ cswap import backup-acct2.cswap
Skipped bob@example.com (already exists, use --force)
Done: 0 imported, 0 overwritten, 1 skipped
```

### See also

`cswap export`, [COMPATIBILITY](#compatibility).

---

## cswap tui

### Synopsis

```
cswap tui [--debug]
```

Legacy: `cswap --tui`. A bare `cswap` in an interactive terminal opens the TUI.

### Description

Opens the full-screen interactive dashboard (accounts, usage, live switching,
an auto-switch screen). Requires an interactive terminal on both stdin and
stdout; a bare `cswap` invoked from a pipe or non-interactive context prints the
`no command given` usage error instead of opening the dashboard.

A keybinding bar at the bottom of each screen lists the keys available there:
on the dashboard, `s` switch accounts, `w` watch, `q` quit; on the switch
screen, `enter` switch, `b` best pick, `esc` back; on the watch screen, `s`
switch (`enter` confirm while a target is selected), `esc` back; on the
auto-switch screen, `l` go live / dry-run, `t` threshold (with `←`/`→` to adjust
and `enter` to finish while adjusting), `esc` back.

When a Codex store or a codex-auth registry is present, the dashboard's Codex
rows follow the Claude rows, each tagged `⟨codex⟩` after its workspace tag, on
the full card and the compact row alike; the remove and disable menu labels
carry the tag too. Claude rows keep their bare slot number as their id; Codex
rows are addressed by key (`codex:1`). Switch, disable / enable, and remove act
on the row's own provider and name the resolved slot: `Switched to Codex
account <n>: <email>`, `Disabled Codex account <n>`, `Enabled Codex account
<n>`, `Removed Codex account <n>`. When codex sessions are still running after a
Codex switch, a second warning lists them: `codex is running (pid <pid>[,
<pid>...]) — restart it for the new account to take effect.` Adding an account
stays Claude-only (use `cswap codex add` or `cswap codex login`). The
auto-switch screen's candidate list shows Claude rows only, since that engine
switches Claude; the Codex engine runs inside `cswap auto`.

The dashboard's menu nests: "Add account…", "Disable / enable account…" and
"Remove account…" each open a submenu, and `esc` or `←` returns to the
level above. The two submenus that list accounts show one row per managed
account, and those rows track the roster as it changes: an account removed,
added, renamed, or disabled from another `cswap` instance or by the
auto-switch engine appears, disappears, or relabels itself on the next
poll, rather than at the next visit to the submenu. A remove row also
carries the account's state, in the account card's colors — `● active` for
the account currently signed in, `(disabled)` in the warning color for one
held out of rotation.

While either of those submenus is open, the accounts monitor above it shows
only the active account's card, without the per-account rows it shows on
every other screen. The submenu is already listing the roster; the card
above states which account the terminal is signed in to now, which is what
the list itself cannot say about the row the cursor happens to be on.

Selecting an account in either submenu acts on the roster as it is at that
moment, not as it was when the submenu was drawn. An account that has left
the roster in between is reported and nothing is done — including on the
disable path, which has no confirmation step. The removal confirmation
names an account by email; if the slot changes hands while that
confirmation is on screen, answering it removes nothing and reports who
holds the slot instead. Two accounts sharing an email are told apart by
organization, which is the only thing that distinguishes them. A completed
removal or disable reports itself; both previously completed without
acknowledgement.

Disabled accounts appear in the switch and watch lists and remain valid
explicit targets there, since disabling holds an account out of automatic
rotation rather than out of the roster. The auto-switch screen's ranked
"Next best" candidates panel excludes disabled accounts entirely — an
account held out of automatic rotation never appears in a ranking of
automatic-switch targets. A quarantined account (one the auto engine has
sidelined for a dead or identity-mismatched stored credential; see
`cswap auto` below) is not excluded the same way: the panel keeps its row
but labels it "quarantined (<reason>)" in place of a usage figure, rather
than ranking it as a target, since quarantine is transient and clears
itself once the account's stored credential is replaced. Recovery is
logging in with the account and running `cswap add`.

A candidate with a readable usage figure lists every rate-limit window the
account reports, not only the one it is ranked by, laid out as one shared
table rather than repeated per row: a header line names each window once,
in a fixed relative order — `5h` before `7d` before every per-model weekly
window — regardless of which candidate's row happens to report one first.
The per-model columns follow those two, ordered by name. No part of the
header depends on the ranking: were a column's place decided by which
candidate happens to report it first, an account reporting only a `7d` figure
ranked ahead of one reporting both would print `7d` before `5h`, and two
accounts reporting different models would swap their columns as they
re-ranked — the header reshuffling itself from one poll to the next with no
resize and nothing about any account changed, which no other usage display in
`cswap` does, since the account card, the minimized account line, and
`cswap list` all read `5h` before `7d` always. Every candidate below the header supplies only its own figures
under those headings. A candidate that does not report a given column's
window shows an em dash there, in plain muted text rather than an empty
cell — plain, never the dimming an unmatched window's own figure carries
and never the color an exhausted one's would, so a missing figure is never
mistaken for either.

A quarantined, sentinel, or usage-unknown candidate reports no window at
all and so occupies no column: its row carries the reason in place of the
figures, starting right after that row's OWN email and running to the row's
own width budget from there — not a column shared with the window rows, so
two labeled rows can start their reasons at different columns when their own
emails differ in length. What IS shared, and laid out against the SAME
width on every row, is how far the identity column as a WHOLE may narrow: a
labeled row's own reason may narrow it only as far as the reason's own
guaranteed floor requires, never by its full length: a single long reason
(the re-login sentinel's note runs to 82 characters) narrowing the shared
column on behalf of its own full text would buy itself room out of every
OTHER candidate's email, on the panel and the monitor alike. The floor a
reason is guaranteed is its classifying first word plus a trailing
ellipsis — "quarantined…", "re-login…", "usage…" — since "quarantined",
"re-login", or "usage" is the reason's classification, and a cut landing
inside that word ("quarantin…") would state nothing a reader could act on;
the reason never clips shorter than that, or the whole reason when it is
already shorter. A reason with no word to keep — an unrecognized state
identifier the store reported verbatim, carrying no mapped wording of its
own — has no classification to protect, so it may clip inside itself once
it runs past twelve columns; a prefix of an identifier still identifies it
as well as anything short of the whole does. Down to that floor, and no
further, does a labeled row's
reason narrow the shared identity column; a reason that still does not fit
past that point clips ITSELF instead, in its own color — the warning color
for a quarantine reason, muted for a sentinel or "usage unknown" — never the
plain muted marker a narrowed email carries. Because the reason is drawn
against that row's OWN email rather than the widest one on the panel, a row
whose own email is shorter than the panel's widest is charged nothing for
the difference and states more of its reason than the shared floor alone
guarantees. In practice this means a labeled row spends its OWN email first
as the terminal narrows — clipping toward its own bare ellipsis while its
reason stays whole — and only once its own email is fully spent does the
reason itself begin to clip, toward its floor. The shared identity column
every OTHER row's email draws from is a separate, later concession: it
narrows only once even the widest reason's floor no longer fits beside a
full shared column, and even then it costs a row nothing until its own
email would not have fit the narrower column anyway. A width too narrow to
hold even a table's widest floor
is not a width this table renders a reason row at all — it is a width at
which the table gives up, in its entirety, the same way it gives up when a
readable row's slot number and fully-narrowed email do not fit: never a row
stating half a reason, never a row stating none.

A cell holds two figures: the utilization percentage, right-aligned, and
beside it, in its own aligned sub-column, the reset countdown, left-aligned.
Each column is exactly as wide as its widest FIGURE, so a percentage always
sits under its heading regardless of how many digits it or its neighbors
carry — and a column costs what its numbers cost, never what its window is
called. A utilization past 999%, in either direction, prints as `>999%` or
`<-999%` rather than the true figure, so one absurd measurement never sets
the width every account on the row pays for; the ranking, the severity
color, and whether a window counts as exhausted are unaffected and still
read the real, unbounded number. The
account card and `cswap list` do not share a column with any other account
and always print the real figure, however large. A heading too wide for the
room it sits over is shortened instead, and
every heading on the row is shortened together, never past the point at which
two different models would read alike: an ambiguous heading is terse, a
colliding one is false. So the same three accounts cost the same width whether
their model is called `Fable` or `claude-opus-4-5-20251101`. On this panel a
heading never shortens past four columns — a whole syllable — because the
panel's own per-line fallback (below) always spells a model's name in full,
and a heading trimmed any further would name a window worse than that
fallback does. The countdown is recomputed from the window's reset
timestamp at render time, never served from the countdown string the
measurement was fetched with — a stored string is correct only at fetch time
and drifts as the measurement ages — and reads `now` once the reset has
elapsed, or nothing when the reset timestamp is absent or unparseable. It
keeps the wording `cswap tui` uses everywhere else (`2d 4h`, `6d`, `now`),
minus the leading "resets": the column heading above it already names the
window, so the cell does not repeat it on every row.

Which windows count toward a row's rank — and toward the auto engine's own
pick — is a narrower set: `5h` and `7d` always, plus a per-model window named
by `autoswitch.model` (see SETTINGS below). Every other per-model window
still gets a column — so it can be watched before it is configured to
matter — but never moves any row's rank. Emphasis is per cell, not per row,
since which window binds a candidate's rank varies from row to row within
one column, and it reads three ways, not two. Severity color states what a
figure MEANS, and every COUNTED figure carries it — the same color the
account card's bars, the monitor's own per-row fallback line, and
`cswap list` already show a used-up window in — whether or not that figure
happens to be the one the row is ranked by. A window that has run out — at
or over its limit — carries that same severity color too, whether or not it
counts: an EXHAUSTED figure is the single most important number on its row,
the one that says the account cannot serve that window at all, and it never
fades into the muted background merely because nothing presently counts it.
Bold states a third, separate fact: which figure the ranking and the engine
act on. Only the binding window — the counted window with that row's
highest percentage — renders bold; a counted window that is not binding
carries the identical severity color, not bold, and neither does an
exhausted-but-uncounted one, since bold marks the ranking figure and an
uncounted window is never that. Only a window that is BOTH uncounted and
short of its limit carries neither color nor bold: muted and dim. This
third level matters most on the dashboard's accounts monitor, whose columns
are never counted at all — its ranking axis is always the bare `5h`/`7d`
pair, never `autoswitch.model` — so without it a per-model window that had
run completely out would read there exactly as one sitting at 40% does; the
per-row layout the table replaces has always flagged that case outright (as
`Fable (!)`, further below), and the table states the same fact in color
rather than in nothing. A countdown beside a cell always stays
muted and never bold, whichever cell it sits beside and whichever of the
three levels its own figure carries, since a reset time is supporting
detail and never the ranking figure itself.

An account that reports two windows under the same display name — two
per-model weekly windows sharing a name is the practical case — gets two
columns of that name, not one shared column: the table never merges same-
label figures from one row into a single cell. A merge would have to pick
which of the two figures a reader sees and which one is silently dropped,
the row's ranking figure among the candidates for dropping, so the table
gives every reported window its own column instead, at the cost of a
repeated heading exactly when, and only when, an account repeats a label.
Whichever windows a row reports and however its labels land, one fact never
varies: the cell carrying the row's ranking figure is on the table, and it
is the row's one bold cell.

A column heading is always muted, and additionally dim when its window is
uncounted on the currently configured axis, so a dimmed heading and the
header's own `Next best · counting 5h, 7d` note (`Next best · counting 5h, 7d, Fable` with
`autoswitch.model` set; `Next best · counting 5h, 7d, all models` for the
`all` sentinel) always agree about which columns matter to the ranking.

No line of the table ever wraps, regardless of terminal width. A table that
does not fit narrows one reduction at a time, re-measuring after each: the
column headings shorten first, all together, down to the narrowest spelling
that still tells two different models apart — a heading names a column
whose figures are on the screen regardless of how it is spelled, so it gives
ground before anything a reader cannot infer some other way does. Once every
heading is at its narrowest, the countdowns give way next: uncounted
columns first, then counted ones, rightmost column first within each, with
the ranking figure's own countdown held back to last on this panel — the
same order this panel's per-line layout gives them up in. Then the row's
email narrows toward a bare ellipsis, because a row's figures are what the
row is there to show and the slot number alone still identifies it once the
email is gone; a labeled row's reason concedes the other way around, itself
first and this shared column only once its own floor is reached (above).
Only past that does a whole column go.

Three kinds of column never go at all. A counted one — some row's rank may
rest on exactly that column, and dropping it would hide that row's ranking
figure to make room for a column that matters less to every other row. The
column carrying any row's own ranking figure, or, for a row nothing counts on
at all, its highest figure — so no account is ever left a line of bare dashes.
And, on the accounts monitor, a column some account has exhausted (used up, at
or over 100%) in: a window that has run out is the single most important
figure on that row, and that monitor's own per-line layout states it
unconditionally, so the table does too. This panel does not pin an exhausted
per-model column, because its own per-line layout drops that figure at every
width as well; pinning it here would cost the whole panel its table to protect
something the layout replacing it then discards.

Among the columns that CAN go, the one the FEWEST accounts report goes first —
a model one account reports before a model three do — and all the columns of
one model name go together or not at all, so an account that does report that
model is never shown a dash under a heading it belongs under. A table that has
dropped columns says so, once, at the end of its heading row: a muted `+2`.

The width below which no table can exist at all is not a fixed column count
and is not guesswork: it is the width of the fully narrowed table — every
droppable column gone, every heading at its shortest, every email down to
its ellipsis — and it moves with the data, more pinned columns, longer
labels, a longer reason or a wider slot number all pushing it out. Below
it the panel never attempts a table; laying out each candidate on its own
line is the only option there is.

At or above that width a table CAN be built, but building one is not
reason enough to show it. Comparing the two layouts at the SAME width
turns out not to be safe on its own: a table and a candidate's own line
each grow more informative as the terminal widens, but not always at the
same pace, so a bare side-by-side comparison taken at one width can favor
the per-line layout, then the table, then the per-line layout again as the
terminal keeps growing — measured, not hypothesized: on real rosters the
naive comparison can cost several figures on a panel exactly one column
WIDER, the opposite of what widening a terminal is supposed to do. So the
panel instead prices the table against a FIXED target: what each
candidate's own line would state at the width the table itself needs to
show everything it has, never at the narrower width actually on screen.
Because a candidate's own line only ever gains detail as it is given more
room, that fixed target already states at least as much as comparing at
the render width would demand, and it does not move as the terminal is
resized — which is what keeps the comparison well behaved as the terminal
grows: once a wider terminal earns the table, every terminal wider still
keeps it.

The fixed target depends on neither the width of the terminal nor on
anything that changes between two polls of the same terminal. It does not
depend on how long any one candidate's reason is: sizing it by the widest
reason on the panel would let one account's own text — not even shown at
the width in question — decide whether every OTHER account gets the
table, so a reason costs nothing beyond its own row. Nor does it read a
clock: every reset countdown priced into it is stated at the widest a
countdown's wording can ever be, never at how much time a window actually
has left, so the choice between the table and the per-line layout is the
same at every moment a terminal holds still — a countdown ticking down
between two polls narrows what a chosen layout draws; it never flips which
layout is chosen.

Held to that fixed target, the panel counts what each layout actually puts
on the screen: how many utilization figures survive whole, how many reset
countdowns survive whole, and, for a quarantined, sentinel, or
usage-unknown row, how many characters of its reason survive the cut. The
table is drawn only when it states no fewer of each of those than the
per-line layout would; the moment it states fewer of any one of them, the
whole panel falls back instead, to laying out every candidate on its own
line — `5h 12% (resets 3h 20m) · 7d 88% (resets 2d 4h) · Fable 40%
(resets 6d)`, narrowing on its own down to a bare slot number, a labeled
row narrowing its email before its reason. A tie goes to the table: where
both layouts would state the same figures, the same resets and the same
reasons, the table's aligned columns are pure gain the reader pays nothing
for. The choice is a property of the whole panel, never of one row: at a
given width every candidate goes through the table or every candidate goes
through the per-line layout.

The reason a wider shared table can still lose this comparison is
structural, not a defect in how it narrows. Its columns are the union
across every candidate, so a row pays the width of every OTHER candidate's
windows too — including an em dash under a column it has nothing to
report there — while a candidate's own per-line layout pays only for what
that candidate has. On a roster where accounts report different per-model
windows, the table can therefore end up stating the same figures spread
over more columns, and buys the room by shedding countdowns a candidate's
own line still affords. No reordering of what narrows first changes that;
it is the price a shared column pays for lining every account's figures up
under one heading, and it is why the comparison, not the width alone,
decides which layout a reader sees.

One thing this comparison deliberately leaves out: how much of a
candidate's own identity — the alias or email — is on the screen. A
shared table buys its column alignment out of that very cell, so weighing
identity in the same comparison would refuse the table at exactly the
widths where lining every account's figures up under one heading is what
it is for; the slot number still names the account for `cswap switch`
either way. Identity is measured, not compared, and it is the one respect
in which the panel does not simply give back more of everything as the
terminal widens: at the exact width where the panel switches into table
mode, the visible email can be SHORTER than it was one column narrower, in
the per-line layout the panel just left behind.

The dashboard's always-visible accounts monitor (the panel above the menu on
the main screen) lays its non-active accounts out through this same table,
so a window reads the same way in both places. Each row's label cell carries
the account's identity exactly as it always has: the alias form
`alias (email)` when an alias is set, else the bare email, then `[tag]`,
then a `(disabled)` marker in the warning color when the account is
disabled. Which windows count stays `5h` and `7d` here regardless of
`autoswitch.model` — the axis this monitor has always used — so a per-model
window is always an uncounted column: muted and dim short of its limit, but
carrying its full severity color, the same as a counted figure, from the
moment it runs out — an exhausted per-model window is the one figure on
that row a reader most needs to see, on this axis or any other. A sentinel
state or an account with no usable measurement carries its reason the same
way a candidate's does on the panel: the row's own email narrows first,
down to a bare ellipsis, then the reason itself narrows toward its own
floor, and only past that point does the shared label cell narrow in turn.
The table is laid out once across every non-active
account, so its columns still line up above and below the active account's
own full card wherever
that account falls in roster order — the active account is not moved to
the top, and its card can visually split the table into two blocks that
still share one column layout.

Narrowing inside a table follows the identical ladder wherever a table is
built, but the monitor's fallback is its own long-standing one-line-per-account
shape, not the panel's: only `5h` and `7d` appear, a reset countdown appears
only once that window has reached 100%, and a per-model window appears only
once it too has reached 100% (as `Fable (!)`). Because that fallback never
names a per-model window until it has run out, a heading on this table may
shorten all the way to two columns — the width of `5h`/`7d` and of a
percentage — rather than stopping at the panel's four-column syllable floor:
there is no fuller naming here for a shorter heading to fall short of.

Which shape renders is decided by the identical priced comparison the panel
makes, not by fitting alone: below the width at which a table can exist at
all, the monitor shows every account on its own mini line, the same shape
it has always used. At or above that width, existence is necessary but not
sufficient — the monitor prices the fully-shed table against what the mini
lines would state, and shows the table only where it states no fewer
utilization figures, reset countdowns, and characters of a sentinel or
unusable account's reason than the mini lines would. On this surface that
comparison is close to a formality rather than a real contest: the mini
line's own long-standing contract already states nothing the table's
protected columns do not also guarantee, so a terminal wide enough for the
table almost never finds the mini lines ahead on any count — unlike the
panel, where the comparison regularly decides things. Table mode does not
imply the full picture even so: the table can already have dropped an
uncounted column's countdown, or the column itself, and still be the one
shown, so a terminal only wide enough for a heavily-shed table still
renders in table mode with a countdown or a per-model column missing. Only
once the fully-shed table — its label narrowed to a bare ellipsis, every
droppable column gone, every heading at its shortest — no longer states at
least what the mini lines would, does the monitor fall back, in its
entirety, to this narrower shape rather than wrapping or clipping the
table. That narrower shape is itself held to the terminal's width: it clips
its own line, like everything else on the monitor, rather than running off
the screen. The one thing this comparison leaves out, here as on the
panel, is how much of an account's own alias or email is on the screen:
the table can show a shorter one at the width where it takes over than the
mini line showed one column narrower.

The dashboard also caps how many lines this monitor may use when the
terminal is too short for both it and the menu below, dropping trailing
accounts behind a muted "· N more accounts" note. This is a SECOND,
independent price, layered on top of the comparison above: that first
comparison decides table against per-row shape by what each states at the
FULL content width; the height cap, only when the monitor does not fit the
terminal's height at all, prices the same two shapes again under a fixed
LINE budget instead, counting accounts shown rather than figures,
countdowns, or reason characters, and can still choose the per-row shape
even where the first comparison favored the table. The table's column
header is bound to the first non-active account's row for that cap: a
budget too small for that row drops the header along with it, so the
monitor never shows a header naming columns with no row visible under it,
and never drops the header while a row it describes is still shown.

That header spends a line the monitor's per-row, one-line-per-account shape
does not, and at a tight budget a line is an account, so the cap prices the
SAME budget in both shapes and keeps whichever shows more of them. The
table wins unless the per-row shape fits strictly more accounts at that
budget, or fits exactly as many while also keeping the muted "· N more
accounts" note where the table's own attempt loses it — in either case the
monitor renders in the per-row shape instead, buying back the line the
header cost. A budget too tight for either shape to keep
the note at all renders as the table regardless (with its lone-header
rescue, above): the column header naming what is on screen is what is left
to show, in place of a count of what is not.

### Files

Reads and writes the same files the underlying commands do while the dashboard
is open.

### Exit status

`0` on a clean quit; `1` when a terminal is unavailable.

### Output

Full-screen terminal UI (no JSON). `--json` does not apply.

### Errors

| Message | Condition | Exit |
|---------|-----------|------|
| (terminal error, no dashboard opened) | stdin or stdout is not an interactive terminal (`cswap tui` from a pipe or redirect). | 1 |
| `no command given — try 'cswap help'` | A bare `cswap` (which would open the TUI) in a non-interactive context. | 2 |

### Example (illustrative — requires an interactive terminal)

```
$ cswap
# ... full-screen dashboard ...
```

### See also

`cswap watch`, `cswap list`.

---

## cswap watch

### Synopsis

```
cswap watch [--debug]
```

Legacy: `cswap --watch`.

### Description

Opens the same interactive dashboard as `cswap tui`, starting on the live watch
page.

### Files

As for `cswap tui`.

### Exit status

As for `cswap tui`: `0` on a clean quit; `1` when a terminal is unavailable.

### Output

As for `cswap tui`: a full-screen terminal UI (no JSON).

### Errors

As for `cswap tui`: a terminal error (exit 1) when stdin or stdout is not an
interactive terminal.

### Example (illustrative — requires an interactive terminal)

```
$ cswap watch
# ... dashboard on the watch page ...
```

### See also

`cswap tui`.

---

## cswap menubar

### Synopsis

```
cswap menubar
```

Legacy: `cswap --menubar`.

### Description

The macOS menu bar app. It is not available in this build. On macOS the command
reports that menu bar mode is not available; on every other platform it reports
that the menu bar is macOS-only. Either way it exits 1.

### Files

None. `cswap menubar` touches no files.

### Exit status

Always `1`.

### Output

- macOS: `Menu bar mode is not available in this build.` (stderr)
- Other platforms: `The menu bar is only available on macOS.` (stderr)

### Errors

| Message | Condition | Exit |
|---------|-----------|------|
| `Menu bar mode is not available in this build.` | Invoked on macOS. | 1 |
| `The menu bar is only available on macOS.` | Invoked on any non-macOS platform. | 1 |

### Example

```
$ cswap menubar
The menu bar is only available on macOS.
$ echo $?
1
```

### See also

`cswap tui`.

---

## cswap upgrade

### Synopsis

```
cswap upgrade
```

Legacy: `cswap --upgrade`. Alias: `cswap update`.

### Description

Self-upgrades to the latest release. It runs before the switcher is
constructed, so upgrading never touches config or credentials. When the running
binary lives in a Go-managed bin directory (`$GOBIN`, `$GOPATH/bin`, or
`$HOME/go/bin`), it re-runs `go install
git.dpemmons.com/dpemmons/cswap/cmd/cswap@latest`. Otherwise it prints manual
upgrade guidance. On Windows the running executable is locked, so the command
always prints the upgrade command rather than running it.

### Files

None of cswap's data files. On a `go install` upgrade, the Go toolchain
replaces the binary.

### Exit status

The exit status of the `go install` subprocess on a `go install` layout; `1`
when only guidance was printed (unknown layout, Windows, `go` missing from
PATH).

### Output

On an unknown layout:

```
Could not detect a `go install` layout (looked for $GOBIN, $GOPATH/bin, $HOME/go/bin).
  binary: <path>
To upgrade manually, run:
  go install git.dpemmons.com/dpemmons/cswap/cmd/cswap@latest
Or download a release from:
  https://git.dpemmons.com/dpemmons/cswap/releases
```

On Windows: `To upgrade claude-swap on Windows, run:` followed by the
`go install` command.

### Errors

`Detected a go install layout but \`go\` is not on PATH. Run the upgrade
manually from a shell where it is available.` (exit 1).

### Example

```
$ cswap upgrade
Could not detect a `go install` layout (looked for $GOBIN, $GOPATH/bin, $HOME/go/bin).
  binary: ~/go/bin/cswap
To upgrade manually, run:
  go install git.dpemmons.com/dpemmons/cswap/cmd/cswap@latest
Or download a release from:
  https://git.dpemmons.com/dpemmons/cswap/releases
$ echo $?
1
```

### See also

The passive update notice under NOTES.

---

## cswap purge

### Synopsis

```
cswap purge [--debug]
```

Legacy: `cswap --purge`.

### Description

Removes all claude-swap data — the backup directory and every stored account
credential file. It does not affect the current Claude Code login. Prompts for
confirmation (`[y/N]`) on an interactive terminal; any answer other than `y`
cancels. The passive update notice is suppressed for this command.

### Files

Removes the backup root and its contents (and the macOS Keychain entries under
service `claude-swap`).

### Exit status

`0` on success or a cancelled prompt; `1` on a handled error.

### Output

A warning block naming the backup directory, then the prompt. `Cancelled` when
declined.

### Errors

A filesystem or Keychain removal failure surfaces as `Error: <message>`
(`ClaudeSwitchError`), exit 1.

### Example

```
$ cswap purge
This will remove ALL claude-swap data from your system:
  - Backup directory: ~/.local/share/claude-swap
  - All stored account credential files

Note: This does NOT affect your current Claude Code login.

Are you sure you want to purge all data? [y/N] Cancelled
```

### See also

`cswap remove`.

---

## cswap codex

### Synopsis

```text
cswap codex [--debug] <verb> [args]
cswap codex -h | --help
cswap codex <verb> -h | --help
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `-h`, `--help` | flag | off | Before the verb: prints the verb list, examples, and notes. After a verb: prints that verb's usage line. |
| `--debug` | flag | off | Accepted before or after the verb. Enables debug logging. |

A verb is required. Value flags accept both `--flag value` and `--flag=value`.
`--debug` writes debug records to `<backup_root>/claude-swap.log` and to stderr
for the Codex network client, process detection, and the codex-auth import.

### Description

The `codex` namespace manages Codex (ChatGPT) accounts. It is a second provider
beside Claude: every verb below acts on Codex accounts only, and bare `cswap
list`, `cswap switch`, and the other top-level verbs keep acting on Claude
accounts. Codex slot numbers are a separate sequence from Claude slot numbers.

Two rules govern every verb. The live `auth.json` decides which account is
active: cswap reads its identity and matches it to a slot, and the store's own
record of the active account is intent only. The active account is never
refreshed from its stored snapshot, because the codex CLI holds the same refresh
token and keeps the live file current; inactive accounts are refreshed from
their snapshots and a rotated token is written back before it is used.

Every verb except `import-codex-auth` first runs the one-time codex-auth import:
when cswap holds no Codex account and `~/.codex/accounts/registry.json` exists,
its accounts are copied into cswap's store (see `cswap codex import-codex-auth`).
The import prints `Imported <n> Codex account(s) from <path> (the original is
left untouched).` and, when rows had no usable auth file, `Skipped <n>
account(s) with no usable auth file.` A registry whose schema is newer than 4
prints `codex-auth registry uses schema <n>, which this version of cswap does
not understand — not importing.` and imports nothing; the verb then runs as
usual. Under `--json` these notices go to stderr, so stdout holds only the JSON
document.

**Account resolution.** A verb that names an account accepts the slot number,
the email, or the alias. The identifier is trimmed and compared
case-insensitively against all three at once; this differs from the Claude
resolution order above. An identifier that matches nothing raises `No Codex
account matches '<id>'`.

This namespace is a Go-side extension relative to the Python reference pinned in
`docs/port-spec/`; it ports claude-swap PR #252 (DESIGN Amendment A22).

### Files

Reads and writes the Codex store under `<backup_root>/codex/` (`sequence.json`,
`credentials/`, `cache/`, `.lock`) and the codex CLI's live
`$CODEX_HOME/auth.json` (`~/.codex/auth.json` when `CODEX_HOME` is unset),
written 0600 with an existing file's group and world bits dropped, inside a
`~/.codex` created 0700 when it does not exist yet. Reads
codex-auth's `~/.codex/accounts/` during the import and never writes it.
Mutations run under `<backup_root>/codex/.lock`, which is separate from the
Claude lock, so a Codex operation never blocks a Claude one. `export`,
`import`, and `purge` take no store lock, like the Claude transfer verbs.

### Exit status

`0` on success. `1` on a handled error. `2` on a usage error (unknown verb,
unknown flag, missing or extra argument, invalid `--strategy`). `130` on
Ctrl-C. `cswap codex login` exits with the codex CLI's status when `codex
login` fails.

### Output

Human output per verb, below. Only `list` and `status` accept `--json`; each
writes one indented JSON document to stdout carrying `schemaVersion`.

### Errors

Handled errors print a red `Error: <message>` to stderr and exit 1. Under
`--json` the error is instead the standard error envelope on stdout (see [JSON
OUTPUT CONTRACT](#json-output-contract)), with the same exit code. An invalid
alias is reported the same way. A mutation that cannot take the store lock
reports `Another cswap process is using the Codex store; try again.`

### Example

```text
$ cswap codex list
* 1. alice@example.com [personal]  5h 12%  7d 40%
  2. alice@example.com [Example Team] (work)  7d 3%
```

### See also

`cswap codex list`, `cswap codex switch`, `cswap auto`,
[COMPATIBILITY](#compatibility).

---

## cswap codex list

### Synopsis

```text
cswap codex list [--json] [--skip-api] [--token-status]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--json` | flag | off | May be combined with `--token-status`. |
| `--skip-api` | flag | off | Serves cached usage only; no network. |
| `--token-status` | flag | off | Adds token diagnostics; never prints a token. |

### Description

Lists every managed Codex account in slot order with its usage summary. Usage is
served through the same cache the Claude side uses (serve TTL, cross-process
fetch leases, failure backoff, `Retry-After` handling), so two consecutive
listings cost one round of requests. Windows are classified by their declared
length, not their position in the response: a window of one day or longer is
the weekly (`7d`) window and a shorter one is the `5h` window. A plan that
reports only one window shows only that window.

Workspace names for Business, Enterprise, and Edu accounts ride along with a
usage fetch. A request is made only for a user who holds more than one stored
account, at least one of them on a workspace plan and still unnamed; a failed
request leaves stored names untouched.

`--token-status` adds one line per account: its access-token expiry, whether a
refresh is due, whether a refresh token is present, and the last refresh time.

### Files

Reads `codex/sequence.json`, the stored snapshots, `codex/cache/`, and the live
`auth.json`. Writes the usage cache, refreshed snapshots of inactive accounts,
and workspace names in `codex/sequence.json`.

### Exit status

`0` on success; `1` on a handled error; `2` on a usage error.

### Output

One line per account:

```text
<marker> <n>. <email> [<workspace|personal>][ (<alias>)][ [disabled]][  <usage>]
```

`<marker>` is `*` for the active account and a space otherwise. `<usage>` is
`5h <pct>%  7d <pct>%` for the windows the account reports, or the fetch
sentinel when there is no measurement: `api key`, `http <status>`, `network`,
`bad-response`, `MissingAuth`. An empty roster prints `No Codex accounts. Run
'cswap codex add' or 'cswap codex login'.`

`--token-status` adds, per account, `      token: <state>` for an account that
is not an OAuth login (`api key`, `no credentials`), otherwise
`      token: <valid|refresh due>, expires in <duration>[, NO refresh token];
last refresh <time|never>`. `<duration>` reads `6d 4h`, `3h 12m`, `12m`,
`expired`, or `unknown`.

With `--json`:

```json
{
  "schemaVersion": 1,
  "provider": "codex",
  "activeNumber": "<num>|null",
  "accounts": ["<codex-row>"],
  "tokenStatus": ["<token-status>"]
}
```

`tokenStatus` is present only with `--token-status`. Slot numbers are strings
in this payload. A `<codex-row>` holds:

| Key | Type | Meaning |
|-----|------|---------|
| `number` | string | Slot number. |
| `email` | string | Recorded email. |
| `workspace` | string | Workspace name, or `""` for a personal account. |
| `alias` | string | Alias, or `""`. |
| `active` | bool | `true` for the account the live `auth.json` holds. |
| `disabled` | bool | `true` when held out of auto-rotation. |
| `kind` | string | `oauth` or `api_key`. |
| `usage` | object \| null | The last good measurement in the `cswap list --json` usage encoding: `fiveHour` and `sevenDay` (present when reported) and `spend` (when the account has credits). |
| `sentinel` | string \| null | Why there is no fresh measurement (values above). |
| `fetchedAt` | number \| null | When `usage` was measured, POSIX seconds. |
| `ageSeconds` | number \| null | Age of `usage`. |
| `plan` | string \| null | The plan reported with `usage`. |

A `<token-status>` object holds `number` and `state` (`oauth`, `api key`, or
`no credentials`); an `oauth` entry adds `expiresAt` (POSIX seconds or null),
`expiresInSeconds`, `refreshDue`, `hasRefreshToken`, and `lastRefresh`.

### Errors

Handled errors print `Error: <message>` to stderr, exit 1; under `--json`, the
error envelope on stdout.

### Example

```text
$ cswap codex list
* 1. alice@example.com [personal]  5h 12%  7d 40%
  2. alice@example.com [Example Team] (work)  7d 3%
  3. bob@example.com [personal] [disabled]  http 429
```

### See also

`cswap codex status`, `cswap list`.

---

## cswap codex status

### Synopsis

```text
cswap codex status [--json]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--json` | flag | off | — |

### Description

Reports the account the codex CLI is using, read from the live `auth.json`, and
the number of managed Codex accounts. The usage lines are rendered by the same
code as `cswap status`, so the two blocks have the same layout.

### Files

Reads the live `auth.json`, `codex/sequence.json`, and `codex/cache/`; may
refresh and write inactive accounts' snapshots and the usage cache.

### Exit status

`0` on success; `1` on a handled error.

### Output

Human output is `Status: Codex-<n> (<email> [<workspace|personal>])`, then
`  Total managed Codex accounts: <n>`, then the usage lines. When the live login
matches no managed account it is `Status: No active Codex account`.

With `--json`:

```json
{
  "schemaVersion": 1,
  "provider": "codex",
  "active": {
    "number": 1,
    "email": "<email>",
    "workspace": "<name or empty>",
    "alias": "<alias or empty>",
    "plan": "<plan>",
    "managed": true,
    "usageStatus": "<status>",
    "usage": "<object|null>"
  },
  "totalManagedAccounts": 3
}
```

`number` is an integer here, unlike the string slot numbers of `list --json`.
`usageStatus` and `usage` use the `cswap status` encoding (`fiveHour`,
`sevenDay`, camelCase sub-keys). With no active Codex account the document is
`{"schemaVersion": 1, "provider": "codex", "active": null}`, without
`totalManagedAccounts`.

### Errors

Handled errors print `Error: <message>` to stderr, exit 1; under `--json`, the
error envelope on stdout.

### Example

```text
$ cswap codex status --json
{
  "schemaVersion": 1,
  "provider": "codex",
  "active": null
}
```

### See also

`cswap codex list`, `cswap status`.

---

## cswap codex switch

### Synopsis

```text
cswap codex switch
cswap codex switch <NUM|EMAIL|ALIAS>
cswap codex switch --strategy best
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `NUM\|EMAIL\|ALIAS` | positional | — | Optional. |
| `--strategy` | choice | — | Only `best`. Takes precedence over the positional. |

### Description

Bare `switch` rotates to the next rotatable account after the active one; when
no rotatable account is active it activates the first one. A rotatable account
is enabled and is not an API-key login. With an identifier it activates that
account. `--strategy best` activates the rotatable, non-active account whose
worse window (5h or weekly) is lowest, skipping accounts with no measurement.

The switch runs under the store lock end to end: it stores the live login into
the slot its own identity matches, then writes the target's snapshot to
`auth.json`. If the write fails, the previous `auth.json` is put back. After the
switch cswap looks for running `codex` or `codext` processes by executable name.
Naming the account that is already live stores the live login and writes
nothing, so a refresh token rotated by codex since the last capture is kept.

### Files

Reads and writes the live `auth.json`, the stored snapshots, and
`codex/sequence.json`, under `codex/.lock`.

### Exit status

`0` on success; `1` on a handled error; `2` on a usage error.

### Output

`Switched to Codex account <n>: <email>`. When codex processes are running, a
second line follows: `codex is running (pid <pid>[, <pid>...]) — restart it for
the new account to take effect.` A running session keeps its old account until
it is restarted. When the named account is already live the only line is
`Codex account <n> is already active: <email>`, and no restart warning follows.

### Errors

| Message (exit 1) | Condition |
|------------------|-----------|
| `No Codex account matches '<id>'` | The identifier resolves to nothing. |
| `Codex account <n> has no stored credentials` | The target has no snapshot. |
| `Failed to activate Codex account: <err>` | Writing `auth.json` failed; the previous file was restored. |
| `No rotatable Codex accounts` | Bare `switch` with no enabled OAuth account. |
| `Only one rotatable Codex account — nothing to rotate to` | Bare `switch` when the active account is the only candidate. |
| `No Codex account with a known measurement to switch to` | `--strategy best` found no measured candidate. |
| `Another cswap process is using the Codex store; try again.` | The store lock is held. |

### Example

```text
$ cswap codex switch work
Switched to Codex account 2: alice@example.com
codex is running (pid 4242) — restart it for the new account to take effect.
```

### See also

`cswap codex list`, `cswap switch`.

---

## cswap codex add

### Synopsis

```text
cswap codex add [--alias NAME]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--alias` | string | none | Validated like `cswap alias`. |

### Description

Stores the account the codex CLI is logged in as: it reads the live `auth.json`,
creates or updates the slot keyed by the account's identity (user id and ChatGPT
account id), writes the snapshot, and records the account as active. An account
already stored keeps its slot; a new one takes the lowest free slot number.

### Files

Reads the live `auth.json`. Writes `codex/sequence.json` and the snapshot, under
`codex/.lock`.

### Exit status

`0` on success; `1` on a handled error.

### Output

`Added Codex account <n>: <email> [<workspace|personal>]`.

### Errors

| Message (exit 1) | Condition |
|------------------|-----------|
| `No Codex login found. Run 'cswap codex login' (or 'codex login') first.` | No readable `auth.json`. |
| `The current Codex login carries no account id, so it cannot be told apart from another account. API-key logins are not switchable.` | The login has no ChatGPT account id. |
| `Another cswap process is using the Codex store; try again.` | The store lock is held. |

### Example

```text
$ cswap codex add --alias home
Added Codex account 1: alice@example.com [personal]
```

### See also

`cswap codex login`, `cswap add`.

---

## cswap codex login

### Synopsis

```text
cswap codex login [--device-auth] [--alias NAME]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `--device-auth` | flag | off | Passed to `codex login`. |
| `--alias` | string | none | As for `cswap codex add`. |

### Description

Runs `codex login` (with `--device-auth` when given), then stores the resulting
account as `cswap codex add` does. The `codex` binary is resolved on `PATH` and
executed directly, without a shell, so a shell function named `codex` does not
add flags to the login.

### Files

As `cswap codex add`; `codex login` itself writes the live `auth.json`.

### Exit status

`0` on success; `1` when `codex` is not on `PATH`, cannot be started, or is
killed by a signal, or on a handled error; the codex CLI's status when `codex
login` exits non-zero.

### Output

The codex CLI's own login output, then `Added Codex account <n>: <email>
[<workspace|personal>]`.

### Errors

| Message | Exit | Condition |
|---------|------|-----------|
| `The 'codex' CLI is not on PATH. Install it, or run 'cswap codex add' after logging in another way.` | 1 | No `codex` binary. |
| `codex login did not complete.` | codex's status, or 1 | `codex login` exited non-zero, could not start, or was killed. |

Both messages are printed in red on stderr without an `Error:` prefix.

### Example

```text
$ cswap codex login --device-auth --alias work
...
Added Codex account 2: alice@example.com [Example Team]
```

### See also

`cswap codex add`.

---

## cswap codex remove

### Synopsis

```text
cswap codex remove <NUM|EMAIL|ALIAS> [-y|--yes]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `NUM\|EMAIL\|ALIAS` | positional | — | Required. |
| `-y`, `--yes` | flag | off | Skips the confirmation prompt. |

### Description

Forgets an account: removes its slot and deletes its stored snapshot, including
the Keychain item on macOS. Removing the active account prints `Warning: Codex
account <n> (<email>) is currently active` first; the live `auth.json` is not
changed. Without `--yes` it prompts `Are you sure you want to permanently remove
Codex account <n> (<email>)? [y/N]`; any answer other than `y` prints
`Cancelled`.

### Files

Writes `codex/sequence.json`; deletes the snapshot.

### Exit status

`0` on success or a cancelled prompt; `1` on a handled error.

### Output

`Removed Codex account <n>`, naming the resolved slot number, printed only when
the account was removed. A declined prompt prints `Cancelled` and nothing else.

### Errors

`No Codex account matches '<id>'`, exit 1.

### Example

```text
$ cswap codex remove 3 --yes
Removed Codex account 3
```

### See also

`cswap codex disable`, `cswap codex purge`.

---

## cswap codex alias

### Synopsis

```text
cswap codex alias <NUM|EMAIL> [NAME]
cswap codex alias <NUM|EMAIL> --unset
```

The account may also be named by its current alias.

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `NAME` | positional | empty | Validated like `cswap alias`. |
| `--unset` | flag | off | Clears the alias. |

### Description

Sets an account's alias. `--unset`, or omitting `NAME`, clears it. There is no
listing form; `cswap codex list` shows aliases.

### Files

Writes `codex/sequence.json`.

### Exit status

`0` on success; `1` on a handled error.

### Output

`Codex account <n> is now '<alias>'`, or `Cleared alias for Codex account <n>`.

### Errors

`No Codex account matches '<id>'` or the alias validation message, exit 1.

### Example

```text
$ cswap codex alias 2 work
Codex account 2 is now 'work'
$ cswap codex alias 2 --unset
Cleared alias for Codex account 2
```

### See also

`cswap alias`.

---

## cswap codex disable

### Synopsis

```text
cswap codex disable <NUM|EMAIL|ALIAS>
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `NUM\|EMAIL\|ALIAS` | positional | — | Required. |

### Description

Holds an account out of rotation: bare `cswap codex switch`, `--strategy best`,
and the Codex side of `cswap auto` skip it. It stays switchable by name.

### Files

Writes `codex/sequence.json`.

### Exit status

`0` on success; `1` on a handled error.

### Output

`Codex account <n> disabled`, naming the resolved slot number.

### Errors

`No Codex account matches '<id>'`, exit 1.

### Example

```text
$ cswap codex disable work
Codex account 2 disabled
```

### See also

`cswap codex enable`.

---

## cswap codex enable

### Synopsis

```text
cswap codex enable <NUM|EMAIL|ALIAS>
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `NUM\|EMAIL\|ALIAS` | positional | — | Required. |

### Description

Returns a disabled account to rotation.

### Files

Writes `codex/sequence.json`.

### Exit status

`0` on success; `1` on a handled error.

### Output

`Codex account <n> enabled`, naming the resolved slot number.

### Errors

`No Codex account matches '<id>'`, exit 1.

### Example

```text
$ cswap codex enable 2
Codex account 2 enabled
```

### See also

`cswap codex disable`.

---

## cswap codex swap

### Synopsis

```text
cswap codex swap <A> <B>
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `A`, `B` | positional | — | Each a slot number, email, or alias. |

### Description

Exchanges two accounts' slot numbers. Only `codex/sequence.json` changes:
snapshots are keyed by account identity, so renumbering never moves a secret.

### Files

Writes `codex/sequence.json` under `codex/.lock`.

### Exit status

`0` on success; `1` on a handled error.

### Output

`Swapped Codex slots <a> and <b>`.

### Errors

| Message (exit 1) | Condition |
|------------------|-----------|
| `Cannot swap an account with itself` | Both arguments name one account. |
| `No Codex account matches '<id>'` | An argument resolves to nothing. |
| `Another cswap process is using the Codex store; try again.` | The store lock is held. |

### Example

```text
$ cswap codex swap 1 2
Swapped Codex slots 1 and 2
```

### See also

`cswap codex move`, `cswap swap`.

---

## cswap codex move

### Synopsis

```text
cswap codex move <NUM|EMAIL|ALIAS> <SLOT>
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `SLOT` | positional | — | A positive integer. |

### Description

Assigns an account to a slot number. When the slot is taken, its occupant moves
to the account's old number. Only `codex/sequence.json` changes.

### Files

Writes `codex/sequence.json` under `codex/.lock`.

### Exit status

`0` on success; `1` on a handled error.

### Output

`Moved Codex account <from> to slot <to>`, with ` (swapped with its occupant)`
appended when the slot was taken, or `Codex account is already in slot <n>`.

### Errors

| Message (exit 1) | Condition |
|------------------|-----------|
| `'<slot>' is not a valid slot number` | `SLOT` is not a positive integer. |
| `No Codex account matches '<id>'` | The account resolves to nothing. |
| `Another cswap process is using the Codex store; try again.` | The store lock is held. |

### Example

```text
$ cswap codex move 3 1
Moved Codex account 3 to slot 1 (swapped with its occupant)
```

### See also

`cswap codex swap`, `cswap move`.

---

## cswap codex export

### Synopsis

```text
cswap codex export <PATH> [--account NUM|EMAIL]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `PATH` | positional | — | `-` writes to stdout. |
| `--account` | string | all accounts | Limits the export to one account. |

### Description

Writes Codex accounts, with their live OAuth tokens, to a JSON file. An export
that cannot log in is not a backup, so the file is as sensitive as the stored
credentials: it is created with mode 0600 before any token is written (an
existing file is narrowed to 0600 before it is overwritten), and it carries a
`warning` field saying so. Accounts without a stored snapshot are
left out. The format is separate from the Claude `.cswap` format, and a
`provider` field lets an import refuse a file from the other provider.

### Files

Reads `codex/sequence.json` and the snapshots. Writes `PATH`, creating parent
directories. Takes no store lock.

### Exit status

`0` on success; `1` on a handled error.

### Output

`Exported <n> Codex account(s) to <path>`; nothing else when `PATH` is `-`. The
file:

```json
{
  "version": 1,
  "provider": "codex",
  "warning": "This file contains live OAuth tokens for the accounts below. Anyone who can read it can use those accounts. Keep it private and delete it once imported.",
  "accounts": [
    {
      "accountKey": "<user id>::<account id>",
      "email": "<email>",
      "plan": "<plan>",
      "workspaceName": "<name>",
      "alias": "<alias>",
      "disabled": false,
      "authMode": "chatgpt",
      "auth": {}
    }
  ]
}
```

`auth` is the account's `auth.json` payload.

### Errors

| Message (exit 1) | Condition |
|------------------|-----------|
| `No Codex accounts to export` | The roster, or the `--account` selection, is empty. |
| `No Codex accounts with stored credentials to export` | No selected account has a snapshot. |
| `No Codex account matches '<id>'` | `--account` resolves to nothing. |

### Example

```text
$ cswap codex export ~/codex.json
Exported 2 Codex account(s) to ~/codex.json
```

### See also

`cswap codex import`, `cswap export`.

---

## cswap codex import

### Synopsis

```text
cswap codex import <PATH> [--force]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `PATH` | positional | — | `-` reads from stdin. |
| `--force` | flag | off | Overwrites accounts already stored. |

### Description

Reads a `cswap codex export` file into the store. Accounts are matched by
`accountKey`; an account already stored is skipped unless `--force` is given.
Rows without an `accountKey` or an `auth` object are skipped, and so is a row
whose `auth` payload carries an identity that is not the row's `accountKey`
(a payload with no decodable identity, such as an API-key login, is taken as
labelled). An `alias` goes through the same rule as `cswap codex alias`; an
alias that rule rejects is dropped, the row is still imported. A file whose
`provider` is present and not `codex`, or whose `version` is newer than 1, is
refused whole, as is a document larger than 8 MiB. Import does not change the
live `auth.json`.

### Files

Reads `PATH`. Writes `codex/sequence.json` and the snapshots under
`codex/.lock`.

### Exit status

`0` on success; `1` on a handled error.

### Output

`Imported <n> Codex account(s)`.

### Errors

| Message (exit 1) | Condition |
|------------------|-----------|
| `Cannot read <path>: <err>` | The file cannot be read. |
| `<path> is not valid JSON: <err>` | The file does not parse. |
| `<path> is not a cswap export` | The document is not a JSON object. |
| `<path> is a '<provider>' export, not a Codex one` | `provider` names another provider. |
| `<path> uses export version <v>, newer than this cswap understands` | `version` is above 1. |
| `<path> contains no accounts` | `accounts` is missing or empty. |
| `<path> is larger than 8 MiB; refusing to import it` | The document exceeds the import size limit. |

### Example

```text
$ cswap codex import ~/codex.json
Imported 2 Codex account(s)
```

### See also

`cswap codex export`, `cswap import`.

---

## cswap codex purge

### Synopsis

```text
cswap codex purge [-y|--yes]
```

### Options

| Option | Type | Default | Constraints |
|--------|------|---------|-------------|
| `-y`, `--yes` | flag | off | Skips the confirmation prompt. |

### Description

Deletes every Codex snapshot cswap holds, including the Keychain items on macOS,
then removes `<backup_root>/codex/`. The codex CLI's own login in `~/.codex` is
left alone. Without `--yes` it prompts; any answer other than `y` prints
`Cancelled`. `cswap purge` removes the Codex store with the rest of the backup
root.

### Files

Removes `<backup_root>/codex/` and the Keychain items under service
`claude-swap-codex`. Takes no store lock.

### Exit status

`0` on success, a cancelled prompt, or nothing to remove; `1` on a handled
error.

### Output

`No cswap Codex data to remove.` when there is none. Otherwise the prompt
`Remove <n> managed Codex account(s) and all cswap Codex data? Your ~/.codex
login is left alone. [y/N]`, then `Removed <n> Codex account(s) and <path>`.

### Errors

| Message (exit 1) | Condition |
|------------------|-----------|
| `refusing to purge: the Codex store root is empty` | The store root resolves to an empty path. |
| `refusing to purge <root>: it contains the codex login at <CODEX_HOME>` | The store root, with symlinks resolved on both sides, is `CODEX_HOME` or contains it, so the removal could reach the live login. A root or home that cannot be resolved is refused the same way. |

### Example

```text
$ cswap codex purge --yes
Removed 2 Codex account(s) and ~/.local/share/claude-swap/codex
```

### See also

`cswap codex remove`, `cswap purge`.

---

## cswap codex import-codex-auth

### Synopsis

```text
cswap codex import-codex-auth
```

### Description

Re-runs the codex-auth import by hand, whether or not cswap already holds Codex
accounts. It reads `~/.codex/accounts/registry.json` (schema 2, 3, or 4) and the
matching `<key>.auth.json` snapshots beside it, and upserts each account into
cswap's store with its email, plan, workspace name, and alias. Plan names from
a schema below 4 are normalized to schema 4 names; schema 4 plans are taken as
they are, and a plan that is not a string becomes empty. Schema 2 rows carry no account key and
are skipped. A row whose auth file is missing or unreadable is skipped, and so
is a row whose auth file carries an identity that is not the row's account key.
An alias goes through the same rule as `cswap codex alias`; one that rule
rejects is dropped and the row is still imported. The registry's active account
becomes cswap's recorded active account when it is stored. `~/.codex/accounts/`
is never written. A registry with a schema newer than 4 imports nothing.

### Files

Reads `~/.codex/accounts/`. Writes `codex/sequence.json` and the snapshots
under `codex/.lock`.

### Exit status

`0` on success; `1` on a handled error.

### Output

`Imported <n>, skipped <m>.` A registry whose schema is newer than 4 first
prints the unsupported-schema warning (see `cswap codex`), then `Imported 0,
skipped 0.`

### Errors

Handled errors print `Error: <message>` to stderr, exit 1.

### Example

```text
$ cswap codex import-codex-auth
Imported 2, skipped 0.
```

### See also

`cswap codex`, `cswap codex import`.

---

## cswap help

### Synopsis

```
cswap help
cswap -h | --help
```

### Description

Prints the usage line, the command list, the visible options, and the flag
examples. `-h` / `--help` after any subcommand prints that subcommand's usage
line instead.

### Files

None. `cswap help` touches no files.

### Exit status

`0`.

### Output

The full help text (usage line, `Commands:` block, `options:` block, examples,
and the legacy-spelling note).

### Errors

None. `cswap help` cannot fail.

### Example

```
$ cswap help
usage: cswap <command> [args] [options]

Multi-Account Switcher for Claude Code

Commands:
  cswap help                       show this help
  cswap list                       list managed accounts
  ...
Aliases: ls=list  rm=remove  update=upgrade
  ...
```

### See also

`cswap --version`.

---

# Global reference

## ENVIRONMENT

cswap reads the following environment variables.

| Variable | Effect |
|----------|--------|
| `CLAUDE_CONFIG_DIR` | Overrides the Claude Code config home: the global config is `<dir>/.claude.json` and credentials are `<dir>/.credentials.json`. `cswap env` and `cswap run` treat any set value as a preset and override it with the session profile they prepare. For every other command the effect depends on the value: a cswap session pin — a value inside `<backup_root>/sessions/`, as set by `cswap env` — is ignored, and the command operates on the default login and prints `This shell is pinned via cswap env; operating on the default login.` to stderr; a custom value outside `<backup_root>/sessions/` is honored. The default-profile mirror used by session sharing deliberately ignores this and reads the real `~/.claude` profile. |
| `XDG_DATA_HOME` | On Linux/WSL, selects the backup-root parent: `<XDG_DATA_HOME>/claude-swap`. Ignored when unset, empty, or non-absolute; a leading `~` is expanded. |
| `HOME` | The user's home directory; the base for `~/.claude.json`, `~/.claude/`, `~/.local/share`, `~/.claude-swap-backup`, and `~/go/bin`. |
| `WSL_DISTRO_NAME` | When non-empty, the platform is detected as WSL (which uses the Linux/WSL backup-root layout). |
| `CONTAINER`, `container` | When either is non-empty, the root-user guard is bypassed (running as root is permitted in a container). |
| `NO_COLOR` | When present (even empty), disables ANSI color. Highest color precedence. |
| `FORCE_COLOR` | When present, forces color on (unless `NO_COLOR` is also present). |
| `TERM` | `TERM=dumb` disables color on a TTY. |
| `CODEX_HOME` | The codex CLI's home, resolved as the codex CLI resolves it: the live login is `<CODEX_HOME>/auth.json` and codex-auth's registry `<CODEX_HOME>/accounts/registry.json`. Defaults to `~/.codex`. |
| `GOBIN`, `GOPATH` | Consulted by `cswap upgrade` and the passive update notice to detect a `go install` layout. |

Color precedence: `NO_COLOR` present → off; else `FORCE_COLOR` present → on;
else non-TTY → off; else `TERM=dumb` → off; else on. The result is computed once
per process and cached.

**Authentication-override variables (scrubbed).** The following make `claude`
bypass account OAuth. `cswap run` and `cswap env` scrub them from the prepared
session environment (and `cswap env` emits an unset line for each one currently
set, so the calling shell drops it too):

- `ANTHROPIC_API_KEY`
- `ANTHROPIC_AUTH_TOKEN`
- `CLAUDE_CODE_OAUTH_TOKEN`
- `CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR`
- `CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR`

They are not scrubbed on the same-account fast path or when `cswap run` falls
through to launching the default login.

## FILES

The **backup root** is platform-dependent:

| Platform | Backup root | Credential store |
|----------|-------------|------------------|
| Linux / WSL | `${XDG_DATA_HOME:-~/.local/share}/claude-swap/` | file-based, under the backup root |
| macOS | `~/.claude-swap-backup/` | login Keychain, service `claude-swap` |
| Windows | `~/.claude-swap-backup/` | file-based, under the backup root |

A legacy `~/.claude-swap-backup/` on Linux/WSL is migrated to the XDG location
on first use.

Inside the backup root:

| Path | Contents |
|------|----------|
| `sequence.json` | The account registry: `activeAccountNumber`, `lastUpdated`, the `sequence` array of slot numbers, and an `accounts` map keyed by slot (`email`, `uuid`, `organizationUuid`, `organizationName`, `added`, and optional `alias`, `kind: "api_key"`, `disabled: true`). |
| `settings.json` | Settings (see SETTINGS). |
| `mappings.json` | Directory→account mappings (`schemaVersion`, `mappings` keyed by absolute path). |
| `autoswitch_state.json` | `cswap auto` cooldown / quarantine state, guarded by `.autoswitch_state.lock`. |
| `configs/` | Per-account config snapshots, `.claude-config-<n>-<email>.json`. |
| `credentials/` | Per-account credential files, `.creds-<n>-<email>.enc` (file backend). macOS stores these in the Keychain instead. |
| `sessions/` | Per-account session-mode profiles, `<n>-<email>` (the `@` in the email replaced by `_`), created by `cswap run` / `cswap env`. |
| `cache/usage.json` | Cached usage measurements. |
| `cache/update_check.json` | Last passive update-check result (`{"timestamp": <epoch-seconds>, "data": <latest-version>}`). |
| `claude-swap.log` | Rotating log file, 1 MB per file, 3 backups (`claude-swap.log.1`, `.2`, `.3`). |
| `codex/sequence.json` | The Codex slot registry: `accounts` keyed by slot (`account_key`, `email`, `plan`, `workspaceName`, `alias`, `added`, `disabled`, `authMode`), `activeAccountKey`, `lastUpdated`. No secrets. |
| `codex/credentials/` | Per-account Codex `auth.json` snapshots, `<file key>.json`, mode 0600, keyed by account identity. macOS stores these in the Keychain under service `claude-swap-codex` instead, except a snapshot too large to reach the `security` command over stdin, which stays in this file (mode 0600) rather than on a command line; reads and deletes cover both places. |
| `codex/cache/` | The Codex usage cache (the same usage table format as `cache/usage.json`). |
| `codex/.lock` | The Codex store's lock, separate from the Claude lock. |

Claude Code's own files that cswap reads and writes:

| Path | Role |
|------|------|
| `~/.claude.json` (or `<CLAUDE_CONFIG_DIR>/.claude.json`, or the legacy `<config_home>/.config.json` when present) | The global config; the active `oauthAccount` lives here. |
| `~/.claude/.credentials.json` (file backend) | The active OAuth credentials. |

The codex CLI's files that cswap reads and writes:

| Path | Role |
|------|------|
| `~/.codex/auth.json` (or `<CODEX_HOME>/auth.json`) | The live Codex login; it decides which Codex account is active. `cswap codex switch` rewrites it. |
| `~/.codex/accounts/` | codex-auth's registry and snapshots; read during the import, never written. |

## SETTINGS

`settings.json` holds one section, `autoswitch`, plus a `schemaVersion` (1).
Every key, with its type, range, default, and meaning:

| Key | Type | Range | Default | Meaning |
|-----|------|-------|---------|---------|
| `autoswitch.threshold` | float (percent) | 50–99.9 | 90 | Switch when the binding 5h/7d window reaches this percent. |
| `autoswitch.intervalSeconds` | float (seconds) | 15–3600 | 60 | Poll interval for the `cswap auto` loop. |
| `autoswitch.codexEnabled` | bool | — | true | Also auto-switch Codex accounts in the `cswap auto` loop. A no-op without Codex accounts. |
| `autoswitch.codexThreshold` | float (percent) | 0–99.9 | 0 | Codex-only switch threshold; 0 uses `autoswitch.threshold`. |
| `autoswitch.cooldownSeconds` | float (seconds) | 0–86400 | 300 | Minimum seconds between proactive switches. |
| `autoswitch.hysteresisPct` | float (percent) | 0–50 | 10 | A switch target must beat the active account by at least this many percent. |
| `autoswitch.strategy` | choice | `best`, `soonest-reset` | `best` | How auto-switch orders qualifying targets: `best` (most headroom) or `soonest-reset` (earliest weekly renewal). See below. |
| `autoswitch.includeApiKeyAccounts` | bool | — | false | Allow rotating onto managed API-key accounts (billed per token). Claude only; Codex API-key accounts are never rotation targets. |
| `autoswitch.unhealthyTicks` | int | 1–100 | 3 | Consecutive failed polls before an account is treated as unhealthy. |
| `autoswitch.model` | string | — | (none) | Also switch on these models' weekly limits (for example `Fable`, `Fable,Opus`, or `all`). |

Reads are forgiving: a missing file, a bad type, or an out-of-range value
degrades to the (clamped) default without error. Writes via `cswap config set`
are strict: an out-of-range or mistyped value is rejected with a `ConfigError`.
A whole-number float is stored and shown without a fractional part
(`80.0` → `80`).

**`autoswitch.strategy` ordering.** The strategy governs only the order in
which already-qualifying candidates are offered to `cswap auto`; it changes
none of the qualification gates. Known and positive headroom, the cooldown,
quarantine exclusion, and the API-key last resort apply identically under
both values and for every trigger (`proactive`, `at-limit`, `failover`). The
threshold-landing and hysteresis checks apply only under the `proactive`
trigger — an `at-limit` or `failover` tick must leave the active account
regardless, so refusing every imperfect target would strand it — and a
qualifying candidate reached by either of those two triggers may therefore
sit at or above the threshold.

- `best` orders candidates by headroom, most remaining first; accounts tied
  on headroom keep sequence order. This is the setting's default.
- `soonest-reset` orders candidates by *renewal time*, in two tiers. The
  first tier holds every candidate below the threshold — headroom such that
  `100` minus headroom is under `autoswitch.threshold` — and ranks by the
  latest parseable `resets_at` among the account's weekly-scope windows: the
  7-day window plus every per-model scoped window matched by
  `autoswitch.model`. The 5h window is never part of the renewal, since it
  is not weekly. A window whose `resets_at` is absent or unparseable is
  skipped; an account with no parseable weekly `resets_at` at all has an
  *unknown* renewal. Within this tier, a known renewal sorts before an
  unknown one; among known renewals the earliest sorts first; ties — an
  equal renewal, or two unknown renewals — fall back to headroom descending,
  then to sequence order. The second tier holds every candidate at or above
  the threshold and ranks by headroom descending, as a last resort. This
  tier is reachable only under the `at-limit` and `failover` triggers: under
  `proactive`, the threshold-landing gate above already excludes such a
  candidate from qualifying at all, so the second tier is always empty and
  `soonest-reset` orders proactive candidates exactly as the first tier
  describes. Every first-tier candidate sorts before every second-tier
  candidate — a candidate at or above the threshold is never preferred over
  one below it merely for an earlier renewal.

```
$ cswap config set autoswitch.strategy soonest-reset
autoswitch.strategy = soonest-reset
```

**`autoswitch.model` name matching.** A model name is matched against the
scoped weekly window's display name, compared case-insensitively and exactly.
The value stored and matched is the bare display name (for example `Fable`), not
a decorated form; a name that matches no window is silently inert (no window is
counted and no error is raised). The `--strategy` command-line flag and the
usage-aware `switch`/`auto` strategies accept `best` and `next-available` — a
separate vocabulary from the persisted `autoswitch.strategy` setting, which
accepts `best` and `soonest-reset`.

This matching decision is visible, not just enforced: `cswap tui`'s
auto-switch screen (above) lists every per-model weekly window an account
reports in its "Next best" candidates panel whether or not `autoswitch.model`
names it, but only a window this setting matches counts toward that row's
binding percentage and rank. An unmatched window renders muted for
visibility alone while it still has room left, so a user can watch a
model's weekly window climb before deciding to fold it into the switching
decision; once it runs out it carries its usual severity color regardless
of the match, the same as any other exhausted window (above).

## EXIT STATUS

| Code | Meaning |
|------|---------|
| `0` | Success. For `switch`, includes a no-op "already active". |
| `1` | A handled domain error (a `ClaudeSwitchError`: config, switch, session, credential, lock, transfer, or migration). Also printed for guidance-only `upgrade`. |
| `2` | A usage error: an unknown flag, a bad value, a missing required argument, or an illegal flag combination (the argument-parser surface). |
| `3` | `cswap auto --once` only: blocked — a switch was wanted but no viable target exists / all accounts exhausted. |
| `130` | Interrupted by SIGINT (Ctrl-C). |

For `cswap auto --once` the status is the tick outcome: `0` switched, `1` error,
`2` no action, `3` blocked. `cswap run` on success is replaced by the launched
`claude` and exits with its status. `cswap menubar` always exits `1` in this
build.

## JSON OUTPUT CONTRACT

`--json` is accepted by `list`, `status`, `switch` (and `switch <id>`),
`config list`, and `config get`; `cswap auto --json` emits JSONL. Every JSON
payload carries `"schemaVersion": 1`.

`cswap codex list --json` and `cswap codex status --json` also carry
`schemaVersion` 1, add `"provider": "codex"`, and are documented under those
commands. Both use the camelCase usage encoding of `cswap list` / `cswap
status`. Slot numbers are strings in the `list` payload (`number`,
`activeNumber`) and an integer in the `status` payload. Errors under `--json`
in the `codex` namespace use the same error envelope as every other verb. The
Codex line in `cswap auto --json` is described under `cswap auto`.

- **One document per command.** `list`, `status`, `switch`, and `config`
  write exactly one indented (2-space) JSON document to stdout.
- **JSONL for `auto`.** `cswap auto --json` writes one compact JSON object per
  event, one per line.
- **Additive rule.** Under schemaVersion 1 the schema only grows: optional keys
  (`alias`, `disabled`, `atLimit`, `limitingWindows`, `usageFetchedAt`,
  `usageAgeSeconds`, and the window sub-keys `resetsAt`, `countdown`, `clock`)
  are present only when they apply and omitted otherwise; no existing key
  changes type or meaning. Consumers must ignore unknown keys and, for `auto`,
  unknown `event` kinds.
- **Error envelope.** A handled error under `--json` is emitted as:
  ```
  {
    "schemaVersion": 1,
    "error": { "type": "<ErrorClass>", "message": "<text>" }
  }
  ```
  On the main commands the envelope is indented on stdout; for `cswap auto` it
  is compact on stdout. `<ErrorClass>` is one of `ConfigError`, `SwitchError`,
  `SessionError`, `ValidationError`, `AccountNotFoundError`, `CredentialError`,
  `CredentialReadError`, `CredentialWriteError`, `LockError`,
  `ClaudeCodeLockTimeout`, `TransferError`, `MigrationError`, or
  `MigrationIncomplete`.
- **`auto` event kinds** and their fields are enumerated under `cswap auto`
  above (`poll`, `switch`, `no-switch`, `account-quarantined`,
  `account-unquarantined`, `all-exhausted`, `sleep`, `error`,
  `config-warning`). Every event also carries `event` and `ts` (RFC3339 UTC,
  `Z` suffix).

## SIGNALS

| Signal | Effect |
|--------|--------|
| `SIGINT` (Ctrl-C) | Prints a dimmed cancellation note (to stderr under `--json`, otherwise stdout) and exits `130`. During `cswap auto` the note reads `Auto-switch stopped`; elsewhere `Operation cancelled`. |
| `SIGTERM` | During the `cswap auto` loop, stops the loop cleanly and exits `0` (the systemd-stop path). Other commands take the default action. |

## COMPATIBILITY

- The on-disk formats are shared with claude-swap (Python): `sequence.json`,
  `settings.json`, `mappings.json`, the per-account `configs/` and
  `credentials/` files, `cache/usage.json`, and the log format are read and
  written identically. A backup directory created by either implementation
  works in place with the other.
- The `.cswap` export file format is identical; either implementation reads the
  other's export files byte-for-byte. Exports are always written with
  `"encrypted": false`, and an export marked `"encrypted": true` is rejected on
  import by both.
- The command grammar, `--json` schemas, exit codes, and the Claude Code
  credential-lock protocol are preserved. The legacy `--flag` spellings work
  alongside the memorable verbs.
- This implementation defines these behaviors differently from claude-swap
  (Python):
  - It ships as a single static binary; there is no Python runtime.
  - The macOS menu bar app is not included (`cswap menubar` exits 1).
  - `cswap upgrade` and the passive update notice use this repository's releases
    and `go install`, not PyPI/uv/pipx. Pre-release builds do receive update
    notices.
  - `cswap env` is provided; it has no claude-swap (Python) counterpart.
  - `cswap list` / `cswap status` (human and `--json`) and the TUI surface an
    at-limit marker that folds in the per-model weekly windows configured by
    `autoswitch.model`. The additive `--json` fields `atLimit` and
    `limitingWindows` are present only when an account is at limit.
  - The `cswap codex` namespace, the Codex engine in `cswap auto`, and the
    `autoswitch.codexEnabled` / `autoswitch.codexThreshold` settings port
    claude-swap PR #252, which the pinned Python reference does not contain.
    The Codex store (`codex/`), the Codex export format (`version` 1,
    `provider: "codex"`), and the Codex `--json` field names follow that PR, so a
    Codex store written by either implementation reads in the other. cswap
    deviates from the PR where the PR is inconsistent with the rest of the
    CLI (`schemaVersion` and the camelCase usage encoding in the `--json`
    payloads, the error envelope, `--debug`, the `cswap auto` event line);
    DESIGN Amendment A22 lists the deviations.

## NOTES

- **macOS Keychain propagation (~30 s).** On macOS, a running Claude Code
  process reads credentials through a Keychain cache and may take up to about 30
  seconds to observe a just-switched account. `cswap switch` on macOS prints
  `Restart Claude Code to apply immediately — otherwise the session can take up
  to ~30 seconds to pick up the new account.` Restarting Claude Code applies the
  change at once. `cswap auto` switches proactively (while the old account is
  still valid) so this latency is harmless.
- **A switch never requires a restart to be correct.** On the file-credential
  platforms (Linux, WSL, Windows) the new account is active on the next message;
  `cswap switch` prints `New account is active on your next message — no restart
  needed.` The restart advice on macOS is only about applying the change sooner
  than the Keychain cache would.
- **Same account in two places can drift.** An account held as both the default
  login and a session-mode profile can make one copy's token go stale if the
  server rotates it, because each profile refreshes independently. `cswap run`
  and `cswap env` do not create a second copy for an account that is already the
  active default login — `run` launches the default login directly and `env`
  exports nothing — so this arises only when the two roles are established
  separately, for example pinning an account with `cswap env` and later making it
  the default login with `cswap switch`. If a session later fails to
  authenticate, exit it and re-run `cswap run <n>`. `cswap list` flags two slots
  that report identical usage and reset times as possibly the same account.
- **At-limit is independent of `usageStatus`.** `atLimit` reflects a relevant
  rate-limit window (including a per-model weekly window from `autoswitch.model`)
  being at or over its limit. An account can be `usageStatus: "ok"` and still
  `atLimit: true` when a scoped weekly window is exhausted even though the
  account-wide 7d window has room; conversely `atLimit` is absent whenever no
  window is at its limit. Treat the two fields as orthogonal.
- **macOS Keychain unavailable.** When the login Keychain is locked or busy,
  cswap does not misreport the active account as having no credentials. A read of
  the active account's OAuth credential is retried briefly and, on continued
  failure, surfaces as `usageStatus: "keychain_unavailable"` (human line
  `keychain unavailable — locked or in use; try again`) rather than
  `no_credentials`. Backup-store credential reads and writes fall back to
  file-based storage for the rest of the process run, so a flaky Keychain never
  blocks a switch.
- **Auto-switch quarantine.** During a real (non-`--dry-run`) `cswap auto` tick,
  a slot whose token cannot be freshened is quarantined: excluded from the
  auto-switch candidate pool until recovered. A quarantine is raised for
  `invalid_grant` (the stored refresh-token lineage is dead) or
  `identity-conflict` (the stored credential now authenticates as a different
  account or organization); the `account-quarantined` event carries the slot
  `number`, `email`, and this `reason`, and its human line is `Account-<n>
  (<email>) quarantined: <reason>. Log in with it and run 'cswap --add-account
  --slot <n>' to recover.` The state persists in `autoswitch_state.json`.
  Quarantine affects only `cswap auto`; a direct `cswap switch <n>` still
  activates a quarantined account. A quarantine is released automatically at the
  next real tick once the slot's recorded email changes (`account-replaced`) or
  its stored credential fingerprint changes (`credentials-replaced`) — which is
  exactly what re-running `cswap add --slot <n>` (or `cswap add-token ... --slot
  <n>`) after logging the account back in produces; an `account-unquarantined`
  event is then emitted.
- **Passive update notice.** After most commands, a muted one-line update notice
  may be printed to stderr when a newer release is known. It is suppressed in
  `--json` mode and after `purge` and `upgrade`, and it never affects the exit
  status.

## See also

Project overview: `README.md`. Architecture and design decisions:
`docs/DESIGN.md`.
