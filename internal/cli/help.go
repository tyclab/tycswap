// help.go — the main parser's usage / description / options / epilog text
// (spec 08§3, VERBATIM per spec 08§14 "keep working" note).
//
// Implements spec 08§3 (canonical command list + epilog) and 08§14 (--help
// structure). The description and epilog are byte-for-byte the Python strings
// with %(prog)s rendered as the resolved program name; the visible options
// block lists only the non-suppressed flags (the legacy --flag group stays
// hidden, spec 08§3.2).
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/tyclab/tycswap/internal/version"
)

// usageLine is argparse's `usage=` for the main parser (spec 08§3).
func usageLine(prog string) string {
	return "usage: " + prog + " <command> [args] [options]"
}

// mainDescription is the RawDescription help body (spec 08§3), verbatim. Every
// literal "tycswap" is a %(prog)s slot, including the purge line's "remove all
// tycswap data", which names the program the user invoked.
const mainDescription = `Multi-Account Switcher for Claude Code

Commands:
  tycswap help                       show this help
  tycswap list                       list managed accounts
  tycswap status                     show current account
  tycswap switch                     rotate to the next account
  tycswap switch <num|email>         switch to a specific account
  tycswap add                        add the current account
  tycswap add --login [--switch]     log another account in beside the live one
                                   (claude auth login), then store it
  tycswap add-token [TOKEN|-]        register a setup-token or API key
  tycswap remove <num|email>         remove an account
  tycswap disable <num|email>        hold an account out of auto-rotation
  tycswap enable <num|email>         return a disabled account to rotation
  tycswap run <num|email> [-- ...]   run as an account, this terminal only
  tycswap run                        run the current dir's mapped account
  tycswap env <num|email>            print an eval-able CLAUDE_CONFIG_DIR export for this shell
  tycswap map <num|email> [path]     map a directory to an account
  tycswap map                        list directory mappings
  tycswap unmap [path]               remove a directory mapping
  tycswap alias <num|email> <name>   set a short alias for an account
  tycswap alias <num|email> --unset  remove an account's alias
  tycswap alias                      list all aliases
  tycswap swap <a> <b>               exchange two accounts' slot numbers
  tycswap move <a> <slot>            assign an account to a slot (swaps if taken)
  tycswap auto                       auto-switch when nearing rate limits
  tycswap config [set KEY VALUE]     show or change settings (settings.json)
  tycswap export <path>              export accounts
  tycswap import <path>              import accounts
  tycswap tui                        interactive dashboard (also: bare tycswap)
  tycswap watch                      dashboard, opened on the live watch page
  tycswap web                        browser dashboard on 127.0.0.1
  tycswap menubar                    macOS menu bar app
  tycswap upgrade                    self-upgrade to latest
  tycswap purge                      remove all tycswap data
  tycswap migrate [--dry-run]        copy the claude-swap store into this one, once

Codex (ChatGPT) accounts — the commands above stay Claude-only:
  tycswap codex list                 list Codex accounts and usage
  tycswap codex status               show the account codex is running as
  tycswap codex switch               rotate to the next Codex account
  tycswap codex switch <num|email>   switch to a specific Codex account
  tycswap codex add                  store the current Codex login
  tycswap codex login                run ` + "`codex login`" + `, then store it
  tycswap codex remove <num|email>   remove a Codex account
  tycswap codex alias <num> <name>   set a short alias (--unset to clear)
  tycswap codex disable|enable <n>   hold out of / return to auto-rotation
  tycswap codex swap <a> <b>         exchange two Codex slot numbers
  tycswap codex move <a> <slot>      assign a Codex account to a slot
  tycswap codex export <path>        export Codex accounts
  tycswap codex import <path>        import Codex accounts
  tycswap codex purge                remove all tycswap Codex data
  tycswap codex --help               full Codex help

Aliases: ls=list  rm=remove  update=upgrade`

// mainEpilog is the RawDescription epilog (spec 08§3), verbatim.
const mainEpilog = `Flags combine with subcommands:
  tycswap switch --strategy best           # pick the account with most quota left
  tycswap switch --strategy next-available # rotate, skipping rate-limited accounts
  tycswap switch user@example.com
  tycswap list --json
  tycswap add --slot 3                      # add to a specific slot
  tycswap add --login --alias work          # second account, the live one untouched
  tycswap add --login --switch -- --email me@example.com
                                          # log in, store, make it live
  tycswap add-token --email me@example.com # prompts for the token (or pipe it: add-token -)
  tycswap run 2 -- --resume                 # forward args after '--' to claude
  eval "$(tycswap env 2)"                   # pin THIS shell to account 2 (no claude launch)
  tycswap auto --once                       # single auto-switch tick (cron-friendly)
  tycswap config set autoswitch.sevenDayThreshold 92
  tycswap codex list --token-status         # Codex token expiry (never the token)
  tycswap codex list --json --skip-api      # machine-readable, no network

The original flag spellings (tycswap --switch, tycswap --list, ...) keep working.`

// visibleOptions is the --help options block for the non-suppressed flags
// (spec 08§3.1). The legacy --flag group is hidden (spec 08§3.2), so it never
// appears here — the substring check in spec 08§14 relies on this.
const visibleOptions = `options:
  -h, --help            show this help message and exit
  --version             show program's version number and exit
  --debug               Enable debug logging
  --token-status        Show OAuth token expiry state (use with 'list')
  --json                Emit machine-readable JSON to stdout (use with 'list',
                        'status', or 'switch')
  --strategy {best,next-available}
                        With bare 'switch': pick the target by remaining 5h/7d
                        quota
  --model NAMES         With 'switch --strategy': also count these models'
                        per-model weekly limits
  --slot NUM            Specify slot number when adding account (use with 'add'
                        or 'add-token')
  --email EMAIL         Email address for the account (use with 'add-token')
  --account NUM|EMAIL   Limit export to one account (use with 'export')
  --alias NAME          Set a short display alias for the account (use with
                        'add')
  --login               With 'add': run 'claude auth login' in a scratch
                        profile and store that account; the live login is
                        untouched. Args after '--' go to the login
                        (--email EMAIL, --sso; --console makes an API key,
                        which add refuses: use 'add-token')
  --switch              With 'add --login': switch to the new account after
                        storing it
  --force               Overwrite existing accounts during import; with
                        'switch <num|email>', activate without backing up first
  -y, --yes             With 'switch <num|email>' onto an API-key account:
                        answer the confirmation (for scripts)
  --full                Include full ~/.claude.json in export`

// renderMainHelp writes the full --help text (spec 08§14) and returns exit 0.
func renderMainHelp(prog string, out io.Writer) int {
	rep := func(s string) string { return strings.ReplaceAll(s, "tycswap", prog) }
	fmt.Fprintln(out, rep(usageLine(prog)))
	fmt.Fprintln(out)
	fmt.Fprintln(out, rep(mainDescription))
	fmt.Fprintln(out)
	fmt.Fprintln(out, visibleOptions)
	fmt.Fprintln(out)
	fmt.Fprintln(out, rep(mainEpilog))
	return 0
}

// renderVersion writes "<prog> <version>" (v stripped, DESIGN A5) and returns 0.
func renderVersion(prog string, out io.Writer) int {
	fmt.Fprintf(out, "%s %s\n", prog, version.Display())
	return 0
}
