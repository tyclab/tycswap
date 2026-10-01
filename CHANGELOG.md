# Changelog

## Unreleased

- **Breaking:** renamed to tycswap. The module is `github.com/tyclab/tycswap`, the binary `tycswap`, the store `~/.local/share/tycswap` (Linux/WSL, `$XDG_DATA_HOME/tycswap` when set) or `~/.tycswap` (macOS/Windows), the log `tycswap.log`, the macOS Keychain services `tycswap` and `tycswap-codex`, and exports are named `.tycswap`. There is no `cswap` alias and no fallback to the old store; `tycswap migrate [--dry-run] [--json]` copies an existing claude-swap store once and never changes it. The startup move of `~/.claude-swap-backup` is gone, and `purge` no longer removes that directory.
- `cswap add --login [--switch]` runs `claude auth login` in a scratch profile and stores the second account without a logout; the live login stays untouched.
