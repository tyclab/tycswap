# tycswap — Idiomatic Go Architecture

A full-fidelity Go port of `claude-swap`, grounded in the behavioral specs in
`docs/port-spec/01–10` (10-audit.md corrections override the rest).

- Module: `github.com/tyclab/tycswap` · Go 1.25 · single binary `tycswap`
  (also installed as `claude-swap`).
- No cgo. macOS Keychain via the `security` subprocess exactly as Python.
- TUI: bubbletea v1.3 + lipgloss v1.1 + bubbles v1.0. HTTP: stdlib `net/http`.
  Extra deps limited to `golang.org/x/sys` (flock/`LockFileEx`, Windows PID
  probe, console VT) and `golang.org/x/text/unicode/norm` (NFC for
  `slugify_email` / keychain service hashing). No other third-party deps.
- Out of scope: the rumps macOS menu bar (`menubar.py`, spec 09 §10). The
  update-check/upgrade subsystem is redesigned for Go (§6, §7 below).

The 4,840-line `switcher.py` is **decomposed**, not transliterated. The
"switcher" becomes a thin façade (`core.Switcher`) over a shared substrate
(`store`) and three behavior packages (`lifecycle`, `switching`, `reporting`)
that operate on it as free functions. Seams are interfaces: credential store,
keychain client, OAuth/usage client, clock/sleeper, and consumer-defined
façade interfaces for the auto engine and TUI.

---

## 1. Package dependency graph (`internal/...`)

No import cycles. Arrows point "depends on". Layers are strictly downward.

```
cmd/tycswap ─────────────────────────────────────────────► internal/cli

                                       ┌──────────────► internal/tui
internal/cli ──────────────────────────┼──────────────► internal/autoswitch
   │  (front controller, dispatch,     ├──────────────► internal/transfer
   │   config cmd, exit codes)         ├──────────────► internal/session
   │                                   ├──────────────► internal/update
   │                                   └──────────────► internal/core
   │                                                        │
   ▼                                                        ▼
internal/core  (Switcher façade) ──► lifecycle ─┐   switching ─┐   reporting ─┐
   │                                             │              │              │
   └───────────────► internal/migrations         └──────┬───────┴──────┬───────┘
                                                         ▼              ▼
                                                   internal/store  (substrate)
   ┌───────────────────────────────────────────────────┼───────────────────────┐
   ▼                 ▼            ▼            ▼          ▼           ▼           ▼
credstore        usage        oauth      sessprofile  procdetect  mappings   filelock
   │  ┌──────────────┘            │          │            │          │           │
   ▼  ▼                          ┌┘          │            │          │           │
 ccfile   cclock                 │           │            │          │           │
   │        │                    │           │            │          │           │
   ▼        ▼                    ▼           ▼            ▼          ▼           ▼
 keychain  ── paths ── atomicfile ── platform ── clock ── cerr ── logging ── printer ── jsonout
                                  (leaves — no internal deps beyond each other)
```

Settings sits beside `usage` (depends `paths`, `atomicfile`, `cerr`,
`logging`); `cache` (TTL JSON) is a small file inside `usage`.

The Codex provider (Amendment A22) adds a second column of packages beside
`core`. It depends on the shared leaves, `usage`, and `reporting`; outside
the Codex packages it is imported only by `cli`, `tui`, and `providers`, and
`providers` is the only package that sees both `core` and `codex/switcher`.

```text
internal/cli ──► internal/providers ──► core, internal/codex/switcher ──► reporting
      │                                         │
      ├──► internal/codex/{registryimport,transfer,autoswitch}
      │                                         │
      ▼                                         ▼
internal/codex/usagecache ──► internal/codex/store ──► internal/codex/authfile
      │         │                    │                         │
      ▼         ▼                    ▼                         ▼
    usage   internal/codex/api   keychain, filelock,     paths, atomicfile,
                  │              atomicfile, clock        platform
                  ▼
           oauth, logging          internal/codex/procdetect (stdlib only)
```

### What each package owns

| Package | Owns (spec §) |
|---|---|
| `platform` | `Platform` enum + `Detect()` via `runtime.GOOS`+`WSL_DISTRO_NAME`; `IsWindows()` chmod gate; container detection (env `CONTAINER`/`container`, `/.dockerenv`, cgroup `docker\|lxc\|containerd\|kubepods`, mountinfo `docker\|overlay`). (03§1, 08§15, 10 Gap3) |
| `clock` | `Clock` (wall `Now`), `Sleeper`, `Timer`; `System` + `Fake` impls. Two clocks: wall for persisted state/staleness, monotonic (via `time.Since`) for acquire timeouts/cooldown. (03§9.2, 05§22.5) |
| `cerr` | Typed error set mirroring the Python hierarchy; `Kind` string == Python class name for the JSON `error.type`; `TypeName`, `IsClaudeSwitchError`, constructors. (08§11) |
| `atomicfile` | The single unified atomic-write helper (temp-in-dir → chmod → rename), `WriteJSON`/`WriteJSONValidated` (round-trip re-parse for `sequence.json`), `Write`; parent mkdir + 0700/0600 gated on non-Windows. (01§2.3, 07§9) |
| `logging` | Named `"claude-swap"` logger; **lazy** dir creation on first write; rotating 1 MB × 3. (08§12) |
| `printer` | ANSI styles, color detection precedence (`NO_COLOR`/`FORCE_COLOR`/TTY/Windows-VT/`TERM=dumb`) with first-call caching; `force_color` ctx; Windows console UTF-8 + VT; entrypoint/IDE labels, `abbreviate_path`, `format_age`. (02§11, 08§10) |
| `jsonout` | schema-v1 envelope builders: `account_row`, `usage_to_json`, `error_envelope`, freshness fields, `usageStatus` mapping. camelCase, optional-key omission. (02§10, 08§9) |
| `paths` | All path resolution: backup root (XDG/legacy), Claude config home/global/default-global/credentials, legacy backup dir + `MigrateLegacyBackupDir` protocol. (03§2) |
| `keychain` | `/usr/bin/security` wrapper (get/set/delete/item_exists), exact argv, hex `-X`, single-`\n` strip, rc 44, 5s timeout; `KeychainClient` interface + real + in-memory fake; `IsUnusable(err)` = `KEYCHAIN_ERRORS`. (03§4) |
| `ccfile` | Claude Code file I/O: read `~/.claude.json` as `map[string]any`, key-scoped atomic RMW preserving unknown keys, `oauthAccount` splice, raw `.credentials.json` read/write. (03§3, 03§5.5) |
| `cclock` | proper-lockfile interop: directory `mkdir` mutex, 10s staleness (wall mtime), 3s touch goroutine, 9s acquire (monotonic), `[0.25,0.50)` jitter; `ClaudeCodeLockTimeout`. (03§6) |
| `filelock` | tycswap's own advisory lock (`flock`/`LockFileEx`, 0.1s poll, 10s default), non-reentrant. `LockError`. (03§7) |
| `credstore` | `Store` interface + `FileKeychainStore`: active OAuth/managed-key read/write, backup `.enc`/Keychain routing, usability cache (None/T/F + 60s monotonic cooldown + sticky pin), `.prev`, unclaimed stash, `looks_like_api_key`, `approved_form`. (03§5, 01§3) |
| `oauth` | Network client (`Client` interface + HTTP impl + fake): refresh/profile/usage, normalization (`build_usage_result`), classification tokens, `relevant_windows`, `account_headroom`, fingerprint, reset formatting. Owns the normalized `Usage` type. (04§1) |
| `usage` | `UsageStore` (schema v2, identity-guarded, lock protocol), `UsageEntry`/`FetchRecord`, poll policy (`plan_after_fetch`, backoff, due-candidate), TTL `cache`. (04§2–4) |
| `settings` | `settings.json` store, `SETTING_SPECS`, `AutoSwitchSettings` (frozen value), clamp, `config set/get/unset`, `merged_with_cli`, `parse_model_names`. (08§8, 05§2) |
| `sessprofile` | Session-profile leaf: dir naming (`slugify_email`, `session_dir_for`), NFC keychain service hashing, invalidation/stale-marker/delete. (06§1.5, breaks store↔session cycle) |
| `procdetect` | Read Claude Code `sessions/*.json` + `ide/*.lock`, `is_pid_alive` (POSIX `kill 0`, Windows `OpenProcess`). (06§4) |
| `mappings` | `MappingStore`: `mappings.json`, `normalize_path`, nearest-ancestor `resolve`, prune. (06§5) |
| `store` | The substrate: `sequence.json` model + read/write, path wiring, credential proxies (+`_post_backup_write`), identity resolution, org-field backfill, live-session guards, `FileLock` handle, `UsageStore` handle, migrations-at-construct. (01, 03 wiring) |
| `lifecycle` | Free funcs over `*store.Store`: add/add-token/remove/move/swap/alias/disable/enable/purge. (01§5–11) |
| `switching` | switch/switch-to/best/next-available, `performSwitch`, `classifyOutgoing`, replan, self-switch provenance. (02§4–9) |
| `reporting` | list/status renderers + JSON payloads, `collectUsageEntries` (paced, identity-guarded), snapshots, duplicate/lockstep warnings. (02§10–13) |
| `core` | `Switcher` façade: composes `store` + `lifecycle`/`switching`/`reporting`; satisfies the façade interfaces `autoswitch`/`tui`/`session`/`transfer` consume. |
| `session` | `SessionManager`: run/exec-default, bootstrap/reuse, `_sync_sharing`, MCP mirror, share-history. (06§1–3) |
| `transfer` | `.tycswap` export/import (reads Python files). (07§1–4) |
| `migrations` | Registry migrations (windows keyring, macos keyring) + `.migrations.json` state; org backfill is in `store`. (07§5–6) |
| `autoswitch` | `AutoSwitchEngine`, events, tick, `run_loop`, quarantine, cooldown, freshen. (05) |
| `update` | Go-redesigned update check + upgrade guidance (§6). |
| `cli` | Two-layer front controller, verb→flag translation, dispatch, cross-flag validation, exit codes, config command. (08§1–7) |
| `tui` | bubbletea app/screens/widgets/modals/theme + `SnapshotSource`. (09§1–9) |
| `codex/authfile` | Codex paths (`CODEX_HOME`, live `auth.json`, codex-auth registry, `<backup root>/codex/` store layout), `auth.json` identity parsing (JWT claims, `AccountKey` = `user::account`, `FileKey`), atomic live-file write, plan normalization. (A22; PR #252 `codex/paths.py`, `auth_file.py`, `plans.py`) |
| `codex/procdetect` | Running `codex`/`codext` process detection by executable name; injectable lister; never fails. (A22; `codex/processes.py`) |
| `codex/api` | Codex network client (`Client` interface + HTTP impl + fake): token refresh, `wham/usage` fetch and normalization into the Claude usage shape (windows classified by length), `/backend-api/accounts` workspace names. (A22; `codex/oauth.py`, `usage.py`, `workspaces.py`) |
| `codex/store` | Codex slot registry (`codex/sequence.json`) and per-identity snapshot store (Keychain service `claude-swap-codex` on macOS, 0600 files elsewhere); the Codex lock. (A22; `codex/store.py`) |
| `codex/usagecache` | Adapter onto `usage.UsageStore` + poll policy with identity `(email, chatgpt_account_id)`; grouped-scope workspace-name refresh. (A22; `codex/usage_cache.py`, `workspaces.py`) |
| `codex/switcher` | Codex verbs: resolve, add, switch/rotate/best, remove, alias, disable, swap, move, status, token status, snapshots. Owns both Codex invariants. (A22; `codex/switcher.py`) |
| `codex/registryimport` | One-time, read-only import of codex-auth registries (schemas 2–4). (A22; `codex/registry_import.py`) |
| `codex/transfer` | Codex export (v1, `provider`, `warning`, 0600) / import / purge. (A22; `codex/transfer.py`) |
| `codex/autoswitch` | The small Codex auto engine: one threshold-and-hysteresis tick, never raises. (A22; `codex/autoswitch.py`) |
| `providers` | Provider interface, presence registry (Codex only when it has accounts or an importable registry), merged provider-major snapshot and row-key → owner map. (A22; `providers/*.py`) |

---

## 2. Per-package plan (files, responsibilities, key signatures)

Signatures are sketch-level. All fallible ops return `error` (a `cerr` value at
the domain boundary). "spec" tags the governing section(s).

### 2.1 `platform` — 03§1, 08§15, 10 Gap 3
`platform.go`
```go
type Platform int
const ( MacOS Platform = iota; Linux; WSL; Windows; Unknown )
func Detect() Platform                 // runtime.GOOS + WSL_DISTRO_NAME (non-empty ⇒ WSL)
func (p Platform) String() string      // "macos"/"linux"/"wsl"/"windows"/"unknown" (export tag)
func IsWindows() bool                   // chmod-gate helper
func RunningInContainer() bool          // env + /.dockerenv + cgroup + mountinfo (distinct substr sets)
```
Never calls `platform.system()`-equivalent (no WMI risk in Go; noted for parity).

### 2.2 `clock` — 03§9.2, 04§7.3, 05§22.5
```go
type Clock interface { Now() time.Time }                 // wall clock
type Sleeper interface { Sleep(time.Duration); After(time.Duration) <-chan time.Time }
func Seconds(c Clock) float64                             // Now().UnixNano()/1e9 (usage store / autoswitch clock() seam)
type System struct{}                                     // real
type Fake struct{ t time.Time }                          // tests; advanceable
```
Monotonic elapsed uses `time.Since(start)` (Go embeds the monotonic reading).

### 2.3 `cerr` — 08§11, 02§18
```go
type Error struct { Kind Kind; Msg string; wrapped error }
type Kind string   // "ConfigError","SwitchError","CredentialReadError",... == Python class names
func (e *Error) Error() string; func (e *Error) Unwrap() error
func Config(format string, a ...any) *Error            // + Switch, Session, Validation, AccountNotFound,
                                                       //   CredentialRead, CredentialWrite, Credential, Lock,
                                                       //   ClaudeCodeLockTimeout, Transfer, Migration, MigrationIncomplete
func IsClaudeSwitchError(err error) bool               // any *Error
func TypeName(err error) string                        // Kind string for JSON error.type; "" if not a *Error
```
`ClaudeCodeLockTimeout` and `LockError` are distinct Kinds; `TypeName` round-trips
the external script contract.

### 2.4 `atomicfile` — 01§2.3, 03§9.3, 07§9
```go
type Opts struct { FileMode, DirMode os.FileMode }      // default 0600/0700; skipped on Windows
func Write(path string, data []byte, o Opts) error      // CreateTemp(dir) → chmod → Rename; unlink temp on err
func WriteJSON(path string, v any, o Opts) error        // MarshalIndent(v,"","  ")
func WriteJSONValidated(path string, v any, o Opts) error // re-parse temp before Rename → cerr.Config("Generated invalid JSON")
```
Unifies `_write_json`, `atomic_write_json`, `_atomic_b64_write`, `_atomic_write_file`,
`_mark_applied` (07§9). `WriteJSONValidated` used for `sequence.json`.

### 2.5 `keychain` — 03§4, 03§9
```go
type KeychainClient interface {
    Get(service, account string) (string, bool, error)  // value, found(rc0), err; rc44 ⇒ (_,false,nil)
    Set(service, account, password string) error
    Delete(service, account string) error               // rc0|44 ⇒ nil
    Exists(service, account string) bool                // never errors; rc0 ⇒ true
}
func AccountName() string                               // $USER → os/user → "claude-code-user"
func IsUnusable(err error) bool                         // KeychainError | ctx-deadline | missing-binary (KEYCHAIN_ERRORS)
type Security struct{ Path string; Timeout time.Duration } // real: exec /usr/bin/security
type Fake struct{ m map[[2]string]string }              // conftest block_real_keychain parity
```
`Set` hex-encodes via `-X` on stdin `-i`; over the 4032-byte line limit it refuses with a
`TooLarge` `KeychainError` (never argv; see Amendment on the security pass);
`Get` uses `find-generic-password -a … -w -s …`, `strings.TrimSuffix(out,"\n")`
(not TrimSpace). 5s `exec.CommandContext` timeout → `IsUnusable`-classified.

### 2.6 `ccfile` — 03§3, 03§5.5
```go
func ReadGlobalConfig() (map[string]any, error)         // nil if absent; json→map preserving unknown keys
func UpdateGlobalConfig(mutate func(map[string]any)) error // key-scoped atomic RMW, preserves unknown keys
func ReadCredentialsFile() (string, bool, error)        // raw text, exists
func WriteCredentialsFile(raw string) error             // raw verbatim, atomic, 0600
func SpliceOAuthAccount(configText string, oauth map[string]any) (string, error) // preserve local keys
func ReadOAuthIdentity() (email, orgUUID string, ok bool) // ~/.claude.json oauthAccount
```

### 2.7 `cclock` — 03§6
```go
const ( StalenessS = 10*time.Second; TouchIntervalS = 3*time.Second; DefaultTimeoutS = 9*time.Second )
type Handle struct{ /* dir, stop chan, done chan */ }
func Acquire(lockDir string, timeout time.Duration, clk clock.Clock) (*Handle, error) // ClaudeCodeLockTimeout
func (h *Handle) Release()                               // stop toucher (join ≤1s) then rmdir; tolerate stolen
func CredentialsLockDir() string; func ConfigLockDir() string
```
`Acquire` spawns the touch goroutine (`time.Ticker(3s)`, `stop chan`, stops on first
`Chtimes` error). Staleness = `time.Now().Sub(fi.ModTime()) > 10s`; acquire timeout
monotonic. Jitter `0.25 + rand*0.25`.

### 2.8 `filelock` — 03§7
```go
type FileLock struct{ path string; timeout time.Duration; clk clock.Clock }
func New(path string, timeout time.Duration) *FileLock   // default 10s
func (l *FileLock) Acquire(timeout time.Duration) (bool, error) // false on timeout (no raise)
func (l *FileLock) Release() error                       // safe twice
func (l *FileLock) With(fn func() error) error           // ctx-manager form → cerr.Lock on false
```
POSIX `unix.Flock(fd, LOCK_EX|LOCK_NB)` 0.1s poll; Windows `LockFileEx` 1 byte.
**Non-reentrant** — documented; callers never nest.

Three measured facts (Linux; Windows status is A20 Known limitation (vii))
govern every caller built on `FileLock`, and are load-bearing for A20's RULE 4:

- **F1 — two handles, one process, same file, serialize like two processes.**
  Two independent `*FileLock` objects opened on the same path from within a
  single process contend at the OS `flock` level exactly as two separate
  processes would; there is no in-process fast path between them.
- **F2 — release on handle close or process death.** The OS-level lock
  (`flock` / `LockFileEx`) releases automatically when the holding file
  descriptor closes or its process exits; no explicit `Release` call is
  required, and a crashed tycswap leaves no stuck lock.
- **F3 — an in-process waiter on the SAME `*FileLock` object ignores its own
  timeout.** `Acquire` takes the `hold` mutex unconditionally before it enters
  its timeout loop, so a goroutine sharing one `*FileLock` with the current
  holder blocks on `hold` until that holder releases, regardless of the
  timeout the waiter asked for — measured: a waiter requesting a 500ms
  timeout blocked 3.001s, bounded only by the holder's own hold time. See A20
  Known limitation (ii).

### 2.9 `credstore` — 03§5, 01§3
`store.go` (interface), `filekeychain.go`, `active.go`, `backup.go`, `unclaimed.go`, `classify.go`
```go
type Store interface {
    ReadActive() (value string, keychainUnavailable bool, err error) // "" none, nil-string-as-"" per contract
    WriteActive(creds string) error                       // single-axis; OAuth clears managed & vice-versa
    ReadBackup(num, email string) (string, error)         // .enc-wins; "" missing
    WriteBackup(num, email, creds string) error           // retain .prev; routing; reconcile .enc
    DeleteBackup(num, email string) error                 // best-effort sweep incl account-None
    DeleteBackupStrict(num, email string) error           // fail-closed; cerr.Credential "aborting before commit"
    ReadPrev(num, email string) (string, error); DeletePrev(num, email string) error
    KCReadBackup(num, email string) (string, error)       // security-service-only (migrations)
    KCWriteBackup(num, email, creds string) error
    WriteUnclaimed(creds string, ctx map[string]any) (id string, err error)
    ListUnclaimed() (map[string]map[string]any, error)
    LastActiveBackend() string                            // "keychain"|"file"|"" (switch follow-up)
}
func LooksLikeAPIKey(s string) bool; func ApprovedForm(k string) string
func New(cfg Config, kc keychain.KeychainClient, clk clock.Clock, log *logging.Logger) *FileKeychainStore
```
`Config{ Platform, CredentialsDir }`. Usability cache (`*bool`/nil, monotonic
`disabledUntil`, `pinFileMode`) guarded by a mutex if shared. macOS-only Keychain
branch gated on `cfg.Platform==MacOS`.

### 2.10 `oauth` — 04§1
`client.go` (interface+HTTP+fake), `normalize.go`, `windows.go`, `classify.go`, `format.go`, `fingerprint.go`
```go
type Client interface {
    Refresh(ctx context.Context, creds string) RefreshOutcome            // 10s
    Profile(ctx context.Context, accessToken string) *Identity           // 5s; 401 fail-open
    Usage(ctx context.Context, accessToken string) (raw map[string]any, err error) // 5s
}
type RefreshOutcome struct { Credentials string; Error string; TokenAccount *Identity } // "invalid_grant"/"no_refresh_token"/"transient"
type Usage struct { FiveHour, SevenDay *Window; Spend *Spend; Scoped []ScopedWindow }    // normalized; float64 pcts
func BuildUsageResult(raw map[string]any) *Usage
func RelevantWindows(u *Usage, models []string) []Window                 // (label,pct,resetsAt); spend excluded
func AccountHeadroom(u *Usage, models []string) *float64                 // nil = unknown
func CredentialFingerprint(creds string) *string                        // sha256: / sha256-full:
func FormatReset(resetsAt string, now time.Time) (countdown, clock string, ok bool)
func FreshResetStrings(w *Window, now time.Time) (countdown, clock string, ok bool)
func TryFetchUsageForAccount(ctx, c Client, num, email, creds string, isActive bool, persist PersistFn) UsageOutcome
```
Error tokens (`"http-429"`, `"timeout"`, `"network"`, `"bad-response"`,
`"invalid_grant"`, …) are exact literals (04§7.5). `Retry-After` parse: seconds
only, negatives→0, HTTP-date→nil. HTTPError-before-URLError-before-Timeout ordering
via `errors.As` + status inspection. Refresh permanent iff status∈{400,401,403} AND
body contains `invalid_grant`/`invalid_client` (case-sensitive substring).

### 2.11 `usage` — 04§2–4
`store.go`, `entry.go`, `pollpolicy.go`, `cache.go`, `consts.go`
```go
const SchemaVersion = 2
type Identity struct{ Email, OrgUUID string }
type FetchRecord struct { Usage *oauth.Usage; Error, Sentinel string; RetryAfterS *float64 }
type UsageEntry struct { /* Sentinel, LastGood, FetchedAt, AgeS, ... TrustExtended */ }
func (e UsageEntry) DecisionValue() any                  // *oauth.Usage | string sentinel | nil
type Store struct{ path, lockPath string; clk clock.Clock }
func NewStore(cacheDir string, clk clock.Clock) *Store
func (s *Store) Entries(ids map[string]Identity) map[string]UsageEntry     // lock-free read
func (s *Store) Reserve(nums []string, ids map[string]Identity, respectPlans bool) []string
func (s *Store) Record(outcomes map[string]FetchRecord, ids map[string]Identity)
func (s *Store) Claim(...); func (s *Store) SetPollPlan(...); func (s *Store) ClearDeadToken(...)
func DueCandidate(cands []string, entries map[string]UsageEntry, now float64) string
func PlanAfterFetch(in PlanInput) (nextPollAt, intervalS float64)          // rng injectable
```
Legacy/foreign file (missing/corrupt/`schemaVersion!=2`) → empty (reads Python
version-less cache as empty; **reading a v2 Python cache works**). Identity guard
`_matches`. Lock protocol (a) lock-read-reserve (b) fetch unlocked (c) lock-merge-write.

### 2.12 `settings` — 08§8, 05§2
```go
type AutoSwitchSettings struct { /* Threshold, IntervalSeconds, CooldownSeconds, HysteresisPct, Strategy, IncludeAPIKeyAccounts, UnhealthyTicks, Model *string */ }
var SettingSpecs []Spec                                  // single source of truth; dotted key ↔ field
func Load(root string) AutoSwitchSettings                // forgiving clamp
func SetSetting(root, dotted, raw string) (any, error)   // strict; cerr.Config
func UnsetSetting(root, dotted string) (bool, error); func EffectiveSettings(root string) []Effective
func MergedWithCLI(s AutoSwitchSettings, o CLIOverrides) AutoSwitchSettings   // identity when no override
func ParseModelNames(v *string) []string                 // case-insensitive dedupe, first-spelling-wins
```

### 2.13 `store` — 01, 03 wiring
`store.go` (construct + fields), `sequence.go` (read/write/model), `identity.go`
(resolve, org backfill), `credproxy.go` (`_post_backup_write`), `guards.go`
(live-session), `dirs.go`.
```go
type Store struct {
    Home, BackupDir, SequenceFile, ConfigsDir, CredentialsDir, LockFile string
    Platform platform.Platform
    Creds credstore.Store; Usage *usage.Store; Lock *filelock.FileLock
    OAuth oauth.Client; Log *logging.Logger; Clk clock.Clock
}
func New(opts Options) (*Store, error)                   // reproduces __init__ order: home/platform → backup root
                                                         // → MigrateLegacyBackupDir (fallible) → derive paths →
                                                         // logging (lazy) → UsageStore → credstore → migrations.Run
type SequenceData struct { ActiveAccountNumber *int; LastUpdated string; Sequence []int; Accounts map[string]json.RawMessage }
func (s *Store) ReadSequence() (*SequenceData, error); func (s *Store) WriteSequence(*SequenceData) error
func (s *Store) SequenceMigrated() (*SequenceData, error)          // org backfill on read
func (s *Store) ResolveAccount(id string) (num, email, org string, err error)
func (s *Store) FindAccountSlot(d *SequenceData, email, org string) string
func (s *Store) WriteAccountCredentials(num, email, creds string) error   // credstore + _post_backup_write once
func (s *Store) EnsureNoLiveSession(num, email, action string) error      // procdetect via sessprofile
```
Account records kept as `map[string]any`/`json.RawMessage` so `alias`/`kind`/
`disabled` **absence** survives (never zero-filled). `sequence` ints, `accounts`
keys strings, `activeAccountNumber` int|null. Timestamp `2006-01-02T15:04:05Z` UTC.

### 2.14 `lifecycle` — 01§5–11
`add.go`, `addtoken.go`, `remove.go`, `moveswap.go`, `alias.go`, `disable.go`, `purge.go`
```go
func AddAccount(s *store.Store, slot *int, assumeYes bool, alias *string) error
func AddAccountFromToken(s *store.Store, token string, email, slotArg *string, assumeYes bool) error
func RemoveAccount(s *store.Store, id string, assumeYes bool) error
func MoveAccount(s *store.Store, account, target string) (srcNum, tgtNum string, swapped bool, err error)
func SwapAccounts(s *store.Store, a, b string) (numA, numB string, err error)
func SetAlias(s *store.Store, id, alias string) (num, normalized string, err error)
func UnsetAlias(s *store.Store, id string) (num string, err error); func ListAliases(...) 
func SetAccountDisabled(s *store.Store, id string, disabled bool) error
func Purge(s *store.Store) error
```
Interactive prompts (`Prompter` seam injected via `store.Options`) so TUI/tests
supply `assume_yes`/EOF behavior. Move/swap run the whole resolve-validate-mutate
span under one `FileLock` acquisition; best-effort vs fail-closed preserved per call
site (01§14, 02§18) — strict clears return errors that abort the commit; post-commit
cleanup logs and continues.

### 2.15 `switching` — 02§4–9
`switch.go`, `switchto.go`, `strategies.go`, `perform.go`, `classify.go`, `provenance.go`, `replan.go`
```go
func Switch(s *store.Store, strategy *string, jsonOut bool, models []string, modelSrc *string) (any, error)
func SwitchTo(s *store.Store, id string, jsonOut, force bool) (any, error)
func performSwitch(s *store.Store, target string, emit, forceActivate bool, prov *Provenance) (SwitchOp, error)
func classifyOutgoing(...) (kind string, foreignSlot string)     // 02§9 identity oracle
```
Triple lock `FileLock → cclock.Credentials → cclock.Config` in that order (03§7.4);
network refresh runs **outside** the FileLock (non-reentrant), persist callback
re-locks. `SwitchTransaction` rollback (reverse-order restore).

### 2.16 `reporting` — 02§10–13
`list.go`, `status.go`, `collect.go`, `snapshot.go`, `warnings.go`, `render.go`
```go
func ListAccounts(s *store.Store, showTokenStatus, jsonOut bool, fetch map[string]bool) (any, error)
func Status(s *store.Store, jsonOut bool) (any, error)
func CollectUsageEntries(s *store.Store, infos []AccountInfo, fetch map[string]bool) map[string]usage.UsageEntry
func AccountsSnapshot(s *store.Store, fetch map[string]bool) *AccountsSnapshot   // TUI/menubar consumer
func UsageByAccount(s *store.Store) map[string]any                                // strategies (DecisionValue)
```
`CollectUsageEntries` implements the static-sentinel → quarantine → reserve →
staggered fetch → record → replan pipeline (02§13), sentinels never persisted.
Fetch stagger `idx*250ms` via bounded goroutines (§4).

### 2.17 `core` — façade
```go
type Switcher struct{ *store.Store }
func New(opts store.Options) (*Switcher, error)
// thin delegations, e.g.:
func (sw *Switcher) AddAccount(slot *int, yes bool, alias *string) error { return lifecycle.AddAccount(sw.Store, slot, yes, alias) }
func (sw *Switcher) SwitchTo(id string, j, f bool) (any, error)          { return switching.SwitchTo(sw.Store, id, j, f) }
func (sw *Switcher) AccountsSnapshot(fetch map[string]bool) *reporting.AccountsSnapshot { ... }
// ...plus the methods the façade interfaces (§2.18/§2.20) require.
```

### 2.18 `autoswitch` — 05
`engine.go`, `tick.go`, `events.go`, `state.go`, `collect.go`, `select.go`, `freshen.go`, `loop.go`
```go
// Consumer-defined façade interface (idiomatic Go — defined HERE, implemented by *core.Switcher):
type Switcher interface {
    CurrentAccountNumber() *string; HasLiveLogin() bool; AccountEmail(num string) string
    SwitchableAccountNumbers() []string; AccountKindFor(num string) string
    AccountIdentity(num string) map[string]string; ReadAccountCredentials(num, email string) string
    PersistBackupCredentials(num, email, creds string) error; BackfillAccountUUID(num, uuid string)
    UsageEntriesByAccount(fetch map[string]bool) map[string]usage.UsageEntry
    SwitchTo(num string, jsonOut bool) (map[string]any, error)
    LiveSessionPidsFor(num, email string) []int
    SetPollPolicyInputs(threshold float64, models []string); ClearPollPolicyInputs()
    BackupDir() string
}
type Engine struct{ /* sw, settings atomic.Pointer, clk, onEvent, dryRun, stopCh, wakeCh, state file */ }
func NewEngine(sw Switcher, s settings.AutoSwitchSettings, onEvent func(Event), dryRun bool) *Engine
func (e *Engine) Tick() TickOutcome; func (e *Engine) RunLoop() int
func (e *Engine) Stop(); func (e *Engine) Wake(); func (e *Engine) ApplyThreshold(v float64)
type TickOutcome int   // SWITCHED=0 ERROR=1 NO_ACTION=2 BLOCKED=3 (value == --once exit code)
```
State file `autoswitch_state.json` under its own `.autoswitch_state.lock`
(`filelock`), `_mutate_state` never nested under another lock. `Tick()` never panics
out (recover→ErrorEvent, transient).

### 2.19 `session` — 06§1–3
`manager.go`, `bootstrap.go`, `sharing.go`, `mcp.go`, `history.go`, `exec_posix.go`, `exec_windows.go`
```go
type Manager struct{ sw *core.Switcher }
func (m *Manager) Run(id string, tail []string, share, shareHistory bool) error   // execs; never returns (POSIX)
func (m *Manager) ExecDefault(tail []string) error
func (m *Manager) SetupSession(id string, share, shareHistory bool) (dir, num, email string, err error)
```
`sessprofile` leaf holds dir naming/invalidation (used by both `store` and here,
breaking the cycle). MCP mirror + share manifest + history merge as spec §2–3. `_exec`
POSIX = `syscall.Exec` (replaces image; `FileLock` already released); Windows =
spawn+wait, Ctrl+C→130.

### 2.20 `tui` — 09
`app.go`, `dashboard.go`, `switchscreen.go`, `watchscreen.go`, `autoview.go`,
`widgets.go`, `snapshot.go`, `modals.go`, `theme.go`
```go
type Facade interface {   // consumer-defined; *core.Switcher satisfies it
    AccountsSnapshot(fetch map[string]bool) *reporting.AccountsSnapshot
    SwitchTo(id string, jsonOut bool) (map[string]any, error); Switch(...) (map[string]any, error)
    SetAccountDisabled(id string, disabled bool) error; RemoveAccount(id string, yes bool) error
    AddAccount(...) error; AddAccountFromToken(...) error; BackupDir() string
    SetPollPolicyInputs(float64, []string); ClearPollPolicyInputs()
}
func Run(f Facade, start string) int
```
bubbletea model holds single-flight bools; workers are `tea.Cmd`s returning typed
msgs; 3s poll via `tea.Tick`; engine hosted as a long-lived goroutine draining onto
a channel re-armed by a `tea.Cmd`. **Deliberate departure**: mutating ops return
structured results (no stdout capture) — see §6.

### 2.21 `cli` — 08§1–7
`main.go` (front controller), `translate.go`, `parse.go`, `dispatch.go`,
`config.go`, `run.go`, `map.go`, `alias.go`, `swapmove.go`, `auto.go`, `exit.go`
```go
func Main() int
```
Hand-rolled front controller mirroring Python `main()`: pre-dispatch first token
(`run/auto/config/map/unmap/alias/swap/move`) → bare-TTU gate → verb→flag translate
→ main flag parser → cross-flag validation (exit 2) → dispatch → single
`json.MarshalIndent` of the one payload → passive update notice. `--upgrade` runs
before constructing the switcher. Root guard (POSIX geteuid==0 & not container).

---

## 3. Cross-cutting conventions

### 3.1 Error taxonomy → exit-code mapping (mirrors the Python table, 08§11)

`cerr.Kind` strings are the external contract (JSON `error.type`):

| Go `cerr` Kind | Python class | Typical exit |
|---|---|---|
| `ConfigError`, `SwitchError`, `SessionError`, `ValidationError`, `AccountNotFoundError`, `CredentialError`, `CredentialReadError`, `CredentialWriteError`, `LockError`, `ClaudeCodeLockTimeout`, `TransferError`, `MigrationError`, `MigrationIncomplete` | same names | **1** (handled `ClaudeSwitchError`) |

Exit codes (single source in `cli/exit.go`):

| exit | when |
|---|---|
| 0 | success; `--version`/`--help`; TUI/config/run normal return; `auto --once` SWITCHED; upgrade success |
| 1 | any `cerr.IsClaudeSwitchError` (init or dispatch); root guard; `--menubar`; upgrade failure; `auto --once` ERROR |
| 2 | flag parse / cross-flag validation / "no command given" / unknown subcommand-action / mutually-exclusive |
| 3 | `auto --once` BLOCKED |
| 130 | SIGINT (Ctrl-C) — cancelled note, then exit |

`cli` maps: `cerr.IsClaudeSwitchError(err)` → JSON envelope on **stdout** (json mode)
or `printer.Error("Error: "+msg)` on **stderr** → exit 1. `auto --once` uses
`os.Exit(int(engine.Tick()))`.

### 3.2 JSON output envelope discipline (stdout/stderr split, 08§9, 02§17)

- Commands **return** `(payload any, err error)`; they never print JSON. `cli`
  performs exactly one `json.MarshalIndent(payload,"","  ")` to **stdout** after a
  clean dispatch — so stdout carries exactly one JSON document in `--json` mode.
- Handled errors in JSON mode → `jsonout.ErrorEnvelope` on **stdout**, exit 1,
  nothing on stderr.
- `auto` emits **compact** JSONL (`json.Marshal` + `\n`, flushed) per event; its
  error envelope is also compact.
- Human errors → stderr (red). Warnings → stdout (yellow). Passive update notice →
  stderr (muted). Skip-slot export warnings → stderr. Ctrl-C note → stderr if json
  else stdout.
- Optional keys (`alias`, `disabled`, `kind`, freshness fields, `resetsAt`,
  `countdown`/`clock`) are **omitted when unset** (pointers / `omitempty` /
  build-the-map), never zero-valued (load-bearing, 02§18, 08§15).

### 3.3 Atomic-write helper — see `atomicfile` (§2.4)
One helper, temp-in-same-dir → chmod (skip Windows) → rename. `WriteJSONValidated`
adds the re-parse guard for `sequence.json`. Cleanup temp on any error.

### 3.4 Platform detection incl. WSL — see `platform` (§2.1)
`runtime.GOOS` + `WSL_DISTRO_NAME` (non-empty ⇒ WSL). No `/proc/version` sniff.
Container detection uses the two **distinct** substring sets for cgroup vs mountinfo
(10 Gap 3). `platform.IsWindows()` gates every chmod.

### 3.5 Path resolution — see `paths` (§2.7)
One `GetBackupRoot()` used everywhere (XDG on Linux/WSL with absolute-and-`~`-expanded
guard; `~/.tycswap` on macOS/Windows/unknown since A23). The `.claude.json`
home-root asymmetry and legacy `.config.json` precedence are external Claude Code
contracts (03§2.3).

### 3.6 Logging — see `logging` (§2.6)
Named `"tycswap"`, file `tycswap.log` (A23); **lazy** dir creation on first write (a no-op run must not
materialize `cache/` or the log under the XDG path — would trip the migration
collision check). Rotating 1 MB × 3. Console handler (stderr) only with `--debug`.
Paste-safe invariant preserved (never log email in usage-failure WARNING; 04§1.17).

---

## 4. Concurrency mapping

Every Python thread/loop → Go construct, with lifecycle/shutdown:

| Python | Go | Lifecycle / shutdown |
|---|---|---|
| `claude_locks` daemon toucher thread (`os.utime` every 3s, `threading.Event` stop, join ≤1s) | `cclock.Handle` per-lock goroutine: `time.Ticker(3s)` + `stop chan`; stops on first `os.Chtimes` error (lock stolen) | `Release()` closes `stop`, waits ≤1s, then `os.Remove(dir)`; tolerates stolen/vanished lock (log only). |
| autoswitch `run_loop` (`_stop`/`_wake` `threading.Event`s, `_wake.wait(delay)`, clear-at-top) | goroutine; `stopCh chan struct{}` (closed once, latching), `wakeCh chan struct{}` (buffered-1); inter-tick `select { <-time.After(delay); <-wakeCh; <-stopCh }`. Drain/clear wake at top of loop before checking stop (so a wake racing a timeout is never lost) | `Stop()` closes `stopCh` + non-blocking send on `wakeCh` (idempotent, safe before start); **CLI installs `signal.Notify(SIGTERM)` → `engine.Stop()`** for the loop. `Wake()` after `Stop()` is a harmless no-op. |
| autoswitch `--once` | synchronous `engine.Tick()`; `os.Exit(int(outcome))` | no goroutine. |
| usage fetch `ThreadPoolExecutor().map` with `idx*0.25s` stagger | bounded goroutines (an errgroup or a `sync.WaitGroup` + worker cap); each goroutine `clk.Sleeper.Sleep(idx*250ms)` before its `oauth.Usage` call; results collected into `map[string]FetchRecord` under a `sync.Mutex` (or via a results channel). Context carries per-request 5s deadline | joined before `store.Record`; never holds the usage-store lock across the network calls (lock protocol a/b/c). |
| active-refresh persist callback re-acquiring locks from inside a fetch | network refresh runs with **no** `FileLock` held; the persist callback re-acquires `FileLock → cclock.Credentials → cclock.Config` and re-checks owner/lineage before writing (non-reentrant lock preserved) | callback returns `USAGE_TOKEN_EXPIRED` on mid-refresh owner-appears/lineage change. |
| TUI Textual workers (`run_worker(thread=True, group=…)`) + 3s poll timer | bubbletea: `tea.Tick(3s)` poll; workers are `tea.Cmd`s returning typed msgs (`refreshDoneMsg`/`refreshErrMsg`/`actionDoneMsg`/`engineEventMsg`/`engineStoppedMsg`) handled in `Update`; single-flight = plain `bool` model fields (Update is single-goroutine) | goroutines never touch model state directly — only send messages. Engine hosted as long-lived goroutine draining onto a channel, re-armed by a `tea.Cmd` blocking-receive. |
| `FileLock` (flock/msvcrt, 0.1s poll, 10s) | `filelock.FileLock` `unix.Flock`/`LockFileEx`, monotonic timeout | released on `Release()`/fd-close/process death; non-reentrant. |
| SIGINT / Ctrl-C (Python: no handler, `KeyboardInterrupt` → 130) | `cli` installs `signal.Notify(sigint)`; on receipt prints the cancelled note and `os.Exit(130)`. For `tycswap run` POSIX the process is already replaced by `syscall.Exec` (no tycswap signal handler after exec); Windows run wrapper mirrors child exit / Ctrl-C→130. The auto loop installs **only** SIGTERM→Stop (matching the single Python signal handler); its Ctrl-C path prints "Auto-switch stopped" → 130. |

Data-race discipline: usability cache (`credstore`) guarded by a mutex if a store
is shared across goroutines (TUI). Store CRUD is single-threaded under `FileLock`.
Two clocks kept distinct: wall (persisted state, lock staleness vs mtime) vs
monotonic (acquire timeouts, keychain cooldown).

---

## 5. Implementation plan for parallel agents

Work packages with explicit files, dependency order, and per-package test strategy.
"Blocking" WPs gate downstream work; same-tier WPs run concurrently.

### Tier 0 — Foundation (1 agent, blocking; ~6 leaf packages)
**WP0** `platform`, `clock`, `cerr`, `atomicfile`, `logging`, `keychain`,
`printer`, `jsonout`, `paths`, `filelock`.
- Files per §2.1–2.8, 2.4, 2.6, 2.5.
- Tests: table tests for platform/container detection (env-var matrices),
  color-detection precedence + caching, atomic-write success/failure/cleanup,
  `keychain.Fake` round-trip incl. `"quotes"`/`\`/`é` and the 4032 argv/stdin split
  (mock `exec`), path resolution (XDG absolute/relative/empty/`~`, `.config.json`
  precedence, migration state machine from 03§2.8 table), `cerr.TypeName`
  round-trip, filelock acquire/contention/reacquire (subprocess holds lock).

### Tier 1 — Core leaves (parallel; 3–4 agents)
- **WP1** `oauth` — depends WP0. Files §2.10. Tests: **httptest** for refresh/profile/
  usage; the full refresh classification matrix (04§1.9), `build_usage_result`
  transformation rules (cents/spend gating/scoped surfacing/null resets_at),
  `relevant_windows`/`account_headroom` unknown-vs-at-limit, fingerprint edge cases,
  `format_reset` local-time/day-no-pad, Retry-After parsing.
- **WP2** `usage` (+ `cache`) — depends WP0, oauth types, filelock. Files §2.11.
  Tests: reserve race semantics, backoff sequences (04§2.6 worked values),
  `plan_after_fetch` worked test values (04§3.4, rng=0.5), trust/age computation,
  legacy/foreign file → empty, **read a Python-produced v2 `usage.json` fixture**.
- **WP3** `ccfile`, `cclock` — depends WP0. Tests: RMW preserves unknown keys;
  oauthAccount splice; cclock acquire/stale-takeover/toucher-keeps-fresh/release-
  tolerates-stolen (03§8 test list); honors `CLAUDE_CONFIG_DIR`.
- **WP4** `procdetect`, `sessprofile`, `mappings`, `settings` — depends WP0. Tests:
  `is_pid_alive` (EPERM-is-alive), session PID/IDE parsing skip-on-malformed,
  `slugify_email` rune-by-rune NFC (the `bø@x.com`→`b__x.com` case), keychain
  service NFC-order-insensitivity, `normalize_path`+nearest-ancestor resolve
  (component-aware, longest-key), settings clamp table (08§8.3).

### Tier 2 — Credential store + substrate
- **WP5** `credstore` — depends WP0, ccfile. Files §2.9. Tests: `.enc`-wins reads
  (corrupt/empty/whitespace fall-through), usability cache flips + 60s cooldown +
  sticky pin, `.prev` one-generation retention (+ Keychain-not-a-file on Mac),
  strict-clear fail-closed, unclaimed stash entry-before-manifest,
  `looks_like_api_key`/`approved_form`. macOS contract tests behind a build tag,
  run on macOS CI against real `security` (03§4.8).
- **WP6** `store` — depends credstore, usage, sessprofile, procdetect, oauth,
  paths, atomicfile, filelock, mappings. **Blocking** for Tier 3. Files §2.13.
  Tests: sequence.json round-trip **byte-golden against a Python-produced file**,
  optional-key omission, identity resolution precedence (number→alias→email,
  ambiguity), org backfill, live-session guard, construction order (migration
  before dir setup). Integration: fixture fake `~/.claude`.

### Tier 3 — Behaviors (parallel; 3 agents) — all depend WP6
- **WP7** `lifecycle` — Files §2.14. Tests: the entire 01§13 edge-case list as
  table tests (sparse slots, move cap, same-email swap staging/rollback, alias
  travel, cross-kind collision, dead-token clear, `.prev` lifecycle, prune-mappings
  on remove/overwrite not migrate/swap).
- **WP8** `switching` + `reporting` (may split into two agents) — Files §2.15/§2.16.
  Tests: strategy scoring (`best` never moves onto worse; ties→earliest;
  next-available anchor drift), `_perform_switch` rollback, classify-outgoing oracle
  (02§9), JSON payloads golden, list/status rendering column alignment, usage-collect
  gating (02§17), snapshot coherence.
- **WP9** `session` — depends WP6 + sessprofile. Files §2.19. Tests: bootstrap/reuse,
  stale-marker deferral while live, sharing symlink/copy + repoint + never-touch-user-
  data, MCP mirror fail-open matrix + adopted-in-sync-takes-no-lock, history merge
  dedupe/collision, `_exec` split (mock exec).

### Tier 4 — Composition & consumers (parallel; 3–4 agents)
- **WP10** `core` façade — depends WP7/8. Thin; implements `autoswitch.Switcher` and
  `tui.Facade`. Tests: interface-satisfaction compile checks + delegation smoke.
- **WP11** `transfer` — depends core/store, credstore, oauth. Files. Tests: **import a
  Python-produced `.tycswap` file**, path-traversal defense, `--full` privacy boundary,
  bool-not-int number check, alias self-collision, active-account slim/full, broken-
  slot tolerance, dead-token clear on import.
- **WP12** `migrations` — depends store, credstore, keychain. Tests: windows/macos
  keyring relocation write-verify-delete, `account-None` disambiguation, idempotent
  runner short-circuit, **honor an existing Python `.migrations.json`** (§7 keep list).
- **WP13** `autoswitch` — depends `autoswitch.Switcher` interface (fakeable) → can
  start against a fake before WP10. Files §2.18. Tests: the entire 05§20 edge list
  (hysteresis, at-limit/failover escapes, idle-hold, all-exhausted reset math,
  quarantine lifecycle, run-loop timing/jitter, `pct_label`), FakeClock, fake engine
  Switcher.
- **WP14** `update` — depends paths, cache, printer, platform. Small (§6). Tests:
  semver compare (incl. pre-release notifies), cache TTL/failure-caching, install-
  method detection, Windows print-only.

### Tier 5 — Top level (2 agents)
- **WP15** `cli` — depends everything. Files §2.21. Tests: `_translate_subcommand`
  unit table (08§14), cross-flag validation (each exit-2 message), dispatch routing,
  no-command non-TTY exit 2, `--upgrade` doesn't construct switcher, `run`/`map`/
  `alias`/`swap`/`move`/`auto`/`config` sub-parsers, end-to-end golden CLI output via
  fixture home.
- **WP16** `tui` — depends core façade, autoswitch, settings, oauth, usage, printer.
  Files §2.20. Tests: bubbletea `Update` message handling, single-flight guards,
  cursor-preservation-on-same-set, flash-on-advance, menu structure, threshold
  session-override never persisted, dry↔live carry-forward. `teatest` harness.

**Dependency order summary:** WP0 → {WP1,WP2,WP3,WP4} → WP5 → WP6 → {WP7,WP8,WP9} →
{WP10,WP11,WP12,WP13,WP14} → {WP15,WP16}. WP13 can begin against the fake
`autoswitch.Switcher` before WP10 lands. 6–10 agents fit: one on WP0 then fanning to
the tiers.

**Shared test scaffolding (built in WP0, used everywhere):** `keychain.Fake`,
`clock.Fake`, an `oauth.Client` fake, a fixture-`$HOME` builder (fake `~/.claude`,
`~/.claude.json`, `.credentials.json`), and a set of **Python-produced golden
files** (`sequence.json`, `usage.json` v2, a `.tycswap` export, `.migrations.json`)
committed under `testdata/` to enforce data compatibility.

---

## 6. Deliberate deviations from Python behavior

1. **Version comparator uses real semver** — Python's `_parse_version =
   tuple(int(x) for x in v.split("."))` throws on `0.22.0b1` (`int("0b1")`), and
   `check_for_update`'s blanket `except→None` swallows it, so **pre-release builds
   never show an update notice** (spec 10 §Corrections.2). The Go `update` package
   uses a real semver comparator, so a pre-release build **does** get notified. This
   is an intentional bug-fix, not a behavior to replicate.
2. **Update/upgrade subsystem redesigned for Go** (task-mandated). No PyPI/uv/pipx.
   `update.CheckForUpdate` queries a GitHub Releases (or configured version) endpoint
   for the latest tag, compares to the embedded build version, caches with the **same
   `{"timestamp","data"}` 24h-TTL cache format** at `<backup_root>/cache/
   update_check.json` (interop preserved). `update.SelfUpgrade` detects install shape
   (Homebrew / `go install` GOBIN / downloaded release / package manager) and prints
   guidance; on Windows it prints the command instead of self-replacing a locked
   `.exe`. "Any error → no notification" and "cache failures too" behaviors kept.
3. **Version embedded at build time** (`-ldflags "-X …=<version>"` / generated
   `version.go`) — there is no `importlib.metadata` (10 Gap 1). Surfaced at
   `--version`, export `swapVersion`, and the update check.
4. **`truststore`/`_use_native_tls` dropped** — Go `crypto/tls` uses the platform
   verifier (SChannel on Windows) natively, so the Windows OpenSSL stale-intermediate
   workaround is unnecessary (08§15). Action item: verify inactive-account refresh
   against `platform.claude.com` on Windows before finalizing.
5. **macOS menu bar excluded** (task-mandated; spec 09 §10). `tycswap --menubar` on any
   platform prints a "not available in this build" message and exits 1 (macOS text
   preserved for parity where reasonable).
6. **Four atomic-write helpers unified into one** `atomicfile` (07§9), keeping the
   JSON round-trip validation only where Python had it (`sequence.json`, via
   `WriteJSONValidated`). Same observable outcome; less code.
7. **TUI mutating ops return structured results instead of capturing ANSI stdout**
   (spec 09 §11.4). Python's `run_action` does a process-global
   `redirect_stdout`+`Text.from_ansi`; the Go lifecycle/switching functions return
   `(payload, []LogLine)` and the bubbletea layer formats its own modal/toast. The
   rendered modal text is therefore **not byte-identical** to Python's captured
   output — a deliberate, documented change (avoids fd-level redirect + its
   single-writer caveat).
8. **`build_usage_result` pct stored as `float64` consistently** — Python leaves
   `five_hour`/`seven_day` `pct` as the raw (possibly int) `utilization` while
   coercing `spend`/`scoped` (04§6). Not observable in tests; Go normalizes to
   `float64` throughout.
9. **Import takes the `FileLock` around its write pass** — Python's `import_accounts`
   does an unlocked per-account RMW (07§9), unsafe against a concurrent `switch`. The
   Go port acquires `FileLock` around the write pass. Single-process observable
   behavior is unchanged; concurrent safety improves. Flagged as an intentional,
   low-risk hardening rather than a silent fix.
10. **CLI front controller preserved as-is (no cobra)** — `run`/`map`/etc. must be the
    first argv token (`tycswap --debug run 2` unsupported). A cobra port would silently
    change this (spec 06 §7.3). We keep the exact Python two-layer dispatch for
    fidelity — an explicit decision *not* to "improve" it.
11. **TUI "Next best" candidates panel excludes disabled accounts** — Python's
    `_candidates_text` filter admits any `switchable` account regardless of its
    `disabled` flag, so a disabled account can top the displayed ranking even
    though the auto engine's own candidate set, `SwitchableAccountNumbers`,
    excludes disabled slots and could never pick it — breaking the panel's
    documented contract that the display can never disagree with the pick
    (spec 09 §4.7). The Go filter (`internal/tui/autoview.go`
    `candidatesText`) also excludes `acc.Disabled`; the account card and
    mini-line `(disabled)` marker renders in the warning color rather than
    muted. See A18.
12. **Dashboard account submenus are rebuilt from every snapshot, and a
    per-account action re-validates its target** — Python builds the remove and
    disable submenu rows once, at push time, and stores them in the menu stack
    frame (spec 09§3.2); nothing on the dashboard watches the snapshot, so the
    rows go stale against the accounts monitor rendered directly above them and
    a stale row's action fires against a slot that may no longer hold the
    account named on it. The Go port makes the dashboard a `snapshotObserver`
    and re-derives those rows, re-validates every per-account action against the
    live roster before acting, and reports actions that previously succeeded in
    silence. See A21.

---

## 7. Top 5 data-compatibility / lock-interop risks & defenses

A user points the Go binary at an existing Python `claude-swap` backup directory
and everything must work. The five things most likely to break:

1. **macOS Keychain byte-compatibility with Claude-Code-seeded items.** A divergent
   account name (`$USER` → OS user → literal `"claude-code-user"`), service string
   (`Claude Code-credentials` / `Claude Code` / `claude-swap`), hex `-X` encoding, or
   the single-trailing-`\n` strip keys a *different* item or misreads a value —
   breaking interop on headless hosts and corrupting the active login.
   *Defense:* `keychain` package pins exact argv + `/usr/bin/security` absolute path;
   `AccountName()` replicates `getUsername()`; `Get` uses `TrimSuffix(out,"\n")` (not
   `TrimSpace`); rc 44 handled everywhere; macOS **contract tests run against the real
   `security` binary on CI** (03§4.8), plus an in-memory `Fake` mirroring rc-44/rc-51
   semantics for other platforms.
2. **proper-lockfile interop with a live Claude Code.** If the directory-`mkdir`
   mutex, 10s staleness (wall-clock vs filesystem mtime), 3s touch, 9s acquire, or
   `[0.25,0.50)` jitter diverge, tycswap and a refreshing `claude` can stomp each
   other's `~/.claude.json` / credentials during the token-refresh window.
   *Defense:* `cclock` mirrors the protocol exactly — staleness from
   `fi.ModTime()` (wall), acquire timeout monotonic, a touch goroutine on a 3s ticker
   stopped on first `Chtimes` error, `rmdir`+retry stale takeover; integration test
   simulating a held-fresh lock (times out) and a back-dated lock (taken over,
   new mtime fresh), plus a stolen-lock-on-release tolerance test.
3. **`sequence.json` shape fidelity / optional-key omission.** `sequence` as ints,
   `accounts` keys as strings, `activeAccountNumber` int|null, and — critically —
   `alias`/`kind`/`disabled` **absent** (not `""`/`false`/`"oauth"`) drive duplicate
   detection, back-compat identity matching, and rotation. Zero-filling any of them,
   or writing the add-token config's `null` org fields as `""` (or vice-versa),
   silently corrupts behavior.
   *Defense:* records modeled as `map[string]any`/`json.RawMessage`, never a fixed
   struct with `omitempty` guesses; a **byte-golden round-trip test against a
   Python-produced `sequence.json`** (modulo 2-space indent); explicit tests that
   omitted keys stay omitted after a mutate-rewrite; the add-token config-`null` vs
   record-`""` asymmetry pinned by a test.
4. **Usage cache (schema v2) migrate-in-place.** The Python tool's existing
   `cache/usage.json` should keep working. A wrong `schemaVersion` gate, a broken
   identity guard (`email`,`organizationUuid`), or persisting the never-persisted
   `sentinel`/computing `age_s`/`trust_extended` at write time would either discard a
   valid cache or serve another account's usage after slot reuse.
   *Defense:* `usage.Store` treats missing/corrupt/`!=2` as empty (never crashes),
   keeps `_matches` identity-guard on every read/write, computes trust/age at read
   time only; a test **reads a Python-produced v2 `usage.json` fixture** and asserts
   identity-mismatch rows are invisible and replaced on write.
5. **`.enc` backups + fail-closed/best-effort semantics.** base64 with strict
   `validate=True` decode + the non-empty guard, `.enc`-wins reads, and — most
   dangerous — the per-call-site split between **fail-closed** transactional clears
   (`DeleteBackupStrict`, move/swap required-clears that *abort the commit*) and
   **best-effort** post-commit cleanup/`.prev`/session-profile/Keychain deletes.
   Getting this backwards either bricks switching on a flaky Keychain or leaks a
   stale credential onto a reused slot (01§14, 03§9.4).
   *Defense:* Go threads explicit `error` returns through every helper (no blanket
   swallow); strict clears return errors that propagate to abort; best-effort sites
   log-and-continue by explicit choice; base64 via `base64.StdEncoding.DecodeString`
   (errors on junk) + non-empty guard; the 01§13 / 02§17 edge-case lists become table
   tests (unbacked-relocation clears stale target key, metadata-write-is-commit-point,
   same-email swap staging durability, `.enc`-wins fall-through). Legacy migrations
   (windows/macos keyring, legacy→XDG dir) are **kept** and honor an existing Python
   `.migrations.json` (07§9) so a long-gap upgrader's data is rescued exactly once.

---

## Appendix — assembly & construction order (parity-critical)

`core.New` / `store.New` reproduces `ClaudeAccountSwitcher.__init__` order exactly
(07§5.6): (1) resolve `home`/`platform`; (2) `GetBackupRoot()`; (3)
`MigrateLegacyBackupDir` — the one construction step that may return a hard
`MigrationError` aborting startup; (4) derive `sequenceFile`/`configsDir`/
`credentialsDir`/`lockFile`; (5) lazy logging + `usage.NewStore`; (6) `credstore.New`;
(7) `migrations.Run` — must **never** abort construction (every error caught, logged,
left for retry). Swapping (3) and (7), or making (7) fallible, is an observable
regression.


---

# Amendments (final design decisions — these OVERRIDE anything above)

These amendments state final design decisions. Where an amendment contradicts
the body above, the amendment wins.

## A1. Usage round-trip fidelity (REJECTS Deviation #8)

`usage.UsageEntry.LastGood` is persisted as `map[string]any` (or
`json.RawMessage`), NOT as a typed struct. `oauth.BuildUsageResult` returns the
normalized dict as `map[string]any` with **no numeric coercion** of
`five_hour`/`seven_day` `pct` (04§1.18: the raw `utilization` value passes
through as-is — int stays int in JSON round-trip). Any typed `oauth.Usage`
struct is a **read-only projection** for decision logic (strategies,
autoswitch, TUI) and is never the persisted form. What Python writes into
`usage.json`, Go must re-write byte-compatibly (modulo key order/indent).

## A2. Consumer-defined interfaces for session and transfer

`internal/session` does NOT hold `*core.Switcher`. It declares:

```go
// session package
type Accounts interface {
    ResolveAccount(id string) (num, email, org string, err error)
    ReadAccountCredentials(num, email string) (string, error)
    WriteAccountCredentials(num, email, creds string) error
    ReadAccountConfig(num, email string) (map[string]any, error)
    AccountKindFor(num string) string
    CurrentAccountNumber() *string
    BackupDir() string
    Platform() platform.Platform
    SlotForDirectory(dir string) (slot *string, email *string, err error)
}
```

satisfied structurally by `*core.Switcher` (compile-asserted in `cli`).
`internal/transfer` gets the same treatment with its own narrow interface
(resolve/read/write accounts + sequence access + backup dir). Neither package
imports `core` or `store`. This lets WP9/WP11 build and test against fakes
before the substrate is final.

## A3. migrations.Host consumer interface (breaks store→migrations→store cycle)

`internal/migrations` declares a `Host` interface exposing exactly what the
registry migrations need (backup dir, credentials dir, sequence read,
`credstore` handles, keychain client, platform, logger, migration-state file
path). `store.New` constructs an adapter and calls `migrations.Run(host)` as
construction step 7. `migrations` never imports `store`.

## A4. Log-format contract is a hard interop requirement (custom handler)

Spec 09§12: the TUI switch-history reader parses the log file produced by the
logging package, format `YYYY-MM-DD HH:MM:SS,mmm - LEVEL - message` (Python
`%(asctime)s`, **comma** milliseconds) and the switch INFO line is exactly
`Switched from account X to Y`. Neither `slog.TextHandler` nor any stock Go
logger emits this. `internal/logging` therefore implements a small custom
formatter (hand-rolled; slog optional underneath but the on-disk bytes are the
contract). The `Switched from account X to Y` line is emitted by the
`switching` package. Golden test: write lines, re-parse with the TUI history
reader, and cross-check against a Python-produced log-line fixture.

## A5. Version scheme + comparator (resolves the 0.22.0b1 confusion)

The Go port's own versions are **semver with a leading `v`** (`v0.1.0`,
`v0.2.0-beta.1`), embedded at build time via
`-ldflags "-X github.com/tyclab/tycswap/internal/version.Version=v0.1.0"`
(package `internal/version`, default value `v0.0.0-dev`). `--version` prints
`<prog> <version>` with the `v` stripped for display parity. The update
comparator is `golang.org/x/mod/semver` (approved dep) on the `v`-prefixed
strings; pre-releases DO get update notices (intentional fix of the Python
quirk, Deviation #1). The export envelope `swapVersion` carries the display
form (no `v`). Never feed a PEP-440 string to the comparator; the Python
tool's versions are irrelevant to the Go update check.

## A6. Update endpoint

`update.CheckForUpdate` queries the Forgejo releases API of the canonical
repo: `https://git.dpemmons.com/api/v1/repos/dpemmons/cswap/releases/latest`
(response field `tag_name`, same as GitHub's schema), 3s timeout, any error →
no notice, negative results cached too. Endpoint string is an `internal/update`
variable overridable via ldflags. Cache file keeps the Python
`{"timestamp": <unix>, "data": ...}` 24h-TTL format at
`<backup_root>/cache/update_check.json`. `SelfUpgrade`: if the running binary
resolves inside `$GOBIN`/`$GOPATH/bin`/`$HOME/go/bin`, run
`go install git.dpemmons.com/dpemmons/cswap/cmd/cswap@latest`; otherwise print
download/upgrade guidance (Windows always print-only). **Superseded by A24**
for the endpoint, the module path, and checkout builds.

## A7. SIGINT handling rationale (audit 10 §Signal handling)

The audit warns "do not invent a SIGINT trap" — in Python, no handler exists
and `KeyboardInterrupt` unwinds to the exit-130 paths. In Go, default SIGINT
delivery kills the process before the cancel note / JSON-vs-plain routing can
run, so `cli` MUST install a SIGINT notifier to **reproduce** (never extend)
Python's semantics: print the exact cancelled note, route stderr-vs-stdout by
JSON mode, exit 130. Interactive prompts convert cancellation into the
prompt-local "Cancelled" behavior. On the POSIX `tycswap run` path the handler
is irrelevant after `syscall.Exec` (process image replaced). The auto loop
additionally installs SIGTERM→`engine.Stop()` — the only signal handler the
Python code has.

## A8. Explicitly non-atomic cache writer

`usage`'s TTL `cache` file writer (04§4) is the ONE deliberately non-atomic,
non-chmod'd writer in the codebase (plain create/truncate write). Do not route
it through `atomicfile`; a comment in the code marks it deliberate.

## A9. `internal/wincred` build-tagged leaf

The Windows legacy-keyring migration (`migrateWindowsKeyringToFiles`) uses a
tiny `internal/wincred` package (build tag `windows`) wrapping
`golang.org/x/sys/windows` CredRead/CredDelete for generic credentials. On
macOS the legacy read stays `keychain.Get("claude-code", ...)`. Non-Windows
builds compile a stub returning not-found.

## A10. File-level spec tagging

Every file in `store`, `lifecycle`, `switching`, `reporting` carries a header
comment naming the spec sections it implements (e.g. `// Implements spec
01§5.2–5.4 (add --slot displacement).`) so fidelity review stays mechanical
after the decomposition.

## A11. snapshotsrc seam

`reporting.AccountsSnapshot` + a small `RunAction` structured-result seam live
in their own file group inside `reporting` (not a separate package), but the
TUI consumes them ONLY through `tui.Facade` — keeping Deviation #7 isolated
behind one seam.

## A12. Dependency freeze (go.mod is frozen)

Allowed third-party deps, already in `go.mod`:
`github.com/charmbracelet/bubbletea` v1.3.x, `charmbracelet/lipgloss` v1.1.x,
`charmbracelet/bubbles` v1.0.x, `golang.org/x/sys`, `golang.org/x/text`,
`golang.org/x/mod` (+ transitive). Implementation agents MUST NOT add
dependencies or edit `go.mod`/`go.sum`. `internal/deps/deps.go` blank-imports
the charm stack until the TUI lands (keeps `go mod tidy` from pruning); it is
deleted in the final integration pass.

## A13. Pinned cross-package interfaces

The `autoswitch.Switcher` (§2.18) and `tui.Facade` (§2.20) interface method
sets as written in this document are FROZEN. `core.Switcher` implements them;
`cli` carries the compile assertions
(`var _ autoswitch.Switcher = (*core.Switcher)(nil)`, etc.). An agent needing
a signature change must change it in BOTH the consumer interface and core in
the same work package, and note it in the commit/summary.

## A14. Genuine Python-produced fixtures

`testdata/python-fixtures/` contains files generated by actually running the
Python claude-swap (add-token accounts — no network needed —, aliases,
disable, mappings, settings, an export file, `.migrations.json`, and a
hand-written spec-conformant `usage.json` v2 where network would be required).
Golden tests read these to enforce data compatibility. Do not regenerate or
hand-edit them casually; a README in that directory records how they were made.

## A15. At-limit surfacing (Go-side additive extension)

`tycswap list` / `tycswap status` (human and `--json`) and the TUI dashboard/watch
views surface when an account sits at a rate limit, folding in the per-model
weekly windows configured via the existing `autoswitch.model` setting. An
account whose named "Fable 5" weekly window is exhausted reads as at-limit even
when the account-wide 7d window has room ("expired when EITHER is expired").

**Source & semantics.** Models come from `settings.Load(backupDir).Model`
through `settings.ParseModelNames` (comma lists and the `all` sentinel) — no new
CLI flag and no new settings key; `list`/`status` stay flag-free (CLI-surface
parity). The decision reuses the same projection the switch strategies and auto
engine decide with: `oauth.RelevantWindows(usage, models)` /
`oauth.AccountHeadroom(usage, models)`, computed only for accounts whose
decision-grade usage is a usage map (`usageStatus` "ok"). `atLimit` ⇔
`AccountHeadroom != nil && <= 0`; `limitingWindows` is every relevant window
with `pct >= 100`, in `RelevantWindows` order (e.g. `["7d","Fable 5"]`). A nil
headroom (no window data) is *unknown*, never at-limit. With the setting unset
the decision runs on 5h/7d alone — uniform semantics, no special case.

**Fields.** `reporting.AccountSnapshot` carries `AtLimit bool` /
`LimitingWindows []string`. The `--json` list rows and the status active-account
object gain `"atLimit": true` and `"limitingWindows": [...]` — but only when
at-limit; both keys are OMITTED when false. Human surfaces append an
`at limit: <labels>` marker beside the existing row markers; the TUI shows the
same compact marker on the account card.

**Additive-contract argument.** This is additive under schemaVersion 1: no
existing field changes and `usageStatus` values are untouched (an at-limit "ok"
account is still `usageStatus: "ok"` plus the new optional keys), following the
same optional-key omission discipline as `alias`/`disabled`/the freshness
fields. It is a deliberate Go-side extension with no Python counterpart: the
Python original shows per-model pct rows but its `usageStatus` and list markers
ignore them, so this adds signal without breaking any documented shape. The
Python-fidelity contract in `docs/port-spec/` is unchanged.

## A16. `tycswap env` — pin a shell to an account (Go-side additive extension)

`tycswap env [NUM|EMAIL|ALIAS] [--no-share] [--share-history] [--shell sh|fish|pwsh]
[--unset] [--debug]` gives the CURRENT shell a per-account Claude Code identity —
`eval "$(tycswap env 2)"` — by preparing the same persistent session profile
`tycswap run` uses and then PRINTING shell-evalable env lines instead of exec'ing
claude. It is a deliberate Go-side extension with no Python counterpart, added to
the `--help` command list (one line) and epilog as a documented deviation from
the verbatim Python help text; `docs/port-spec/` is untouched.

**Surface & dispatch.** Pre-dispatched on the first argv token exactly like
`run`/`map`/`alias` (front-controller pre-dispatch list + its own flag parser +
`_guard_root` + the `ClaudeSwitchError`→exit 1 / Ctrl-C→130 wrapping). `--shell`
defaults to `sh`; an invalid value is a usage error (exit 2) listing the choices.
`--unset` skips account resolution and bootstrap entirely and prints only the
`CLAUDE_CONFIG_DIR` unset line for the chosen shell; `--unset` with an account
argument is a usage error (exit 2).

**Account resolution** is identical to `run`: explicit `NUM|EMAIL|ALIAS`, else the
cwd's directory mapping, else error (exit 1). Unlike `run` there is NO
default-login fallback — an unset `CLAUDE_CONFIG_DIR` IS the default, so the
error points the user at passing an account, mapping the directory, or
`tycswap env --unset`.

**Profile preparation** reuses the existing session machinery verbatim:
`session.Manager.SetupEnv` calls the SAME exported `SetupSession`
(bootstrap/validate/mirror/share, honoring `--no-share`/`--share-history`) that
`Run` calls — no duplicated bootstrap logic. The one intentional divergence from
`Run` is the same-account case: `Run` has an exec fast path when the requested
account is already the active default login, but a shell pinned to a profile is
still pinned even when the default currently matches, so `env` does NOT
short-circuit — it prints an informational stderr note and still emits the
export of the session profile.

**Output discipline (critical).** stdout carries ONLY the eval-able lines; every
notice/warning goes to stderr. The `SessionManager`'s message sink is wired to
stderr, so its bootstrap/scrub/"Prepared" notices never pollute the eval stream.
Emitted lines: for each `AUTH_OVERRIDE_ENV_VARS` currently set, a shell-specific
unset line (`unset VAR` / `set -e VAR` / `Remove-Item Env:VAR …`) plus ONE
stderr warning naming them (the `run` scrub wording adapted for env); then the
export line (`export CLAUDE_CONFIG_DIR='<dir>'` with POSIX single-quote escaping
/ `set -gx CLAUDE_CONFIG_DIR '<dir>'` / `$env:CLAUDE_CONFIG_DIR = '<dir>'`), each
quoted for its shell so a profile path containing a single quote survives.

**Staleness.** The shell keeps the pinned profile until the user re-evals; after
switching accounts or a credential change they re-run the `eval` or use
`tycswap run` (which re-prepares on every launch). Claude Code's live-session
guards keep working because it writes its session files inside the profile
directory the export points at.

## A17. `autoswitch.strategy: soonest-reset` — renewal-ordered candidates (Go-side additive extension)

`autoswitch.strategy` (`internal/settings/settings.go` `SettingSpecs`, kind
`KindChoice`) gains a second choice, `soonest-reset`, alongside the existing
`best`. Default stays `best`; an invalid persisted value still falls back to
the default via the existing `KindChoice` clamp — no new fallback path. It is
a deliberate Go-side extension with no Python counterpart: Python's auto-switch
has no ranking axis beyond most-headroom, so this adds a second one without
touching the Python-fidelity contract in `docs/port-spec/`.

**Ordering only.** `internal/autoswitch/tick.go` `selectCandidates` changes
exactly the sort of the already-built `qualifying` slice; it changes no gate.
Known and positive headroom (`headroom != nil && > 0`), the cooldown,
quarantine, and the API-key last resort are IDENTICAL for both strategies and
every trigger (`proactive`, `at-limit`, `failover`). The threshold-landing
and hysteresis checks apply only when `trigger == "proactive"`: an `at-limit`
or `failover` tick has no in-force active account to land under the
threshold against or beat by the hysteresis margin, so any candidate with
positive headroom qualifies under those two triggers regardless of where it
sits relative to the threshold.

`sortQualifying` gains the threshold as a parameter —
`sortQualifying(qualifying []qual, strategy string, threshold float64)`,
called with `s.Threshold` — because the two-tier `soonest-reset` order below
needs it even though it changes no qualification gate. `best` stays
byte-identical to today: headroom descending, `sort.SliceStable` so ties keep
sequence order; the threshold parameter is unused on this branch.

`soonest-reset` becomes a two-tier comparator. Tier A holds every qualifying
candidate whose utilization sits below the threshold — `(100 - h) <
threshold` — and orders exactly as before: known renewal before unknown,
known renewal ascending, then headroom descending, then sequence order (via
`sort.SliceStable` over the sequence-ordered list) for remaining ties. Tier B
holds every remaining qualifying candidate — utilization at or above the
threshold, headroom still positive since an at-limit account never qualifies
at all — and orders by headroom descending alone, as a last resort. Every
tier-A candidate sorts before every tier-B candidate: an account at or above
the threshold is never preferred over one below it for an earlier renewal.
Under the `proactive` trigger tier B is always empty, since the
threshold-landing gate above already excludes any candidate at or above the
threshold from qualifying — proactive ordering is therefore unchanged from
tier A alone. The tiering is observable only under the `at-limit` and
`failover` triggers, where the threshold-landing gate does not apply and an
at/above-threshold candidate can reach `qualifying`. Engine events are
UNCHANGED — no new fields, `Poll`/`Switch`/`NoSwitch` stay byte-stable; the
strategy is observable only through which account gets picked.

**`oauth.RenewalTS` projection.** `internal/oauth/windows.go` gets one new
exported function, `RenewalTS(u *Usage, models []string) *float64`, returning
an account's renewal as epoch seconds: the LATEST parseable `resets_at` among
`RelevantWindows(u, models)` restricted to weekly-scope windows — the `7d`
window plus every scoped per-model window `autoswitch.model` matches — with
the `5h` window excluded (it is not weekly). An entry with an absent or
unparseable `resets_at` is skipped; if no weekly window has a parseable
`resets_at`, the result is `nil` (unknown). Parsing reuses the accepted
layouts from `autoswitch/reset.go`'s private `parseResetTS`
(`time.RFC3339Nano` then `time.RFC3339`, empty → `nil`) via a small unexported
helper local to `oauth` — `autoswitch`'s `parseResetTS` stays unexported;
`oauth.RenewalTS` is the one shared, exported entry point and both
`autoswitch` and `tui` call it. `selectCandidates` threads the engine's
per-account usage (the `usageMap` built in `tickInner`) alongside the existing
headroom map so it can compute renewal per candidate.

**TUI visibility (`internal/tui/autoview.go`).** `summaryText` appends
` · soonest-reset` (plain segment, after the `poll every Ns` segment) only
when `a.settings.Strategy != "best"`. `candidatesText` computes
`oauth.RenewalTS` from `acc.Usage.LastGood` via `oauth.NewUsage` (mirroring how
it already derives `bindingPct`) and, only under `soonest-reset`, ranks by a
six-tier ladder (`candidateRank.tier`, ordered by `candidateLessSoonest`)
that mirrors the engine's tier-A/tier-B split by the same threshold —
`a.settings.Threshold`, the session-adjusted value the engine itself receives
via `ApplyThreshold`. The method is on `autoScreen`, which already holds
`a.settings`, so the threshold reaches both the tier assignment in
`candidatesText` and `candidateLessSoonest` without a new parameter on either.
The ladder: tier 0 (binding pct below the threshold, known renewal) by
renewal ascending; tier 1 (pct below the threshold, unknown renewal) by pct
ascending; tier 2 (pct at or above the threshold and below 100 — headroom
remains, but the candidate is a last resort, mirroring the engine's tier B)
by pct ascending, i.e. headroom descending; tier 3 (pct at or over 100,
at/over limit) by renewal ascending with unknown renewal last in the tier,
then pct ascending; tier 4 the quarantined case (`bestKey` `997`, A18); tier 5
the existing sentinel (`bestKey` `998`); tier 6 the existing usage-unknown
case (`999`). Ties at every tier resolve by
account number ascending, same as today. Under `best` the panel's ranking is
untouched.

## A18. TUI "Next best" panel excludes disabled accounts (Go-side deviation)

`internal/tui/autoview.go`'s `candidatesText` filter (spec 09§4.7) skips a
row when `acc.Number == snap.ActiveNumber || !acc.RotationEligible` —
the active account, and every account the auto engine's own candidate set
excludes, in one predicate. This is a deliberate Go-side deviation from
the Python original, recorded as an amendment rather than folded silently
into the port, because the Python filter reads identically on paper while
`Switchable` alone does not carry the meaning the auto engine assigns to
its own candidate set. `RotationEligible` is `store.RotationEligible`'s
rule, carried on the snapshot rather than re-ANDed here; ownership of the
predicate, and of its call sites, belongs to A19, not this
amendment — A18 documents the panel's filter contract, A19 the shared
predicate the filter is stated in terms of.

**The defect.** `reporting.AccountSnapshot.Switchable` reports only whether
a slot has stored credentials and config (`store.AccountIsSwitchable`);
`Disabled` is a separate, independent field. The auto engine's candidate
set, `store.SwitchableAccountNumbers` (`internal/store/identity.go`),
requires both: `AccountIsSwitchable && !disabled`. Python's
`_candidates_text` carries the identical `switchable`-only filter
(`switchable and account.number != active_number`; spec 09§4.7), so a
disabled account with stored credentials can top the Python panel's ranking
as well, though the engine can never select it. The defect is inherited
from the Python original, not introduced by the port.

**The correction.** The panel's documented contract, stated in Python's own
source comment and carried into spec 09§4.7, is that it "uses the exact
same model axis... so the displayed ranking can never disagree with the
account it picks." A disabled account ranked at the top of a display the
engine will never act on breaks that contract on its face. The Go filter
closes over both conditions the engine requires (A19's `RotationEligible`);
the panel never shows a disabled account.

**Quarantine labeling.** The panel also reads the engine's quarantine
state. `autoswitch.ReadQuarantine(statePath)` (`internal/autoswitch/state.go`)
is a read-only, unlocked reader over `autoswitch_state.json`'s `quarantine`
map, returning slot number to reason with the empty string standing for an
entry that carries no readable reason. Its read is tolerant and lock-free,
matching the engine's own `readState` at tick start exactly — missing
file, parse error, or a non-object top level all yield an empty map — and
`readState` and `ReadQuarantine` share one tolerant-read helper, so the two
stay byte-identical by construction. `autoScreen` holds
the result in a `quarantined map[string]string`, refreshed in both
`onMount` and `onSnapshot` from `m.facade.BackupDir()` (mirroring how
`loadThreshold` locates the settings file).

A quarantined slot is not dropped the way a disabled slot is: disabling is
a durable, user-chosen exclusion from the auto surface, so hiding the row
costs the user nothing, while quarantine is transient and self-healing — it
clears itself, with no action on the auto surface, the moment the slot's
stored credential is replaced (`account-replaced` / `credentials-replaced`,
spec 05§14). Dropping a quarantined row would tell the user only that a
healthy-looking candidate is gone, not why or what to do about it.
`candidatesText` labels the row instead: number and email render as
usual, and the usage/sentinel cell is replaced by `"quarantined (<reason>)"`,
or plain `"quarantined"` when the reason is empty, in the warning color
(`colSevWarn`). Quarantine takes precedence over sentinel and usage
rendering for that row — a quarantined slot's cached usage or sentinel
value never reaches the panel once quarantined.

Quarantined rows rank into the non-viable tail, above the sentinel and
usage-unknown rows. Under `best`, `bestKey` `997` sorts after every pct row
(pct is `<=100`) and before the `998` sentinel and `999` usage-unknown
keys. Under `soonest-reset`, the tier ladder gains a non-viable tier and
runs tier 0/1/2/3 (viable, as above; 3 = at/over limit) < tier 4
(quarantined) < tier 5 (sentinel) < tier 6 (usage-unknown). Ties resolve by
account number ascending, as at every other tier. This is the panel's
contract restated in its strict form: a row ranked as a viable target
(tier 0-3, pct `<=100`) is always one the engine could pick this tick,
modulo cache freshness; every row the engine cannot pick — disabled
excepted, which is dropped rather than shown — is labeled with the reason
it cannot be picked. The existing "no other switchable accounts"
empty-state line is unchanged and covers the all-remaining-disabled case;
it does not cover an all-remaining-quarantined case, since a quarantined
slot still contributes a labeled row rather than being excluded from the
ranked set.

**Marker color.** The account card (`accountCardText`) and the mini list
line (`miniAccountText`), both in `internal/tui/widgets.go`, render their
`(disabled)` marker in the warning color (`colSevWarn`) in place of the muted color
(`colMuted`); text, spacing, and position are unchanged. This tracks the
marker's meaning under this amendment: a disabled slot is a valid explicit
switch/watch target but is never an automatic one. CLI (non-TUI) output is
untouched — `tycswap list`'s `(disabled)` marker
(`internal/reporting/list.go`, via `printer.Muted`) stays exactly as it is,
preserving byte-fidelity with the Python original for CLI surfaces.

**Per-window display and emphasis (Go-side additive extension).**
Python's `_candidates_text` (spec 09§4.7) renders a row's usage cell as the
single blended figure `f"  {pct:3.0f}% used"`. `candidatesText`'s usage cell
for a row with readable usage instead lists every window `oauth.NewUsage`
projects for the account — `Usage.FiveHour`, `Usage.SevenDay`, then each
`Usage.Scoped` entry in the account's own order — as `{label} {pct:.0f}%`
segments joined by ` · `, with no filtering by `autoswitch.model`: an
unmatched scoped window still renders, because a user configuring
`autoswitch.model` needs to see a window filling up before naming it. This
is a deliberate Go-side extension with no Python counterpart, exactly as
A17's `soonest-reset`; Python's `_candidates_text` has no per-window
breakdown to port.

Which windows *count* is unchanged from what `bindingPct` and
`oauth.AccountHeadroom` already compute: a window counts iff it is a member
of `oauth.RelevantWindows(u, models)` — `5h` and `7d` always, plus a scoped
window `autoswitch.model` names. The counted window with the highest `Pct`
is the row's binding window: the same figure `bindingPct` returns, and the
only one `candidateLessBest`/`candidateLessSoonest` compare against — this
amendment touches display, not ranking, so both comparators and the tier
assignment they read (A17) are untouched. Ties on the maximum resolve to
the first window in `RelevantWindows` order (`5h` before `7d` before a
scoped window), matching `oauth.AccountHeadroom`'s own `max` scan, which
only replaces `max` on a strict `>` and so keeps the first window on a tie.

Emphasis has three levels, applied per window segment (`addCandidateCell`):

- The binding window renders its whole head — label AND `Pct` together — in
  `severityColorF` of its own `Pct`, bold.
- A counted, non-binding window keeps its label in `colMuted` but renders
  `Pct` in the identical `severityColorF` color the binding window's `Pct`
  uses, not bold. Severity states what the figure MEANS, and every other
  surface already colors a counted `Pct` that way — `miniAccountText`'s
  window cells, the account card's bars (`barCells`/`usageBar`), and
  `tycswap list`'s figures — so a candidate row states it no differently.
  Bold is the separate fact of which figure the row is ranked by and the
  engine acts on; only the binding cell carries it. The two facts are not
  interchangeable, so withholding color from a non-binding `Pct` to make the
  binding one stand out would conflate them; bold alone already does that
  job.
- An uncounted (unmatched scoped) window renders muted (`colMuted`)
  throughout, label and percentage alike, so it reads as informational
  rather than as a figure that could be mistaken for a ranked one.

**Reset countdowns.** A window cell carries the time until that window
resets — `7d 88% (resets 2d 4h)` — so a row answers both how used an
account is and when it frees up. `candidateWindow` carries the raw
`ResetsAt` projected by `oauth.RelevantWindows`; `candidateCountdown`
derives the displayed text through `resetText`, so the panel shares one
duration grammar and one elapsed wording (`resets now`) with the account
card and the mini rows rather than growing a second vocabulary. The
derivation is live, against the `now` `view` passes: a window's stored
countdown string is correct only at fetch time and drifts as the
measurement ages (09§12), so it is never displayed. A window whose
`ResetsAt` is absent or unparseable renders no parenthetical, leaving the
cell exactly as it would be without this projection.

A countdown takes its cell's emphasis level but is never bold, the binding
cell included: the percentage is the ranking figure and holds the emphasis
alone. `candidatesText` is called per render rather than cached, so a
displayed countdown is at most one repaint old — the TUI's own poll timer
re-arms every `pollIntervalS`, bounding that regardless of the engine's
poll interval or user activity.

The panel header states the counted axis once so the muting is explained
rather than mysterious: `candidatesText` appends a muted suffix to `"Next
best"` naming the counted labels in `RelevantWindows` order — `5h, 7d` with
`autoswitch.model` unset; `5h, 7d, Fable` with it set to `Fable`
(`settings.ParseModelNames`'s output, not the raw stored string); `5h, 7d,
all models` for the `all` sentinel, since naming every matched scoped
window individually would defeat the point of the sentinel.

**The shared window table (`internal/tui/table.go`).**
The auto-switch panel and the dashboard's accounts monitor both lay a
non-active account's usage windows into one shared column layout, so a
window reads the same way wherever it appears — when that layout is the
one drawn; which surfaces draw it and when is a PRICED CHOICE, not an
assumption, and is its own subject below. A `tableRow` carries an
explicit `kind` (`windowRowKind` / `spanRowKind`) rather than deriving its
shape from its fields, because the two shapes are MUTUALLY EXCLUSIVE and
nothing upstream enforced that on its own: `newWindowRow` builds a WINDOW
row — `Slot`, a `Label` `richText`, and `Windows []candidateWindow`, the
same slice `candidateWindows` already builds — and `newSpanRow` builds a
SPAN row carrying one `Span` message in `SpanFg` instead of window cells:
the quarantined/sentinel/usage-unknown shape on the panel, the
sentinel/usage-unknown shape on the monitor. `tableRow.span()` reads the
`kind` field the constructor set. A SPAN row's own width contract — how
`tableLine` fits `Span` against the label cell the width ladder has
already sized — is below, alongside that ladder. `tableOpts` is the two
surfaces' only remaining difference — the slot cell's indent and color
(`candidateTableOpts`: 2-column indent, plain foreground, matching
`candidateNumber`'s margin; `monitorTableOpts`: no indent, bold muted,
matching `miniAccountText`'s prefix) — plus `headerFloor` and `policy`
(below).

Every surface also keeps its own PER-ROW fallback: the panel's
`candidateRow`/`candidateLabelRow`, the monitor's `miniAccountText`. The
two fallbacks state a window differently — the panel repeats a window's
label on every row and states every window at every percentage; the
monitor states only `5h`/`7d` by default and names a reset, or a
per-model window at all, only once it has run out — and each fallback's
own contract, described further below, is the RELEASE BAR the table is
priced against on that surface (`tablePolicy`, `layoutScore`).

`tableColumns` builds the column set in two passes. The first is the union of
every WINDOW row's window labels, keyed by `(label, occurrence-within-the-row)`
rather than the label alone, in the order the rows are handed to it — so a
row's Nth window under a given label lands in the table's own Nth column
carrying that label, never merged into an earlier one. An account CAN report
two windows under one display name — two scoped per-model weekly windows
sharing a name is the practical case — and keying on the label alone would
collide those two cells into a single column, the later-reported window
silently overwriting the earlier one there. That earlier window can be the
row's BINDING one — binding is a property of the WINDOW, not of its label —
so the collision would leave the row ranked by a figure the table does not
show at all. One column per reported window is the only assignment that
cannot lose one; its price is a label repeated in the header exactly when,
and only when, an account repeats it, which is what the account itself
reports.

The second pass, `canonicalTableColumns`, reorders that union CANONICALLY and
rewrites the per-row assignment (`at`) to match, so nothing downstream ever
sees the raw scan order. The order is TOTAL — `tableColumnRank` (0 for
`windowLabel5h`, 1 for `windowLabel7d`, 2 for everything else), then the
label itself, then the occurrence within the row — which makes the header a
function of the label MULTISET and of nothing else. Ordering by first
appearance across the rows instead makes it a function of ROW order: a
7d-only account listed above a 5h+7d one prints `7d 5h`, and two accounts
reporting different models swap their scoped columns as they re-rank. On the
panel row order IS the live ranking, so such a header re-reads itself from
one poll to the next with no resize and no change in what any account
reports, disagreeing with the account card, the mini account line and `tycswap
list`, all of which read `5h` before `7d` always. Whichever way a row's
labels land, one guarantee holds regardless of either pass: the cell carrying
a row's ranking figure is always present on the table, and it is always that
row's one bold cell — the width ladder below drops only columns nothing has
PINNED (`pinTableColumns`, below).
`tableGrid` resolves every row's cells against the per-row column assignment
`tableColumns` returns (`at [][]int`, one column index per entry of
`Windows`, in the same order) and derives each cell's countdown once, through
`tableCountdown` — `candidateCountdown` with the leading `"resets "` word
trimmed off, so the table reuses `resetText`'s duration grammar rather than
introducing a second one. `measureColumns` sizes each column's percentage
sub-width to its widest CELL — never to its header, so a column costs what its
figures cost and not what its window is called — and its countdown sub-width
to its widest non-empty countdown (0, and hidden, when no cell in the
column has one). It also reads off the column what the width ladder judges it
by: how many ROWS report a figure in it, whether some row BINDS on it, and
whether some row has RUN OUT in it. `tableLine` writes a cell as the percentage right-aligned
into the percentage sub-width, then, when the column shows one, the
countdown left-aligned into the countdown sub-width; a row with no cell for
a column writes `tableMissing` (`"—"`) in plain `colMuted` there —
unconditionally, whether or not the column counts and whether or not some
other row has exhausted it, so a missing figure never borrows either the
dimming an uncounted, unexhausted figure of its own carries or the color an
exhausted one would.

Emphasis applies per `tableCell` rather than per row-segment (`cellPctStyle`,
`cellCountdownStyle`), and it reads three ways too — the same COUNT as the
panel fallback's contract above, but not the same AXIS. `addCandidateCell`'s
third branch turns on BINDING: a counted-but-not-binding cell and a binding
one differ only by bold. `cellPctStyle`'s third branch turns on EXHAUSTION
instead (below): a counted cell and an exhausted-but-uncounted one differ by
whether `cell.counted` is read at all. `cellPctStyle` tests `cell.counted`
first: a
COUNTED cell is severity-colored, additionally bold when it binds. Failing
that it tests `cell.exhausted`, the flag `candidateWindows` sets once
(`Pct >= exhaustedPct`, below) rather than a second `>= 100` comparison of
its own: an EXHAUSTED cell carries the identical severity color whether or
not it counts, never bold — bold marks the ranking figure, and an uncounted
window is never that. Only a cell that is neither counted nor exhausted — an
uncounted window still short of its limit — stays muted and dim. The third branch exists because of who the accounts monitor's
columns are: `monitorRow` enumerates every account's windows on the empty
model axis (`candidateWindows(..., nil)`), so on that surface every
per-model column is UNCOUNTED BY CONSTRUCTION, and a scoped window at or
over its limit — the one figure on that row a reader most needs to see —
would otherwise render no differently from one at 40%. The per-row fallback
the table replaced never had that gap: `miniAccountText` has always flagged
an exhausted scoped window outright, as `"Fable (!)"` in the critical color,
regardless of whether anything counts it; the third branch is what lets the
table say the same thing a different way rather than saying less. A
countdown still takes its own cell's level but is never bold, whichever of
the three percentage branches applies — the countdown is supporting detail
on every cell, binding included, so it never competes with the percentage
for the eye (`cellCountdownStyle` reads only `cell.counted`, two levels,
since a countdown carries no severity of its own to promote out of the muted
band). `tableLayout.header` renders each surviving column's label muted, and
additionally `Dim` when `!counted` — the same predicate `cellPctStyle`'s
first branch reads — so the header and a row's own muting can never
disagree about which columns count; the header itself carries no third
branch, since exhaustion is a fact about one row's CELL, not about the
column as a whole.

**A figure too wide to spell — `displayPctCap`, `pctText`.** A window's
`Pct` is carried at the value the store reported, never clamped: the
ranking (`bindingPct`, `oauth.AccountHeadroom`), the severity ramp
(`severityColorF`), and the exhaustion test (`Exhausted: w.Pct >=
exhaustedPct` in `candidateWindows`) all read the real number, so nothing
the table decides from a window's percentage can disagree with a decision
`bindingPct` already made from the same figure. Only the SPELLING is
bounded. `pctText` is the ONE function every surface renders a percentage
through — `table.go`'s `tableGrid`, the panel's `candidateCell.head` /
`addCandidateCell`, and the monitor's `miniAccountText` — so no two surfaces
can round or bound one differently: it prints the rounded figure up to
`displayPctCap` (999) in EITHER direction and, past that, the elision
marker — `">999%"` above it, `"<-999%"` below — rather than a rewritten
`"999%"`/`"-999%"`, honest about what the store did not report, in place
of a figure that would be true of exactly one value and false of every
other past the cap. Both tails are bounded because both are reachable:
`oauth`'s projection drops NaN and ±Inf but deliberately keeps a NEGATIVE
utilization, a measurement worth showing and one a store can report as
easily as an oversized positive one. Bounding the VALUE instead would have
sized every column a store-supplied number touches off a single absurd
measurement, since `minTableWidth` charges a pinned column its widest
figure; bounding only the rendered text keeps that cost fixed — six
columns, the width of `"<-999%"`, whatever the number — without touching
what the account card and `tycswap list` — which read the same entry and
share no column with any other account — still print in full.

**How a header abbreviates — `headerLadders`, injectivity.** A column's
name is fitted to the sub-cell `measureColumns` already sized from its
figures (`bodyW`), never the reverse: a name too wide for that sub-cell
abbreviates, and the sub-cell never widens to make room for a name.
`headerLadders(cols, floor)` builds the abbreviation ladder once per render,
from the table's DISTINCT label set alone (`distinctColumnLabels`) — never
from width — so which spellings exist to choose among is a fact about the
roster, and only which one is IN FORCE is a width question. `headerLevels`
returns a sequence of LEVELS, level 0 the identity mapping, each later level
a map from every distinct label to a shorter spelling, admitted only when it
is `injectiveLevel` — one-to-one from distinct label to spelling, so no two
different models are ever spelled alike; an ambiguous heading is terse, a
colliding one is FALSE, and a table that lies is worse than one that does
not fit. `windowLabel5h`/`windowLabel7d` sit outside `scoped`
(`distinctColumnLabels`) and so pass through every level unabbreviated —
they are two columns already, and the two windows every other surface
names the same way.

The levels are tried in order, each built from the last one accepted.
`dropSharedPrefix` strips the longest token prefix (cut at the last `-`/`_`)
every SCOPED label shares — a no-op with fewer than two distinct scoped
labels — and `dropDateToken` strips a trailing eight-digit release-date
token (`-20251101`) from any scoped label that carries one; both also have
to `clearsFloor` (no label's spelling falls under `floor` columns, unless
the label's own full width already sits under it, in which case that label
is spelled whole) or the step is skipped, base unchanged, and the next step
tried in its place. Both steps are STABLE: each depends on the label set
only through a prefix or a token shape it shares, so admitting an account
never reflows a spelling the ladder already settled on for another label.
Once both are tried, `elideLevel`/`middleElide` takes over: it cuts a
scoped label's MIDDLE out at `k` columns and keeps both ends — a model
distinguishes itself by family at the head and by version at the tail,
where a plain prefix clip keeps only the half every sibling shares. `k`
descends one column at a time from the current widest label's width down to
`floor` — the loop's own bound rather than a `clearsFloor` call, since every
`k` tried is already at least `floor` and `middleElide` only ever cuts a
label down TO `k`, never past it. The first `k` whose
elision collides ends the ladder outright (`injectiveLevel` fails → the
loop breaks rather than skips this step), since an elision that collides at
`k` collides at every narrower `k` too — the head and tail it keeps are
only ever cut further, never rearranged. `windowColumn.hdrMin()` is the
width of the ladder's last, coarsest accepted level, and `floorW()` — the
term `minTableWidth` charges the column — is `max(pctW, hdrMin)`.

`floor` is `opts.headerFloor`, raised to `headerHardFloor` (2 — the width
of `"5h"`/`"7d"` and of a percentage, the narrowest anything on the table
may ever be spelled) when a surface asks for less. The two surfaces ask for
different floors because their release bars name a window differently: the
MONITOR (`monitorTableOpts`) takes the ladder's own floor, `headerHardFloor`
— its own fallback, `miniAccountText`, never names a scoped window at all
until it has run out, so the ladder is free to spend width on figures
rather than syllables. The PANEL (`candidateTableOpts`) sets `headerFloor:
4` — never below a whole syllable — because its own fallback, `candidateRow`,
always prints a model's name in full, and a header trimmed to two columns
would name a window worse than the layout it replaces.

Once the ladder is built, which LEVEL is in force is one number shared by
every column (`setHeaderLevel`), never a per-column choice — the header
reads at one level of detail, not a mixture. `shrinkTableHeaders` — the
width ladder's rung (a), and the FIRST rung walked — coarsens that shared
level by exactly one step per call, while a column is still wider than its
own figures need, and never past the ladder's last level. It is walked to
exhaustion before any countdown is shed, so every countdown rung below it
always measures against one fixed, already-coarsest header level, never one
still in motion.

**Hard minimums — `pinTableColumns`, `protectedColumn`.** What the table may
never give up is DECLARED before the ladder walks, not guarded after it.
`pinTableColumns` marks three kinds of column as PINNED. Every COUNTED one:
it is the binding column for some row, and the panel's `"counting …"` note
names it, so dropping it would both hide the figure that row is ranked and
decided by and make the header disagree with the note above it. Every column
holding some row's PROTECTED cell — `protectedColumn(cells)` is that row's
BINDING window, or, for a row with no counted cell at all, its HIGHEST
figure. That second case is not a corner: the monitor enumerates on the empty
model axis, so an account whose only windows are per-model ones has nothing
counted anywhere, and the figure that says whether it can serve anything is
the largest of them. And every column some row has RUN OUT in, on the surface
whose own per-row layout states an exhausted window
(`opts.policy.PinExhausted`). Pinning is then closed over LABEL GROUPS: all
the columns of one label, or none. Dropping one occurrence of a repeated
label leaves a row that really does report that model rendering an em dash
under it — a false statement, and the only one the `(label, occurrence)`
column key makes structurally possible.

**A table's EXISTENCE is one comparison — `minTableWidth`.** Whether a
table can be laid out AT ALL, at a given width, is decided before any
layout, by a closed-form floor:

	slot cell + label floor
	  + for each PINNED column: the gutter + max(its figures, its coarsest name)
	  ... or, when a SPAN row asks for more,
	slot cell + label floor + the gutter + the widest span floor

`slotW` is `opts.indent` + the widest slot number (at least 2) +
`tableSlotGap`; the label floor is one column for the bare ellipsis, or none
at all when no row carries a label. `windowColumn.floorW()` is
`max(pctW, hdrMin)` — the column's own figures, or the width of the LAST rung
of its abbreviation ladder, whichever needs more. `minTableWidth(rows, opts)`
is the larger of the two, and `layoutWindowTable` returns `(tableLayout{},
false)` for any width below it — the same `false` `renderWindowTable` and
`pickWindowTable` give whenever no table can be built.

Two properties follow, and both are load-bearing. It READS NO CLOCK: every
countdown is already shed at the floor, so no term of it can move as a reset
comes due, and a table's EXISTENCE cannot change between two frames at one
terminal width. And it is MONOTONE in width, so a terminal that grew never
loses the POSSIBILITY of a table — there is exactly one false→true
transition over any width range. Below `minTableWidth` this is the whole
answer: a surface never attempts to build a table, and its per-row layout
is the render. AT OR ABOVE `minTableWidth`, existence is NECESSARY but no
longer SUFFICIENT — which layout a surface actually draws is a further,
PRICED question, below.

**The priced choice — `pickWindowTable`, `layoutScore`, `releaseBar`,
`fullWidth`.** A union-column table existing is not the same as it being
the better read, and the reason is structural, not a defect in how far it
narrows: its columns are the union ACROSS rows, so every row pays the
gutter and the sub-cell of every OTHER row's windows too — including an em
dash where it reports no such window — while a per-row layout pays only
for what its own row has. On a roster whose accounts report different
scoped models the table can therefore end up stating the same figures
spread over more columns, and buys the room by shedding countdowns a
per-row layout still affords. No reordering of the width ladder's rungs
fixes that; it is the PRICE of column alignment, and it is why the
existence floor above can only ever be a cheap PRE-CHECK — there is no
sense pricing a layout whose own floor already rules it out — never the
whole decision.

`pickWindowTable(rows, width, now, opts, perRow) (windowTable, bool)` is
the single entry point both `candidatesText` (autoview.go) and
`monitorLayout` (widgets.go) call on every render. `now` reaches only the
layout actually DRAWN once the choice is made; the choice itself is priced
without it (`renderClock`, below). `perRow` is a `perRowPricer` —
`func(width int) layoutScore` — the caller's OWN closure over its per-row
layout, able to price it at ANY width, not only the one being rendered:
both callers build their per-row LINES for the current width
unconditionally first, spelled live against `now` (those are exactly the
lines drawn if the table loses), and `perRow` itself prices the identical
rows at `widestClock()` regardless of which width it is asked about
(`candidateRowPriced`/`candidateLabelRowPriced` on the panel,
`miniAccountPriced` on the monitor). `layoutScore{figures, countdowns,
spanChars, identChars}` is what a render actually DISPLAYS on each axis —
the reader's three questions: how used is each window (figures), when does
each free up (countdowns), why can the engine not use this account
(spanChars) — and `identChars`, deliberately excluded from every
comparison below (IDENTITY IS NOT AN AXIS, further down). `pricedText` is
the line builder both the table's `tableLine` and the per-row renderers
share to produce one: every appended run is either CHROME (`.chrome`) — a
gutter, a window's label, a separator, priced on no axis — or a DATUM on
one of the three compared axes (`.figure`, `.countdown`, `.span`/
`.spanWhole`, plus `.identity`/`.identityWhole`/`.identityRun` for the
measured-but-unused fourth). `pricedText.fit` clips the line exactly as
`truncRich` always has and reports only what SURVIVED: a figure or a
countdown counts only when the WHOLE of it is on the line — half a
percentage states nothing — while a span message or an identity cell
counts by the columns of it that made it (`shownContent`), one marker's
width forfeited to the cut mark where it did not fit whole. `layoutScore.
plus` sums two rows' scores, so a whole per-row layout is priced the
identical way one `tableLayout.render` prices a whole table.

SCORING AND DRAWING READ TWO DIFFERENT CLOCKS, and that is what keeps the
CHOICE itself off the clock (`renderClock`, `data.go`). A countdown's
spelling narrows as it ticks — "2h 13m" is four columns wider than "9m" —
so a layout measured against the live clock is a different width on every
frame; harmless while it only decides how much a layout SHOWS, not
harmless when it decides WHICH layout a surface draws, since the panel
would then flip between the table and the per-row layout between frames at
one unchanged terminal width. A PRICED layout (`widestClock`) spells every
countdown at `countdownWidest`, the widest `countdownSpelling` can ever
produce over its whole `float64` domain ("23h 59m") — `countdownSpelling`
bounds `formatDuration`'s otherwise-unbounded day form at `displayResetCap`
(ten days), eliding a farther-out reset as `displayResetOver` (`">9d"`)
rather than spelling it out, so `countdownWidest` is a BOUND rather than an
assumption about which resets this codebase happens to report — whatever the
hour — a `layoutScore`, `fullWidth`, and
therefore the choice between two layouts, are pure functions of the rows,
the width and the surface. A DRAWN layout (`liveClock`) spells every
countdown from the render's actual `now`, so the terminal shows the real
figure and the columns a short countdown frees are spent on real detail.
The drawn layout is never narrower per countdown than the priced one, so
it states at least what it was priced at and usually more — the bar it
cleared is a lower bound, never a promise of exactly that much.

NAIVE SAME-WIDTH DOMINANCE IS NOT MONOTONE, and that is a MEASUREMENT, not
a hypothesis. `layoutScore.atLeast(other)` — `figures >= `, `countdowns >=
`, `spanChars >= `, ALL THREE, nothing summed or exchanged, since the axes
do not convert into one another and no exchange rate between a countdown
and a column of a reason is defensible — compared at the RENDER width
alone lets the two layouts go INCOMPARABLE over a band of widths: the
table ahead on figures, the per-row layout ahead on countdowns, and a
choice that flips inside such a band takes back on one axis what it gives
on another as the terminal WIDENS. On the property corpus the naive
same-width comparison costs three of six figures on a panel exactly ONE
COLUMN WIDER (29 → 30) — precisely the defect "widening a terminal only
ever gives detail back" (I8) exists to forbid.

`pickWindowTable` closes that with a CONSTANT reference rather than a
sharper comparison:

	priced := layoutWindowTable(rows, width, widestClock(), opts)
	here   := perRow(width)
	ref    := here
	if priced.full > width { ref = perRow(priced.full) }
	draw the table iff priced.score.atLeast(releaseBar(here, ref))
	(then re-lay the table out at liveClock(now) for what is actually drawn)

`tableLayout.fullWidth()` (`priced.full`) is the width at which the WINDOW
rows shed no COUNTED data at all — every column present, every countdown
shown, the identity cell at its full desire; only naming is already at its
coarsest there, since a header is priced on no axis and rung (a) is spent
before any of the three that are.

NO SPAN ROW'S MESSAGE LENGTH APPEARS IN `fullWidth`, and that is the
point: it is the reference the release bar is taken at, so anything
charged to it is charged to EVERY account on the surface — sizing it by
the widest whole message would let the LENGTH of one account's reason
text, not even rendered at the widths in question, decide whether every
OTHER account gets the table. The message axis is priced at the render
width instead (below), where both layouts are equally bound by the
terminal, so a message costs nothing but its own row.

`fullWidth` is measured on the PRICED layout, so it inherits the same
guarantee: every countdown it sums is `countdownWidest` wherever a column
shows one at all, never a live remaining time, so a countdown ticking down
cannot move it. `releaseBar(here, ref)` builds the bar the table's own
score must clear:
`ref`'s figures and countdowns — the per-row layout priced at `fullWidth`,
which by the per-row layout's own width-monotonicity can never be less
than what it displays at the actual, narrower render width — combined with
`here`'s span characters, priced at the ACTUAL width, because a span
message is bound by the terminal in BOTH layouts alike and a wider
reference would demand of the table what no layout can show in hand
(`spanIdentW` already guarantees the table's message is never the shorter
of the two at one width, so this clause is a standing check that it never
starts to be, not an active one).

A CONSTANT bar is what restores monotonicity. Both layouts are
individually monotone in the width on their own compared axes (I8), so a
bar that does not move with the render width makes the set of widths at
which the table wins UPWARD CLOSED, and an upward-closed choice between
two monotone layouts is itself monotone on every axis: two widths that
draw the same layout are trivially monotone with each other; at the single
boundary where the choice flips from per-row to table, the table displays
at least the bar, and the bar is the per-row layout's score at `fullWidth
≥` that boundary, which per-row monotonicity makes at least what the
per-row layout displayed one column narrower; and "table below, per-row
above" cannot happen at all — that is what upward closure means.

THE SAME CONSTRUCTION PROVES I13 (below) rather than arguing for it
separately: at or below `fullWidth` the bar already dominates the per-row
layout's actual score at that width — `ref = perRow(fullWidth) ≥
perRow(width)` by the identical per-row monotonicity — so a table that
clears the bar clears the per-row layout too; at or above `fullWidth`,
`ref = here` and the table is stating EVERYTHING the rows contain, which
nothing built from the same rows can ever exceed.

IDENTITY IS NOT AN AXIS. `identChars` — the columns of an account's own
alias or email a layout renders — is measured by the identical machinery
and carried on every `layoutScore`, but neither `atLeast` nor `releaseBar`
reads it (`releaseBar`'s own return value does not even forward it). A
shared table buys its column alignment out of that very cell (rung (e),
below), so weighing identity in the same comparison would refuse the table
at exactly the widths where lining every row's figures up under one
heading is what it is FOR — and the slot number still names the account
for `tycswap switch`/`tycswap use` either way. This is the one respect in
which the choice does not stay monotone: a surface can show a SHORTER
email at the width where it just switched into table mode than it showed
one column narrower, in the per-row layout it left behind. It is measured
and reported precisely so this can be stated as a documented
NON-GUARANTEE rather than discovered as a surprise — see I8, below.

**The width ladder — NAMING is the cheapest thing on the row, DATA outranks
IDENTITY.** `renderWindowTable`'s own comment states the rungs, re-measured
after every single step so the table gives up only as much as the width
demands:

	(a) the HEADER TEXT, one abbreviation level at a time, all columns together
	(b) countdowns of UNCOUNTED columns, rightmost first
	(c) countdowns of COUNTED non-binding columns, rightmost first
	(d) the BINDING cell's countdown (opts.policy.KeepBindingCountdown)
	(e) the label cell, narrowing toward the bare ellipsis
	(f) the countdown of an EXHAUSTED column (opts.policy.PinExhausted)
	(g) whole LABEL GROUPS, the group the FEWEST rows report going first
	(h) a SPAN row's message, cut with the ellipsis marker, never past its floor

(a) is `shrinkTableHeaders`; (b)–(d) and (f) are one function,
`shedCountdown`, walking the rungs `countdownRung` assigns each column; (g)
is `dropTableGroup`; (h) is `tableLine`'s span branch cutting against
`spanBudget`. Below (g) the width is under `minTableWidth` by construction
and the ladder never arrives there.

NO RUNG'S PLACE IS A MATTER OF TASTE, but (a)'s two neighbors are NOT
placed for the same kind of reason, and only one of the two reasons is a
hard requirement. Both orderings of (a) and (e) were measured against the
monotonicity sweep — header-before-label as built, and label-before-header
as a deliberate trial — and BOTH came back monotone: nothing about
monotonicity alone picks between them, so (a) sitting above (e) is a VALUE
choice, not a forced one. It is the identical INFORMATION-VALUE argument
that places (a) above the countdowns (b)–(d): a NAME is not a MEASUREMENT,
and it is not an IDENTITY either. The header names a column whose figures
are on the screen regardless of how the column is spelled — a reader who
has lost the spelling can usually still tell one of two or three columns
from the figures and their fixed canonical order (`5h`, then `7d`, then
scoped by name) — while the shared label states each row's own identity,
which nothing else on the row restates once it is gone; a countdown is
likewise the only cell that says when a window frees up. With a real model
name ("claude-opus-4-5-20251101") the header term is what makes a column
dear at every ordinary width, so it is spent first: not because narrowing
it costs nothing, but because, cell for cell, it buys the least of
anything else on the row — countdown, label, or column. (e) sits above (g)
because a WINDOW row's cells are the DATA the row exists to show while the
label is only its IDENTITY, and the slot number still identifies the row
once the label is a bare ellipsis. (e) is a single clamp rather than a
re-measuring loop, so a table with far too little room falls through with
the label already pinned at its floor. (f) sits BELOW (e) because an
exhausted window's reset is the one countdown the per-row layout states
unconditionally — worth more than every account's email, and nothing else
buys it. Rungs (a)–(d), (f) and (g) read `cols` alone, so they answer a
WINDOW row's overflow and only a WINDOW row's: no countdown a SPAN row does
not carry can buy that row's message a column.

Each rung is walked to EXHAUSTION before the next begins, which is what
keeps this order monotone as well as forced. (a) stops early only by
FITTING, so a countdown is shed only at a width where the headers are
already spelled at their coarsest — the surviving countdown set at any width
is then a function of that width and one fixed level, never of a level still
in motion. Likewise the label narrows only once (a)–(d) are spent, so
`labelW = clamp(width − K, floor, labelWant)` against a CONSTANT K, and a
column drops only once the label is at its floor. Each quantity the reader
sees is therefore monotone in the width on its own (I8).

**Per-surface policy — `tablePolicy`.** Two of those rungs cannot read the
same way on both surfaces, because the table's release bar is each surface's
OWN per-row fallback and the two fallbacks say different things.
`PinExhausted` is true on the MONITOR, where `miniAccountText` states every
exhausted window unconditionally (`"Fable (!)"`, `"5h 100% (resets 12m)"`),
and false on the PANEL, where `candidateRow` DISCARDS an exhausted uncounted
figure at every width — pinning one there would trade the panel's whole table
away to protect a figure the layout it replaces then throws away.
`KeepBindingCountdown` is true on the PANEL, mirroring `candidateShedSteps`'
final rung, and false on the MONITOR, whose fallback has no such rung.

A SPAN row answers to (e) too, but the reason is a different one — not DATA
outranking IDENTITY within the row, but one row's DATA never outranking
every OTHER row's IDENTITY. The label column is SHARED: every row renders
into the same `labelW`, so a message measured at its own full width would
buy itself columns out of every other account's email — the 82-column
re-login sentinel note, sized that way, would narrow every healthy row's
identity to make room for one unhealthy row's reason. So `tableWidth` weighs
a SPAN row's demand on (e) at `spanFloorWidth` — the stub its message is
GUARANTEED — never at the message's own width, and (e)'s clamp fires on a
span row's behalf only once the terminal cannot hold even that stub.
Above that point the message alone narrows, for free, as `spanBudget`
shrinks with the terminal while `labelW` sits untouched; only once the
message is already pinned at `spanFloor` and the terminal is still too
narrow does (e) begin narrowing the shared label — toward ITS OWN floor, the
bare ellipsis — on the message's behalf. The visible order on a SPAN row is
therefore the message first, down to its own floor, and the shared label
second, down to its own — the reverse of a WINDOW row's label-before-columns
order, because what rung (e) protects there is not this row's own data but
every OTHER row's identity.

**Which column goes — `dropTableGroup`.** A pinned column is on no rung at
all, and pinning is closed over label groups, so every group rung (g) can
reach is droppable whole. Among those, the victim is the group the FEWEST
ROWS report a figure in, ties going to the rightmost in canonical order.
Rightmost-first alone made the table rob the many to spare the one: a model
three accounts reported went before a model one stranger reported, purely
because the stranger's column sorted later. ISOLATION IS UNATTAINABLE for a
shared column — it is bought for everyone or for no one — so which column
goes is the only fairness question a table can actually answer, and the
count is of ACCOUNTS, not of cells: a row reporting two windows of one model
is one account either way.

**The header says when a column went — `tableElision`.** A row's em dash
means "this account reports no such window"; without a marker there would be
nothing anywhere to say that a window it DOES report is not on the grid at
all. So a table that dropped columns ends its header with a muted, dim
`"+N"` — at most two columns, charged to the header line only and printed
only where it fits. Past what two columns can spell it reads `"+9"`, which is
all a marker this size can honestly say.

**The post-conditions — `sound`.** `windowRowsWidth() <= width`, `spansFit()`
and `rowsKeepAFigure()` are checked after the ladder settles, as
POST-CONDITIONS rather than gates: at or above `minTableWidth` the
fully-shed table fits, every span row clears its floor, and every window
row keeps its protected cell, so none of the three is ever false. `sound`
states them as a hard assertion — a `false` return, the same total flip —
so that a change to the pinned sets that makes one of them reachable shows
up as a surface that flips rather than as a table that lies. The property
suite asserts
`ok == (width >= minTableWidth(rows, opts))` over the whole corpus, which is
the proof they never fire; a mutation removing the per-row protected column
makes `rowsKeepAFigure` fire and that assertion catch it.

Label narrowing (e) sits entirely inside the table's own layout machinery
(`layoutWindowTable`, shared by `renderWindowTable`, `priceWindowTable` and
`pickWindowTable`) — neither `candidatesText` nor `monitorLayout` ever sees
a label width or takes any part in narrowing it; each receives only the
finished `windowTable` and the `bool` outcome `pickWindowTable` gives them.

**A SPAN row's floor — `spanFloor`, `spanTokenFloor`, `spanMin`,
`measureSpans`, `spanFloorWidth`, `spansFit`.** A SPAN row (`tableRow.span()`)
has one cell of content, its whole `Span` message, and the table's layout
machinery protects a minimum of it the way `PinExhausted` protects an
exhausted WINDOW column. `spanFloor` treats two kinds of message
differently. A SENTENCE — a classification word and then the detail —
keeps its whole first word plus `footerEllipse`'s width, or the whole
message when that is shorter: the message's classifying first word —
"quarantined", "re-login", "usage" — is the reason the row is on the table
at all, and a cut landing inside it ("quarantin…") would state nothing a
reader could act on, so the floor never falls inside that word. A single
TOKEN — a message with no space at all, which is what `sentinelLabel`
prints for a sentinel state absent from `sentinelNotes`, the usage store's
own diagnostic identifier verbatim — has no classification word to keep, so
`spanFloor` may cut it down to `spanTokenFloor` (12 columns, the width the
widest PHRASED first word in this codebase already demands, "quarantined
…"): a prefix of a diagnostic identifier identifies it as well as anything
short of the whole does, and bounding it here keeps the width this
package's own choice rather than one a store-supplied string makes on its
behalf — the alternative, rewording the raw state behind a word of this
package's own, was rejected because it would change what `tycswap list`, the
account card and the watch and switch screens print to answer a question
only the narrow table asks.

`measureSpans` walks every SPAN row once and returns `tableSpans{any,
floor}`: whether the table carries any SPAN row, and the widest FLOOR among
them — the ladder reasons about a table's SPAN rows as one combined demand,
not per row. The LADDER asks only for the floor: a span message is fitted
to whatever the row has left (rung (e)), and the only width it may ever
demand OF THE SHARED LABEL CELL there is its floor; the full message plays
no part in sizing any cell, and no part in the PRICING either (`fullWidth`,
below) — a span row's length is answered where it is rendered, never where
the reference width is located. `measureSpans` takes each floor through
`spanMin`, which bounds it by `spanHardCap` (24). That cap is a documented
backstop and nothing else: every message this codebase can produce, a
sentence or a token, has a floor at or under `spanTokenFloor`, well inside
the cap, and a standing test proves so. A floor that reached the cap would
be a DATA bug — a store-supplied string setting every account's identity
width — and `spanTokenFloor` is what rules it out without rewording what
the store wrote.

`tableWidth` reads BOTH row shapes and returns the wider:
`windowRowsWidth()`, and `spanFloorWidth()` — the slot and label cells, then,
behind one `tableGutter`, `sp.floor`, never a SPAN row's message at full
width. Rung (e)'s single clamp reads this combined width, so it narrows
`labelW` on a SPAN row's behalf only down to the point that guarantees the
floor, never further — past that point the row's own message absorbs the
narrowing instead (above). `spansFit(width)` is vacuously true when the table
carries no SPAN row (`!sp.any`), else whether `spanBudget(width, slotW,
labelW)` (`width` less `slotW`, `labelW` and one `tableGutter`) reaches at
least `sp.floor`; `spanBudget` already falls below any non-negative floor once
the identity cells alone overflow `width`, so one comparison covers both a
too-narrow label and a message too long for what is left of it. It is a
POST-CONDITION, not a gate: `minTableWidth` already charges the widest span
floor, so wherever the table renders it is satisfied by construction. This is
the LADDER's use of `spanBudget` — deciding how far the SHARED `labelW` may
narrow — and it is the only place `spanBudget` is ever called against that
shared width.

`tableLine`'s span branch calls `spanBudget` a second way, and this is where
the DRAWN message states more than the ladder's guarantee requires. The
message is drawn starting right after THIS row's OWN label — `clipText(r.Span,
spanBudget(width, slotW, rtWidth(label)))` in `r.SpanFg`, where `label` is
the row's own (already-clipped) label, not the padded-out shared column — so
it has nothing to align with and pays nothing for a wider label two rows
down. A row lays no figure into any column, so charging it for the table's
widest label would make it state LESS than the per-row layout it replaces
(`candidateLabelRow` / `miniAccountText` each give the message the whole
rest of their own line); never-less-than-the-fallback outranks column
alignment, so two SPAN rows may begin their messages at different columns
from one another when their own labels differ in width. `spansFit`'s
guarantee is what makes this safe without `tableLine` re-checking the floor
itself: a row's own label is never wider than the shared `labelW` the ladder
measured, so the shared floor is a LOWER bound on every row's real budget,
and a row whose own label is narrower than the widest simply gets more room
than the ladder promised, never less. `spanBudget`'s baseline here is NOT the
shared `slotW+labelW+tableGutter` offset `windowRowsWidth` measures its first
column from — it is `slotW` plus THIS row's own label — so a SPAN row's
message begins at the same column a WINDOW row's first cell would only on a
roster where the row's own label happens to be the table's widest
(`TestWindowTableSpanStartsAfterItsOwnLabel`); on any other roster a
shorter-labelled SPAN row's message begins earlier and states more of its
reason than the shared floor alone guarantees, a direct consequence of the
lower-bound relationship above, never a violation of it. The message shrinks
well before `labelW` does: since rung (e) leaves the SHARED `labelW`
untouched across the whole band of widths wide enough for the floor alone, a
SPAN row's message clips steadily as the terminal narrows while every OTHER
row's email stays full, and only once the message is pinned at its own
`spanFloor` does the shared label begin to give ground, toward its own
floor in turn (`TestWindowTableSpanPrecedence`). A message that already
fits its budget renders whole, with no ellipsis at all; a message the row
must cut carries `r.SpanFg` — its own color — on the trailing ellipsis,
never `truncRich`'s `colMuted` marker (`TestWindowTableSpanRowFitsWidth`).

**Total-flip fallback.** The choice is still not a per-row signal —
`pickWindowTable` is called once per surface, over every row that surface
would show, and its one `bool` verdict (existence AND `score.atLeast
(releaseBar(here, ref))`, above) covers the whole set. Both call sites
treat it as total: `candidatesText` builds every `ordered
[]candidateEntry` through `candidateEntry.rowPriced` FIRST,
unconditionally — summing the results into `here`, the score of the lines
it draws if the table loses — then wraps `here` (and a re-render at any
other width `pickWindowTable` asks for) in a `perRowPricer` closure and
hands the whole row set to `pickWindowTable`, drawing the table's `Header`
(when it has any segments — `len(table.Header.segs) > 0`; a table of
nothing but SPAN rows has no columns and so no header, per
`tableColumns`) and every `table.Lines` entry when it wins, or walking the
already-built per-row lines — `candidateLabelRowPriced`/
`candidateRowPriced` under their unpriced aliases `candidateLabelRow`/
`candidateRow` — when it does not; `monitorLayout` (below) does the
analogous thing over `miniAccountPriced`. There is no width at which one
row renders through the table while its neighbor renders through the
per-row shape: the same verdict decides every row's layout for that
render. The point below which no table is even attempted is
`minTableWidth(rows, opts)` (above), so IT moves with the row set — more
PINNED columns, longer labels, a longer reason, or a wider slot number all
push it out — but it is a FLOOR on where the table can start winning, not
the width at which it does: at or above it, the actual point where the
table first clears `releaseBar` depends on how the per-row layout's own
score grows with width too, and can sit anywhere from `minTableWidth`
upward, though never later than `fullWidth` (above `fullWidth` the table
states everything the rows contain and nothing beats that). It can differ
between the panel and the monitor for the identical snapshot for the same
reason it always could — the two surfaces' own row sets, PINNED columns
and labels differ even when the underlying accounts are the same — and
additionally because their two release-bar layouts (`candidateRow` vs
`miniAccountText`) price differently against the identical table. Both
surfaces measure against their content budget IN FULL: `monitorLayout`
treats its `width` parameter as the monitor's whole content budget — the same
terminal width `dashboard.go` hands every other block, with no internal
margin subtracted — exactly as the panel measures against its own full inner
width. Neither surface reserves a margin for a frame that does not exist:
nothing draws a border or margin around either block's lines, and the render
path (`clipRichLines`) already holds every block to the width it is given,
so a reserved margin buys nothing but costs real columns per line and,
through the flip, whole columns of the table.

**The per-row fallback layout.** This is what a surface renders whenever
the priced choice (above) does not draw the table — whether because no
table exists at this width at all (`pickWindowTable`'s `bool` is `false`
before it ever reaches the pricing) or because one exists but does not
clear `releaseBar` — and it is ALSO what the panel builds unconditionally,
on every render, to have a `perRowPricer` to hand `pickWindowTable` in the
first place: the layout itself, and its own width discipline, are
independent of the table, and every row reports what it displays as it
builds (`candidateRowPriced`/`candidateLabelRowPriced`; `candidateRow`/
`candidateLabelRow` are thin wrappers that discard the score for a caller
that only wants the line). Every line the panel emits in this mode — the
`"Next best · counting ..."` header (rendered whether or not a table was
even priced; it carries no window cells to shed either way and no axis to
price), a readable usage row (`candidateRow`/`candidateRowPriced`), a
quarantined/sentinel/usage-unknown row (`candidateLabelRow`/
`candidateLabelRowPriced`, one function for all three), and the
`"no other switchable accounts"` empty-state line — must never wrap
regardless of terminal width. `candidatesText` takes the same inner width
`accountsPanelText` already receives from `view` (`m.width`, defaulted the
same way, `width <= 0` falling back to 80), and each row shape fits its own
body to that width before render. The header runs straight through
`truncRich`; every row below it — a readable usage row, a label row, and
the empty-state line — runs its fitted body through `truncRich` via the
shared `candidateRowText` (or its priced sibling `pricedRowText`, below).
The table's own `Header` and `Lines` route through the identical
`candidateRowText` when the table WINS the price comparison, so both
layouts share the one row-break mechanism regardless of which one a given
render uses.

The ellipsis falls in a different place for each row shape:

- A readable usage row sheds one reduction per re-measure, down the rungs
  of `candidateShedSteps`: the countdowns of its uncounted windows, those
  windows, the countdowns of its counted non-binding windows, those
  windows, and the binding window's own countdown — rightmost first within
  each rung — after which the email clips down to a bare ellipsis. A
  countdown is supporting detail, so it always precedes the cell carrying
  it, and a whole class precedes the next more informative one. The binding
  window's label and percentage are on no rung at all: they are the row's
  ranking key and the figure the engine's own pick agrees with, and they
  survive every reduction.
- A quarantined, sentinel, or usage-unknown row carries no window cells to
  shed, so `candidateLabelRow` narrows it in a different order: the slot
  number always survives; the email clips first, down to a bare ellipsis;
  only once the email is fully clipped and the row still does not fit does
  the label itself lose its tail to an ellipsis. The label's own wording is
  truncated, never reworded — the reason a row sits on the panel at all is
  the last thing given up.
- The header and the empty-state line carry no cells or label to shed —
  each is one run of muted text — and clip as a whole line with a trailing
  ellipsis when the terminal is narrower than the text itself.

A row that still overflows after its own shape-specific narrowing truncates
as a whole line via the shared `truncRich` guard rather than wrap,
consistent with `view`'s pinned-chrome layout, where no panel line is
allowed to push the scrollable event log around.

**Why every row below the header routes through `candidateRowText`.**
`pricedRowText` gives a `pricedText` body the identical row break and fit
and additionally reports the `layoutScore` that fit left standing —
`candidateLabelRowPriced` builds on it directly; `candidateRowPriced` fits
its own body through `pricedText.fit` first (so the row's OWN shed ladder
runs before the row break is prepended) and then wraps the result the same
way. Each of those rows' fitted body picks up its leading row break there, and the
break is appended as its OWN unstyled segment ahead of the styled body
rather than folded into the body's first colored segment (the header needs
no such break — it opens the panel). `richText.render` styles each segment
independently, and lipgloss's left-align padding stretches every rendered
line out to the widest line in the block; a styled segment whose text
starts with a newline would have that blank leading line padded out to the
panel's full width, and the padding lands at the END of the PRECEDING row,
not the row the segment belongs to — so a row that individually fits its
own width could still push the row ABOVE it past the terminal width and
wrap. Routing every such row through one function that keeps the break
unstyled is what makes the never-wrap contract hold across the whole
rendered panel at once, not merely line-by-line in isolation.

**The dashboard accounts monitor (`internal/tui/widgets.go`).**
`accountsPanelText` and `accountsMonitorCapped` both draw their content from
`monitorLayout(snap, width, showMinis, threshold, now, allowTable)`, the one
place every caller builds a monitor's blocks. `accountsPanelText` is
`monitorPanelText(..., allowTable: true)`, the unbudgeted caller that
always wants the table when it fits. `accountsMonitorCapped`, the budgeted
caller, reaches `monitorLayout` through `fitMonitor` and `cappedMonitor`
(below) with `allowTable` either way, so it alone can ask for the table's
column layout to be skipped even where it would otherwise fit.
`monitorRow(acc reporting.AccountSnapshot) tableRow` projects one non-active
account: `Slot` and `Label` (`miniLabelCell` — the alias form, `[tag]`, and
the `(disabled)` marker in the warning color — shared with `miniAccountText`
byte-for-byte, not just in effect), `Windows` from
`candidateWindows(acc.Usage.LastGood, nil)` — the `nil` model list is
deliberate: the monitor's counted axis is always the bare `5h`/`7d` pair,
never `autoswitch.model`, and passing `nil` reproduces that axis through the
same projection the panel uses for its own configured one, rather than a
second axis implementation — or, for a sentinel or a slot with no windows at
all, a SPAN row carrying `sentinelLabel` / `"usage unknown"`. It takes no
clock, and neither does the panel's projection: a row carries the raw
`ResetsAt`, and the hour enters only where a layout SPELLS a countdown
(`renderClock`) — which is what lets the same rows be priced off the clock
and drawn against it. Whenever `showMinis`, `monitorLayout`
builds `miniAccountPriced` for every non-active account first, summing the
result into `here`: this is both what the monitor draws when the table
loses (or is disallowed) and the score its `perRowPricer` closure returns
when asked to price the render width, so it is built whether or not a
table is even attempted, eagerly rather than lazily, since it is itself
half of the bar the table is priced against.
Only when `allowTable` too does `monitorLayout` additionally collect this
row set into `monitorRow`s and hand both it and the `perRowPricer` closure
to `pickWindowTable` ONCE per render; `tabled` is set — and the table
drawn — only when that call reports `true` (a table exists AND its score
clears `releaseBar(here, ref)`, `ref` being the SAME closure re-run at the
table's own `fullWidth` when that is wider than the render — above;
`monitorTableOpts`'s `PinExhausted: true` policy is most of why this
comparison is close to a formality on this surface: see I13, below). So
the column layout, when it is used at all, is one priced decision for the
whole monitor, not one per visible fragment of it; only the TABLE's own
build is skipped when `allowTable` is false, which is the height cap's own
saving (below) — the per-row layout is needed there regardless, since it
is what the height cap's own `allowTable=false` pass renders.

`monitorLayout` returns `[][]richText`, one GROUP per account, so its
caller can admit or drop a whole account atomically: the active account is
a group of one (`accountCardText`, its own full card); a non-active account
is a group of one `miniAccountText` line when the table does not fit or is
disallowed (`!tabled`), or, when it fits and is allowed, a group of one
table line — except the first such group in snapshot order, which is two:
the table's `Header` prepended ahead of that account's own line.
`monitorPanelText` flattens `monitorLayout`'s groups back into the flat
sequence `joinBlocks` already knows how to breathe (a blank line around a
multi-line block, a single line between two one-liners); `cappedMonitor`
instead walks `monitorLayout`'s groups directly so its height cap admits or
rejects a header together with the row under it, never one without the
other — which is what makes both halves of that contract true at once: the
monitor never shows a header row with nothing under it, and never drops the
header while rows below it remain. A pathological one-line budget can
still trim that first group's own two lines down to just the header once
it has been flattened and re-split; `cappedMonitor` special-cases exactly
that (`len(lines) == 1 && lines[0] == header`) and substitutes the row the
header was standing in front of, so even a one-line monitor shows an
account, never a bare column heading.

**The height cap is a SECOND, independent price, layered on top of the
first.** `layoutScore.atLeast` (above) has already decided, inside
`monitorLayout` itself, whether a render wants the table at its full
content width; `accountsMonitorCapped` prices the outcome of THAT decision
again, under a fixed LINE budget, and can still veto it. The axis differs
too: `layoutScore` counts figures/countdowns/span characters at unlimited
height, `monitorFit` counts ACCOUNTS at a limited one. The table's column
header spends a line the monitor's per-row shape does not, and at a tight
budget a line is an account, so `accountsMonitorCapped` does not treat the
header as a fixed cost to route around — it prices the SAME budget in both
layouts and keeps whichever shows more. `monitorFit{lines []string, shown int, indicated bool}` is what
one budget buys in one layout: the lines to print, how many accounts are on
them, and whether the muted "· N more accounts" note survives (`indicated`,
vacuously `true` when nothing is elided). `monitorFit.beats(other)` is
`f.shown > other.shown` when the counts differ, else `f.indicated &&
!other.indicated` — a tie on both shown AND indicated favors `other`, so a
genuine tie keeps whichever layout the caller priced first rather than
switching for no gain.

`fitMonitor(snap, width, threshold, now, budget, allowTable)` renders
`monitorPanelText(snap, width, true, threshold, now, allowTable)` whole
first: a monitor that already fits within `budget` is never capped at all
(`shown == monitorAccounts(snap)`, `indicated` trivially `true`), which is
where the header's line can cost an account, since the per-row layout of
the identical monitor is one line shorter. Only when it does not fit does
`fitMonitor` call `cappedMonitor(snap, width, threshold, now, budget,
allowTable) monitorFit`: it walks `monitorLayout`'s groups, admitting whole
accounts (the header riding with the first TABLE row's group, above) until
the next one would push the joined block past `budget-1`, reserving that
line for the note, and rescues a budget that clips the always-shown first
account down to a bare header (`len(lines) == 1 && lines[0] == header`) by
substituting that account's own row. Its `monitorFit{lines, shown,
indicated}` return lets a caller comparing two attempts read off accounts
shown, not just whether the note survived.

`accountsMonitorCapped` runs `fitMonitor` in BOTH layouts at the same
budget, unconditionally — the table layout (`allowTable=true`) as `tabled`,
the per-row layout (`allowTable=false`) as `perRow` — and returns
`perRow.lines` exactly when `perRow.beats(tabled)`: strictly more accounts
fit without the header's line, or the same count of accounts with the note
surviving where the table's own attempt lost it to the header's cost.
There is no shortcut for a monitor that already fits whole: `fitMonitor`'s
own early return already gives `tabled` and `perRow` the same maximal
`shown` and `indicated: true` in that case, so `beats` naturally prefers
`tabled` (a tie keeps the first-priced layout) without a caller needing to
special-case it. Every other outcome keeps `tabled`, header and all —
including a budget too tight for either layout to keep the note where both
also show the same number of accounts, which is where the table's own
lone-header rescue (above) is what a one-line monitor ends up showing.

Because `monitorLayout` walks `snap.Accounts` in its existing (roster)
order and only ever substitutes CONTENT, never POSITION, the active
account's card renders wherever the active account sits in that order — the
table can end up visually split across the card when the active account is
not first, its two halves sharing column widths from the one
`pickWindowTable` call that built them together. The split is a property
of `monitorLayout`'s ordering; the table only makes the shared column
alignment across the split visible.

The monitor's own fallback, reached whenever `pickWindowTable` reports
`false` for the whole non-active set — no table exists, or one exists but
does not clear `releaseBar` against the minis — is `miniAccountText` — the
`5h`/`7d`-only, reset-shown-only-at-100%, scoped-window-shown-only-at-100%
shape spec 09§5.5 describes, narrower than the panel's own fallback
(`candidateRow`, which states every window and its live countdown at any
percentage). A terminal too narrow for the shared table loses the wide
view on this surface; it does not gain one it never had.

`miniAccountPriced(acc, width, clk renderClock)` (and its unpriced wrapper
`miniAccountText`, which fixes `clk` to `liveClock(now)`) takes a WIDTH and
fits its finished line through `pricedText.fit`/`truncRich`, so the per-row
layout `pickWindowTable` prices — at the render width, and again at
`fullWidth` through the same `perRowPricer` closure — holds itself to the
terminal at every width either comparison is made. `clk` is the same
DRAWING/PRICING split every layout on this surface makes (`renderClock`,
above): the line `monitorLayout` draws spells its countdowns live
(`liveClock(now)`), while the score it hands `pickWindowTable` as the
release bar spells them at `widestClock()`, so the bar this surface holds
the table to reads no clock.
Its windows come from `candidateWindows(acc.Usage.LastGood, nil)` —
the one projection every surface reads — rather than the stored map
directly, so a window `oauth`'s numeric guard drops (`pctFloat` rejects only
NaN and ±Inf, a window with neither compares against nothing and gates
nothing; a NEGATIVE measurement is not dropped — it compares fine, is not a
width hazard, and is a figure `tycswap list --json` has always reported, so
the projection passes it through unchanged) never renders on the fallback as
a figure the table correctly omits: the two surfaces agree about which
windows an account has.

`monitorLayout` fits every block it returns through `clipRichLines(t, width)`
— `truncRich` applied per LINE, since the account card and the joined monitor
build their own row breaks and a single-line truncation would cut everything
after the first one away. That is the same last-resort guard
`candidateRowText` gives the panel's rows, and it is what holds
`accountCardText`, whose bar rows are laid out from their content rather than
bounded by the width they are handed, to the terminal. `monitorLayout` does
not build a table when the caller disallows one (`allowTable=false`, which
the height cap asks for on every frame) — a saving, since the height cap
prices the same monitor in both layouts on every render.

### What the shared table guarantees, and what it cannot

The layout is held to seventeen properties, each stated as a predicate over
(roster, surface, width, `now`) and swept deterministically over a corpus of
78 rosters × both surfaces × widths 1..160, plus four large rosters at a
reduced width set (`internal/tui/table_props_test.go`, generator in
`internal/tui/rosterspec_test.go`). Measurement is on the RENDERED lines with
ANSI stripped, never on `plain()`: `richText.render` styles each segment on
its own and lipgloss pads the empty first line of a styled segment carrying a
newline, so `plain()` cannot see the columns the terminal receives. Emphasis
is the one exception and is compared by struct equality on `seg.Style`.

- **I1 never-wrap.** No rendered line of the table exceeds its width or
  carries a `"\n"`, at any width ≥ 1 — asserted on the renderer's own output,
  never through `candidatesText`, whose `truncRich` backstop would mask an
  overflow the monitor emits raw. Separately end-to-end, including BELOW the
  flip and including the active account card.
- **I2 EXISTENCE AND THE CHOICE ARE BOTH PURE, TIME-INDEPENDENT FUNCTIONS OF
  (ROWS, WIDTH, OPTS); ONLY THE DRAWN TEXT READS `now`.** `renderWindowTable`'s
  `ok` (`priceWindowTable`'s degenerate case) is exactly `width >=
  minTableWidth(rows, opts)`: the floor does not read `now`; `ok` is
  unchanged as `now` sweeps 14 days at fixed width; exactly one false→true
  transition over widths 1..300; `renderWindowTable(nil, …)` is
  `(windowTable{}, true)`. WHICH layout a surface actually draws is
  `pickWindowTable`'s separate verdict — existence AND
  `score.atLeast(releaseBar(here, ref))` — and comparing the two layouts at
  the render width alone is measurably NOT monotone in WIDTH either
  (`releaseBar`'s own doc comment: three of six figures lost on a panel one
  column WIDER, 29 → 30, under that comparison); `releaseBar`'s constant
  reference — the per-row layout's score at `fullWidth` rather than at the
  render width — is what restores single-transition, the set of widths at
  which the table wins being proven UPWARD CLOSED (`releaseBar`'s
  three-case argument, above). The SAME verdict is ALSO single-transition
  in `now` at fixed width, and this is a separate, explicit result rather
  than a corollary: `score`, `here` and `ref` are every one of them priced
  at `widestClock()`, which spells a countdown at `countdownWidest`
  wherever a column shows one at all rather than at its live remaining
  time, so the boundary width itself is a pure function of the rows, the
  width ladder and the surface (`renderClock`, `data.go`). Swept a
  fortnight at six-hour steps, every roster's boundary is the identical
  width at every clock reading — drift zero, not merely drift bounded
  (`TestChoiceIsClockFree`); scoring the two layouts on the text they would
  actually DRAW instead of at `widestClock` makes a boundary travel up to
  19 columns over the same fortnight, parking a terminal inside the
  travelled band in a layout that flips, and loses figures, with no
  resize. The layout actually DRAWN once the choice is made still reads
  `now` live, as it must to show the real countdown, but this cannot move
  the choice: a live spelling is never wider than the `countdownWidest` it
  was priced at (`TestCountdownsAreNeverWiderThanTheyArePriced`), so the
  drawn table states at least what cleared the bar and often more
  (`TestTheDrawnTableStatesAtLeastWhatItWasPricedAt`) — the bar is a lower
  bound on the terminal, never a promise of exactly that much.
- **I3 binding survival.** Every WINDOW row renders its BINDING cell's exact
  percentage, in exactly one bold segment, at every width. A row with no
  counted cell renders its highest-valued cell instead.
- **I4 no row renders as only em dashes.**
- **I5 a counted column is never dropped**, so the header and the panel's
  `"counting …"` note always agree.
- **I6 exhausted visibility, PER SURFACE** — see below.
- **I7 every span row states its reason**, its first word whole; and every
  message the codebase can produce has `spanFloor ≤ spanHardCap`, so the cap
  is provably a no-op.
- **I8 monotonicity**, four flavours over adjacent widths. Three are DATA
  axes and hold UNCONDITIONALLY, across any width including one at which the
  priced choice flips between layouts: FIGURES (the (row, column identity,
  percentage) set at W is a subset of that at W+1), NAMING (no column's
  header gets finer as the terminal narrows), SPAN (no message grows as the
  terminal narrows) — these are exactly `layoutScore`'s three compared axes,
  and `atLeast` never draws a table that regresses on one of them relative to
  the layout it replaces. IDENTITY (each row's label at W is a prefix of its
  label at W+1) holds WITHIN one continuously-drawn layout exactly as before,
  but is a DOCUMENTED NON-GUARANTEE at a width where the choice ITSELF flips
  between the table and a per-row layout: `identChars` is measured on every
  `layoutScore` but `atLeast` never reads it (above), precisely so the table
  is never refused at the width it earns its columns — a surface may
  therefore show a SHORTER label at the width where it switches into table
  mode than it showed one column narrower.
- **I9 scoped isolation, in the only form achievable.** With the column set,
  every column width, the abbreviation level and `labelW` held fixed, each
  rendered row is a function of that row's own data alone: move one
  percentage by one point and every other row and the header are
  BYTE-IDENTICAL at every width.
- **I10 drop fairness.** A column is dropped only when no row's protected set
  holds it, and among droppable LABEL GROUPS the victim is one the fewest
  rows report.
- **I11 column identity and order.** Permuting the rows leaves the header
  byte-identical; two same-labelled windows on one row occupy two columns and
  both figures render.
- **I12 attribution.** A percentage under a column is exactly that row's
  window at (label, occurrence); an em dash means the row reports no such
  window; a label group is present in full or absent in full; abbreviated
  headers are injective over the table's distinct labels.
- **I13 never less than the per-row layout — unconditional, no surface
  exception.** `pickWindowTable`'s `score.atLeast(releaseBar(here, ref))`
  is the drawing condition, not a post-hoc measurement of one: on BOTH
  surfaces the table renders only where its `figures` and `countdowns` are
  each no fewer than the per-row layout's at `fullWidth` (which per-row
  monotonicity makes no fewer than at the render width itself) and its
  `spanChars` no fewer than the per-row layout's AT THE RENDER WIDTH — see
  below for what this replaced, and for what identity, the one axis
  excluded from the comparison, does not inherit from it.
- **I14 the newline boundary.** `table.go` emits no `"\n"`; `Span` and every
  `Label` segment are normalised newline-free at row construction.
- **I15 no over-reserve.** `minTableWidth` for each corpus roster matches a
  recorded baseline exactly (`tableFirstFitBaseline`, `table_props_test.go`)
  and never exceeds the width recorded there.
- **I16 emphasis is width-independent.** The six style rules hold at every
  width; clipping never changes a surviving segment's style.
- **I17 height-cap baseline.** Over widths {24, 40, 60, 80, 120} × budgets
  1..14, the accounts shown and whether the "· N more accounts" indicator
  survives match a recorded baseline (`monitorCapBaseline`,
  `table_props_test.go`) exactly.

**I6 remains PER SURFACE — a statement about what one CHOSEN table's own
ladder pins, orthogonal to I13's fix below.** On the MONITOR it holds
unconditionally, and structurally: `miniAccountText` names `5h`, `7d` and a
scoped window only once it has run out, and every one of those columns is
PINNED there (`monitorTableOpts.policy.PinExhausted`), so a table drawn on
this surface always states them. On the PANEL the guarantee is explicitly
NOT made: `candidateTableOpts.policy.PinExhausted` is `false`, because the panel's own
release bar (`candidateRow`/`candidateRowPriced`) DISCARDS an exhausted
uncounted figure at every width too — pinning one in the table would spend
the whole comparison protecting a figure the release bar itself does not
guarantee. This is unaffected by the priced choice: it is a fact about what
ONE layout's own ladder keeps, not about which of two layouts a render
draws.

**I13 holds with no per-surface exception — the priced choice is what
closes it, not a sharper shed order.** A bare existence threshold — "the
table exists" and "the table is drawn" as the same test — cannot make I13
hold unconditionally, and the reason is structural: the table's width is
the UNION over rows; `candidateRow`'s is one row's own cells. Six accounts
each reporting `5h`, `7d` and ONE distinct model make eight columns
costing 53 columns of terminal, while `candidateRow` states any one of
those accounts' three figures in 37 — so across [37, 52] a table that
merely EXISTS states strictly less than its own release bar, whatever the
shed order sheds first. No reordering of the width ladder closes that band
while existence and drawing are the same test: closing it at runtime would
require the surface to be a table at 20, a per-row layout at 40 and a
table again at 53 — two transitions, which I2's single false→true
transition forbids outright — and the per-row layout's own figure set at a
fixed width MOVES as a countdown shortens, so an extra transition would
read the clock.

A COMPARISON PRICED AT THE RENDER WIDTH ALONE IS NOT THE FIX EITHER, and
that is a MEASUREMENT, not an assumption. `layoutScore`s compared at the
render width alone correctly report the table `false` over [37, 52], but
the SAME comparison, swept across every width, is not itself monotone —
the property corpus catches it costing three of six figures on a panel
exactly ONE COLUMN WIDER (29 → 30), because the table and the per-row
layout are each individually monotone but at DIFFERENT rates, and a
same-width comparison between two such curves can flip against a widening
terminal. `releaseBar` is what the comparison must be priced against
instead: it turns "does the table state at least as much" from an
argument about the render into a condition priced against a CONSTANT —
the per-row layout's score at `fullWidth` — so the set of widths at which
the table wins is PROVEN upward closed rather than merely observed to look
that way on one corpus. Over [37, 52] the table still loses exactly as a
render-width comparison has it lose — `releaseBar` does not change WHETHER
the table loses there, only guarantees the loss is confined to one
contiguous band rather than scattered — so that band is where the surface
renders per-row, on both surfaces, unconditionally (I13, above). WHY a
table can lose there at all is untouched by the pricing: its columns are
the union across rows, so every row still pays for every other row's
windows (`layoutScore`'s own doc comment, above); the pricing does not
eliminate that structural cost, it guarantees only that the cost is never
passed to the reader as a shortfall, or as a flicker as the terminal is
resized.

**General isolation is unattainable, stated plainly — and the priced choice
makes the DEPENDENCE it names strictly larger, never smaller.** A
shared-column table buys a column for every row or for none, so one
account's window can still cost another account a figure; the per-row
victim rule (`dropTableGroup`) only reduces the magnitude, never eliminates
it — I9 and I10 remain the honest, testable replacements for isolation
within a chosen table. But the choice ITSELF is also a function of the
whole roster, and in TWO ways at once: `candidatesText` and `monitorLayout`
sum every row's per-row score into one `here` total before pricing it
against the table's, so a mutation to one account that changes what its
OWN row displays (adding a figure, losing a countdown) can move that sum
past or below the `releaseBar` line and flip which LAYOUT every OTHER row
on the surface is drawn through — a table row into a per-row line or back,
for accounts the mutation never touched; and the table's own `fullWidth`,
the reference the bar is priced at, is itself a function of every row's
columns and countdowns, so the same mutation can also move WHERE (`ref`)
the per-row layout is re-priced. I13's unconditional guarantee (above) is
what bounds the consequence regardless: whichever layout a mutation
elsewhere tips the surface into, every row still states no fewer figures,
countdowns or span characters than its own release bar would at that
width.

**The corpus's EXISTENCE-first-fit widths (`tableFirstFitBaseline`).** This
baseline is `minTableWidth`'s own value — the pre-check's floor (above),
UNCHANGED by the priced choice — not the width at which a table is
necessarily DRAWN. The narrowest terminal at which a table can EXIST for
each corpus roster, panel then monitor: `real/scoped` with a configured
axis 29 / 15; `real/dup` 41; `real/six` 72; `real/exhausted` 17 and 27;
`real/scopedonly` monitor 25; a twelve-account real-model monitor 34; a
thirty-account `all`-axis panel 81. Two properties account for most of the
headroom a wide roster gets: a column costs what its figures cost rather
than what its header spells (`measureColumns`, `headerLadders`), and
existence is the single closed-form comparison `minTableWidth` computes
before any layout runs. Where the panel's own release bar still states
more than a just-barely-existing table at one of these widths, the priced
choice draws the per-row layout instead, so the width at which the panel
actually STARTS SHOWING a table can sit above this baseline; I13 is the
guarantee that when it does, the reader has lost nothing by the wait.

## A19. `store.RotationEligible` — one owner for rotation eligibility; two audited limitations

**One predicate, four call sites across three files, one inlined
derivation.** `store.RotationEligible(data, num)` — `AccountIsSwitchable(num)
&& !disabledFromData(data, num)` — is the single expression of "eligible to
rotate onto by number alone." A nil `data` (roster absent or unreadable)
makes it return false for every slot rather than falling through to
`AccountIsSwitchable` alone; the predicate fails closed, never reporting a
disabled slot eligible because its disabled flag could not be read. Four
`s.RotationEligible(` call sites, across three files, call it directly, so
none can compile an inline AND that has drifted from the predicate's own
body: `store.SwitchableAccountNumbers` (`internal/store/identity.go`),
`switching.selectBestSwitchable` (`internal/switching/strategies.go`), and
`switching.switchFreshMachine`'s two decisions
(`internal/switching/switch.go`) — detailed below.
`reporting.Snapshot` (`internal/reporting/snapshot.go`)
is a further, deliberate surface that does NOT call `store.RotationEligible`:
the `RotationEligible` field of the `AccountSnapshot` it builds comes from a
package-level helper local to `reporting`, `rotationEligible(data,
switchable, disabled)` — `data != nil && switchable && !disabled`, the same
fail-closed shape as the owner's, over the `Switchable` and `Disabled` values
`Snapshot` already has in hand, to avoid a second per-account credential and
config read inside the deliberately one-pass snapshot. The rationale is
stated in the doc comment on `rotationEligible` itself, immediately below
`Snapshot` in the same file, not inside `Snapshot`'s own body. That surface
is guarded by a runtime parity test instead —
`TestSnapshot_RotationEligible`
(`internal/reporting/snapshot_test.go`), which compares the snapshot's
eligible set against `store.SwitchableAccountNumbers` — not by the compiler.
Naming and defining the rule once is not style: a divergence between an
inline AND and the engine's own rule is exactly what A18 records — the TUI
candidates panel filtered on `Switchable` alone, so a disabled account with
stored credentials could rank at the top of a display the engine could never
act on. The shared definition removes that class of divergence at compile
time for its four call sites; the snapshot's inline copy remains a named,
test-guarded duplication by design, not an oversight.

**The boundary.** `RotationEligible` is necessary but NOT sufficient for the
auto engine to select a slot. The engine additionally excludes the current
account (it cannot switch onto itself), quarantined slots
(`autoswitch_state.json`, A18), and API-key slots unless
`autoswitch.includeApiKeyAccounts` is set. A `RotationEligible` account can
therefore still be off the engine's candidate list for any of those three
further reasons; the field tells a display "this slot is a legitimate
rotation target in principle," not "the engine will pick it this tick."

**`switch.go`'s rotation loop is the deliberate exception.** The bare
`switch` no-target rotation loop, inside `Switch`
(`internal/switching/switch.go`), keeps its own separate `disabledFromData`
/ `!AccountIsSwitchable` tests rather than calling `RotationEligible`,
because it emits a distinct skip warning worded per reason —
`(disabled)` versus `(no stored credentials/config)` — across a whole loop
of candidates, and `RotationEligible` collapses that distinction by design.
`switchFreshMachine` is not a second exception: both of its decisions —
whether to skip its preferred target, and which fallback to select once
skipped — call `s.RotationEligible` directly. Its own `disabledFromData`
call on the preferred target survives only to WORD the one notice that
target can produce (`(disabled)` versus the re-add hint); it plays no part
in deciding whether to skip it, which is `RotationEligible`'s decision
alone.

### Known limitations (audited, accepted)

Two narrow windows are inherited from the Python original and are
deliberately NOT closed: each is self-healing, each has a low-probability
trigger, and closing either costs more machinery than the residual risk
justifies.

**(i) A rotated OAuth token can be lost on a lock-timeout race during a
background usage-fetch refresh.** `internal/oauth/fetch.go` performs the
network refresh before acquiring the store lock (the lock is non-reentrant,
so it cannot be held across a network call); the persist that follows
(`persistCredentials`) can then fail to acquire it. Its failure path
(`internal/oauth/fetch.go`) logs a warning and prints a notice — there is no
rollback and no retry. The store lock timeout is 10s
(`filelock.DefaultTimeout`); the realistic trigger is a long-held lock, such
as a macOS Keychain prompt blocking a concurrent switch. Consequence: that
slot's stored refresh token is stale, and its next refresh fails
`invalid_grant`. Recovery: log in with the account and run `tycswap add`.

**(ii) A CLI switch strategy can install a credential the engine has just
quarantined.** `internal/switching/strategies.go` filters candidates on
rotation eligibility only; it does not consult `autoswitch_state.json`'s
quarantine map. Within the usage cache's serve window, a manual
`tycswap switch --strategy best` can therefore select a slot the auto engine
quarantined moments earlier, installing a credential whose refresh token is
already dead. Consequence: the installed access token works until it
expires, then the account fails; auto-switch fails over, and the usage
store's dead-token strikes surface as "re-login needed." Recovery: log in
with the account and run `tycswap add`. A naive fix — have the CLI strategy
skip quarantined slots — is unsafe on its own: quarantine entries are
released only by a real engine tick
(`releaseRecoveredQuarantines`), so a CLI-side check keyed on slot number
alone would wrongly exclude a slot whose credential was already replaced
and recovered. Closing this gap correctly requires comparing the slot's
current credential fingerprint against the quarantine entry's
`refreshTokenFingerprint`, not merely checking slot membership in the
quarantine map.

## A20. Roster read failure: absence heals, corruption refuses (Go-side deviation from Python)

`store.ReadSequence` (`internal/store/sequence.go`) collapses two distinct
causes of a nil result into one `(nil, nil)`, matching Python's `None` (spec
01§2.3): the roster file is absent, or the file is present but does not
parse (0-byte, truncated, malformed JSON, or a top-level JSON `null`). Many
callers, and Python parity, depend on that collapse for the paths that only
display or inspect the roster; it stays exactly as it is. A write-capable
path cannot use it, because the two causes demand opposite answers: an
absent file is a fresh install (an empty roster is the truth, and proceeding
is healing), while a present-but-unparseable file is corruption (the account
records it held — emails, aliases, uuids, orgs, slot mapping — are
frequently hand-repairable ASCII, and the credential and config backups
those records point at are still intact on disk, referenced by nothing
else). The Python original does not distinguish them: it substitutes an
empty roster for both alike and, on a write path, proceeds to write it,
silently replacing a corrupt roster with an emptied one (plus whatever the
run adds) while every account's credential and config backups stay on disk,
orphaned. This is a deliberate Go-side deviation from that behavior.

**RULE 1 — an entry read that can write classifies absence from
corruption, and refuses on corruption.** Every write-capable operation,
repo-wide, begins with exactly one classified entry read — `store.SequenceForUpdate`,
or `store.MigratedSequenceForUpdate` wherever the org-field backfill must run
first (`internal/store/sequence.go`, `internal/store/identity.go`) — with the
same two outcomes:

- absent — an empty roster; the operation proceeds, a fresh install with no
  accounts yet being the truth, not an error.
- present and unparseable — the operation refuses instead of writing over
  it, with a `cerr.Config` error naming the `sequence.json` path. Its text
  states what the user needs in order to act: the account backups
  (credentials and config) the roster indexes are intact on disk; the file
  is JSON and may be hand-repairable (emails, aliases, uuids, orgs, and slot
  numbers are ordinary ASCII fields); and removing the file outright is
  also an option, but one that re-registers every account from scratch —
  the next `add` / `add-token` starts an empty roster rather than
  recovering the existing slot numbers, aliases, or org fields the removed
  file held. Nothing is written; the bytes on disk survive for repair.

Scope is every write-capable path repo-wide, not only `internal/lifecycle`:
`AddAccount` (add.go), `AddAccountFromToken` (addtoken.go), `RemoveAccount`
(remove.go), `SetAlias` and `UnsetAlias` (alias.go), `SetAccountDisabled`
(disable.go), and `MoveAccount` and `SwapAccounts` (moveswap.go) each reach
RULE 1's classification through `store.WithRosterLocked` (RULE 4) rather
than a direct `SequenceForUpdate` call; `RemoveAccount` alone additionally
takes an advisory copy of the same classified, backfilled read on its own,
unlocked, through `MigratedSequenceForUpdate`, ahead of its confirmation
prompt (RULE 4). `transfer.Import` (`internal/transfer/import.go`) is in
scope on the same footing: it is a write-capable path that can persist a
roster, through its own consumer-defined store interface (`transfer.Accounts`,
DESIGN A2) rather than `store.Store` directly. RULE 1's classification
governs a write to `sequence.json` on the strength of what that write does,
not which package issues it — the mechanism a caller reaches
`store.WriteSequence` through does not change the guarantee.

The invariant this enforces is narrower than "every write begins with a
classified read": no write-capable path destroys a roster it never
classified as parsed. Three further `WriteSequence` call sites exist outside
the operations named above and do not begin with a classified read; none is
a counterexample, because each is structurally unable to reach its write
with a roster that did not already parse:

- `internal/switching/perform.go` commits `activeAccountNumber` twice — once
  in `directActivate`'s commit closure, once in `normalSwitchBody` — onto
  the `*SequenceData` `performSwitch` obtains from its own single,
  unclassified `ReadSequence`, taken under `withTripleLock`. That pointer is
  dereferenced for the current active slot immediately after the read, with
  no nil guard: an absent or unparseable roster is nil there, and the
  dereference panics before either commit site is reached. A crash is not a
  write, so the operation still cannot commit over an unclassified roster —
  the failure mode is a goroutine panic, not a graceful refusal.
- `internal/switching/transaction.go`'s `rollbackStep`, case
  `"sequence_updated"`, reads through an unclassified `ReadSequence` and
  nil-guards explicitly (a nil result returns without writing). It commits
  only `activeAccountNumber`, never an account record.
- `internal/store/credproxy.go`'s `BackfillAccountUUID` reads through an
  unclassified `ReadSequence`, nil-guards identically, and commits only one
  record's `uuid` field, and only when that field was previously empty.

Each of the three is nil-guarded against, or otherwise unreachable over, an
unparseable roster; each commits only a field already inside a record that
parsed, never a new record or a replaced `Accounts` map. RULE 1's refusal
exists to stop a corrupt file from being silently renamed away with its
records lost; none of these three sites can do that.

`internal/store/identity.go`'s `migrateOrgFields`, reached through
`SequenceMigrated`, is not a further exception: it classifies its own entry
read too, through the same `classifiedRoster` call `SequenceForUpdate` is
built on, and refuses with the same `cerr.Config` a corrupt file produces
anywhere else, before its `WriteSequence` call ever runs.

**The classifier.** `SequenceForUpdate` and `ReadSequence` are two views of
one read: a single `os.ReadFile` followed by one `json.Unmarshal` into
`SequenceData`, never a follow-up `os.Stat` — a file created or removed
between two syscalls would be classified as the opposite case, and this
classification decides whether user data is destroyed. Five refinements
keep that single read from misclassifying bytes that only look like a
roster, and from handing back a result a later step can crash on:

- a top-level JSON `null` is not a roster. Unmarshaling `null` into a
  struct target is a silent no-op that would otherwise leave `Accounts` and
  `Sequence` nil and classify the document as a healthy empty roster rather
  than as unreadable; the classifier treats it as unreadable, and it is
  refused like any other unparseable file.
- a parsed JSON object is accepted, but normalized: `Accounts` and
  `Sequence` are never nil on a successful classification, whether or not
  the source carried those keys. An object with no `accounts` key holds no
  records, so nothing is lost by proceeding with it; what may never happen
  is a later assignment into a nil map (`lifecycle.putRecord` assigning
  into `data.Accounts` is exactly the call this closes against panicking
  after credential and config backups have already been written).
- an individual account record is normalized the same way: `decodeRecord`
  (`internal/store/sequence.go`) yields an empty `map[string]any` rather
  than nil when a record's own bytes fail to parse, so a field accessor
  (`strField`, `recordFor`) never indexes a nil map. Neither the roster's
  `Accounts` map nor any record inside it is ever nil after classification
  succeeds.
- an existing file that cannot be read at all — a directory sits where the
  file should, or its mode denies read access — is unreadable in exactly
  the sense malformed JSON is, and yields the same actionable `cerr.Config`
  refusal rather than the raw OS error.
- the org-field backfill cannot act on a roster it cannot parse.
  `SequenceMigrated`, which `MigratedSequenceForUpdate` runs ahead of its
  own classified read, calls `classifiedRoster` directly — the same
  classification `SequenceForUpdate` is built on — before deciding whether a
  backfill is needed; `migrateOrgFields` calls it again before its own
  write, since a roster can go unreadable between the two calls. Either call
  refuses with the same `cerr.Config` a corrupt file produces anywhere else,
  before the backfill's `WriteSequence` runs — so a corrupt file is refused,
  not silently written over, regardless of which of the two classified reads
  reaches it first.

`ReadSequence`'s own `(nil, nil)` contract for absent-or-unparseable carries
these refinements too, but reports them identically as before: the
classification lives beside that read, computed from the same single
`os.ReadFile`, and only `SequenceForUpdate` acts on which of the two
outcomes occurred.

**A command that writes nothing itself can still surface RULE 1's refusal,
by way of the org backfill.** `SequenceMigrated` (`internal/store/identity.go`)
looks read-only from every caller's side — it returns a roster, never a
commit signal — but its own body can end in a `WriteSequence` (the backfill),
so RULE 1 requires it to classify before running, exactly as any other
write-capable entry read does. `ResolveAccount` (same file) calls
`SequenceMigrated` first for the same reason. A caller that propagates either
function's error inherits RULE 1's refusal even though the caller's own
command never touches `sequence.json`. Five do: `lifecycle.ListAliases`
propagates it for `tycswap alias` with no arguments; `store.ResolveAccount`
propagates it for `tycswap env <id>`, `tycswap map <id>`, and `tycswap run <id>`
(each resolves through it — `session.Manager.setupPreamble`/`SetupSession`
for the first and third, `cli.mapCommand` for the second);
`core.Switcher.SlotForDirectory` propagates it for a bare `tycswap env` or
`tycswap run` resolving the current directory's mapping; and `transfer.Export`,
through its `MigratedSequence` adapter method, propagates it for
`tycswap export`. Each reports `corruptSequenceError`'s `cerr.Config` text and
exits 1 in place of what it gives an absent (not corrupt) roster:
`tycswap alias` prints "No aliases set" for absent, refuses for corrupt;
`tycswap env`/`tycswap map <id>`/`tycswap run <id>` report `AccountNotFoundError`
for absent (the identifier resolves against zero accounts), refuse for
corrupt; `tycswap export` already errors on absent (`cerr.Transfer("no
accounts to export...")`), so corrupt trades one error message for a more
specific one — the misleading "no accounts to export" for the diagnostic
refusal naming the file and the intact backups.

Not every read-only surface propagates it. `reporting.BuildAccountsInfo`
(`tycswap list`), `reporting.buildStatusPayload` / `renderStatus`
(`tycswap status`), `reporting.Snapshot` (`tycswap tui` / `tycswap watch`), and the
bare, no-argument `tycswap map` listing (`cli.listMappings`) all discard
`SequenceMigrated`'s or `ReadSequence`'s error and keep the collapse: a
corrupt roster displays as an empty one there, silently. This is not a gap
to close — RULE 1 governs writes, and none of these four ever writes
`sequence.json` — it is the boundary between the commands the refusal
reaches and those it does
not, and it means the two behaviors coexist by design on the same corrupt
file depending on which command is run against it.

Truthful refusal is preferred to the fabricated empty result the discarding
surfaces still give, for the same reason RULE 1 refuses on a write:
"No aliases set" against a roster that cannot be read is not the true
answer "there are no aliases" — it is the unreadable-roster case, reported
as if it were the empty-roster case. A user who set three aliases and then
hit a truncated `sequence.json` is told none were ever set; nothing steers
them toward the file that still holds the answer. Refusing costs one failed
command, the same price RULE 1's own rationale already accepts for a write;
fabricating an empty result costs the user the fact that anything is wrong
at all.

**RULE 3 — a read-only resolution site refuses too, so a miss is never
confused with corruption.** `SetAlias` and `UnsetAlias` (alias.go),
`SetAccountDisabled` (disable.go), and `MoveAccount` (moveswap.go) each
resolve their identifier only after `store.WithRosterLocked`'s classified
entry read has already succeeded; a corrupt roster is refused there, before
resolution runs, so resolution is never asked to answer against a roster it
cannot trust. `SwapAccounts` (moveswap.go) reaches that same entry read the
same way, through its own `WithRosterLocked` call. `relocateLocked` — the locked body
`MoveAccount` hands the read roster to on its non-swap branch — takes that
roster as a parameter and reads the file no further. `swapAccountsLocked`
takes the same kind of parameter but resolves each of its two identifiers
itself, through its own `ResolveAccount` call, and `ResolveAccount` runs
`SequenceMigrated` (a further `ReadSequence`, gated on a backfill this
span's entry read has already satisfied) plus its own `ReadSequence`. Both
extra reads are harmless here: the `FileLock` `MoveAccount` / `SwapAccounts`
already hold excludes every other writer for its duration, so neither read
can observe a write from any other process, and each slot number
`ResolveAccount` returns is re-checked against the roster passed in
(`data.Accounts[numA]`, `okA`, and the `numB` equivalent) before it is
trusted — a resolution and the roster the operation commits can never
disagree. Left unguarded, a resolution step given no roster, or a nil
roster silently treated as empty, would land on the same
`cerr.AccountNotFound("No account found with identifier: %s", ...)` a
genuine typo produces; corruption and "no such account" would be
indistinguishable to the user and to a test. RULE 1's refusal upstream of
every resolution call is what keeps the two apart.

**RULE 4 — the read-decide-write span of a roster-mutating operation runs
under the store lock, with exactly one classified read taken inside it.**
`store.WithRosterLocked` (`internal/store/rosterlock.go`) is the shape:
it acquires the store `FileLock`, runs the org-field backfill, takes the
roster read the operation's decisions are made from
(`MigratedSequenceForUpdate`, RULE 1's classifier), and hands that roster
to the caller's function — every `WriteSequence` call the operation makes
runs inside that function, so the acquisition spans the whole
decide-and-commit sequence, not merely the read. `AddAccount` (add.go),
`AddAccountFromToken` (addtoken.go), `RemoveAccount` (remove.go), `SetAlias`
and `UnsetAlias` (alias.go), `SetAccountDisabled` (disable.go), and
`MoveAccount` and `SwapAccounts` (moveswap.go) — RULE 1's full scope, less
`transfer.Import` — call it; the eight operations converge onto the same
shape rather than each inventing its own. `transfer.Import` does not call
it: RULE 1 places it in scope on a different footing, reaching its
classified read through its own `*filelock.FileLock` and its own
consumer-defined `transfer.Accounts` interface rather than through
`store.Store`, so it has no `store.WithRosterLocked` call to make. This
list is the one an entry point consults before taking the store `FileLock`
of its own accord: `FileLock` is non-reentrant, and its `Acquire` takes the
in-process `hold` mutex before it ever consults a timeout (F3, §2.8), so
calling `store.WithRosterLocked` — or acquiring the same `*store.FileLock`
any other way — from inside one of these eight operations' own locked span
is a permanent, timeout-free goroutine hang, not a slow acquisition.

*What is atomic, and against what.* The span from the locked classified
read to the operation's last `WriteSequence` call is atomic against every
other write-capable path that reaches `sequence.json` through the same
`FileLock` — by RULE 1's scope, every write-capable path in the repository.
Two overlapping runs of any two of these operations do not lose a record:
the second run's classified read cannot start until the first run's writes
and lock release have completed, so it always decides from a roster that
already carries the first run's commit. Measured without the lock, two
concurrent `AddAccountFromToken` calls against one store lose a record and
both report success, in every run; under the lock, both records and both
accounts' backups survive, in every run.

*What a caller may assume about the roster it holds.* A roster obtained
inside the lock is current for the whole acquisition: no other writer can
act on `sequence.json` between the classified read and any of the
operation's own commits, so the roster a slot-occupancy, identity-collision,
or confirmation decision is made from and the roster the operation
eventually writes are the same roster — not merely the same in-memory
object by construction, but the same bytes on disk, because nothing else
could have changed them in between.

*Where the lock is NOT held: never across a human prompt.* Reading a
confirmation from the terminal blocks for as long as the user takes; the
store lock's cross-process acquire budget is bounded (`filelock.DefaultTimeout`,
10s) precisely so that nothing holding it can block indefinitely.
`AddAccount`'s slot-displacement confirmation, `AddAccountFromToken`'s
slot-overwrite confirmation, and `RemoveAccount`'s removal confirmation
therefore all run BEFORE the lock is acquired, decided from an advisory read
taken only to decide whether a prompt is needed at all and what identity to
show in it. That advisory read governs nothing the write depends on: it is
superseded by the locked read, never merged with it.

`RemoveAccount`'s advisory read is a direct, unlocked
`MigratedSequenceForUpdate` call: classified, so an unparseable file refuses
before the prompt is ever shown, and backfilled, because the multi-match
disambiguation list renders each candidate's org tag from the record the
backfill fills in — an un-backfilled pre-v0.6.0 record would print every
same-email candidate as `[personal]` on the one screen whose purpose is
telling them apart. Its confirmation premise is the email alone, which the
backfill does not touch, so nothing here can misread a backfill as the slot
changing hands, and the read needs no locked acquisition to protect that
premise. `AddAccount` and `AddAccountFromToken` take theirs through a
SEPARATE, short `WithRosterLocked` acquisition that is released before the
prompt is printed — one acquisition to look, none while the human thinks,
one to commit. The reason is the org-field backfill: their premise is the
composite `(email, organizationUuid)` identity, and on a pre-v0.6.0 roster
an unlocked read taken before the backfill reports `organizationUuid` as
absent where the locked read (which runs the backfill first) reports the
recovered value — the re-validation below would then read a backfill as
the slot changing hands and refuse a confirmation the user correctly gave.
Reading both premises through the same classified, backfilled path removes
that false abort, and it keeps the backfill's own write under the lock
rather than adding an unlocked one.

*Prompts are re-validated, not trusted.* The read that governs the write
is the locked, classified read described above, taken after the prompt (if
any) has returned. When a prompt preceded it, the operation re-validates
the premise the prompt showed against this read before taking any
destructive step:

- the slot's occupant matches what the prompt showed → proceed as
  confirmed;
- the slot is now free → proceed with no destructive step, since there is
  nothing left to displace;
- the slot holds a different identity than the prompt showed, or the
  operation needed a confirmation it never obtained → refuse with
  `cerr.Config`, naming the slot and what it now holds, and write nothing.

`AddAccount`'s displacement decision and `AddAccountFromToken`'s overwrite
decision both take this form. `RemoveAccount` re-resolves its identifier
under the lock rather than trusting its pre-prompt resolution: a slot key
absent from the locked roster is checked against the confirmed identity's
composite (email, organizationUuid) across the whole roster — found under a
different slot, a concurrent move or swap has renumbered it, and the
operation refuses, naming the new slot; found nowhere, another tycswap has
already removed it, and there is nothing to do (no error) — and a slot key
present but holding a different email than the one confirmed refuses. Both
replace what would otherwise be a second, unlocked classified read taken
after the prompt with the SAME locked read the operation's write is already
made from — safe because the store `FileLock` this acquisition already
holds excludes every other writer for the rest of it: once the locked
classified read has been taken, no write from any other process can land
between it and this re-validation, so a second look at the same in-memory
roster could only ever agree with the first. A changed premise always
aborts or de-escalates; it never destroys — the failure mode RULE 1 already
refuses to walk into, extended from "the file was corrupt" to "the file
changed while a human was answering a question about it."

*Scope boundary.* `Purge` (`internal/lifecycle/purge.go`) is out of scope:
it deletes the directory the lock file lives in, so there is no lock left
to hold across it. `internal/switching` is out of scope on different
grounds — its roster-mutating span already runs under `withTripleLock`
(`internal/switching/switching.go`), which acquires the same store
`FileLock` first in a three-lock stack, and has since the port.
`internal/reporting` never writes `sequence.json`. No re-entrancy mechanism
exists or is needed: the store `FileLock` remains non-reentrant, and each
of these operations acquires it exactly once, itself, never calling into
another lock-taking operation from inside its own acquisition.

**Rationale.** Overwriting a corrupt roster is silent and irreversible: the
account records it held are frequently hand-repairable ASCII, and the
credential and config backups those records point at are still intact on
disk, referenced by nothing else. A write that replaces the roster with an
emptied one destroys the repairable records and orphans the backups in the
same stroke, with no warning and no way back. Refusing costs the user one
failed command; it is fully recoverable by repairing the file, by removing
it and re-registering, or simply by retrying once whatever produced the
truncation (a crash mid-write, a hand edit) has been addressed. Between a
mistake that costs one command and one that destroys data, this design
always takes the former.

### Known limitations (accepted)

RULE 4 governs the roster-level lost update: without its locked span, two
concurrent `AddAccountFromToken` runs against one store lose a record and
both report success, in every run. Nine residuals remain. None is
roster-level data loss; none is claimed fixed by
this design that is not.

**(i) The locked span is not transactional across files.** RULE 4 makes the
roster read-decide-write atomic; it does not extend that atomicity to the
credential and config backups an operation also writes. A failure inside
the lock after `WriteAccountCredentials` / `WriteAccountConfig` but before
the operation's final `WriteSequence` call — disk-full, a permission error,
a process kill — leaves a credential and config pair on disk with no
roster record naming it: the same orphaning this amendment's own rationale
treats as irreversible for a corrupt file, arising here from a mid-operation
failure instead of a hand-edited one. Trigger: a write failure between a
backup write and the commit, in any of the eight operations RULE 4 names.
Consequence: an orphaned credential/config pair, recoverable only by hand
(the bytes are intact; nothing indexes them). It is the one orphaning path
that survives locking the roster read-modify-write itself.

**(ii) In-process contention on the same `*store.FileLock` object ignores
the caller's requested timeout (F3, §2.8).** `FileLock.Acquire` takes its
`hold` mutex unconditionally before it ever consults a timeout, so a
goroutine sharing one `*store.Store` with a current holder waits for that
holder to release, not for its own requested timeout, however long that
takes. A process that hosts more than one lock-taking actor over one
`*store.Store` — the TUI, which runs lifecycle actions and the autoswitch
engine's ticks in one process — can have an action queue behind a tick, or a
tick behind an action, with no bound and no wait indicator: a tick stalled
on a slow network refresh, or an action stalled on a macOS Keychain prompt,
silently stalls the other. This risk does not appear in a suite that never
exercises two lock-taking actors sharing one process, which is every
current automated test.

**(iii) A contended lifecycle operation now fails outright instead of
racing to a silent loss.** Cross-process, a locked acquisition that cannot
obtain the `FileLock` within `filelock.DefaultTimeout` (10s) returns
`cerr.Lock("Failed to acquire lock - another instance may be running")`
rather than proceeding unlocked. Session bootstrap's own acquire attempt on
the same lock uses a larger figure, `bootstrapLockTimeout`
(`internal/session/manager.go`, 30s) — an ACQUIRE budget, how long bootstrap
itself waits to obtain the lock, not a bound on how long bootstrap then
HOLDS it once acquired. The 30s is sized against that hold, not against a
contending caller's wait: `bootstrapLockTimeout`'s own comment states
bootstrap may hold the lock across one token refresh plus `claude auth
status` probes. Concretely, the hold can span up to two probes
(`authStatusTimeout`, 10s each, plus a 2s `WaitDelay` grace) and one refresh
(the OAuth client's own 10s HTTP timeout) — each individually capped, but
nothing bounds their sum, so the hold itself carries no fixed ceiling, 30s
or otherwise. A lifecycle command queued behind a live bootstrap waits on
its OWN acquire budget (`filelock.DefaultTimeout`, 10s), not on bootstrap's
30s: its `cerr.Lock` failure arrives, and ordinarily does, before bootstrap
releases. A reader who budgets retries against 30s — expecting a queued
lifecycle command to clear on its own within that window — is wrong twice:
the command does not wait 30s, it fails at 10s, and bootstrap's hold is not
bounded by 30s to begin with. A script that runs two of these operations
concurrently receives one success and one `cerr.Lock` failure rather than
two successes — the correct outcome, but one a caller polling for two
successes must account for.

**(iv) Re-validation can abort an operation the user already confirmed.**
When the slot a displacement or overwrite prompt described has changed by
the time the locked read re-checks it, `AddAccount` / `AddAccountFromToken`
report a `cerr.Config` refusal naming what changed rather than completing
the confirmed action — a user who answered the prompt is told nothing
happened and to re-run the command. This is a worse moment than the silent,
wrong success it replaces, though never a worse outcome: nothing is
destroyed either way, and the refusal names the slot and what now occupies
it.

**(v) The guarantee is advisory and tycswap-only.** A hand edit of
`sequence.json`, or a future tool that writes it without taking the store
`FileLock`, is invisible to every rule in this amendment and still
last-writer-wins against a tycswap process mid-operation. RULE 1's corruption
refusal catches an unparseable result of such an edit; it cannot catch a
well-formed one that simply disagrees with what a concurrent tycswap decided
from.

**(vi) RULE 4's locked span makes a lock order load-bearing.** `clearDeadToken` (`internal/lifecycle/lifecycle.go`),
called from inside `AddAccount`'s and `AddAccountFromToken`'s locked
bodies, mutates the usage store and so acquires `.usage.lock` while the
store `FileLock` (`.lock`) is already held: every such call site now takes
`.lock` before `.usage.lock`. Nothing in the repository takes them in the
reverse order today — `usage.Store`'s own mutators (`internal/usage/store.go`)
never call back into a `store.Store` method, and the usage-fetch protocol
reads the roster, fetches over the network unlocked, then writes the
roster, never the reverse nesting. The invariant holds by the shape of the
code, not by enforcement; a future call from inside a usage mutator back
into a locked store method deadlocks permanently, since neither `FileLock`
has a nested-acquire timeout (F3, §2.8).

**(vii) The three filelock facts recorded at §2.8 are measured on Linux
only.** F2 (release on handle-close / process-death) and F3 (an in-process
waiter on the same object ignores its own timeout) follow from `FileLock`'s
Go-level `hold` mutex and are platform-independent by construction, so they
carry over to the Windows `LockFileEx` path unmeasured but not unreasoned.
F1 (two independent handles on the same file, one process, serializing
exactly as two processes would) is asserted from `LockFileEx`'s documented
contract, not measured — no run of this design's regression suite has
executed on Windows.

**(viii) `transfer.Import` opens its own `*filelock.FileLock` on the store's
lock path rather than sharing `store.Store.Lock`.** Cross-process this is
correct: two independent handles on the same file serialize at the OS
`flock` level regardless of which process or object opened them (F1).
In-process, it means an `Import` call and a lifecycle operation sharing one
`*store.Store` do not queue on the fast, in-memory `hold` mutex (F3); they
contend at the `flock` level instead, and each burns its own acquire
budget rather than one waiting on the other's release. Nothing in the
repository runs both in one process today, so this is a latent difference
between two code paths that look identical, not an observed defect.

**(ix) A corrupt roster's refusal now runs while the store lock is held.**
`WithRosterLocked` classifies after acquiring the lock, so a corrupt
`sequence.json` makes every one of these operations hold the lock for the
few microseconds the refusal takes before releasing it. A read-only command
that does not take the lock is unaffected; a second write-capable command
queued behind the refusal waits only that long — no different in kind from
waiting behind any other short-lived acquisition.

---

## A21. Dashboard account submenus: live rows, re-validated targets (Go-side deviation from Python)

The dashboard is a monitor above a menu. The monitor
(`internal/tui/widgets.go` `accountsPanelText`) is rebuilt from `Model.snapshot`
on every frame. The menu is a stack of `(title, entries)` frames, and two of
those frames — "remove account" and "disable / enable" — carry one row per
account, each row's number, email, alias, org tag and disabled state baked into
a pre-formatted label.

Those rows were computed once, when the frame was pushed. Python does the same
(spec 09§3.2 defines no snapshot watcher on the dashboard; the `_dirty` rebuild
tick at 09§10 belongs to the macOS menu-bar surface, a different program), and
the Go port reproduced it faithfully. `Model.applySnapshot` fans each completed
pass out to `snapshotObserver` implementations only — `switchScreen`,
`watchScreen`, `autoScreen` — and `dashboardScreen` was the one screen on the
stack that was not one. Nothing ever told it the roster had changed.

The result is visible on one screen at one time: remove an account and the
monitor loses it immediately while the menu below keeps offering it. Both halves
are rendered from the same `Model`, in the same frame, and they disagree. Selecting
the stale row resolved its email through the `"?"` fallback, produced the
confirmation `Remove account 3 (?)?`, and failed in `store.ResolveAccount`.

### RULE 1 — a frame whose rows are one per account is rebuilt from every snapshot

`menuFrame` carries an optional `build func(*Model) []menuEntry`
(`internal/tui/dashboard.go`). `pushLiveMenu` sets it; `pushMenu` — for the root
and add frames, whose rows are fixed text — leaves it nil.
`dashboardScreen.onSnapshot` rebuilds every frame that has one, not only the
visible one: the stack is two deep today, and a stale frame under the top one is
the same defect deferred.

This subsumes a second defect. `disableEntries` froze the toggle direction
(`→ disable` / `→ enable`) into the label, while `Model.toggleDisabled` read
`acc.Disabled` from the live snapshot and inverted it — so a row could state the
opposite of the mutation it fired. The two halves now read one snapshot.

### RULE 2 — the cursor follows its action, and parks on the back row when that action is gone

`remapMenuIndex` looks the selected row's `actionID` up in the rebuilt rows. When
it is absent — the account was removed — the cursor goes to the back row, never to
whichever account slid into the vacated position: these rows are the dashboard's
destructive action surface, and a cursor sitting where a removed account used to
be invites the next Enter to act on its neighbour.

There is no "nothing changed, skip the remap" guard. A lookup that finds the
action finds it where it already was, so the common case — a routine usage
refresh, or the relabelling a disable toggle causes — is invariant by
construction. `accountListScreen.onSnapshotBase` needs such a guard because it
rebuilds a whole widget list; this rebuilds a slice and re-derives one integer.

The identity followed is the **slot**, not the account: `actionID` is
`remove:<number>`, and a slot emptied and refilled carries the cursor to its new
occupant. RULE 3 is what makes that safe rather than merely likely-safe.

### RULE 3 — a per-account action re-validates its target against the live roster

Making the rows live, on its own, makes the surface **less** safe. The frozen
frame failed closed: a vanished number produced `(?)` and `store.ResolveAccount`
refused, so nothing was deleted. Live rows make every confirmation name a real,
existing account, so a snapshot landing between the keypress and the dispatch
silently retargets the action — and `Model.applySnapshot` fans out to every
screen in the stack, so the cursor also moves while the frame is behind a modal
or under a pushed screen, with no rendered transition to notice.

Two checks, at the two points where the roster can have moved:

- `dashboardScreen.dispatch` refuses a `remove:` or `disable:` whose number is no
  longer in the snapshot. The `disable:` path has no confirmation modal by design
  (09§3.3) and pops its frame either way, which made it the one that most needed
  this: `Model.toggleDisabled`'s nil return for an unknown number was silent, so
  the user was returned to the root menu having been told nothing at all.
- `Model.confirmRemove` captures `(email, organizationUuid)` at push time and
  re-checks it in `onDone`. The poll runs while the modal is up — `Update` handles
  `pollTickMsg` whatever screen is top — so the sentence on screen can go stale
  before it is answered. The test is the composite, not the email alone, because
  an email alone cannot tell two same-email accounts apart: the same test
  `lifecycle.RemoveAccount` applies under its own lock, for the same reason.

### RULE 4 — an action that changed something says so

`runAction` returns an empty `Message` on success, and `Model.actionDone` raised a
toast only for a switch payload, a `showOutput` action, or a non-empty first line.
Remove and disable satisfy none of the three and so succeeded in total silence;
the monitor losing a row one poll later is not an acknowledgement, it is a second
thing to interpret. Both now raise the completion toast the `showOutput` branch
already used (Deviation #7). Failures are unaffected — they still open the output
modal.

This also has to land with RULE 1 rather than after it. The disable frame pops to
the root menu after acting, which is where the amber `(disabled)` markers become
visible; that pop was the disable path's only user-visible acknowledgement, and
anything that removes it — including making the frame worth staying in — must
replace it first.

### RULE 5 — a frame that lists the roster is the only thing on screen that lists the roster

While an account-listing frame is on top, the monitor renders with `showMinis`
false: the active account's card, and nothing else. The screen states where you
are once and what you can pick once, instead of the same roster twice in two
different vocabularies. The active card stays because removing the account you
are signed in to is a different act from removing any other, and the frame's
rows carry the markers that were missing from them entirely — `● active` and
`(disabled)`, in the card's order and colours (`stateNotes`).

The collapse has to reach both branches of `dashboardScreen.view`. The fitting
branch renders the panel it measured. The overflow branch re-renders under a
height budget through `cappedPanel`, which routes to `accountsMonitorCapped` only
when the minis are on: that function hardcodes them, so routing the collapsed
panel through it would put the roster back on screen under the very menu that
lists it, and at a one-line budget it spends the line on its "N more accounts"
indicator, which names nothing. With the per-account rows collapsed there is
nothing elided to count, so the budget simply takes the card's leading lines,
identity first.

`menuRow` now also holds its row to the terminal width. The account rows are built
from an email, an alias and an org name, so their length is roster data rather than
layout — a realistic disable row measures 51 columns — and the never-wrap property
sweep (I1) does not reach this surface. Two further gaps in that sweep are recorded
but not closed here: `accountListScreen.renderList` hands its cards to
`accountCardText`, whose header never consults `width` at all, so the Switch and
Watch screens wrap unconditionally for a long alias + email + org.

### Audited limitations

**(i) The cursor tracks a slot, not an account identity.** A slot emptied and
refilled between two polls carries the cursor to its new occupant. The row's label
is rebuilt from the same snapshot, so that occupant is named where the cursor sits,
and RULE 3 re-validates before anything is deleted — so the residual is a cursor
on an unexpected row, not an unintended mutation.

**(ii) RULE 3's second check compares the snapshot to the snapshot, not to the
pixels.** It catches a roster that moved between push and confirm. It cannot catch
a roster that moves between the confirm and the lock `lifecycle.RemoveAccount`
takes; that window is closed inside the lifecycle layer by its own under-lock
composite re-check, against the roster the action started with.

**(iii) `menuRow` flattens the row label to one colour.** Only the trailing notes
carry their own. The spec's `MenuItem` is single-coloured by construction (09§3.2),
so a marker embedded mid-label — the disable frame's `(disabled)`, which sits before
its action arrow in the spec's order — renders in the row's foreground rather than
amber. The remove frame's markers are trailing and keep their colours.

**(iv) Only the top frame's cursor is remapped.** A frame below the top keeps
whatever index it had when it was covered; `popMenu` resets the index to 0 anyway,
so this is unobservable today and would matter only if `popMenu` were changed to
restore a per-frame cursor.

**(v) Frozen-derived-state siblings not addressed here.** `autoScreen.settings` is
loaded once at mount and its `.Model`, `.Strategy` and `.IntervalSeconds` are read
on every render, so the candidates panel ranks on a mount-time axis
(`.Threshold`'s freeze is deliberate — 09§4.5). `Model.thresholdPct` is read once at
construction and thereafter only rewritten by an Auto-screen visit. Both are
tracked separately from this amendment.

## A22. Codex provider (Go-side additive extension)

tycswap manages Codex (ChatGPT) accounts as a second provider, under a `tycswap
codex <verb>` namespace. It ports claude-swap PR #252, which the Python
reference pinned in `docs/port-spec/` does not contain, so relative to that
reference it is a Go-side extension in the same sense as `tycswap env` (A16) and
the at-limit markers (A15). Within the extension the PR is the spec: the
command grammar, the messages, the store layout, the export format, and the
`--json` payloads follow it so that a Codex store or export written by either
implementation reads in the other. `docs/port-spec/` is unchanged.

**Surface.** A namespace rather than a `--provider` flag: bare `tycswap list` and
`tycswap switch` keep meaning Claude, so no existing command, script, or JSON
shape changes. `codex` is pre-dispatched on the first argv token like `run`,
`auto`, and `config`, with its own parser: `tycswap codex [--debug] <verb>`,
where every verb also accepts `-h`/`--help` and `--debug` and value flags take
`--flag=value`. Handled errors exit 1 (a red `Error: <msg>` on stderr, or the
error envelope on stdout under `--json`), usage errors exit 2, Ctrl-C exits
130; `codex login` exits with the codex CLI's own status when that login fails.

**Packages.** `internal/codex/{authfile,procdetect,api,store,usagecache,
switcher,registryimport,transfer,autoswitch}` and `internal/providers`; see the
§1 graph and table. The Codex packages depend on the shared leaves, `usage`,
and `reporting`, never on `lifecycle`, `switching`, or `store`: the Claude substrate's provenance machinery has no Codex analogue and
is not imitated.

**Seams.** `api.Client` (with `HTTPClient` and `FakeClient`, mirroring
`oauth.Client`) is the only network seam. `procdetect.ListProcesses` is an
injectable variable. `store.Options` injects the Keychain client, clock, and
platform. `reporting.AccountSnapshot` and `AccountsSnapshot` gain a `Provider`
field whose zero value means Claude, and `AccountSnapshot.Key()` returns
`<provider>:<number>`. Slot numbers are per provider, so any surface that
shows both providers — the dashboard — must not address a Codex row by
`Number` alone: Codex rows are addressed by `Key()` (`codex:1`), while Claude
rows keep their bare number as their id. `providers` decides whether a surface is single- or multi-provider:
Codex is present only when its store has slots or a codex-auth registry awaits
import, so a Claude-only install renders and ticks exactly as before. A
provider whose snapshot fails is dropped from the merged view; the others still
render.

**Invariant 1 — the live file decides who is active.** The codex CLI owns
`$CODEX_HOME/auth.json` and rewrites it whenever it refreshes. The active Codex
slot is the one whose identity matches the live file's identity;
`activeAccountKey` in `codex/sequence.json` records intent only. A session left
open on account A can rewrite the live file after a switch to B, and a
registry-derived answer would then report B while every codex command runs as
A. Capturing the live login before a switch stores it into the slot its own
identity names, for the same reason.

**Invariant 2 — the active account is never refreshed from its snapshot.** The
codex CLI holds the same refresh token as the active account's snapshot. If the
server rotates refresh tokens, two parties refreshing one token logs one of
them out. The active account's usage is therefore read with the live payload;
inactive accounts refresh from their snapshots, and the refresh runs under the
Codex lock so a rotated token is persisted before it is used. A refresh that
could not be persisted is not performed. The rule lives in
`codex/switcher`'s payload callback, so `codex/usagecache` never needs to
know it.

**Separate lock.** Codex mutations take `<backup root>/codex/.lock`, never the
Claude lock or the Claude Code lock. The two providers touch disjoint files, and
a Codex switch must not be able to block a Claude one. A switch holds the lock
end to end (capture, write, roll back on failure), because a concurrent switch
or dashboard refresh that interleaved would land one account's tokens in
another's slot. Beyond the switch, every read-modify-write of
`codex/sequence.json` (`UpsertSlot`, `RemoveSlot`, `Renumber`, `SetActive`,
alias, disabled, workspace name) takes the lock itself, so a background
`tycswap auto` writing workspace names cannot interleave with `tycswap codex add`
in another terminal and drop the new slot; a caller already holding
`Store.Lock()` is not blocked (in-process ownership is tracked per root, the
flock is never taken twice). A `sequence.json` that no longer parses is never
overwritten by a mutation (`ErrCorruptRegistry`); listing reads treat it as
empty. `export` and `purge` take no store lock, as in the PR and as the Claude
transfer verbs do; both importers run their writes under it because their
check-then-act (`--force`, `OnlyIfEmpty`) would otherwise race the same way.

**Hardening beyond the PR (review before the first push).** The refresh
decision is re-taken under the lock: the active account is never refreshed from
its snapshot, and the snapshot is re-read after the lock so two processes never
send the same refresh token (a rotated token reused is `invalid_grant` and can
revoke the token family). `switch <active>` captures the live login and writes
nothing, so a refresh token codex rotated since the last capture survives. On
macOS a snapshot too large for the Keychain's stdin path (three JWTs are) goes
to the 0600 file under `codex/credentials/` instead of the `security` command
line, where `ps` would show it; reads and deletes cover both places. Imports
skip a row whose payload identity is not its account key, normalise aliases with
the switcher's rule, and cap the document at 8 MiB. The purge root guard
resolves symlinks and fails closed. `auth.json` is rewritten 0600 and a missing
`~/.codex` is created 0700. Stopping the auto loop waits for an in-flight Codex
tick.

**Usage table reuse.** Codex usage goes through `usage.UsageStore` and the poll
policy unchanged, in its own directory `codex/cache/`: serve TTL, cross-process
fetch leases, backoff, `Retry-After`, and the adaptive cadence come with it. The
store's row guard is an identity tuple; for Codex it is `(email,
chatgpt_account_id)`, the account id taking the `organizationUuid` position,
since it is what distinguishes two workspaces of one user. The normalized usage
dict has the Claude shape (`five_hour`, `seven_day`, `spend`, `plan`), so every
renderer and the auto comparison consume a Codex row without branching. Windows
are classified by `limit_window_seconds` (one day or longer is weekly), not by
response position, because some plans report the weekly window as the primary
one.

**Why Codex API-key accounts are never rotation targets.**
`autoswitch.includeApiKeyAccounts` exists because a Claude API-key account is a
real rotation target that bills per token. A Codex API-key login reports no
usage at all, so there is nothing for a threshold to compare it against. Codex
API-key accounts are excluded from rotation unconditionally, and the setting is
documented as Claude-only rather than silently ignored.

**Why a second small engine.** The Claude `autoswitch` engine is built around
Claude specifics: per-model scoped windows, setup tokens, session profiles,
credential quarantine, cooldown. Generalizing its tick to fit a provider that
needs almost none of that would rewrite the most load-bearing code in the
project. `codex/autoswitch` is instead one small tick — at or over threshold,
move to the candidate with the lowest worse window that clears the threshold
and the hysteresis margin — and `tycswap auto` runs both engines in one process.
With `--once` the Codex tick runs after the Claude tick and the exit status is
the Claude outcome. In the loop the Codex engine runs on its own goroutine,
ticking once at launch and then every interval, stopped when the loop returns,
so the Claude engine gains no hook and no failure mode, and a slow Codex fetch
never delays a Claude switch. `autoswitch.codexEnabled` (default true) and
`autoswitch.codexThreshold` (0–99.9, 0 inherits the effective Claude threshold)
tune it; the hysteresis margin is the Claude `autoswitch.hysteresisPct`, and
`tycswap auto` gains no flag. A Codex event is printed only when the tick
switched or errored, or on every tick under `--dry-run`, in the Claude events'
format: the `HH:MM:SS` prefix and kind colouring for humans, and
`{"schemaVersion":1,"event":"codex","ts":"<RFC3339 UTC>","outcome","detail",
"switchedTo","runningPids"}` under `--json`.

**Running sessions.** A switch rewrites `auth.json`, but a running codex session
keeps its tokens in memory. `tycswap codex switch` and the Codex engine detect
running `codex`/`codext` processes by executable name — codex writes no
liveness record, so the process table is the only source — and name their PIDs
rather than switching silently.

**Dashboard.** When Codex is present the dashboard's Codex rows follow the
Claude rows, each tagged `⟨codex⟩` after the workspace tag. Switch, disable /
enable, and remove act on the row's own provider; add-account stays
Claude-only, and the auto-switch screen lists Claude candidates only, because
that screen drives the Claude engine. A Codex switch that leaves codex
sessions running raises a second warning naming their PIDs.

**Differences from the Python PR.** Where the PR is inconsistent with the rest
of tycswap or unsafe, tycswap deviates deliberately:

- `list --json` and `status --json` carry `schemaVersion`, and `usage` uses
  the camelCase `tycswap list` / `tycswap status` encoding; `status` reports
  `totalManagedAccounts`. The PR's field names are otherwise kept.
- Errors under `--json` are the standard error envelope on stdout; without
  `--json`, `Error: <msg>` on stderr. Usage errors exit 2.
- The namespace and every verb accept `--debug`, which enables the file
  logger under the backup root (and its stderr console) for the Codex network
  client, process detection, and the registry import.
- The `tycswap auto` Codex event line follows the engine's JSONL contract
  (`schemaVersion`, RFC3339 `ts`, camelCase keys) and the human timestamp
  prefix; the loop's Codex engine ticks at once rather than after one
  interval, on a goroutine where the PR uses a daemon thread.
- `remove` prints `Removed Codex account <n>` only when a removal happened (a
  declined prompt prints only `Cancelled`), and `remove`, `disable`, `enable`,
  and `alias` name the resolved slot number, not the identifier as typed.
- `import-codex-auth` prints the unsupported-schema warning, as the automatic
  import does, and still prints its `Imported N, skipped M.` count; the
  automatic import's notices go to stderr under `--json`.
- The registry import normalises plan names only for schemas below 4; a
  schema 4 plan is taken as is.
- `export` narrows an existing destination file to 0600 before writing
  tokens; `purge` refuses a store root that is empty, is `CODEX_HOME`, or
  contains it.
- `codex login` runs the binary found on `PATH` directly, never through a
  shell; a start failure or a signal death exits 1, and `codex login did not
  complete.` carries no `Error:` prefix.
- Human `list` rows carry the workspace tag (`personal` when none) by the
  Claude renderer's rule.
- Transfer and registry errors carry a cerr kind (`Transfer`,
  `AccountNotFound`); the message text is the PR's.

Any other divergence is a bug against the PR.

---

## A23. Rename to tycswap: own module, binary, store; copy once, never touch the old store

tycswap is its own product, not a cswap with extras, so the cswap name goes
completely: module `github.com/tyclab/tycswap`, `cmd/tycswap`, binary
`tycswap`, every user-facing string, the export extension `.tycswap`, the
macOS Keychain services `tycswap` and `tycswap-codex`. There is **no
compatibility shim**: no `cswap` binary alias, no `CSWAP_*` fallback, no
reading of the old store in place. Sharing a store or a Keychain service with
the tool it was forked from would let two products with diverging behaviour
write the same roster and credentials; a separate store makes that impossible.

**Paths.** Only the store root moves. Linux/WSL:
`$XDG_DATA_HOME/tycswap` (default `~/.local/share/tycswap`); macOS, Windows,
unknown: `~/.tycswap`. The layout INSIDE the store stays exactly as before
(`sequence.json`, `settings.json`, `mappings.json`, `credentials/`,
`configs/`, `cache/`, `sessions/<n>-<email>/`,
`codex/{sequence.json,registry.json,credentials/,cache/}`, `.lock`), because
tools that integrate with the store read that layout. Two names inside it
change: the log is `tycswap.log` (was `claude-swap.log`), and the session
profiles' marker files are `.tycswap-*` (were `.cswap-*`).

**Migration rule: copy once, never touch the old store.** `tycswap migrate`
(package `storemigrate`) copies the first existing old store
(`$XDG_DATA_HOME/claude-swap` when set, else `~/.local/share/claude-swap`,
then `~/.claude-swap-backup` on Linux/WSL; `~/.claude-swap-backup` on
macOS/Windows) into the new one, under the new store's lock, and only when
the new store is absent or holds nothing but throwaway artefacts (lock,
cache, log, migrations ledger). Files land 0600 by temp file and rename,
directories 0700, symlinks are copied as links, lock files are skipped,
`claude-swap.log*` becomes `tycswap.log*` and `.cswap-*` becomes `.tycswap-*`.
On macOS the per-account, Codex and session-profile Keychain items are copied
to their new service names (a session profile's service hashes its path,
which changed). The old directory and the old Keychain items are opened for
reading only: never moved, modified or deleted, since another installed tool
may still own them. A new store holding anything that is neither a throwaway
artefact nor part of this same copy is refused, never merged; a copy that was
interrupted is resumed by the next run, which copies what is missing and
verifies byte for byte what is already there (A25 item 8). Every other
command prints one stderr line while the new store is empty and an old one
exists (not under `--json`).

**Consequences.** The startup move `~/.claude-swap-backup` → XDG (§2.13 step
3, `paths.MigrateLegacyBackupDir`) is removed: it moved another tool's
directory. `purge` removes only tycswap's store, never an old one. `import`
still reads a `.cswap` export: reading an old file is migration, not
compatibility, and the envelope format is unchanged.

---

## A24. Self-upgrade installs tycswap, never the project it was forked from

A6 pointed `update.ModulePath` and `update.Endpoint` at the upstream project,
so after the rename (A23) `tycswap upgrade` of a go-installed binary ran
`go install <upstream>/cmd/cswap@latest`, replacing tycswap with a different
program, and the passive notice announced upstream releases. Now:

1. `ModulePath` = `github.com/tyclab/tycswap/cmd/tycswap`, `Endpoint` =
   `https://api.github.com/repos/tyclab/tycswap/releases/latest` (GitHub
   schema, `tag_name`), `ReleasesURL` = `https://github.com/tyclab/tycswap/releases`.
   All three stay package vars, settable with `-ldflags -X` (a test links a
   probe binary with overrides to keep that true).
2. A binary built from a checkout is never re-installed from a remote. The
   signal is the binary's own build info (`runtime/debug.ReadBuildInfo`): a
   `vcs.*` stamp (Go writes it when building inside a repository: `make
   build`, `make install`, `go build`, `go install ./cmd/tycswap`) or a
   `(devel)`/empty main version means checkout; a real module version with no
   VCS stamp means `go install <module>@<version>` from the module cache. No
   build info is treated as checkout. A checkout build's `upgrade` prints
   `tycswap was built from a checkout: git pull && make install` and exits 1;
   its update notice ends with the same advice instead of "run tycswap
   upgrade". The install-shape detection of A6 applies only to module builds.
3. `SelfUpgrade` has no downgrade guard: it installs `@latest`, and the
   notice only fires when the latest tag is semver-greater.


---

## A25. Security pass: trust nothing that arrives from outside the store

A security review of the renamed tree found two HIGH, five MEDIUM and a run of
LOW/INFO findings. Their common root is that tycswap treated four kinds of
outside input as its own: an export file, another tool's `.claude.json`, an
HTTP response, and the shared filesystem around the store. The rules below
override anything above; the CHANGELOG lists each change.

**1. An import carries an identity, not a configuration.** Finding *"a
malicious export can get code executed"*: `import` stored an export's
`config` whole and `directActivate` wrote a stored config whole when no live
`~/.claude.json` existed, so `mcpServers` or allowed tools in an export ran on
the importing machine. Now `import` keeps only `{"oauthAccount": …}` (also
from a `--full` export), and a switch only ever takes the `oauthAccount` out
of a stored config: with no live config it writes that key alone. Finding
*"corrupt config wipes the user's ~/.claude.json"*: a live config that exists
but cannot be read or parsed is an error for every read-modify-write
(`ccfile.ErrUnusableConfig`), never an empty object; only absent, blank or
`null` start from `{}`. Finding *"import validates one value and stores
another"*: the envelope is decoded once into raw members with exact keys, the
stored credential bytes are those of the validated member, and input is
capped at 8 MiB. Display fields are stored without control characters.

**2. A secret never goes on a command line.** Finding *"Claude credentials can
appear on the `security` command line"*: `keychain.Security.Set` refuses a
payload too large for `security -i`'s stdin with a `TooLarge` KeychainError;
credstore takes its file fallback without marking the Keychain unusable, and
so do `tycswap migrate` (the item goes to the file tycswap reads for it: the
`.enc[.prev]`, the Codex snapshot file, the profile's `.credentials.json`) and
the legacy keyring migration (the `.enc`), so an account whose item the old
tool stored through argv still migrates.
Finding *"tokens on the command line are encouraged"*: the docs use the
prompt and `-`, and `add-token` warns on a positional token.

**3. A string that names a file is validated where it enters the store.**
Finding *"an unvalidated email builds file paths"*: the check that guards a
path is a path-safety rule, not an address format. `storenames.ValidEmail`
accepts at most 254 bytes with exactly one `@` between a non-empty local part
and domain, and refuses whitespace, control characters, `/`, `\` and the
characters Windows forbids (`< > : " | ? *`); `add`, `add --login`, `add-token`
and `codex import` apply it where an email enters the store, and `purge`
applies it to every roster email before it builds a path from one, refusing
with the slot named. A validated email is therefore a single path component,
so the backup files keep the raw email in their names
(`configs/.claude-config-<n>-<email>.json`,
`credentials/.creds-<n>-<email>.enc[.prev]`, built in one place, `storenames`,
for store, credstore and purge); the layout A23 describes holds, and a real
address with an apostrophe or a non-ASCII letter works. The Claude `import`
keeps claude-swap's `_validate_email` pattern as its contract for export
files, defined once as `storenames.StrictEmail`.

**4. A refresh that cannot be persisted is not performed.** Findings
*"inactive-slot refresh can overwrite a newer credential"*, *"`run` can
consume the live login's refresh token"* and *"`codex login` destroys the
live login's newest token"*: the inactive-slot refresh takes the store lock
first, re-checks the slot and re-reads the backup, and refreshes only an
unchanged lineage (the Codex switcher's rule). Bootstrap never refreshes a
backup sharing the live login's lineage. `codex login` captures the live
auth under the lock before codex overwrites it. The auto-switch freshen of a
candidate slot goes through the same guarded refresh
(`store.RefreshBackupGuarded`, shared with the usage fetch), so no path
refreshes an inactive slot without the lock; the HTTP refresh under the lock
is bounded to 5 s, below the lock's 10 s timeout, so a concurrent command
waiting for the lock gets it. The active-account refresh
keeps its post-refresh lineage re-check (A-level behaviour unchanged): holding
Claude Code's own locks across a network call would stall Claude Code.

**5. The store is private, and so are its locks.** Findings *"no check that
the backup root is private"*, *"the log and update cache can be
world-readable"* and *"two processes can both break the same stale lock"*:
every command refuses a store root not owned by the effective user and
makes one writable by group/other 0700 (a mode chmod cannot change, as on
a Windows drive under WSL, is one warning line); lock files open without following symlinks and
without truncation, 0600; directories tycswap creates are 0700; a stale
Claude Code lock is broken by rename, age recheck, then remove.
`settings.json` and `mappings.json` read-modify-writes hold their own lock
files (`.settings.lock`, `.mappings.lock`), not the store lock, because
`remove` prunes mappings while holding the non-reentrant store lock. Atomic
writers fsync the temp file and the directory.

**6. HTTP responses are bounded and typed.** Finding *"the Claude HTTP client
lacks limits and type checks"*: redirects are returned, not followed, by the
Claude and the Codex client alike (a 3xx is a failed request);
success bodies are capped at 1 MiB; `access_token` must be a non-empty string
and `0 < expires_in < 1e7`. Finding *"refresh error bodies go to the log"*:
only the OAuth error code is logged.

**7. Printed strings are inert.** Findings *"no sanitising of printed
strings"*, *"pwsh quoting misses smart quotes"*, *"`add --login` keeps auth
override variables"*, *"`.cmd` argument injection on Windows"*: one helper,
`termsafe.Strip`, removes C0/C1/ESC from every outside-sourced string at
print time; `pwshQuote` doubles U+2018–U+201B; `add --login` scrubs the auth
override variables; a `.cmd`/`.bat` claude refuses cmd.exe metacharacters in
passed-through arguments. *"Codex export is not atomic"* was already fixed.

**8. An interrupted `tycswap migrate` resumes.** A rerun copies what is
missing, verifies byte for byte what is already there, and refuses only on a
real conflict.

**9. The credentials file is the account plus a seat-wide remainder.**
Finding *"a switch logs the MCP servers out"*: Claude Code keeps its MCP
server OAuth tokens in `~/.claude/.credentials.json` (and the macOS Keychain
item) under the top-level key `mcpOAuth`, keyed by server, not by Claude
account; writing a stored blob over that file whole swapped or dropped them on
every switch, swept a fresh MCP login into the outgoing slot's backup where it
went stale, and shipped MCP refresh tokens in exports. The rule is an
allow-list of one key: `mcpOAuth` is the seat's, everything else
(`claudeAiOauth`, `trustedDeviceToken`, which Claude Code mints per login) is
the account's. A switch writes the stored account blob with the live
`mcpOAuth` carried over it (`ccfile.SpliceCredentials`, through
`credstore.WriteActiveAccount`, which reads the live credential first and
writes the blob verbatim when that read fails or does not parse); a capture —
`add`, `add --login`, the switch-time backup and stash, the active refresh
write-back, `export` — stores `oauth.AccountOnly(blob)`, the blob minus that
key; the ownership classifier compares account bytes, so an MCP login never
reads as a changed credential. Rollback and the refresh write-back keep
`WriteActive` verbatim. `import` writes what it is given; a stale `mcpOAuth`
in an old backup or export loses to the live one whenever the seat has MCP
logins, and is written as it is only when the live file has none (Claude Code
then re-prompts, as before) — the next capture of that slot drops it.

**Not done here.** Windows has no owner-only DACL on `credentials/`, exports
and the store root (`CheckPrivateRoot` is a no-op there); a follow-up.
Dependencies: `go.mod` pins `toolchain go1.25.14`; `make vuln` runs
govulncheck.
## A26. `internal/web` — the browser dashboard and its security model (Go-side additive extension)

`tycswap web` serves a single-page dashboard from `internal/web` on a
loopback port. The page (`static/index.html`, `app.js`, `style.css`, the
`docs/logo.svg` mark as `icon.svg`) is embedded and works offline; a small
JSON API and a Server-Sent-Events stream drive it. Like tui (A13),
`internal/web` declares its own seams and imports neither core nor cli:
`Facade` is the frozen tui.Facade method set, and `AccountOps`,
`SettingsFacade` and `AutoFacade` are bound in `internal/cli/webfacades.go`
to the packages the CLI commands use, so the dashboard and
`tycswap switch|config|auto` cannot drift apart. Export and import are not
in the dashboard: writing live credentials to a path a page names is the
CLI's job, where the user sees the path and the file.

**Threat model.** The dashboard can switch, add, remove and re-slot accounts,
drive the auto-switch engine and stop Claude Code sessions. Anything on the
machine can send requests to a loopback port, and any web page the user
opens can make the browser do so. The design keeps both from driving it.

1. **Loopback only.** `Server.Start` refuses a non-loopback address before
   listening and checks the bound address after. A `Host` header other than
   `127.0.0.1:<port>` or `localhost:<port>` answers 421, which defeats DNS
   rebinding.
2. **Three per-launch secrets**, 128 bits each from crypto/rand, all
   different:
   - the *launch token*, only in the printed URL, redeemed once by
     `GET /?token=`; the check and the redemption are one critical section,
     so concurrent redeem attempts agree on exactly one winner;
   - the *session cookie* value, set by that redemption: HttpOnly,
     SameSite=Lax (Strict would be withheld on the redirect from the file://
     launch page), Path=/, named `<brand.SessionCookie>_<port>` because
     cookies are not port-scoped and one name would let a second dashboard
     log out the first;
   - the *CSRF token*, handed to the page once in the fragment of that
     redirect (`/#csrf=<token>`). A fragment never reaches a server; app.js
     moves it into `sessionStorage` (per tab; a reload keeps it, a new tab
     needs a fresh launch URL) and drops it from the URL and the history
     entry with `history.replaceState`. The page is rendered without it.
   The cookie alone is never enough: a cookie set for 127.0.0.1 reaches every
   port on 127.0.0.1, so a hostile local service could see it — and the page
   that cookie fetches holds no second factor to read.
3. **Both factors on every `/api` call**, reads included: cookie plus
   `X-CSRF-Token`. EventSource cannot send headers, so `/api/events` alone
   accepts `?csrf=`; anywhere else a token in a URL would only leak (history,
   logs). Non-GET requests also need `Origin` absent or same-origin and
   `Sec-Fetch-Site` absent, `same-origin` or `none`.
4. **The page and its assets need the cookie**, so a foreign page cannot
   fingerprint the port by loading `/static/app.js`. Neither carries the
   CSRF token, so the cookie buys a reader nothing but the static UI.
5. **Headers.** CSP `default-src 'self'` with no inline script or style (the
   build's accent colour is therefore served as `/static/accent.css`),
   frame-ancestors none, nosniff, no-referrer, no-store, DENY framing.
6. **No credential material leaves the process.** State rows carry
   metadata, usage and, on request, the token *status* line; the add-token
   body is input only and never echoed or logged; no route reads or writes
   a credential file.
7. **The client renders API data as text.** `el()` sets no `href`, `src`,
   `style` or `on*` attribute at all, and the page uses no `innerHTML` or
   `eval`, so an alias, a path or a session title cannot become markup.
8. **Opening the browser** never puts the launch token on a command line on
   unix, where argv is world-readable: `tycswap web` writes a 0600 redirect
   page under a private 0700 directory (`$XDG_RUNTIME_DIR/<name>`, else
   `<user cache dir>/<name>`; never the shared temp directory, which another
   user can fill to force the write to fail) and hands the launcher its
   `file://` URL; when the page cannot be written nothing is handed to a
   launcher and the CLI points at the printed URL
   (`internal/browser`: `open`; `wslview`, `xdg-open`, `sensible-browser`,
   each resolved on PATH and started directly, so no shell sees the URL and
   "could not open a browser" is reported only when no launcher started).
   On Windows, where another user cannot read argv, the URL goes to the
   default browser directly (ShellExecuteW, `rundll32`, `cmd start`).
   `internal/browser` refuses URLs with shell-significant characters.

**Row identity.** Slot numbers are per provider (A22), so every state row
carries `provider` and `key` (`AccountSnapshot.Key`), and every account route
takes the key (`claude:2`). A bare reference answers 400 and another
provider's key 404 before any façade is reached, so a later Codex row can
never address the Claude account with the same number. One row shape for
every provider: when token status is asked for, each row carries
`tokenStatus` as its own string (`""` when there is none), the way the
Claude rows do today. `tycswap codex list --json` reports token status as a
top-level array beside its rows; the dashboard's Codex rows, when they
arrive, carry the per-row string instead, and the state document never
gains a top-level `tokenStatus`.

**Live data.** One poll loop rebuilds the state document every interval and
after every mutation; engine events are fanned out as `auto` frames and
followed by one state per batch. Token status is enriched only while a
subscriber asked for it (`?tokenStatus=1`), from the same snapshot, through
the `tokenStatus` key `reporting.ListAccounts` puts on each JSON row when
asked for token status (the plain `list --json` payload is unchanged). The
page reconnects by itself and, when the server no longer knows its tokens (a
restart mints new ones), stops and says to reopen the printed URL.

**Errors.** Every error body is `{"error"}`; the status comes from the cerr
kind (not found 404, validation 400, lock 409, Claude Code's lock 423, else
500), or from the handler itself where no kind fits (a session gone before
the lock, 404; a pid that is not the recorded process, 409). Ordinary user
mistakes a facade would report as a config error (a slot that is not a
number) are refused in the handler as 400 before the facade is reached.

**Hosted engine.** The Auto tab drives one `autoswitch.Engine` in the `web`
process. `Engine.ApplyThreshold` and `Engine.ApplyModels` retarget a running
engine. Both store the new value in the engine's atomic settings and re-pin
the poll plan from those settings; the model slice the tick goroutine counts
is touched by that goroutine alone, which adopts a queued set at the start
of its next tick. The settings routes call `ApplyModels` themselves whenever
`autoswitch.model` is saved or unset while the engine runs, so the toggle
and the settings grid share one path and the page never has to remember to
retarget. Stop asks the engine to stop and waits up to 2 s for its
loop to return; past that it answers 409 ("stopping") rather than holding the
dashboard's mutation lock, the engine already counts as stopped, and Start
refuses while the old loop is still finishing, so two engines never run at
once. When the loop has returned the engine's poll-plan pin is cleared, as
the TUI's Auto screen does on exit, so a stopped engine's threshold and
models stop steering the usage polling.

**Sessions.** The list covers the default Claude config directory and every
session profile under `<backup root>/sessions/` (A16), so sessions started
with `run` or `env` appear with their slot and can be stopped. A session
file counts only when its name is the pid it carries (`sessions/<pid>.json`,
as Claude Code writes it). Stop acts only on a PID listed at that moment,
consulted again under the mutation lock, and only after the process
holding the PID is verified as the one the file describes: its start time
(Linux `/proc/<pid>/stat` starttime plus boot time; macOS `kinfo_proc`;
Windows `GetProcessTimes` on an `OpenProcess` handle that `TerminateProcess`
then uses, so the process checked is the process ended) must match the
file's `startedAt` within 20 seconds, or the route answers 409 and nothing
is signalled. A session file left behind by a crash or a power loss names a
PID the system may have reused; on Windows, which also has no SIGTERM for
another process and terminates hard, PIDs are reused quickly.

**Branding.** Names, the cookie name, the redirect-file prefix and the accent
colour come from `internal/brand`, overridable at link time and validated
before use.

**Follow-ups, deliberately not in this extension:** Codex rows (each with
the per-row `tokenStatus` string above), a Codex façade and a `codex` state
section; the Codex auto loop moving out of
`autoCommand` into a host the dashboard shares; `add --login` and `codex
login` as a cancellable job streamed over SSE; `map`/`unmap` in the
dashboard; a remote mode for a tray across a VM boundary, which re-adds a
bearer token accepted in place of the cookie and CSRF pair and a route that
mints a fresh one-time URL for such a client (nothing in `tycswap web`
calls either today, so neither is built); the tray itself, which also
brings back the brand vars it needs (a reverse-DNS identifier, an
environment-variable prefix); and a short generic Guide tab: what the tool
does, slots and the active account, the 5h/7d/model windows and the single
threshold, getting started, manual switching, Auto, Sessions, a command
cheat-sheet and where the data lives.

## A27. Dashboard parity: updates, a Settings tab, a Guide, folding cards, the live login and the auth overrides (Go-side additive extension)

A26 shipped the dashboard with three tabs and listed what it still lacked.
This amendment brings it to parity with the reference dashboard for every
feature that is generic: an Updates card with a header indicator, a Settings
tab for every `settings.json` key, a Guide tab, two cards that fold and are
remembered, a callout for a login Claude Code has that is not stored yet, and
a notice for the authentication overrides that make Claude Code ignore the
stored login. What is not generic stays out: no telemetry, no gateway, no
plugin marketplace, plugin updates or plugin cards, and no launch route (that
belongs to a tray, a separate stage). `internal/web` keeps A26's rule:
consumer-defined seams only, no import of core, tui or cli, and the page
renders API data as text.

**Updates** (`internal/web/updates.go`, `internal/cli/updateshost.go`,
`internal/ccversion`). Two things can be updated from the page: tycswap
itself and Claude Code.

- *tycswap.* `update.Checker.Latest` asks the release endpoint `tycswap
  upgrade` already uses and, unlike the passive CLI notice, says why when it
  cannot (the card names what it could not check). A tag it reads refreshes
  `cache/update_check.json`, so the next CLI command's notice agrees with the
  card; a failure leaves the cache alone. The apply is `tycswap upgrade`'s own
  path, `update.Upgrader.SelfUpgrade`, with its output captured: `go install
  <ModulePath>@latest` for a go-installed binary, and for a checkout build, an
  unknown layout or Windows the guidance it prints, which becomes the error's
  output. The card knows beforehand (`AppUpdateView.Hint`) when this build
  upgrades from a terminal and shows the command instead of a button; the
  install layout behind that hint is read from the environment and home
  directory `SelfUpgrade` itself reads, so the button is offered exactly when
  the apply would run `go install`. After a
  successful install the running server is still the old version: the card
  says so (`Installed`) and stops offering until `tycswap web` is started
  again.
- *Claude Code.* `ccversion` finds the binary (PATH, then the places the
  installers put it), reads `claude --version`, and tells the install method
  from where the binary really lives: the native installer, npm (a
  `node_modules` path or the legacy `~/.claude/local`), a Homebrew cask, WinGet,
  else unknown. The newest version is read from that method's own source
  (the native release channel, npm's `dist-tags`, the cask's formula; WinGet
  follows native latest). Native and unknown installs read `autoUpdatesChannel`
  from the user's `settings.json` under `CLAUDE_CONFIG_DIR` or `~/.claude`:
  `stable` or `latest`, defaulting to `latest` only when the file or key is
  absent. Invalid or unreadable settings are a check error. A cached release
  is not reused across channel changes. Project and managed policy remain
  the installer's responsibility; the host verifies the result of an apply.
  The apply is that installer's own command: `claude update` for a native or unknown install,
  `npm install -g @anthropic-ai/claude-code@latest`, `brew upgrade --cask
  <cask>`, `winget upgrade`. A missing Claude Code is said (with the install
  line to type), never run. Versions compare by semver; a version that does
  not parse is an error, never "newer".
- *Cadence.* `tycswap web` had no periodic check. `Server.Serve` now asks the
  `UpdatesFacade` to check once at start and then every `Deps.UpdateInterval`
  (six hours by default; `Deps.UpdateTicker` is the seam tests drive by
  hand), never on a poll tick; `POST /api/updates/check` asks again and
  answers 202 at once, the state it broadcasts already saying "Checking…".
  The host checks in the background, both lookups at once, and pushes a
  fresh state through `Server.Refresh`, which only signals the serve loop
  (one slot, so a burst coalesces and no caller ever blocks; before Serve the
  signal waits for the loop's first turn, after it is a no-op).
- *A failed check forgets nothing.* Being offline says nothing about whether
  an update still waits: a release check that fails keeps the release the
  check before it found and adds its error; a Claude Code check that cannot
  learn the newest version keeps the last one for the same installed Claude
  Code (same path, version and method) and adds its error; a different
  Claude Code (updated by hand meanwhile) inherits nothing and shows as
  "could not check". `checkedAt` is the last check that reached the network;
  before the first the card says "Not checked for updates yet", and while an
  error is set it says "Could not check for every update", never "Everything
  is up to date".
- *Apply.* `POST /api/updates/apply {"target": "app"|"claude-code"}` runs one
  update synchronously, after the page has asked the user, and answers the
  host's `UpdateResult` (`message`, `output`); a failed apply answers
  `{"error", "output"}`, so the page's Output disclosure shows what the
  commands printed after a failure too. One apply runs at a time (409 for a
  second), outside `mutMu`: an account switch must not stall behind a
  multi-minute install. The response gets a 30-minute write deadline. After
  a Claude Code apply the host looks at Claude Code again, so the card shows
  the version now installed. A zero exit status alone is not success: if the
  installed version cannot be verified or is older than the advertised
  release, the response is an error with the installer's output.
- *The indicator.* The header carries a pill on every tab while something
  waits, with the count (one per update the user would run; "9+" past nine)
  and the updates as its tooltip; it pulses three times when it appears, not
  at all with `prefers-reduced-motion`, and a click opens the card. The pill
  is the mark's violet: neither the brand blue (the active account) nor the
  status red (a limit).

**Settings tab.** The settings editor was a card on the Auto tab, where it
read as part of the engine. It is now its own tab, Settings, listing every key
`settings.SettingSpecs` defines — the single source of truth `tycswap config`
reads — with a type-aware control (a number field with its range, a switch
for a bool, a select for a choice, a text field for a string), the default,
the value in effect, *Save* and *Reset to default* per key, through the
existing `GET /api/settings`, `POST /api/settings/{key}` and `DELETE
/api/settings/{key}` routes, so the tab and `tycswap config` cannot drift
apart. The tab's badge counts the keys set away from their default. The grid
depends on the settings facade alone: it is hidden only when `state.settings`
is null, which `tycswap web` never sends (the facade is always wired), and
never on the auto engine. The Auto tab keeps its live threshold slider and
the *Count model limits* switch and gains a short way to the Settings tab in
place of the duplicated card. A test wires the real `settingsFacade` over a
store with no `settings.json` and asserts that `/api/state` carries every
defined key, at its default and marked so, in registry order.

*When a saved value takes effect.* Each row says so (`SettingView.Applies`,
set by the server in `settingApplies`, next to the route that owns the one
live retarget, so the wording and the behaviour live in one file). It
describes what the code does and adds no mechanism: an engine copies the
settings when it starts (`settings.Load` in the host's `Start`) and only
`ApplyThreshold` and `ApplyModels` change a running one.

| Key | Takes effect |
|---|---|
| `autoswitch.model` | at once: a save or reset through the settings routes calls `ApplyModels` on a running engine (A26), and the at-limit marks re-read it for every state document |
| `autoswitch.threshold` | at the next engine start; the Auto tab's slider retargets the running engine for this run only, unsaved |
| `autoswitch.codexEnabled`, `autoswitch.codexThreshold` | at the next `tycswap auto`: the engine this page hosts rotates Claude accounts only |
| every other key (`intervalSeconds`, `cooldownSeconds`, `hysteresisPct`, `strategy`, `includeApiKeyAccounts`, `unhealthyTicks`, and any key added later) | at the next engine start, here, in the TUI's Auto view or in `tycswap auto` |

The TUI's Settings screen (A28) edits the same `settings.json` through the
same package (`settings.SetSetting` / `UnsetSetting` and the
`EffectiveSettings` list); neither surface knows about the other, and a value
saved in one shows in the other on its next read.

**Guide tab.** Static markup in `index.html`, templated with the brand name
only: what the tool is, the five concepts (slot, active account, the 5h / 7d /
model windows, at limit and eligible, the one threshold), getting started,
switching by hand, Auto mode and its settings, the Settings tab, Sessions,
Updates, Codex accounts (the CLI has them, the page does not), a command
cheat-sheet that includes `tycswap config`, troubleshooting, and where the
data lives. It names no URL: the page works offline and the static-asset test
forbids any (`TestIndexHTML_OnlyLocalReferences`), so the Claude Code install
lines live in Go (`ccversion.InstallHint`) and reach the page through the
state. Section fragments (`#g-…`) select the Guide tab and scroll after it
is visible, preserving the fragment for reload and history navigation.

**Folding** (`internal/web/uiprefs.go`, `internal/cli/uiprefs.go`). The
Accounts card and the Updates card fold to their heading with a chevron (a
`<button aria-expanded>` inside the `h2`, the disclosure pattern). A folded
Accounts card keeps its count line, its *Add current login* button and the
callout for a login that is not stored yet; the *Token status* switch hides,
since it changes nothing then. The server remembers the choice, not the
browser: the dashboard's port is ephemeral and browser storage is kept per
origin, port included, and the page has no `localStorage` by rule. `<backup
root>/ui_prefs.json` (`{"version": 1, "folded": {"<card>": true}}`, written
atomically, 0600, under a process-wide mutex so two pages folding two cards
drop neither) is the store; the state carries `ui.folded` (null without
`Deps.UIPrefs`), and `POST /api/ui/folded {"card", "folded"}` changes it for
the two known card ids only (400 otherwise), then broadcasts, so other open
pages fold too. A fold the page sent wins over a state that does not carry
it yet; a card the file names that the page does not know is not carried
into the state.

**The live login** (`state.currentLogin`). `Deps.CurrentLogin` is the email
of Claude Code's OAuth identity (`store.GetCurrentAccount`); `saved` is
exactly "the active account is managed" — the snapshot's active number is
the stored slot of the live login — so while it is false the Accounts card
shows a callout, *Claude Code is signed in as <email>, which is not stored
here yet*, with a large *Add current login*. The button is also the card's
main action, in its head, where the toolbar had it; the click no longer asks:
storing the login Claude Code has changes nothing else, and the callout has
said what it does. A line under the table says how to add another account —
`/login` in Claude Code, then *Add current login*, or `tycswap add --login`
— and that `/logout` first would end the login stored here. *Add token…*
stays in the toolbar as the secondary way in.

**The auth overrides** (`state.authOverrides`,
`web.DetectAuthOverrides`). `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` or
`ANTHROPIC_BASE_URL` in the environment `tycswap web` runs in — which a
plain `claude` from the same shell inherits, while `tycswap run` and `tycswap
env` scrub them — or `apiKeyHelper`, `env.ANTHROPIC_API_KEY`,
`env.ANTHROPIC_AUTH_TOKEN`, `env.ANTHROPIC_BASE_URL` in Claude Code's
`settings.json` make Claude Code sign in with something other than the stored
login. The state lists the names that are set (an empty or null value
selects nothing; a settings file that is missing or does not parse declares
nothing), never a value, and the page shows a notice above the accounts
naming each source. `Deps.AuthOverrides` defaults to the detection over
`os.Getenv` and `<config home>/settings.json`; tests pin it.

**Sessions.** The reference's Sessions tab was compared with A26's: both
group the running sessions by directory in `<details>` groups with the same
columns (session title, PID, kind and entrypoint, status, started, Stop), the
same filters and sort; the reference's markup also carries a flat sessions
table that its client never renders. tycswap's list, which also marks the
slot a `run`/`env` session runs as, is kept as it is.

**Security.** No new unauthenticated route: every new route is under
`requireAuth` (cookie and CSRF token), every one is a POST and so also under
the Origin / `Sec-Fetch-Site` rules; the state's new sections carry names,
versions and commands, never a token, a key or a value; the apply route runs
one update at a time and bounds its response; the fold route stores two
known ids and nothing else; the host's log lines carry the target, not the
output. The Guide's and the card's commands are text the user types, never
run from the page except through the apply route's two fixed targets.

**Follow-ups, deliberately not here:** everything A26 listed that this
amendment does not cover (Codex rows, `add --login` as a streamed job,
`map`/`unmap`, the remote mode and the tray), and no marketplace, plugin or
telemetry surface: tycswap has none of those.
## A28. TUI Settings screen: `settings.json` edited from the dashboard (Go-side additive extension)

`tycswap tui` gains a Settings screen (`internal/tui/settingsscreen.go`),
reached from the dashboard menu's `Settings…` row (above `Quit`) and the `c`
key (as in `tycswap config`; shown in the footer legend). It lands beside the
browser dashboard's Settings tab (A27): the two edit the same file through the
same package, and neither knows about the other.

**What.** The screen lists every key `settings.SettingSpecs` defines, in
registry order under a header per `Spec.Section`, as `key  value` rows with
the muted `(default)` marker on a key that is not set — the values, markers
and aligned columns `tycswap config list` prints, though the CLI lists the
dotted keys flat, with no section header. There is no list of keys in the
TUI: the rows are `settings.EffectiveSettings`,
so a new spec appears without a change here. The highlighted key's detail
sits pinned under the rows: its dotted name and `Help`, its kind with the
`Lo`–`Hi` range or its `Choices`, its default, and when the engine applies
it (below). `enter` edits with the control the kind calls for: a bool
toggles and a choice cycles to the next value, each saved at once; an int,
float or string opens an inline input over the current value (empty for an
unset string, whose `(none)` is a marker). The typed value is checked with
`settings.ParseSettingValue` — the parser `tycswap config set` uses — before
anything is written; a refused value (out of range, wrong type, empty
string) keeps the input open with the message under it, so `50 and 99.9` is
read where it is corrected. `u` resets the highlighted key through
`settings.UnsetSetting`; a key that is not set gets the CLI's `is not set;
nothing to do` notice as a toast. `esc` cancels an open input first and
leaves the screen second, the two-stage escape the Watch and Auto screens
use.

**Writes.** Every write is `settings.SetSetting` / `settings.UnsetSetting`
(strict validation, the `.settings.lock`, atomic rename, the Python float
form), run through the app's single-flight action gate (09§2.6,
`startMessageAction`): a second write while one is in flight is refused
with the `Another action is still running` toast (an open input keeps the
typed value, to be submitted again), a completed write toasts the line
`tycswap config set|unset` prints (`autoswitch.threshold = 80`,
`autoswitch.threshold unset (default: 90)`), and a failed write (a
settings.json that no longer parses, a lock error) opens the output modal
titled `Set <key> — failed` with the error, as every other action's failure
does. The rows are re-read when a write lands (the app now fans every
`actionDoneMsg` out to the stacked screens' `onMessage`, which the list
screens ignore) and on every poll, so a value changed by another tycswap
instance or by `tycswap config set` in a terminal shows on the next tick,
the way the dashboard's account submenus track the roster (A21). A saved
or reset `autoswitch.threshold` moves the dashboard's bar tick
(`Model.thresholdPct`) at once, as the Auto view's mount sync does.

**Why.** Until now the file was reachable only from the command line (and,
beside this, the browser dashboard's tab). The one knob the TUI could change,
the Auto view's threshold-adjust mode, is session-only by contract (09§4.5,
§11.6: never written to `settings.json`, reverted on exit) and stays exactly
that — its adjusting hint now ends in `session only — Settings persists`, so
the user who wants the change to outlive the screen is told where it is made.
The hint (09§4.5's `← → adjust · enter done` plus that ending) moves off the
summary line onto a line of its own under it, shown only while adjusting: on
the summary line it ran past column 80 and was cut off.
The Facade is not extended (A13): the screen reads and writes through the
settings package over `Facade.BackupDir()`, as `loadThreshold` and the Auto
view's `settings.Load` already do.

**Engine pickup, as the CLI path has it.** The engine does not re-read
`settings.json`: `NewEngine` takes a frozen `AutoSwitchSettings`,
`Engine.currentSettings` reads an `atomic.Pointer` that only
`ApplyThreshold` and `ApplyModels` ever replace, and `tycswap config set`
notifies no running `tycswap auto` — a saved key is in force when an engine
next starts. The TUI mirrors that and invents nothing: its engine lives only
while the Auto view is open, is built from a fresh `settings.Load` at mount
(09§4.2) and stopped at exit, and the Settings screen opens from the
dashboard, so no TUI-hosted engine is running while a key is changed, and
the next opening of the Auto view starts one from the saved file. Nor does a
write from here reach an engine running elsewhere: a `tycswap auto` process,
another TUI's Auto view, or the engine `tycswap web` hosts, which its own
settings routes retarget only for an `autoswitch.model` saved through them
(`ApplyModels`, A26). The detail line says so for every key (`applies when
the auto-switch engine next starts`); no engine applies a key earlier than
that, so the note is the same for all of them. Outside the engine,
`autoswitch.threshold` and `autoswitch.model` are read from the file where
they are used — the usage poll plan while no engine pins it, the at-limit
markers of `list`, `status` and the dashboard, and `tycswap switch`'s model
windows — so those follow a save on the next poll or command. The
in-process retargets the browser dashboard uses on a running engine are not
wired here, because here nothing is running to retarget.

**Visual.** The row cursor is the accent left border (09§8.2), the input the
accent-coloured typed text with a `▏` caret, the refused message in the
critical colour under the detail, the section header muted bold. Every line
is fitted to the width (`clipRichLines`) and the rows window around the
cursor on the content height (viewport.go); the title above and the
detail below stay pinned. The detail's help and kind lines wrap between
words, two lines each at most, so a long help or choice list is read whole
at 80 columns instead of being clipped; its message line is always there,
so the layout does not jump when an input opens. The Auto view's badge + summary
line and its adjusting-hint line are now fitted to the width too
(`truncRich`): the summary line with the hint on it was 81 columns while
adjusting, so it wrapped at 80 and threw the viewport's line count off.

**Tests** (`internal/tui/settings_test.go`): the menu row and the `c`
binding; one row per `SettingSpecs` entry, in order, with the section
header and the default markers; int within range persists and out of range
is refused with the message and nothing written; float persists and moves
the bar tick; bool toggles; choice cycles and wraps; string edits and the
empty string is refused; reset removes the key (section too, when empty)
and an unset key gets the notice; the single-flight gate; a failed write
opens the output modal; two-stage escape; the footer legends; no line wider
than the terminal at any width, and every key's detail read whole at 80
columns; and the Auto view's hint names Settings and stays within the width.
## A29. A credential holding only seat-wide keys is no login; MCP client secrets are seat-wide

A25 item 9 split `~/.claude/.credentials.json` into the account and a
seat-wide remainder, but kept two assumptions that do not hold: that a
non-blank credentials file is an OAuth login, and that `mcpOAuth` is the
whole remainder. The rules below override A25 item 9 where they differ.

**The bug.** An API-key account is active: its key is in Claude Code's
managed-key path (`primaryApiKey`, or the macOS `Claude Code` Keychain item)
and no OAuth login is stored. Someone signs in to an MCP server. Every write
Claude Code makes to its credential store is a whole-object
read-modify-write, so over an absent file the result is
`{"mcpOAuth": {...}}` alone. `ReadActive` tried the OAuth Keychain item and
the file before the managed key and returned any non-blank file, so it
returned that MCP-only text as the live login. A switch away then backed it
up into the API-key slot (`{}` once the seat-wide key was stripped; the key
survived only in `.prev`), and the switch back wrote the slot's `{}` as an
OAuth credential and cleared the managed key: the seat ended with no working
credential. `status` and `list` showed the API-key account as `no
credentials`, and `add` stored the MCP data as a new subscription account.
Activating a managed key also deleted the credentials file whole, so even a
correct switch from a subscription account onto an API key logged the MCP
servers out.

**What Claude Code does** (2.1.287). It takes a claude.ai login only from
`claudeAiOauth` with an `accessToken`; otherwise it uses the managed key.
Its Console (API-key) login replaces a claude.ai login by setting
`claudeAiOauth` to undefined and keeps every other key; its move from one
claude.ai login to another deletes `claudeAiOauth`, `organizationUuid`,
`trustedDeviceToken`, `enterpriseGateway` and `designOauth` and keeps
`mcpOAuth` and `mcpOAuthClientConfig`. `claude mcp add --client-id …
--client-secret` (and `add-json --client-secret`) writes the secret to
`mcpOAuthClientConfig[serverKey] = {clientSecret}`, keyed by the same server
key as `mcpOAuth`; the server's OAuth client reads it beside `mcpOAuth[key]`,
and a cross-app-access server whose secret is missing fails with `AS client
secret not found … Re-add with --client-secret`. Like the MCP logins, the
secret is the seat's: it was typed into this machine for this server, not
issued to the Claude account.

**1. The seat-wide keys are a list.** `ccfile.SeatWideKeys` is
`["mcpOAuth", "mcpOAuthClientConfig"]`, still an allow-list: any other key
travels with the account until it is known to be the seat's. The switch
splice (`ccfile.SpliceCredentials`) carries every listed key the live
credential holds over the stored blob and always drops the stored blob's own
copy, whether live has one to replace it or not, so a slot captured before a
key was known to be the seat's can no longer put back a stale login or
secret. Every capture stores `oauth.AccountOnly(blob)`, which strips every
listed key: `add`, `add --login`, the switch-time backup and stash, the
active refresh write-back, `export`, and now also `import` and the legacy
keyring relocation (`migrations.relocate`, which compares its read-back with
the stripped value). The ownership classifier's same-account compare ignores
every listed key.

**2. A credential holding only seat-wide keys is no login.**
`ccfile.SeatWideOnly` is true for a JSON object with nothing but listed keys,
`{}` included, and false for anything else (a managed key, malformed text,
`null`, a key not on the list). `ReadActive` treats such an OAuth Keychain
item or file as absent and goes on to the managed key behind it. Every
decision that asks whether there is a live login, and which, reads through
`ReadActive`: the switch-time backup and classifier, the self-switch
provenance check, the direct-activation stash and rollback snapshot, `add`
(which therefore refuses a live API key as before, or finds no credential),
`export` of the active account, `status`, `list`, the active-account usage
fetch and session bootstrap. The one reader that still returns a
seat-wide-only value is `readLiveOAuth`, which supplies the live remainder to
the splice: that value is no login, but it is exactly what a switch carries.

**3. A switch onto an API key keeps the seat-wide part.**
`writeManagedCredentials` stores the key, then clears the OAuth login by
writing the live credential's seat-wide part (`ccfile.SeatWidePart`) back
through the OAuth write path: the Keychain item and its shadow file while the
Keychain is in use, else the plaintext file. Being seat-wide only, it never
shadows the key. The credential is cleared whole, as before, only when there
is no seat-wide part (or the live text does not parse) or that write fails.
This holds for every path onto a key: a switch, `--force`, auto-switch and a
rollback to an API-key original.

**4. No managed key behind it.** A seat-wide-only file with no managed key
reads as no credential at all. A normal switch away then stops with the
existing `Current account credential is empty (Keychain unreadable?);
refusing to overwrite its backup` instead of writing `{}` into the outgoing
slot; `switch <n> --force` activates the target, and its splice still
carries the seat-wide keys over it.

**Not done here.** Claude Code 2.1.287 keeps more top-level keys in the same
store (`mcpXaaIdp`, `mcpXaaIdpConfig`, `pluginSecrets`, `gatewayTrust`,
`mcpDiscoveryCacheKey`, `coworkRemoteDevice`); they stay with the account
until each is shown to be the seat's. For the same reason a credential that
holds any key off the list but no `claudeAiOauth` (a `trustedDeviceToken`
left beside the MCP data, say) is still read as a login, although Claude Code
would use the managed key.

**Tests.** `internal/switching/apikeyseat_test.go`: from an API-key seat with
a file holding `mcpOAuth`, `mcpOAuthClientConfig` or both, a switch to a
subscription account and back keeps the key in its slot at every step, makes
it live again, keeps the seat-wide keys across both switches and needs no
`.prev`; without a key behind the file the switch away refuses and touches
nothing. `internal/credstore/seatwide_test.go`: the `ReadActive` table (each
seat-wide-only shape and `{}`, with and without a key, a login beside the MCP
data), the macOS Keychain seam with and without a shadow file, and the
write-back on a switch onto a key (file mode, Keychain plus shadow file, a
failed Keychain write landing in the file, no seat-wide part clearing the
file). `internal/ccfile`: the `SeatWideOnly`, `SeatWidePart` and splice
tables. `internal/reporting/apikeyseat_test.go`: `status` and `list` show
the API-key account as `api_key`. `internal/lifecycle/apikeyseat_test.go`:
`add` refuses the live key or finds no credential. Export, import, the
`AccountOnly` table and the relocation cover the second key.
