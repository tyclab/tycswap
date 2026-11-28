# tycswap — Idiomatic Go Architecture

A full-fidelity Go port of `claude-swap`, grounded in the behavioral specs in
`docs/port-spec/01–10` (10-audit.md corrections override the rest).

- Module: `github.com/tyclab/tycswap` · Go per `go.mod` · single binary `tycswap`
  (also installed as `claude-swap`).
- No cgo, except `internal/tray` on darwin (A12 as amended by A35). macOS
  Keychain via the `security` subprocess exactly as Python.
- TUI: Bubble Tea v2 + Lip Gloss v2 (charm.land). HTTP: stdlib `net/http`.
  `golang.org/x/sys` serves flock/`LockFileEx`, the Windows PID probe and
  console VT, and `golang.org/x/text/unicode/norm` NFC for `slugify_email` /
  keychain service hashing; the rest are `go.mod`'s direct requires.
- Out of scope: the rumps macOS menu bar (`menubar.py`, spec 09 §10). The
  update-check/upgrade subsystem is redesigned for Go (§6, §7 below). The
  menu-bar / tray app of A35 is a Go-side addition, not a port of it.

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
| `store` | The substrate: `sequence.json` model + read/write, path wiring, credential proxies (+`_post_backup_write`), identity resolution, org-field backfill, live-session guards, `FileLock` handle, `UsageStore` handle, migrations-at-construct. (01, 03 wiring) |
| `lifecycle` | Free funcs over `*store.Store`: add/add-token/remove/move/swap/alias/disable/enable/purge. (01§5–11) |
| `switching` | switch/switch-to/best/next-available, `performSwitch`, `classifyOutgoing`, replan, self-switch provenance. (02§4–9) |
| `reporting` | list/status renderers + JSON payloads, `collectUsageEntries` (paced, identity-guarded), snapshots, duplicate/lockstep warnings. (02§10–13) |
| `core` | `Switcher` façade: composes `store` + `lifecycle`/`switching`/`reporting`; satisfies the façade interfaces `autoswitch`/`tui`/`transfer` consume. |
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
`Chtimes` error). Staleness = `time.Now().Sub(fi.ModTime()) > 10s` (per lock since A55); acquire timeout
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
**Non-reentrant** — documented; callers never nest. Since A118 a goroutine
asking for a file it holds is refused at once, and code running under a held
lock takes the lock it lends (`Lend`).

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
func Switch(s *store.Store, strategy *string, jsonOut bool) (any, error)
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
Removed with `run` and `env` (A92); kept here as the record of what they did.

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
collision check). Rotating 1 MB × 3, by the file's own size: each record opens, appends and
closes the file, so processes that log at once (the tray app and a command) write one log
(A53). Console handler (stderr) only with `--debug`.
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
| SIGINT / Ctrl-C (Python: no handler, `KeyboardInterrupt` → 130) | `cli` installs `signal.Notify(sigint)`; on receipt prints the cancelled note and `os.Exit(130)`. For `tycswap start` POSIX the process is already replaced by `syscall.Exec` (no tycswap signal handler after exec); on Windows `start` waits for the child, mirrors its exit and owns console Ctrl-C while it runs (A90). The auto loop installs **only** SIGTERM→Stop (matching the single Python signal handler); its Ctrl-C path prints "Auto-switch stopped" → 130. |

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
   platform prints a message and exits 1 (macOS text preserved for parity where
   reasonable). Since A35 both messages name `tycswap app`, the tray application
   that took the menu bar's place; `menubar` is not an alias of it.
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
prompt-local "Cancelled" behavior. On the POSIX `tycswap start` path the handler
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

**Amended by A35:** cgo against Apple system frameworks (Cocoa) on darwin is
permitted for `internal/tray` alone, behind `//go:build darwin && cgo` with a
stub for every other build. No dependency is added: the Windows tray is
`golang.org/x/sys` syscalls and the Linux one a D-Bus client of its own. The
darwin release binaries are therefore built on a macOS runner, Linux and
Windows stay CGO-free cross-builds, and a darwin build with `CGO_ENABLED=0`
(a `go install` without the Command Line Tools) has no tray and serves the
dashboard alone.

**Amended:** the Charm stack moved to `charm.land/bubbletea/v2` (#97) and
`charm.land/lipgloss/v2` (#98); bubbles never entered `go.mod` and
`internal/deps` is deleted. The allowed deps are `go.mod`'s direct requires
plus their transitive modules: the two Charm modules;
`github.com/charmbracelet/colorprofile`, which both require and
`internal/tui/footer_test.go` names for its colour profiles;
`github.com/mattn/go-isatty`, terminal detection that also knows Cygwin and
MSYS ptys (`cli.isTTY`, `lifecycle.StdinIsTerminal`); `golang.org/x/mod`,
`golang.org/x/sys` and `golang.org/x/text`.

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

Removed by A92 (2026-11-16) with `run`, `map` and `unmap`.

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
`best`. Default stays `best` (A32 later makes `soonest-reset` the default);
an invalid persisted value still falls back to the default via the existing
`KindChoice` clamp — no new fallback path. It is a deliberate Go-side
extension with no Python counterpart: Python's auto-switch has no ranking
axis beyond most-headroom, so this adds a second one without touching the
Python-fidelity contract in `docs/port-spec/`.

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
marker's current meaning: a disabled Claude account remains visible for
inspection. Explicit and automatic switching both require an enabled account. CLI (non-TUI) output is
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
account (it cannot switch onto itself) and quarantined slots
(`autoswitch_state.json`, A18). API-key slots were a third reason, behind
`autoswitch.includeApiKeyAccounts`; since A33 `RotationEligible` excludes them
itself and the setting is gone. A `RotationEligible` account can therefore
still be off the engine's candidate list for either of those two further
reasons; the field tells a display "this slot is a legitimate
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
in another terminal and drop the new slot; a caller holding `Store.Lock()`
writes through that transaction's own view, which reuses the lock, while
every other store value in the process takes it again (A50; until then
in-process ownership was tracked per root). A `sequence.json` that no longer
parses is never overwritten by a mutation (`ErrCorruptRegistry`); listing reads treat it as
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
`autoswitch.includeApiKeyAccounts` existed because a Claude API-key account was
a rotation target that bills per token (A33 removed it: no API-key account is a
rotation target any more). A Codex API-key login reports no usage at all, so
there is nothing for a threshold to compare it against. Codex API-key accounts
are excluded from rotation unconditionally, and the setting was documented as
Claude-only rather than silently ignored.

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
`autoswitch.codexThreshold` (0–99.9, 0 inherits the effective Claude threshold;
since A34, the 7d bar) tune it; the hysteresis margin is the Claude
`autoswitch.hysteresisPct`, and `tycswap auto` gains no flag. A Codex event is
printed only when the tick switched or errored, or on every tick under
`--dry-run`, in the Claude events' format: the `HH:MM:SS` prefix and kind
colouring for humans, and
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

**Amended by A36:** a third build source, the release build, is no longer
taken for a checkout build, and `SelfUpgrade` follows one plan
(`update.UpgradePlan`) for every caller. Before, every release binary — and a
Nix or Windows one — was told "built from a checkout: git pull && make
install" by `upgrade`, the passive notice, the dashboard's Updates card and
the tray: the classification looked for the release build's `-ldflags` in
the build info, which Go leaves out whenever `-trimpath` is set
(`cmd/go/internal/load/pkg.go`; go.dev/issue/52372), and Go records no
`-buildvcs` either. The decision now reads only what a build records for
certain (`classifyBuildInfo(info, version.Version)`):

1. any `vcs.*` setting → a checkout build (since Go 1.24 its module version
   is a pseudo-version too) → "git pull && make install";
2. else a module version that is valid semver and not a pseudo-version → a
   module build (`go install …@<version>`; `internal/version` takes that
   version) → `go install …@latest` in a Go bin directory (`$GOBIN`,
   `$GOPATH/bin`, `$HOME/go/bin`; print-only on Windows), guidance elsewhere;
   a module build never downloads;
3. else no module version (`""` or `(devel)`) and a release tag linked into
   `internal/version` (`vX.Y.Z`, or a pre-release such as `vX.Y.Z-rc.1`; not
   the unlinked `v0.0.0-dev`, a pseudo-version, or a `git describe` shape
   `-N-gHASH` / `-dirty`) → a release build. Its path resolved
   (`filepath.EvalSymlinks(os.Executable())`): under `/nix/store/`, in a
   package tree (`/Cellar/`, `/scoop/apps/`, `/WindowsApps/`) or started
   through a symbolic link → the package manager's, and the hint names it
   (never the releases page); else, when this process can write the directory
   and the file and owns the file (on Windows: the file is not read-only) →
   the newest release downloaded over it; else the releases page;
4. else → a checkout build.

`go build`, `make build` and a `-buildvcs=false` build are checkout builds,
the release recipe a release build that downloads, `go install` a module
build, a Nix store binary the package manager's, and a Windows release
`.exe` downloads. Deciding writes nothing (stat and access checks only): the
one temporary file is the download's own. A test builds `./cmd/tycswap` with
`release.yml`'s flags and with a plain `go build`, installs a module build
from a module proxy in a directory, and classifies each binary's real build
info. Downloading has the downgrade guard `go install` lacks: nothing
happens unless the latest tag is semver-greater than the running version.

Superseded by A86 for the release lookup: the tag comes from the redirect of `<ReleasesURL>/latest`, and `Endpoint` is gone.


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
key; the ownership classifier, the provenance check and the active usage
guard compare account blocks whatever their encoding, so an MCP login never
reads as a changed credential. Rollback and the refresh write-back keep
`WriteActive` verbatim. A59 strips every backup read and write; A29 keeps a
stored copy out of the live file.

**Not done here.** Windows has no owner-only DACL on `credentials/`, exports
and the store root (`CheckPrivateRoot` is a no-op there); a follow-up.
Dependencies: `go.mod` pins the toolchain (its `toolchain` line); `make vuln` runs
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

Dashboard text, spacing and page breakpoints follow the browser’s default font size. Controls and tables size to their contents and the available space, with bounds in rem or ch. A card or header that changes layout with its own width uses a container query. Account addresses, quota limits and reset times, and session logins wrap in full. Short identifiers, slot badges and control labels keep whole words. A hyphenated name can wrap at its hyphen. Secondary text such as a session title may use an ellipsis when its full text is accessible. Borders and focus outlines keep their hairline widths. Guide diagrams scale their lettering and geometry together inside a font-relative frame.

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
top-level array beside its rows; the dashboard's Codex rows (A47) carry no
`tokenStatus`, and the state document never gains a top-level one.

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
retarget (until A93: the page sets neither, the threshold, wake and model
routes are gone, and only the in-process tray calls `ApplyModels`). Stop asks the engine to stop and waits up to 2 s for its
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

**Follow-ups, deliberately not in this extension** (the Codex rows, a Codex
façade and the Codex auto loop in a host the dashboard shares came with
A47): `add --login` and `codex login` as a cancellable job streamed over SSE;
`map`/`unmap` in the dashboard; a remote mode for a tray across a VM
boundary, which re-adds a bearer token accepted in place of the cookie and
CSRF pair and a route that mints a fresh one-time URL for such a client
(nothing in `tycswap web` calls either today, so neither is built); the tray
itself, which also
brings back the brand vars it needs (a reverse-DNS identifier, an
environment-variable prefix); and a short generic Guide tab: what the tool
does, slots and the active account, the 5h/7d/model windows and the single
threshold, getting started, manual switching, Auto, Sessions, a command
cheat-sheet and where the data lives.

*Superseded in part:* the tray, its brand vars (`brand.Identifier`,
`brand.EnvPrefix`), the bearer token and the launch route are built (A35,
A45); the Guide came with A27.

## A27. Dashboard parity: updates, a Settings tab, a Guide, folding cards, the live login and the auth overrides (Go-side additive extension)

A26 shipped the dashboard with three tabs and listed what it still lacked.
This amendment brings it to parity with the reference dashboard for every
feature that is generic: an Updates card with a header indicator, a Settings
tab for every `settings.json` key, a Guide tab, two cards that fold and are
remembered, a callout for a login Claude Code has that is not stored yet, and
a notice for the authentication overrides that make Claude Code ignore the
stored login. What is not generic stays out: no telemetry, no gateway, no
plugin marketplace, plugin updates or plugin cards, and no launch route (that
belongs to a tray, a separate stage; built since, A45). `internal/web` keeps A26's rule:
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
  again. Such a build carries no `-ldflags`, so `internal/version` reads its
  version from the build info (the module version, for a module-cache build
  with no VCS stamp); otherwise it would report `v0.0.0-dev` and be offered
  the release it already is.
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
  The apply is that installer's own command: `claude update` for a native
  or unknown install (and for the legacy `~/.claude/local`, whose own
  `node_modules` a global npm install would not touch), `npm install -g
  @anthropic-ai/claude-code@latest`, `brew upgrade --cask <cask>`, `winget
  upgrade`. A missing Claude Code is said (with the install line to type),
  never run. Versions compare by semver; a version that does
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
place of the duplicated card (A93 removes both and leaves `autoswitch.model`
out of the tab, and *Poll interval* while a scheduler owns the ticks). A test wires the real `settingsFacade` over a
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
| `autoswitch.model` | at once for the engine this page hosts: a save or reset through the settings routes calls `ApplyModels` on it while it runs (A26; an engine in the TUI or `tycswap auto` keeps its value until it next starts), and the at-limit marks re-read it for every state document; since A93 the tab does not list it |
| `autoswitch.sevenDayThreshold` (A34; `autoswitch.threshold` before it) | at the next engine start; the Auto tab's slider retargets the running engine's 7d bar for this run only, unsaved (the slider is gone since A93) |
| `autoswitch.codexEnabled`, `autoswitch.codexThreshold` | at the next start of a Codex engine: this page's, which runs beside the Claude one when the dashboard started with Codex accounts (A47), or `tycswap auto`'s; the TUI's engine never reads them |
| every other key (`intervalSeconds`, `cooldownSeconds`, `hysteresisPct`, `strategy`, `unhealthyTicks`, and any key added later) | at the next engine start, here, in the TUI's Auto view or in `tycswap auto` |

The TUI's Settings screen (A28) edits the same `settings.json` through the
same package (`settings.SetSetting` / `UnsetSetting` and the
`EffectiveSettings` list); neither surface knows about the other, and a value
saved in one shows in the other on its next read.

**Guide tab.** Static markup in `index.html`, templated with the brand name
only, written for someone with two or more accounts who opened the page:
what happens (a picture of the saved logins, the live login, the Fable store
and the sessions that use them), getting started, switching by hand (a
picture of what a running session does with it), Auto-switch (a flow of one
check), the settings that steer it, reading the meters, Fable logins,
Sessions, Updates, Codex accounts, "something looks wrong" as symptom, cause
and what to do, the handful of commands a dashboard user needs, and where
the data lives. Since 2026-11-18 (the operator: "Guide needs debloat and
correction use flows and diagramms to explain stuff. this is for humans
after all") the three pictures are inline SVG drawn with the page's colour
tokens, with real text and a title and description for screen readers; each
control is named as the page names it, and the reference stays in
`docs/reference.md` and the README. It names no URL: the page works offline and the static-asset test
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
`web.DetectAuthOverrides`). `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`,
`CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_BASE_URL` in the environment `tycswap
web` runs in — which a plain `claude` from the same shell inherits, while
`tycswap run` and `tycswap env` scrub the key and token ones — or
`apiKeyHelper`, `env.ANTHROPIC_API_KEY`, `env.ANTHROPIC_AUTH_TOKEN`,
`env.CLAUDE_CODE_OAUTH_TOKEN`, `env.ANTHROPIC_BASE_URL` in Claude Code's
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
`map`/`unmap`, the remote mode and the tray — the last two built since, A35
to A45), and no marketplace, plugin or telemetry surface: tycswap has none of
those.
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
On macOS, when that part lands in the plaintext file while the key is in the
`Claude Code` Keychain item (it is too large for the Keychain, or the Keychain
stopped answering), the key is written to `primaryApiKey` as well before the
file replaces the OAuth login, so a failure there leaves the login in place.
A switch rolls the credential back only after a write that succeeded, so the
switch's own write (`WriteActiveAccount`) undoes itself when it fails, as the
file backend, whose key and approval are one config write, leaves nothing
behind. It reads the managed Keychain item first, with the active read's
bounded retry; a read that keeps failing sends the key to `primaryApiKey`,
the fallback a failed Keychain store takes. When a `~/.claude.json` update
fails after the key reached the Keychain, the item is put back, the previous
key or no item, before the error returns. If the failing write is the file
fallback's, the first update has landed: from an OAuth login that leaves one
approval entry beside the intact login; from a key, a previous key that update
dropped from `primaryApiKey`, with no item behind it, goes into the item, which
is read first, so the same key stays live. A restore that fails is named in
the error: the new key is still in the Keychain. The rollback's write
(`WriteActive`) does not undo itself: the key it stores is the one being
restored, and it stays when that write's config update fails as well.
This holds for every path onto a key: a switch, `--force`, auto-switch and a
rollback to an API-key original. A direct activation (`--force`, a fresh
machine, a live login no slot holds) reads `~/.claude.json` once before the
credential write, to refuse a corrupt file, and again after it, as the normal
switch does, before it sets `oauthAccount`. It used to write the first copy
back, which dropped the `primaryApiKey` and the approval the key write had
just stored: outside the macOS Keychain the seat was left with no credential,
and a `--force` from a live key onto a subscription account left the old key
in the file (`internal/switching/directapikey_test.go`).

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
file). `internal/credstore/managedrollback_test.go` and
`internal/switching/keychainrollback_test.go`: a config update that fails
after the key reached the Keychain, from a login and from another key, on
the first config write and on the file fallback's, and through a switch and
a `--force` activation, leaves the Keychain item, the OAuth credential and
the live credential as they were; a previous key held in `primaryApiKey`
alone stays live; a restore that fails is reported; the item read is retried
once, and a read that keeps failing takes the file fallback; on the file
backend the same failure writes nothing. A rollback onto a key whose config
update fails as well, after a switch or a `--force` activation, leaves that
key in the item and live (`TestRollbackOntoAnAPIKeyKeepsItLive_macOS`).
`internal/ccfile`: the `SeatWideOnly`, `SeatWidePart` and splice
tables. `internal/reporting/apikeyseat_test.go`: `status` and `list` show
the API-key account as `api_key`. `internal/lifecycle/apikeyseat_test.go`:
`add` refuses the live key or finds no credential. Export, import, the
`AccountOnly` table and the relocation cover the second key.

---

## A30. A stored credential holding only seat-wide keys is no stored credential

A29 made a live credential holding nothing but seat-wide keys read as no
login, but left alone the slots the switch-time backup had already filled
with one before it. That backup took an API-key seat's seat-wide-only
credentials file for the live login, so the API-key slot holds `{}` (the file
with its seat-wide keys stripped), or the file itself from a build that did
not strip them, and the key survives only in `.prev`. Such a slot still
counted as switchable. A switch onto it (`switch <n>`, `--force`, the
rotation, the dashboard, the auto-switch engine, whose freshening passes an
API-key candidate without looking at its credential) took the stored text
for an OAuth credential, wrote it live with the seat's seat-wide keys
carried over it and cleared the managed key, and reported success over a
seat left with no working credential. `export` shipped the `{}` as the
account's credentials.

**The rule.** `store.ReadAccountCredentials` reads a stored credential for
which `ccfile.SeatWideOnly` holds as missing, the same rule `ReadActive`
applies to the live one. Every question whether a slot holds a credential is
asked there, so such a slot gets the existing answers for a slot without
one: `switch <n>`, with or without `--force`, stops with `Account-<n> has no
stored credentials` before the live login or the managed key is written;
`store.AccountIsSwitchable` is false, so the rotation, the fresh-machine
activation and the auto-switch engine skip it and the snapshot marks it not
switchable; `export` leaves it out, or fails when it is named; `tycswap run
<n>` refuses to bootstrap it. The bytes stay where they are:
`credstore.ReadBackup` is unchanged, so the `.prev` retention, the strict
clear and the migrations still see them, and the key in `.prev` is left
alone. A direct activation (`--force`, or a live identity no slot holds)
whose `ReadActive` finds no credential snapshots the raw live OAuth text for
its rollback instead (`ReadLiveOAuth`), so a commit that fails after the
credential write puts a seat-wide-only file back as it was rather than an
empty one (`TestForcedActivationRollbackRestoresASeatWideOnlyFile`).

**Tests.** `internal/switching/apikeyseat_test.go`: an API-key slot holding
`{}`, the MCP server logins or the MCP client secrets, with its key in
`.prev`, while a subscription login and an MCP server login are live. A
switch to it, with and without `--force`, fails with the no-credential error
and leaves the live credential, the live credentials file and config, both
slots and their `.prev` generations as they were; the slot is not
switchable, and the rotation switches nowhere.

## A31. An at-limit account's poll park ends before the trust in its measurement does

04§3.4 step 9 schedules the next poll of an account with no headroom at the
reset that frees it (`limiting_reset_ts`), however far away that is. 04§2.5
trusts a stale measurement only while `age_s <= TRUST_MAX_AGE_S` (3600 s). The
two disagree whenever the freeing reset is more than an hour out. The rule
below overrides 04§3.4 step 9.

**The bug.** With `autoswitch.model` set to `all` (or naming a model), an
inactive account whose per-model weekly window is at 100% was parked until
that window reset, up to a week away. One hour after its last fetch the
measurement passed the trust ceiling and `UsageEntry.DecisionValue` became
nil: `list --json` and `status --json` reported `usageStatus:
"unavailable"`, the dashboard showed *usage unavailable*, and the human
`list` printed the old numbers with an age note. None of them fetched again,
because an on-demand read refetches a stale entry only when its poll is due
or it has no plan; the engine's due-candidate pick skips a not-yet-due entry
too, so the engine could not rank the account either. It stayed that way
until the window reset, although its 5h window had reset long before. A 5h
or 7d window at 100% with its reset more than an hour out, and a Codex
account at its limit, went the same way for the rest of their park.

**1. The park is capped.** When the account has no headroom and the freeing
reset is later than the jittered interval, the next poll is
`min(reset, now + PARK_CAP_S)` (`usage.ParkCapS`), with
`PARK_CAP_S = TRUST_MAX_AGE_S - SERVE_TTL_S` = 3420 s. A reset nearer than
that is still the next poll, and the returned interval is unchanged. Each
re-poll plans again from the new measurement: an account still at its limit
is parked again, for at most another `PARK_CAP_S`; one whose window has
reset gets the normal cadence. The rule lives in `PlanAfterFetch`, so it
holds for every collector that plans (`list`, `status`, the dashboard, the
TUI, the engine, the Codex usage cache) and for an at-limit active account as
well; in the engine an active account at its limit is also refetched by the
escalation pass whenever there is a candidate.

**2. Why the margin is `SERVE_TTL_S`.** A stale measurement is trusted while
it is within the ceiling and its poll is not yet due (or a failure is on
record, or a fetch is in flight). A park that ends inside the ceiling keeps
the measurement trusted up to the moment its poll falls due, and from then
on whoever reads it next fetches it: `list`, `status` and the dashboard in
the same call, the engine at its next tick and before it decides. The margin
covers two things. The Claude collector plans after it has recorded the fetch
(lock, write, re-read), so the planner's `now` trails `fetchedAt`; the park
must end inside the ceiling measured from `fetchedAt`. And when the re-poll
fails, the stale-on-error trust lasts only up to the ceiling: at the default
engine interval of 60 s (±10%) the re-poll lands at most 66 s after it falls
due, and the retry after the base 30 s failure backoff at most 66 s after
that, 132 s in all, inside the 180 s. One failed re-poll therefore does not
make the account read as unavailable. A wider margin would only poll more
often.

**3. The capped park is jittered downward only.** A pass that fetches
several at-limit accounts at once (the engine's escalation pass, which
ignores poll plans, or a read that finds them all due or without a plan)
parked them all until the same instant, `PARK_CAP_S` later. The engine
fetches one due candidate per tick, stalest first, so the k-th of them
waited k − 1 ticks past its due time and read as untrusted for those ticks:
it had no decision value, for the engine or for a reader that only reads the
store (the TUI while its auto-switch view is open), and so no *at limit*
mark. The cap is now `PARK_CAP_S · (1 − JITTER_FRAC · r)`, with r the draw
that jitters the interval (04§3.4 step 8), so such accounts fall due anywhere
from 3078 s to 3420 s after the fetch. The mean park falls from 3420 s to
3249 s, about 5% more polls per parked account. The jitter is downward only
because `PARK_CAP_S` is already the longest park the trust ceiling allows
with the margin of item 2; the interval's ±10% would carry a park to 3762 s,
past the ceiling. A reset nearer than the jittered cap is still the next
poll, exactly: the reset itself is not jittered.

The draw is new at every plan, so two accounts can still fall due between
the same two ticks, and the second then waits one tick: for a pair fetched
together, about one tick length in 342 s of the time (about 4% at 15 s
ticks, about 15–17% at the default 60 s). Without the jitter every pass that
fetched them together cost the k − 1 tick chain, and afterwards a pair met
again only through tick rounding. In a steady-state simulation of a pair
with a fetch-all pass every N parks, the jitter means fewer waits at 15 s
ticks for every N up to 128, and at 60 s ticks while such passes come at
least every 40 to 50 parks (roughly two days); past that it costs slightly
more.

**Not done here.** Trust extension ends when the poll falls due, so a reader
that does not fetch (a store-only redraw) sees the entry as untrusted between
that moment and the fetch, as it does at every other poll; no margin changes
that. The engine still fetches one due candidate per tick, stalest first;
item 3 only spreads the due times. When the active account nears the
threshold the escalation pass fetches them all.

**Tests.** `internal/usage/pollpolicy_test.go`
(`TestPlanAfterFetchAtLimitPark`): an inactive account under `all` with a
per-model window at 100% resetting in five days is due at
`now + PARK_CAP_S · (1 − JITTER_FRAC · r)`: `now + PARK_CAP_S` at r = 0, the
midpoint at r = 0.5 and `now + PARK_CAP_S · (1 − JITTER_FRAC)` at r = 1,
always within `[now + interval, now + PARK_CAP_S]`; so are a 5h window at
100% resetting in two hours (the old worked value, which parked at the
reset) and an at-limit active account; a reset ten minutes out is still the
next poll, exactly, at every r; with the model window not counted the
cadence is unchanged. `TestAtLimitParksFetchedTogetherFallDueApart`: three
at-limit accounts fetched in one pass over the real store, each planned with
its own draw, fall due at different times; fetched after that the engine's
way, the one candidate `DueCandidate` picks every 15 s for three hours, each
reads as decision-grade after every tick. Each account keeps one fixed draw
at every park, so the replay shows the spread and that every park ends
inside the trust ceiling, not that a pair never meets on a tick: in
production every plan draws afresh. Without the jitter all three fall due
together, and after that tick two of them read as untrusted. The engine
harness cannot give each account its own draw (the Claude collector plans
with the default random source; the engine's `WithRNG` paces only its loop),
so this replay lives in `internal/usage`. `internal/core/usagepark_test.go`
(`TestEngineKeepsAnAtLimitCandidateTrusted`): the engine over the real store,
collector and planner, with a fake clock and a stub usage endpoint, ticks
every 15 s for three hours beside one such candidate. After every tick the
store reads the candidate's measurement as decision-grade, and the stub
served it at least once per trust ceiling and at most once per park.

## A32. `autoswitch.strategy` defaults to `soonest-reset`

A17 added `soonest-reset` beside `best` and kept `best` as the default.
`best` tries the qualifying account with the most headroom first. Across a
working week that piles the load onto whichever account happens to have room
and lets an account whose weekly window is about to renew sit idle, so the
quota it would have spent before the renewal is lost. Ordering by the earliest
weekly renewal spends quota where it comes back soonest and spreads the use
over the accounts. The default in `settings.Default()` and in the
`autoswitch.strategy` spec is therefore `soonest-reset`; `best` stays
selectable. Qualification is unchanged, and `soonest-reset` still never
prefers an account at or over the threshold for its early renewal (A17's two
tiers).

**What an existing settings file means now.** The default applies wherever
the key does not decide:

- a `settings.json` without `autoswitch.strategy` (including no file, a store
  copied by `tycswap migrate` from claude-swap, whose default was `best`, and
  the Python-produced fixture under `testdata/python-fixtures/`) now orders by
  earliest renewal;
- a value outside the two choices falls back to `soonest-reset` through the
  existing `KindChoice` clamp;
- a file that sets `best` keeps `best`. Loading never rewrites the file, and
  `tycswap config set` writes only the key it is given, so the key is present
  only where someone chose it. `tycswap config set autoswitch.strategy best`
  is the way back to the old order.

**Surfaces.** `tycswap config` lists `soonest-reset  (default)`; the TUI's
Settings screen and the dashboard's Settings tab show the new default from the
same spec, and the TUI's enum control cycles from the effective value. The
auto-switch screen's summary names the strategy whenever it is not `best`, so
it now reads `· soonest-reset` on an untouched install. On the dashboard, the
Next best ranking and the Auto tab's Strategy tile both fall back to
`soonest-reset` when the state document carries no strategy (the tile shows
the running engine's strategy, else the saved one, else this fallback), and
the Guide describes it as the default.

**Tests.** `internal/settings` (`TestStrategyDefaultIsSoonestReset`): the
default and the spec agree; a file without the key loads `soonest-reset` and
reports it as not set; a file that sets `best` loads `best` and reports it as
set; neither file is rewritten by the reads; an unknown value falls back to
`soonest-reset` (`TestLoad_ClampTable`). The port spec's `Go:` notes (05§2,
08§8.2, 08§8.3) record the new default and the clamp case. The TUI tests that
pin `best` order set the strategy explicitly instead of relying on the
default, and the summary test checks the segment on a default install.

## A33. A switch onto an API-key account needs the user's approval

tycswap has two kinds of switch, and only one of them is cheap. Switching
between subscription accounts rewrites the OAuth credential, and a running
Claude Code session picks that up on its own (the post-switch note says when).
Switching onto an API-key account changes **how** Claude Code authenticates:
the OAuth login leaves the credential store, the managed key goes into the
Keychain item or `primaryApiKey` with its approval, and usage is billed per
token. A Claude Code session that is already running keeps the login it
started with until it is restarted. The switch in the other direction is the
same change in reverse.

`autoswitch.includeApiKeyAccounts` (the claude-swap setting, default off) let
the engine rotate onto API-key accounts, as ordinary targets when one
qualified and as the last resort (unmeasurable headroom) when no subscription
account did, and let it leave an active API-key account the same way. Such a
switch would leave every open session on the old login while the log, the
TUI and the dashboard reported the new one. **Removed**: the setting, the
`auto --include-api-key-accounts` / `--no-include-api-key-accounts` flags, and
the last-resort path.

**The engine.** It rotates between subscription accounts only. An API-key
account is never a candidate, not even when every subscription account is at
its limit: the tick reports `all-exhausted` (or `no-candidates`) instead. An
API-key account that is already active is left alone with the `no-switch`
reason `active-api-key`, as before when the setting was off.

**Automatic selection.** `store.RotationEligible`, the one owner of the
automatic-selection rule (A18, A19), also excludes API-key accounts, and so
does the snapshot's inline copy of it (`reporting.rotationEligible`): the
engine's candidates, the TUI's and the dashboard's Next best and the
"eligible for rotation" figure agree. `AccountIsSwitchable` is unchanged, so an
API-key account stays a valid target for a switch by hand. The bare `switch`
rotation and `switch --strategy` skip it and say why: `Skipping Account-<n>
(API key — switch to it by hand if you mean to)`, or the `--json` warning
`Skipped Account-<n> (API key: switching to it changes how Claude Code
authenticates)`.

**The guard sits at the chokepoint.** `switching.SwitchTo`, which every
front-end funnels through, refuses an API-key target unless an approval was
recorded for exactly that slot (`switching.ApproveAPIKeySwitch`): single-use
and per slot. Any approval for the resolved slot is used up as soon as the
identifier resolves (so naming the account by email or alias is no way around
it), whatever the slot's kind and whether or not a switch follows: an approval
given for a subscription slot cannot survive to let a later switch onto an API
key put into that slot through unasked. The kind is checked after the
already-active short-circuit, which writes nothing, so a switch to the API-key
account already in use reports `Already on` without an approval; every other
switch onto an API key, `--force` included, is checked before anything is
written. The refusal is a `ValidationError` that names the restart and the way
to confirm: `Account-<n> authenticates with an API key. Switching to it
changes how Claude Code authenticates, and every Claude Code session that is
already running keeps its current login until you restart it. Confirm the
switch to go ahead: `tycswap switch <n>` asks first.` Refusing is therefore
the default; the command line and the dashboard's *Switch* opt in after
asking. After a switch onto an API-key account the follow-up note says the
running sessions need a restart (`switching.APIKeyRestartNote`) instead of "no
restart needed", and on a machine with no live login the bare `switch` skips a
preferred API-key slot with the API-key reason, not as missing credentials.

- **CLI.** `switch <num|email|alias>` onto an API-key account prints what
  changes and how many Claude Code sessions are running (the count comes from
  `procdetect` under the default config directory, the login a switch
  rewrites; a detection failure counts as none, since the notice is advisory),
  then asks `Switch to API-key account #<n>? [y/N]`. Only `y` approves.
  `--yes` / `-y` answers for scripts and still prints the notice; it is valid
  with `switch <num|email>` only (a usage error elsewhere, like every other
  flag outside its command). Stdin that is not a terminal is refused at once,
  without reading, so a pipe that never writes cannot leave the command
  waiting: `Not a terminal — rerun with --yes to confirm the switch to API-key
  account #<n>.` Any other answer, end of input included, is `Cancelled: not
  switched to API-key account #<n>.` Every refusal is a `ValidationError` and
  exits 1, as the switch layer's own refusal does. `--json` never prompts,
  since a machine-readable run has no one to ask: `--json --yes` approves,
  because `--yes` is the documented answer for scripts and approves everywhere
  else, and `--json` alone is refused by the switch layer (the JSON error
  envelope, exit 1). The API-key account already in use is not asked about
  unless `--force`.
- **TUI.** It does not ask. A switch onto an API-key row goes to the switch
  layer like any other and is refused there; the failure shows the refusal,
  which points at `tycswap switch <n>`.
- **Dashboard.** *Switch* on an API-key row asks first, then sends `POST
  /api/switch/{key}?confirmAuthChange=1`. The handler turns the flag into an
  approval through the `AccountOps` facade
  (`core.Switcher.ApproveAPIKeySwitch` resolves the key's identifier to its
  slot and records nothing for an unknown one), so the web package still
  touches nothing but its facades; without account operations the confirmed
  switch answers `503` instead of being attempted. *Force switch* does not ask
  about the auth mode and is refused for an API-key account, as the TUI is.
  The red notice shown while an API-key account is active says auto-switch
  leaves it alone. The Guide says the same.

**The settings file.** A `settings.json` that still carries
`autoswitch.includeApiKeyAccounts` loads exactly as one without it, whatever
its value: `Load` reads only the keys in `SettingSpecs` and ignores the rest,
and every write keeps a key it does not know. `config set`, `get` and `unset`
refuse it as an unknown setting and list the valid keys; the TUI's Settings
screen and the dashboard's Settings tab no longer show it.

**Codex.** Unchanged: a Codex API-key login was never a rotation target, and a
Codex switch already leaves running codex sessions on their old login (the
switch names their PIDs), so no approval is added there. The help text that
called the Claude setting Claude-only now says both sides exclude API keys.

**Tests.** `internal/switching` (`authmode_test.go`): an approval is
single-use and per slot; the refusal names the account, the API key, the
restart and `tycswap switch <n>`; a switch onto the API-key slot without an
approval is refused with nothing written, with and without `--force`, and an
approval for another slot does not help; with one it switches, and the next
switch onto the slot is refused again; naming the account by email is guarded
the same way; an approval is used up by the switch it was given for, onto a
subscription slot or the active one, so a later API key in that slot is still
refused; the active API-key account is `already-active` without an approval,
and `--force` onto it still needs one; the follow-up after a switch onto an
API key says to restart; the rotation skips the API-key slot with the warning,
or finds no valid target when it is the only other account; and the
fresh-machine path names the API-key reason. The existing API-key seat tests
and the Keychain rollback tests record an approval first, and the rollback
tests fail if the approval refusal is what stopped the switch.
`internal/store`, `internal/reporting`: an API-key slot is switchable but not
rotation eligible. `internal/autoswitch` (`apikey_test.go`): an API-key
account is never a target, at the limit or proactively, and an active one is
left alone with `active-api-key`. `internal/settings`: a file carrying the
removed key loads as without it, a whole-file save keeps it, and `config
set`/`unset` refuse it. `internal/cli` (`authmode_test.go`): stdin that is not
a terminal is refused without being read and the error names `--yes`; end of
input and any answer but `y` cancel with an error; `--yes` skips the prompt,
terminal or not, but prints the notice; the notice counts the sessions;
`--yes` and `-y` parse after the verb and are a usage error elsewhere; the
prompt records the approval only on yes, `--json` alone does not and `--json
--yes` does; a subscription target and the active API-key account are not
asked about; end to end, `switch 3` without a terminal and `switch 3 --json`
exit 1, and `switch 3 --json --yes` switches. The `auto` flags are
unrecognized arguments. `internal/tui`: a switch onto an API-key row is
refused with the switch layer's message. `internal/web`: `confirmAuthChange=1`
records the approval before the plain or forced switch, nothing is approved
without it, and without account operations it is `503`.

## A34. One threshold per window

Every window an account reports was collapsed into one number, `100 −
max(pct)`, and judged against one bar, `autoswitch.threshold`. The windows
measure different things, and fill at different rates:

- The rolling **5h window is a rate limit, and it bursts.** A fan-out of
  parallel subagents can take it from 85 % to full inside one poll interval.
  Reaching 100 % costs a wait, but one that lands in the middle of whatever
  was running, with no chance to react: the next poll comes after the damage.
- The **7d window is the account's budget, and it creeps.** It moves at the
  pace of a week's work, so there is always another poll between "nearly
  full" and "full". An account that reaches 100 % is out for days.
- A **per-model weekly window** is a budget too, but a narrower one: at 100 %
  that one model is gone for the week while the account still serves
  everything else. It creeps like the week, and it counts only while
  `autoswitch.model` names it (since A97, which removed the setting, while
  the scheduled tick's live sessions run that model).

One bar could not serve both kinds. Set for the week it left no margin on the
5h window, so a burst overshot it; set for the 5h window it gave up the week
early, wasting budget nothing else would have used.

**Three bars, each named after its window and set by how fast it fills:**

| setting | window | default | why |
|---|---|---|---|
| `autoswitch.fiveHourThreshold` | 5h | **85** | it bursts; leave a margin the next poll can act inside |
| `autoswitch.sevenDayThreshold` | 7d | **97** | it creeps; squeeze it, nothing is wasted by waiting |
| `autoswitch.modelThreshold` | per-model week | **95** | weekly too, so also high; only while counted |

The margin is the point of the 5h bar: 15 points is roughly what a heavy
burst can consume between two 60-second polls. Someone working in small,
predictable steps can raise it; someone running wide fan-outs should lower it
or poll more often. All three clamp to 50–100, where **100 means "never move
proactively on this window"**; only an actual at-limit does.

**The settings file.** `autoswitch.threshold` is gone from the spec. A
`settings.json` that still carries it seeds the 7d bar with its value in
`settings.Load` (`settings.LegacyThresholdKey`), so an upgrade keeps the limit
its owner chose instead of silently reverting to the default; the new key wins
wherever both are present, and the legacy range (50–99.9) lies inside the new
one. `EffectiveSettings` reports the 7d bar as set when only the legacy key
is, and `UnsetSetting` of `autoswitch.sevenDayThreshold` removes the legacy
key too: otherwise a reset from `tycswap config unset`, the TUI's Settings
screen or the dashboard would report nothing to do and leave the old value in
force. Writes keep the legacy key as they keep any key they do not know. The
`auto --threshold` flag is replaced by `--five-hour-threshold`,
`--seven-day-threshold` and `--model-threshold` (`settings.CLIOverrides`).

**The split.** `usage.AccountHeadroomByClass` (`internal/usage/classes.go`)
carries the three axes apart (`usage.Headroom`: `Session`, `Week`, `Model`,
each nil when unknown; `Model` nil while no model window counts), with
`Weekly()` the tighter of the week and a counted model window, `Binding()` the
smallest of all three (the single figure `accountHeadroom` still returns for
the at-limit verdict, the poll planner and the gauges) and `WeeklyExhausted()`.
`usage.ClassOf` maps a relevant-window label to its class;
`oauth.ClassPcts` is the display side, and the TUI and the dashboard build on
it.

**The axis that decides.** A tick picks the costliest window that has reached
its own bar: the week, then a counted model week, then the 5h window, because
losing the week costs days across every model, losing a model's week costs
days for that model, and losing the 5h window costs a wait. (Cost orders the
axes; fill rate sets their bars. The two are separate questions and their
answers point opposite ways.) Everything downstream speaks about that axis
alone: the proactive gate judges a candidate's landing on it, the hysteresis
margin is measured on it (a candidate that reports no window of that class is
judged on its weekly figure instead of being ruled out for an unknown; on a 5h
move one without a 5h window needs only its own bars, A96), the
`switch` event carries it as `axis` (`5h` | `7d` | `model`; empty under
`failover`, where no usage was readable) and in its human line
(`(proactive, 5h)`), and `below-threshold` names the window closest to its own
bar (`7d 50% < 97%`). The poll line carries the 7d bar and says so
(`(7d bar 97%)`), so it does not read as a contradiction beside a 5h window
that has its own bar; the poll event's JSON `threshold` is that bar.

**Two rules hold whatever the trigger:** a candidate whose weekly budget (the
week, or a counted model window) is spent is never a target, even under
`at-limit` or `failover`; and `strategy = best` ranks by weekly headroom, not
by the binding figure, which sent work to whichever account happened to be
resting its 5h window (an account with no weekly window falls back to the
binding figure). `soonest-reset`'s first tier is now "under every one of its
bars".

**Poll cadence** keys on the lowest bar in force, since that is the window
closest to making the engine act; the model bar joins the minimum only while
`autoswitch.model` counts something. The engine pins it (`pollThreshold`, in
`NewEngine`, `ApplyThreshold` and `ApplyModels`), and without an engine the
collectors read it from the file (`reporting.resolvePollInputs`). That makes
an 85 % bar workable: escalation starts `EscalationMarginPct` (15) below the
lowest bar, so from 70 % of the 5h window the active account is fetched more
often than the poll interval. `PlanAfterFetch` itself is unchanged.

**One bar stays adjustable at runtime.** The TUI's `t` adjustment and the
dashboard's slider (removed by A93 with its route) move the 7d bar, the account's whole budget, and nothing
else: `Engine.ApplyThreshold` and `POST /api/auto/threshold` are that bar,
bounded by its spec (50–100), and the dashboard's bar tick and the TUI's
usage-bar tick follow it. The other two bars are settings.

**The surfaces follow the same split**, or they would contradict the engine:

- **TUI.** The auto-switch screen's summary names every bar in force (`switch
  at 5h 85% · 7d 97%`, `· model 95%` while model windows count) and marks the
  7d bar `(session)` once adjusted; the session line reads `— 7d threshold set
  to <n>% for this session —`. The Next best panel marks a row at threshold
  (its tier) when any window has reached its own bar, and ranks `best` and
  the within-tier order by the weekly figure. The Settings screen lists the
  three keys; their help texts fit its detail pane at 80 columns.
- **Dashboard.** The Next best ranking judges each window against its own bar
  (the *at threshold* chip) and ranks by the weekly figure; its subtitle names
  the bars (`switch at 5h 85% · 7d 97%`); the Auto tab's tile and slider are
  the *7d threshold* (A93: a *Weekly threshold* tile, no slider); the Settings tab lists the three keys; the Guide
  explains the three bars and why they differ.
- **CLI.** `tycswap auto`'s startup line names all three bars (`Auto-switch
  running: 5h 85% · 7d 97% · model 95%, every 60s`), its help says each
  window has its own bar, and `tycswap config` lists the three keys.

**Codex.** The Codex engine keeps its single-threshold decision unchanged: the
worse of an account's 5h and weekly windows against one bar, and a candidate
at or over that bar is never a target. Only the bar's source changes, since
`autoswitch.threshold` is gone: `autoswitch.codexThreshold` when it is set
(not 0), else the effective Claude 7d bar (`cli.codexThreshold`). The 7d bar
is the one a pre-A34 `autoswitch.threshold` seeds, so a migrated settings file
keeps its Codex behaviour; on a fresh install the Codex bar moves from 90 to
97 with the 7d default. A bar of 100, reachable through the 7d bar, means the
Codex engine moves only off an account at its limit; the engine applies no
clamp of its own, and the 0–99.9 range of `codexThreshold` itself is
unchanged. Judging the Codex windows per class, as the Claude engine now does,
would be a separate change.

**Tests.** `internal/autoswitch` (`classes_test.go`): a 5h window under its
own bar does not switch and the line names it; the 7d window over its bar
switches; the 5h window over its own bar switches; the model bar applies only
while counted, with `axis` `model`; a 5h-driven move does not land on a nearly
spent week; `best` ranks by the weekly axis, with a fixture where the weekly
and the fullest-window orders disagree; the hysteresis margin is measured on
the deciding window, with a candidate that beats the active account there but
not on its fullest window; the costliest hot window names the move; the axis
reaches the event's JSON and human line, and is empty under `failover`; the
axes split as documented (`TestHeadroomByClassSplitsTheAxes`); a model-driven
move accepts a candidate without that window; poll cadence keys on the lowest
bar in force and `ApplyThreshold` moves the 7d bar only. The existing engine
tests drive the week instead of the 5h window where they meant the old single
bar. `internal/settings`: the three defaults and ranges, the per-bar CLI
overrides and their clamping, the legacy key seeding the 7d bar (and losing to
the new key), `EffectiveSettings` reporting it as set, and unsetting the 7d
bar dropping the legacy key. `internal/oauth`: `ClassPcts`.
`internal/reporting`: `resolvePollInputs` returns the lowest bar in force.
`internal/codex/autoswitch`: a candidate at its 5h limit is never a target,
with the 7d bar or a `codexThreshold` as the bar, and a bar of 100 moves only
off an account at its limit. `internal/cli`: the Codex bar is
`codexThreshold`, else the 7d bar; the three `auto` flags parse and refuse a
non-number, `--threshold` is unrecognized, the help names them, `config` and
the web facades show the 7d bar seeded from the fixture's legacy key, and the
slider's bounds are 50–100. `internal/tui`: the panel judges each window
against its own bar and ranks by weekly room, the summary names every bar and
the session mark, the `t` adjustment moves the 7d bar, and the Settings screen
edits the new keys. `internal/web`: the slider route's bounds and the settings
notes. The dashboard's ranking script has no committed test (the node harness
in `internal/web/testdata` covers tab routing only).

## A35. `tycswap app`: the menu-bar / tray application (Go-side additive extension)

A26 and A27 named the tray and its remote mode as follow-ups. A35 to A45 add
them, ported from the reference's tray application with tycswap's names, its
own icon and the corporate parts left out (the last paragraph says which). The
port-spec is untouched: this is a Go-only addition, like the dashboard.

`tycswap app` is the dashboard as a resident application that starts
minimized. `internal/tray` is a small platform abstraction (title, tooltip, a
menu, notifications, an optional yes/no dialog, Run, Quit) with three
implementations and no third-party code:

- **macOS** — `NSStatusItem` and `NSMenu` in Objective-C compiled by cgo
  against Cocoa only (`tray_darwin.m`; its classes carry the `TS` prefix and
  its export is `tycTrayClicked`). The main goroutine is
  locked to the main thread and `[NSApp run]` owns it; the process is an
  accessory app (no Dock icon). Notifications and the dialog go through
  `osascript`, because a bare binary has no bundle identifier. The switches,
  the active-account dot and the update rows take `brand.AccentColor`
  (`tray_set_accent`). A darwin build without cgo compiles to a stub that
  reports `ErrUnsupported`.
- **Windows** — `Shell_NotifyIconW`, a hidden top-level window (A52) of the
  class `<Name>Tray` (`TycswapTray`) and a `GetMessage` loop through
  `golang.org/x/sys/windows`, on one locked OS thread; the icon comes from a
  PNG via `CreateIconFromResourceEx`, notifications are `NIF_INFO` balloons,
  the dialog is `MessageBoxW`.
- **Linux** — the StatusNotifierItem and `com.canonical.dbusmenu` interfaces
  over a purpose-built D-Bus client (`dbus_linux.go`: EXTERNAL auth,
  little-endian marshalling, calls, returns and signals) plus
  `org.freedesktop.Notifications`; `zenity` or `kdialog` for the dialog.
  Without a session bus or a `StatusNotifierWatcher` (WSL, a console session)
  the tray is `ErrUnsupported`.

**The icon** (`internal/appicon`) is tycswap's octopus, not the logo's full
scene: a rounded square in a darker shade of the accent, the octopus — head
and four arms — in the accent itself with a lighter shade as the shine on its
head, white eyes with dark pupils. Every colour is derived from
`brand.AccentColor` (`PaletteFor`). The head is path data flattened and
scan-converted with 4×4 supersampling, the arms are stroked centre lines;
the drawing framework (paint, winding, flattening, compositing, the rounded
square, ARGB32 and PNG output) is the reference's, and so is drawing the
icons when the tray starts (the 128 px mark, which only macOS shows, on macOS
alone); the badged icons are drawn once per count and kept.

**The shell** (`internal/cli/appshell.go`) is the platform-independent
behaviour, tested with a fake tray: title `#<slot> · <fullest counted
window %>` (`⟳` in front while auto-switch runs), a tooltip with every
window (with a Fable login, `Default #N · Fable #M` instead, A104), the menu of A37, notifications on engine `switch` and
`account-quarantined` events, and once when a window of the active account
reaches its own bar (A34), re-armed when every window is ten points below
its bar. It only ever renders `web.State` and acts through `shellActions`,
which is what makes the remote tray of A45 a second set of hooks. So the
Codex accounts and the Codex engine reach the tray through that state, with
the dashboard (A47). `web.Server`
gained `LaunchURL` (the unused start URL, else a fresh single-use token per
"Open dashboard"), `Snapshot`, `OnState`, `OnAuto` and `AddCurrentLogin`;
in-process observers receive only state documents the hub published, so a
slow build never repaints the tray with an older state.

**Start at login** (`internal/autostart`): a LaunchAgent on macOS, an XDG
autostart `.desktop` entry on Linux, a HKCU `Run` value on Windows, each named
`brand.Identifier` (`io.github.tyclab.tycswap`); `app --autostart
on|off|status` and the menu's *Start at login* drive it. `internal/brand`
gained `Identifier` and `EnvPrefix` (`TYCSWAP`, for `TYCSWAP_REMOTE_TOKEN_FILE`
and the restart's `TYCSWAP_JUST_INSTALLED`), validated like the other vars.

**Without a tray** (`--headless`, or `ErrUnsupported`) the app is `web
--no-open` with the A38 lock, the A43 resume and the A45 token. Its Updates
card is A27's host, as `tycswap web` has it; with a tray the card reads the
shell (A44). The reference's headless app has no card at all: its `web` has
none either, and tycswap's has.

**Releases.** A `v*` tag builds the binaries the README lists (`release.yml`):
Linux amd64/arm64 and Windows amd64 with `CGO_ENABLED=0`, darwin arm64 and
amd64 on a macOS runner with cgo for the tray, each `-trimpath
-buildvcs=false -ldflags "-s -w -X …/internal/version.Version=<tag>"` and
named `tycswap_<tag>_<os>_<arch>[.exe]`, with `SHA256SUMS`, attached to that
tag's GitHub release (a draft when the tag has none yet). Nothing is
code-signed.

**CI on Windows and macOS** (`ci.yml`) vets and builds everything there —
on macOS with cgo, so the Objective-C is compiled. Windows runs the whole
suite, the build-tagged tests included: every test keeps its home in a
temporary directory (`testutil.IsolateHome` sets `USERPROFILE`, which
`os.UserHomeDir` reads there, beside `HOME`). A test that needs POSIX mode
bits skips there, as chmod sets none: the mode checks, and the tests that
make a file or directory unreadable or read-only with chmod. macOS runs the
whole suite too. In a test binary (`testing.Testing()`) the real Keychain
client and `launchctl` refuse to run and ask for a fake: the store helpers
inject a `keychain.Fake`, and the `internal/cli` tests that need Keychain
state across commands get one per home. The tests that failed on macOS for
assuming the Linux layout ask the platform instead (`paths.GetBackupRoot`,
the store's reads), and the `switching` tests, written on the file backend,
pin it: the credential store picks the Keychain on macOS.

**Corporate, not ported.** The reference's tray also carries its owner's
branding item, the fetch of mandatory Claude Code settings, the plugin
marketplace, plugin update and recommended-plugin items with their rows,
cards, checks and sign-in, the gateway login and its notices, and a
telemetry reporter. None of it is generic, and none of it is here — no hook,
no stub, no menu id.

## A36. The app checks for updates and asks before installing

`tycswap app` runs `offerUpdate` 90 s after start and every 6 h
(`--no-update-check` disables it): it asks the release endpoint `tycswap
upgrade` uses (`update.Checker.Latest`, which also refreshes the passive
notice's cache), compares the tag with `version.Version` by semver and, when
newer, asks **once per version** through `tray.Asker` with *Install* /
*Later*. Approval runs `tycswap upgrade`'s own path (`go install` for a
go-installed binary, A24) and notifies the outcome; a decline, or a platform
without a dialog, leaves a notification and an *Install tycswap X…* row.
Nothing is installed without the user's yes.

**The install is `tycswap upgrade`'s own** (A24 as amended here, with its
decision table), which now also downloads over the binary, as the reference
does for every build that was not go-installed: for a release build this
process can replace, outside a package manager's tree, it reads the latest
tag from the release endpoint, downloads that release's `SHA256SUMS`
and `tycswap_<tag>_<goos>_<goarch>[.exe]` from `<ReleasesURL>/download/<tag>/`
over https only (redirects included, at most 256 MiB) into a temporary file
in the binary's directory, refuses the build unless `SHA256SUMS` has exactly
one line for that file and its sha256 matches, marks it 0755 and renames it
over the binary (`replaceBinary`). On Windows the running `.exe` is renamed to
`.exe.old` first and put back if the second rename fails; the app (local and
remote) removes the `.old` file at its next start (`RemoveStaleBinary`). A
build the tray cannot upgrade — a checkout build, a go-installed binary on
Windows (where `go install` cannot replace the running `.exe`) or outside a
Go bin directory, a package manager's binary, a release build it cannot
replace — is told how (the checkout's command, the `go install` line, the
package manager, the releases page: `AppUpdateView.Hint`) in a
notification instead of a dialog, and the row reads *tycswap X is
available…*; a release binary is never told `go install`. The app decides
the plan once, at its start, from stat and access checks (no file is written
to learn it), and the card carries the hint only while an update waits.

**Restart.** An install that replaced the running binary (its modification
time changed) restarts the app by itself: the shell's `Restart` sets a flag
and quits the tray; once the server is down, `restartSelf` starts the binary
as a new detached process (own session) with the same arguments and
environment plus `TYCSWAP_JUST_INSTALLED=<tag>`, and the process exits. Not
`execve` in place: after an exec the Cocoa status item never comes back,
because LaunchServices keeps the PID bound to the old image. The child never
offers the tag it was just updated to. Before `restartSelf` the app releases
its single-instance lock (A38) and removes its remote token (A45), so the
successor finds both free; the deferred release then skips the lock it handed
over — the reference had a nil-pointer panic there at the end of every update
restart, and the test that takes the hand-over with `restartSelf` stubbed
guards against it, for the local app and the remote tray alike. A `go install`
that put the new build into another Go bin directory says so instead of
restarting the old one.

## A37. Tray dropdown components, a menu that stays open, many accounts

`tray.Item` has a `Kind`: `KindGauge` (title, second line, 0–100 % bar,
active marker; the whole row clicks), `KindToggle` (a switch), `KindHeader`
(section label), `KindBrand` (the mark, the product name, version and state
lines) and `KindUpdate` (A44), besides plain commands; `Children` make a row a
submenu. On macOS these are `NSMenuItem` custom views (`TSRowView` with hover,
press and spinner; `TSBarView` coloured by band; `TSSwitchView`, drawn because
an `NSSwitch` in an accessory app's menu renders in its inactive look, a pale
pill whether on or off). Windows and Linux menus only do text, so
`Item.FallbackTitle` renders a toggle as "Name: On/Off" and a gauge as
"title  ▰▰▱▱▱ 45%  —  sub".

The menu: the brand row · *Open dashboard* · the Updates section (A44) ·
**Accounts** (a gauge per account; with more than ten, the active one and
*All N accounts* ▸; then *Add current login*) · **Automation** (*Auto-switch*,
its second line naming the bar of each window in force; the model-limit
switch, A39) · **App** (the Claude Code row, A42; *Start at login*; *Check for
updates…*; *Quit tycswap*). The bars are only named here: they are set where
they were, in `tycswap config`, the TUI and the dashboard (A34). The accounts
are the dashboard's accounts, the Codex ones under a *Codex* heading of
their own, and *Auto-switch* is the dashboard's engine host, which rotates
the Codex accounts too while it runs the Codex engine (its second line says
so; A47).

**The menu stays open** on macOS: every row is a view whose `mouseUp`
reports the click without ending the menu's tracking, and `tray_menu_commit`
writes the new rows into the open menu (`syncMenu`), so a switch or a toggle
shows what it did in place and an open submenu stays open; an unchanged menu
is not touched. *Open dashboard* and *Quit* set `Dismiss` and close it; a row
that asks closes it before the dialog. Windows and Linux close their menus on
every click. Tags number the rows depth-first, a submenu's row before its
children (`walkMenu`), on every platform.

***Add current login*** runs `web.Server.AddCurrentLogin` — the dashboard's
own add, under its mutation lock — and notifies with the slot or "refreshed".
Without a live login it is `web.ErrNoLogin` before anything is tried, on the
dashboard as in the tray.
Its second line comes from `state.currentLogin` (A27): the login that is not
stored yet, else how to get another account there (`/login`, never `/logout`).

A switch onto an API-key account asks first through the dialog and records
the approval A33 requires (`ApproveAPIKeySwitch`); without a dialog the tray
refuses and points at the dashboard or the command line. For an account with
a base URL the row, the question and the notification after the switch are
A46's.

## A38. One app per machine

`tycswap app` holds an exclusive advisory lock on `<backup_root>/app.lock`
for its lifetime, taken before anything else. A second `app` exits with
"already running" instead of adding a second icon to the menu bar, and
`purge` refuses while the lock is held rather than deleting the accounts
under a live dashboard; a lock that cannot be read counts as held for
`purge`. The OS releases the lock when the process dies, so a crash leaves
nothing to clean up. A starting app waits two seconds for the lock, the
window the update restart's successor needs (A36). `app --remote` holds
`remote.lock` instead and does not exclude a local app (A45); it checks that
lock before it builds its tray and takes it after, so a refusal (another
remote tray, no tray here) creates no lock file. Asking whether an app runs
(`purge`, the bare start) creates nothing either: no lock file means no app.

## A39. One switch for the model limit, in the tray as well

The tray's switch is gone since A94; `autoswitch.model` was set with `tycswap
config` until A97 removed it.

The per-model weekly windows decide two things that must not disagree:
whether the engine counts them (`autoswitch.model`) and the percentage the
tray shows. The menu's **`<window> limit`** switch, named after the window
the accounts report ("Fable limit"), writes `autoswitch.model` (`all`, or
unset) and retargets the running engine in the same click (`ApplyModels`,
as the dashboard's *Count model limits* did until A93, A26). Every figure the shell
shows — the title, each account's bar, the threshold alert — counts exactly
the windows the engine counts, read from the running engine's live settings
first and the saved setting otherwise. A window that does not count is still
shown, marked `(not counted)`.

## A40. Bare `tycswap` starts the app in the background

Superseded by A63 for bare invocation; the menu retains the background launcher.

What people want when they type the command is the menu-bar / tray app, and
holding a terminal open for an icon is not. A bare `tycswap` in an
interactive terminal (both ends a TTY, or a Git Bash / MSYS2 terminal on
Windows, A41) therefore spawns `tycswap app` detached — its own session on
Unix (closing the terminal does not SIGHUP it, and a new session is what
gives a Cocoa status item its icon), `DETACHED_PROCESS |
CREATE_NEW_PROCESS_GROUP` on Windows — with stdin on the null device and
stdout and stderr appended to a log: `~/Library/Logs/tycswap.log` on macOS,
the file the LaunchAgent writes, and `<backup_root>/app.log` elsewhere,
rotated to `.1` past 5 MiB. The parent polls the single-instance lock (A38)
and returns once the child holds it; a child that exits first is reported
with the tail of its log and exit 1; one still starting after eight seconds
is left running with a pointer to the log. An app that is already running is
reported and nothing is spawned.

**A change of the CLI's surface, recorded here:** the bare command opened the
TUI (spec 08§1 step 5), which is `tycswap tui` now. The TTY gate stays: a
bare invocation from a script or pipe is still the `no command given` usage
error, not a resident process nobody asked for. `tycswap app` itself stays a
foreground command: start-at-login entries and debugging run it that way.

## A41. Windows: the app behaves like a Windows app

`tycswap.exe` is a console program — it is also the CLI — so Explorer (the
start-at-login Run entry, a double-click) gives `app` a console window of its
own, which would sit in the taskbar and end the app when closed. `app`
recognises that console (this process is its only one,
`GetConsoleProcessList`), moves its output to the log and calls
`FreeConsole`; a console shared with a shell stays, and so does `--headless`'s,
which has no tray icon to quit from. Children get real handles: a nil
`*os.File` becomes `INVALID_HANDLE_VALUE`, also the pseudo-handle of the
current process, so the background start (A40) and the update restart (A36)
are given NUL and the log, and the restart starts `DETACHED_PROCESS`, or a
tray without a console would open a console window after every update. Git
Bash counts as a terminal for the bare command (`MSYSTEM` set: mintty hands
programs pipes). That is the reference's rule, and its limit: any Windows
process with `MSYSTEM` set counts, so `x=$(tycswap)` or `tycswap | …` in Git
Bash or under GitHub's `shell: bash` on Windows starts the resident app too.
The dashboard opens through A26's opener, which hands the
URL to the default browser directly on Windows. A balloon holds 255 UTF-16
units and a tooltip 127: notifications that would be cut drop their reasons
rather than end mid-word, and the tooltip leads with what matters. The
`windows-latest` CI job runs the build-tagged tests (the console hand-off
for real, the detached start, the menu plan); the tray's icon, menus and
balloons, and the Run key itself, are not exercised there.

## A42. Claude Code in the tray

The tray shows and updates Claude Code by A27's rules, which the dashboard's
Updates card already follows: the newest version is what the install's own
installer offers on its own channel (`internal/ccversion`), an update runs
that installer's command, success is the version verified afterwards, and a
missing Claude Code is said with the line to type, never installed. The app
looks at Claude Code without the network right after the first paint (also
with `--no-update-check`), so the brand row names the version at once, then
checks 30 s after start and every 6 h. Rows: *Update Claude Code X → Y…* in
the Updates section (it asks, with the command and that running sessions keep
the old version, runs, checks again and reports); *Claude Code X — latest*
and *… — could not check for updates* as information; *Claude Code is not
installed — how to install…*, which shows the line. A new version is
announced once per version; a check that cannot learn the newest version
keeps the one learnt before for the same installed Claude Code.

The reference recommends one install per operating system, installs a
missing Claude Code from the tray and offers to move an install to the
recommended one; tycswap keeps A27's rule that each install follows its own
installer and nothing installs Claude Code on the user's behalf. A remote
tray (A45) has no Claude Code row: Claude Code lives in the distro.

## A43. Auto-switch survives an app restart

Auto-switch runs inside the app process, so quitting it, logging in again or
the restart after an update (A36) used to come back with auto-switch off. The
app records the choice in `<backup_root>/ui_prefs.json`, the file A27's
folded cards live in, now `{"version": 1, "autoSwitch": true, "folded":
{…}}` (0600, atomic, one process-wide lock for both keys), and resumes it at
start:

- **Recorded:** `Start(false)` and `Stop()` on the app's facade — the tray
  toggle and the app's dashboard both go through them. A dry run is not a
  choice and is not recorded; `tycswap web`'s facade has no state path and
  records nothing.
- **Not recorded:** an engine that ends by itself, and quitting: the app
  stops its engine on the way out as `tycswap web` does, without writing
  "off", since quitting is not turning auto-switch off.
- **Resumed:** right after the tray listens for engine events and before its
  first paint, so the icon shows `⟳` at once; in `--headless` before the
  server starts. A resume that fails is said on the app's output and leaves
  auto-switch off. A missing or unreadable file means off.

Superseded by A83: the choice is `autoswitch.enabled` in `settings.json`.

## A44. Updates in the tray: one answer, a badge with a count, rows that show

`appShell.updateWaiting` decides whether an update waits — a newer tycswap
release (A36), or a Claude Code update the app can run (A42) — and the icon's
badge, the tooltip, the menu's Updates section and the app dashboard's
Updates card follow from it.

- **The badge.** Windows (16–32 px) and Linux (22 px) show a dot: an accent
  disc in a white ring at the square's top-right corner, 36 % of the square
  with a 6 % ring, so it covers the corner and the top of the head and never
  an eye. macOS shows the count beside the mark (`DrawCounted`: 62 % of the
  mark's height, 1–9 then "9+" in a pill, the image wider than tall), also
  clear of the eyes; the test checks every pixel an eye touches. The count is
  one per update. Every icon is drawn once and kept; `SetIcon` is called only
  when the count changes, and the tooltip starts "tycswap · updates
  available — …".
- **The menu.** While something waits, an Updates section headed "Update
  available" / "N updates available" follows *Open dashboard*, with
  `KindUpdate` rows (on macOS an accent arrow disc on a tinted row, a second
  line with what the update brings).
- ***Check for updates…*** runs both checks at once, offers a newer release
  in the A36 dialog, and ends with one notification: what can be updated,
  else what is up to date, and what could not be checked — with the reasons
  while they fit a notification, else pointing at the dashboard. A release
  found before is named even when the re-check failed.
- **A failed check forgets nothing:** a pending release stays pending, a
  Claude Code update learnt before stays for the same install, and the error
  comes along; "checked" is the last check that reached the network.
- **The dashboard.** With a tray, the app's dashboard gets `updatesFacade`,
  which reads the shell, and `Deps.UpdatesOwnSchedule`: the shell's clocks
  check, the server's does not. *Check now* starts both checks and answers
  202 with "Checking…"; an apply runs the tray's run without the tray's
  dialog, since the page asked. One update runs at a time whichever side
  started it (`errUpdateRunning`, 409). Every change calls `Server.Refresh`.
- **Clicks that show** (macOS): a press turns the row the accent colour, a
  switch flips at once, any other row that stays open shows a spinner until
  the menu brings the outcome or four seconds pass, and a row that closes
  the menu blinks first; after every click the menu is written again, so a
  click that changed nothing ends its spinner.

## A45. A tray for a WSL engine: `app --remote`

On a WSL machine Claude Code and tycswap run inside the distro, which has no
StatusNotifierWatcher, so `app` there is headless (A35) and the machine has no
tray. The Windows binary has a complete tray and reaches the distro's
dashboard on `127.0.0.1:<port>` through WSL2's localhost forwarding. The shell
only renders `web.State` and acts through `shellActions`, so a tray for the
distro's engine is a second set of those hooks over the dashboard's HTTP API.

**The token.** The app in the distro mints a 128-bit token per start, hands
it to its server as `web.Deps.RemoteToken` and writes it to
`<backup_root>/remote.token` (0600 under the 0700 root, a temp file renamed
into place), removed when the app exits — on Ctrl-C and on SIGTERM — and
before an update restart's successor writes its own. The headless app prints
`Remote token: <path> (for tycswap app --remote)`. The server accepts
`Authorization: Bearer <token>` (constant-time compare) as both factors,
cookie and CSRF, on every `/api` route and the event stream, never on the
page or the static assets, and never sets a cookie for it; the Host check
stays, and the Origin / `Sec-Fetch-Site` rules apply to any request that
carries those headers. An empty `RemoteToken` turns bearer auth off: `tycswap
web` passes none. A26 insisted on two factors; a bearer is acceptable here
because the URL is loopback only and the command refuses anything else, the
file is 0600 inside the distro and read over `\\wsl.localhost\<distro>\…`
where the distro's permissions apply, and a token dies with the process that
honours it — a remote that gets 401 re-reads the file. `POST /api/launch`,
bearer only (the cookie and CSRF pair gets 403), hands out the one-time
dashboard URL (`LaunchURL`).

**The client.** `tycswap app --remote http://127.0.0.1:<port> --token-file
PATH` (or `TYCSWAP_REMOTE_TOKEN_FILE`; `--remote` with `--headless`, `--port`
or `--interval` is a usage error naming the flag) builds the same tray and
shell over a client: `State`, `Switch` (the account's key, `claude:<slot>`,
A26), `AutoStart`, `AutoStop`, `SetModel`, `Launch`. A
mutation refreshes the cached state at once. It follows `GET /api/events`
and reconnects with backoff (1 s doubling to 30 s); a stream silent for a
minute is dead (the server pings every 15 s), and a line or a frame's data
over 4 MiB ends it. While the engine is out of reach the shell's `Offline`
hook makes the title `⚠` and the tooltip and the menu's first row say why
("engine unreachable at <url>", or that the engine refused the token in the
named file); the tray notifies once per outage and once when the link is
back, and not for a first failure (the Run key starts the tray before the
distro's app is up). The client holds the token, so it uses no proxy,
follows no redirect, and checks the launch URL before it goes to the browser
opener: http, loopback, the engine's port, `/` and a 32-hex token, rebuilt
from those parts.

**What remote mode does not do:** no dashboard, no store lock (it holds
`remote.lock` beside `app.lock`: two remote trays refuse each other, a remote
tray and a local app do not), no auto-switch resume, no Claude Code row, no
*Add current login* (the login is the distro's; the distro's dashboard has the
button), no app state, and an API-key switch is refused with a pointer to the
dashboard. Without a tray it exits 1: "remote mode needs a tray; run tycswap
app in the distro instead". *Start at login* registers `app --remote URL
--token-file PATH` under `<Identifier>.remote`, apart from the local app's
entry; the update restart re-runs `os.Args` and keeps the remote flags; the
self-update, the release check and Quit are as local.

**Setting a machine up** is the README's WSL recipe: no shared secret to
provision, no firewall rule, no second store.

## A46. An API-key account may carry a base URL (Go-side additive extension)

The generic core of the reference's gateway account: **an API-key account may
carry a base URL**, a switch onto it points Claude Code at that endpoint, and
a switch away restores the previous settings exactly. The corporate parts of
the reference's gateway are not ported. A key account with a URL is just an
account; there is no separate gateway concept in tycswap. Go-only:
`docs/port-spec/` is untouched.

**The record.** An `api_key` record may carry `baseUrl` (after `kind` and
`alias`); any other record's `baseUrl` is ignored (`store.AccountBaseURL`,
`store.BaseURLFrom`). `ccsettings.ValidateBaseURL` accepts an absolute `https`
or `http` URL with a host and no user info (a password would land in every
listing and log line), no query and no fragment (Claude Code appends its own
paths to the base URL), no whitespace or control character, at most 2048
bytes; which hosts are allowed is the user's call. `add-token --base-url URL`
checks the URL before the token is read; with one, the token is that
endpoint's key and the account is `api_key` whatever its shape (a gateway
mints keys in its own format), as long as `ccsettings.ValidateToken` passes
it: one run of printable ASCII, at most 8192 bytes, not a JSON object, since
it is written as an HTTP header. A refresh in place (same email, no slot) sets
the URL exactly as given, so a refresh without `--base-url` removes it, and
says which (`(endpoint <host>)`, `(endpoint removed)`). An endpoint key that
is not shaped like an Anthropic key cannot be refreshed without its URL: the
token then reads as a setup-token and the existing cross-kind guard refuses
it. A refresh changes the backup only; when it is the live login's account it
says that `tycswap switch <n> --force` puts the new key and URL in place (a
plain switch to the account in use is `Already on`).

**What Claude Code does with the keys** (2.1.289). It applies the `env` block
of `<config home>/settings.json` to its environment when it starts, and a
running session that sees the file change (in a trusted workspace) clears its
auth caches and applies the block again, adding keys but never removing one.
It fills two headers independently: `Authorization: Bearer` from
`ANTHROPIC_AUTH_TOKEN` (else `apiKeyHelper`), and `X-Api-Key` from
`ANTHROPIC_API_KEY` (always with `-p`, interactively once approved), else
`apiKeyHelper` or a key passed on a file descriptor
(`CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR`), else a stored Console key
(`primaryApiKey`). A key in the
second slot goes to the endpoint beside the bearer token, and Claude Code
warns that both are set. So the endpoint's key goes in as
`env.ANTHROPIC_AUTH_TOKEN` (the convention for an LLM gateway or proxy), the
second slot is emptied where tycswap owns it, and named where it does not.

**The profile** (`internal/ccsettings`, the generic port of the reference's
settings profile). `Apply` writes `env.ANTHROPIC_BASE_URL` and the key as
`env.ANTHROPIC_AUTH_TOKEN`, and deletes `env.ANTHROPIC_API_KEY`, as the
reference does; `OwnedKeys` is that allowlist of three, the complete set it
may change, plus the `env` container it creates when there was none
(`ProfileKeys` are the two it writes). Before `settings.json` is touched, the
prior state of each owned key (present with its value, or absent) and of the
container is written to `<backup root>/claude-settings.prev.json`
(`{"version": 1, "settingsPath", "keys"}`, 0600, atomic). `Revert` restores
exactly those keys from the record (absent → deleted, present → the old value,
the user's `ANTHROPIC_API_KEY` included; a container it created is removed
again when nothing else moved in, a non-object `env` it replaced comes back),
writes the file and removes the record. Every other key is read and written
back untouched, including keys the user changed while the profile was in
place; an owned key the user edited is still reverted (the record wins, as in
the reference). A second `Apply` keeps the ORIGINAL priors, so endpoint A →
endpoint B → subscription lands on the file from before A; a record for
another settings file (`CLAUDE_CONFIG_DIR` changed in between) is reverted
there first, so one record always describes one file, named by its absolute
path, and `Revert` restores the file the record names. A record is only a
complete one: a single JSON object of version 1 with an entry for `env` and
for each owned key and nothing after it; anything else (`{}`, `null`, a
header alone, a missing entry, another version, trailing data) is corrupt and
refused like one that does not parse, so a revert is never reported that was
not made. A new record never takes tycswap's own endpoint for the user's:
without a record (removed by hand), the two written keys holding exactly the
profile being applied or another endpoint account's URL and key are recorded
as absent, and an `env` holding nothing else as absent too, so the way back
removes them instead of putting that endpoint back. A missing settings file
is `{}`; one that is not a JSON object is refused and never rewritten. Writes
are a temp sibling and a rename; an existing file keeps its mode, so a revert
leaves it as it was, and a new one is 0600; an existing config home keeps its
mode. Numbers are kept as written (`json.Number`), so a rewrite changes no
value it does not own; key order is not kept, as in the reference. A settings
file that is a symbolic link (a dotfile manager's, into a repository or a
read-only store) is refused before anything is written, where a rename would
replace the link with a regular file: that breaks the manager, writing
through the link instead would put a key into a tracked or shared file, and
neither is undone by a revert. The rollback snapshot puts a link back as the
same link.

The reference's rule that the way out must always work, also when the record
is gone, is kept in its generic form: without a record, a switch away removes
the two written keys when they hold exactly one endpoint account's URL and
stored key, and leaves them otherwise; what they replaced cannot come back. A
rule there that removes two retired flags is not ported: tycswap's profile
never set them, and one of them is a flag users set on purpose for gateways
that reject beta headers. The profile sets nothing beyond what routing needs.

**Which mechanism the key uses.** The reference does not store the gateway
key as a managed key either. The key goes into the profile as
`env.ANTHROPIC_AUTH_TOKEN`, and neither `primaryApiKey` nor the macOS `Claude
Code` Keychain item is written for such an account. A switch onto one takes
every login off Claude Code's credential store instead
(`credstore.ClearActive`: the managed key from the Keychain item and
`primaryApiKey`, the OAuth login cleared the way a switch onto a key clears
it, keeping its seat-wide part, A29), so nothing there fills the second slot
and `ReadActive` finds no credential. The removals are best-effort one by
one, as for any write that clears the other axis, but the result is checked:
a key or login still readable afterwards (a Keychain item that cannot be
deleted) fails the switch, since Claude Code would send that key along to the
endpoint. Clearing does not undo itself, so the switch records its credential
step before it, and the rollback writes the original credential back. What
tycswap does not own it does not touch, and names instead: when
`settings.json` sets `apiKeyHelper`, or `ANTHROPIC_API_KEY` is set in the
environment the switch runs in (which a Claude Code started from the same
shell inherits), the switch onto an endpoint warns that Claude Code sends
that key as `X-Api-Key` to the endpoint beside the account's key, printed or
as a `--json` warning, by name only. An account with a URL keeps no
credential in the store: its stored key is its credential, the identity in
`~/.claude.json` names it, and the reporting reads it as an API key from the
record (`AccountInfo.BaseURL`), not from the live credential. An API-key
account without a URL is unchanged: its key is the managed key, as before.

**The switch.** `switching.planProfile` decides, before anything is written
(the outgoing backup included), what the switch does to `settings.json`: the
target's endpoint and key to apply; or, for any other target, the record to
revert (whenever one exists, whatever the outgoing account) or, without one,
the by-value cleanup when the file holds both written keys. It refuses with
nothing written when the stored URL no longer validates (a hand-edited
roster), the stored key cannot be a header, `settings.json` is a symbolic
link or not a JSON object, the record is corrupt, or an API-key account
without a URL holds a key that is not an Anthropic key (written live, it
would become an OAuth credential); a target with no stored credential keeps
its existing error. It snapshots the record and both settings files byte for
byte. Then, on every path that writes the live login — the normal switch,
`--force` and the other direct activations, and the fresh machine — the
credential step writes the target's stored credential as always, or
`ClearActive` for an endpoint target, and the profile step applies or
reverts. A switch that fails afterwards rolls back in reverse:
`~/.claude.json`, then both settings files from the snapshot (recorded before
the write, so a half-done `Apply` is rolled back too), then the credential.
With nothing at all in the credential store before (an endpoint account
active without a seat-wide part), the credential rollback clears the store
again instead of writing an empty credential; the direct activation's
rollback does the same where it used to write `""`. On the normal path an
outgoing endpoint account is backed up config-only and never classified:
nothing in the credential store is its own, and a login found there anyway
(written by something that left its identity in place) is stashed as an
alien credential, never stored over the key; the account's own stored key
found there (a URL added to the active plain-key account since) is its own.
The outgoing account counts as one with a base URL when its record has one,
or when the credential store is empty and the record names the live settings
file (its URL removed by a refresh since). An empty store whose Keychain did
not answer is refused as before: it may hold a login a rollback could not
restore. The self-switch check reads the empty live credential as matching
(`Already on`); `switch <n> --force` onto the active endpoint account
re-applies it, which is how a refreshed key goes live. The by-value cleanup
is best-effort; a failed revert of a record fails the switch.

**Approval and notes.** An endpoint account is an API-key account, so A33's
approval applies unchanged; its refusal is `ErrEndpointNeedsApproval`
(`… authenticates with an API key at <host>. Switching to it changes how
Claude Code authenticates and where it sends its requests …`), and the CLI's,
the dashboard's and the tray's questions add `EndpointNotice`, which names the whole URL
and the settings that take the requests there and back; in place of A33's
sentence that running sessions keep their login until restarted, they say
that such a session takes the endpoint up when it re-reads `settings.json`
(`EndpointSessionNotice`, with the session count on the command line and
in the tray). The follow-up says
what a running session does, which differs by direction: after a switch onto
an endpoint (`EndpointAppliedNote`) a session that is already running takes
it up when it re-reads `settings.json`, at once in a trusted workspace, and
one that does not needs a restart; after a switch away (`EndpointRevertedNote`)
every running session keeps the endpoint and its key until it is restarted,
since a re-read adds keys and never removes one. Auto-switch never moves onto
or off an API-key account (A33), so it never applies or reverts the profile
either.

**Surfaces.** `list`, `status` (human: `→ <host>`, `Endpoint: <host> (Claude
Code's settings.json)`; JSON: `baseUrl`, the whole URL), the snapshot
(`AccountSnapshot.BaseURL`), the TUI (a base URL field in the add-token
modal; `API key (no quota) → <host>` on the card, the per-row line and the
monitor span), the dashboard (the add-token form's `baseUrl`, the rows'
`baseUrl` and `→ <host>` chip, the header chip and the red notice, the switch
confirmation, the Guide), the tray (`→ <host>` on the account's row, the
switch confirmation, `EndpointAppliedNote` as the notification after it),
`export` (`baseUrl` beside `kind` and `alias`; an
endpoint account is exported from its backup even while active) and `import`
(`baseUrl` checked as `add-token` checks it, only with API-key credentials,
which are then taken as the endpoint's key; every refusal before any write).
The add-token method with a URL is not on the frozen `tui.Facade` /
`web.Facade` (A13): the TUI and the dashboard reach
`AddAccountFromTokenWithBaseURL` by type assertion, and a facade without it
is an error (`503` on the dashboard), never a dropped URL. After a `/login`
made while an endpoint account was active, the live login is another account
but the profile is still in `settings.json` and takes precedence over it:
the dashboard's auth-overrides notice (A27) then lists the two written keys,
which it leaves out while the endpoint account itself is active, and
`status` adds the same one-line note under that login, managed or not.
`tycswap purge` leaves the live login as it always has, settings keys
included, but deletes the record, so while one exists it says so before it
asks.

**Not done here.** A session started with `tycswap run --share` takes
`~/.claude/settings.json` into its profile (spec 06§2), so while an endpoint
account is the default login such a session inherits the endpoint like any
other `env` key in that file; `run` and `env` still refuse API-key accounts
themselves. On POSIX that is a link and follows the switch; on Windows it is
a copy, so the key stays in that session profile until the session is
launched again. The key goes only as a bearer token; an Anthropic-key
passthrough proxy that wants `x-api-key` would need `ANTHROPIC_API_KEY`
instead, which is not offered. `apiKeyHelper` and an `ANTHROPIC_API_KEY` in
the environment are named, not removed. Only the user settings file is read
and changed for this: an `env.ANTHROPIC_API_KEY` or `apiKeyHelper` in
project, local or managed settings, or in the `env` block of
`~/.claude.json`, is neither removed nor named. `add` while an endpoint account is
live finds no credential and says so, as for any empty store.

**Tests.** `internal/ccsettings` (the reference's tests under tycswap names):
the profile write with every unrelated key, container and the user's other
env keys untouched, the user's `ANTHROPIC_API_KEY` gone, the file's mode kept
and the record 0600, 2-space indent; the allowlist is exactly the three keys
(two written) and nothing else changes; `env.ANTHROPIC_API_KEY` deleted,
recorded and restored, a second `Apply` keeping the original prior, absent
staying absent; a missing file is `{}` and a corrupt, array, `null` or
two-value file is refused byte for byte by `Apply`, `Revert` and `Check`; the
exact revert with the user's later edits kept and an edited owned key
reverted; the created `env` container removed, kept with a user key, a
non-object `env` put back; endpoint → endpoint → revert lands on the
original; a second identical `Apply` changes neither file; no record and
nothing ours is a no-op; the by-value revert of a known pair, and a foreign
pair, a changed token or half a pair left alone; a corrupt record refused by
`Apply`, `Revert` and `Check` and seen by `SidecarExists`; every incomplete
record shape (`{}`, `null`, an array, a header alone, each missing entry,
version 2, trailing data) refused the same way with the settings file
unchanged; the recorded file reverted, another settings file reverting the
first; the sidecar schema; numbers kept; a 0644 file still 0644 after the
revert and a new one 0600; the snapshot restoring bytes, mode, absence and a
link; a symlinked settings file refused by `Apply` (before the record),
`Check` and `Revert`, the link and its target unchanged; a new record
recording tycswap's own endpoint (the profile itself or a known one) as
absent, the user's own pair and an `ANTHROPIC_API_KEY` beside a known pair as
they are; `Competing` naming a non-blank helper and variable and nothing for
a blank, null or non-string helper, a blank variable or an unreadable file; the record naming an absolute path from
a relative one; invalid profiles writing nothing; the URL and key validation
tables.
`internal/switching` (`endpoint_test.go`): from a subscription login onto the
endpoint and back with the user editing `settings.json` meanwhile (the edits
stay, the user's own `ANTHROPIC_BASE_URL` and `ANTHROPIC_API_KEY` come back,
no `primaryApiKey`, no approval entry, the MCP login kept, both slots
intact); endpoint → endpoint keeps the original record; a plain key →
endpoint → the plain key live again; `--force` onto, onto the active endpoint
after a key refresh, and away; the fresh machine; the rotation from an
endpoint account; a corrupt record, an unparseable settings file and a
symlinked one stopping the switch with every file unchanged, and a
subscription switch ignoring an unparseable file; a lost record still coming
out by value, a foreign endpoint left alone; an invalid stored URL, a stored
key with a space and a gateway-minted key without a URL refused with nothing
written, with and without `--force`; a lost record not taken for the user's
when another endpoint (or the same one, forced) is applied over it; a
managed item that cannot be deleted failing the switch onto an endpoint and
rolling back, from a plain key and from a login beside a stale key, with and
without `--force`; the active account's URL removed or added by a refresh,
and the switch away working from either without a stranger's stash; an
unreadable Keychain refusing the switch away from an endpoint; the outgoing
backup left alone by a refused switch; `apiKeyHelper` and an environment
`ANTHROPIC_API_KEY` named in the printed and the `--json` warnings, by name
only, `apiKeyHelper` left in place, and no warning on a switch away; the
approval refusal and both follow-up notes; and a switch whose
`~/.claude.json` update fails after the credential store and `settings.json`
were written (a macOS Keychain whose managed-item delete breaks the file)
leaving `settings.json`, the record, the credentials file and both Keychain
items as they were — onto an endpoint, away from one with and without a
seat-wide part, between two and from a plain key, on the normal path and
with `--force`; each of the four rollback steps, removed, fails one of these,
and so does each guard above. The macOS variant of the round trip, from a
subscription login and from a plain key in the managed item.
`internal/credstore`: `ClearActive` on the file backend (a login with and
without MCP logins, a managed key with and without a file, nothing) and on
the Keychain. `internal/store`: `AccountBaseURL` for an `api_key` record
only. `internal/lifecycle`: `add-token --base-url` stores the endpoint, a
gateway-minted key and the record's key order; the refusals before the prompt; the
refresh setting or removing the URL; the cross-kind refusal of a gateway-minted key
without its URL; `--slot`; the hint after a refresh of the live account;
`purge` warning only with a record. `internal/reporting`: list, status and
the snapshot with the endpoint account inactive and active (`api_key`,
`baseUrl`, the host only in human output, never `no credentials`), and the
`status` note under another login, managed or not, absent while the endpoint
account is live. `internal/cli`: the flag's usage errors, and end to end the
add, `list --json`, `list`, the prompt naming the URL with the endpoint's
sentence about running sessions in place of A33's, the switch, `status`,
`status --json` and the switch back. `internal/tui`: the modal's field and
focus ring, the call with and without a URL and on a facade without the
method, the rows. `internal/web`: the add-token route with and without a URL
and the `503`, the row's `baseUrl`, the overrides notice with and without the
record, for another file, and with the endpoint account active or not.
`internal/transfer`: export with the endpoint account live, import storing
the URL after `kind` and `alias`, every import refusal writing nothing, and
the round trip.

## A47. Codex in the dashboard and the tray, through one state path (Go-side additive extension)

A26 left the Codex accounts out of `tycswap web` and named them a follow-up:
rows, a façade, and the Codex auto loop moving out of `autoCommand` into a
host the dashboard shares. This amendment brings them in through the paths
the Claude accounts already take, so nothing renders, routes or ticks Codex
on a second path. Go-only: `docs/port-spec/` is untouched.

**One state path.** The state document keeps one `accounts` list, now
provider-tagged rows from both providers, rather than gaining a `codex`
section: the row shape, the `key`, the key-addressed routes, the page's row
helpers, the TUI's merged snapshot and the tray's account rows all iterate
one list already, and a section would need a second projection, a
second renderer and a second tray path. `web.Deps.Snapshot` is the read
model the document is built from (the Facade's own when nil); `tycswap web`
hands it `providers.NewMultiSnapshotSource` over the Claude switcher and the
Codex one, so the Codex rows follow the Claude rows, provider-major and never
interleaved, and a provider whose pass fails or panics is dropped while the
other still renders (A22). A Codex row is the Claude row shape: `provider`
`codex`, `key` `codex:<n>`, `orgName` the workspace, `kind` `oauth` or
`api_key` (an API-key login is `switchable: false`), `usage` with `fiveHour`
and `sevenDay` only, the decision-grade value as for every row. It never
carries `atLimit` (Codex has one bar, no per-window verdict), `baseUrl`, or
`tokenStatus`: token status is a Claude row's, and a Codex account's is
`tycswap codex list --token-status` (this replaces A26's note that Codex rows
would carry the per-row string). `activeNumber`, `currentLogin`, the
endpoint-profile check of the overrides notice (A46) and the add-token alias
lookup by email read the Claude rows only; the merged snapshot's active
number is the Claude one by construction.

**`auto.codex`.** `AutoView.Codex` is the Codex engine beside the Claude one:
`{"enabled", "running", "threshold", "lastTick"}`, `lastTick` being `{"at",
"outcome", "detail", "switchedTo", "runningPids"}` (the names of `tycswap auto
--json`'s codex line; `switchedTo` null unless it switched) or null before the
first tick. It is null on an install that had no Codex accounts at launch, so
the Claude-only document is the one from before plus `"codex": null`.
`enabled` is `autoswitch.codexEnabled` and `threshold` the engine's one bar,
`codexThreshold` (the setting, or the 7d bar when it is 0), both as the
running engine started with them or as they would start.

**Events.** `AutoEventView.Provider` is omitted for the Claude engine's
events, so their JSON is unchanged, and `codex` for a Codex tick. A tick takes
the Claude engine's kind for its outcome, so the page's colours, its quiet
filter and the tray's switch rule apply untouched: switched → `switch`, error
→ `error`, blocked → `all-exhausted`, ok and no-accounts → `no-switch`. The
message is `Tick.Human()`, `account` the slot switched to, `fields` the
`--json` line's fields. The SSE event names stay `state` and `auto`.

**Routes.** No new route. `splitKey` resolves a key to its provider and
reference. Switch, disable, enable and remove take a `codex:` key and go to
`web.CodexOps`, a narrow façade of its own (the Claude `AccountOps`
signatures do not fit: a Codex switch reports the codex sessions still on the
old account): `SwitchTo` answers `{"number", "email", "runningPids",
"alreadyActive"}`, `SetAccountDisabled`, `RemoveAccount` (`codex remove -y`),
and `AddCurrent`, which `POST /api/accounts/add` reaches with
`{"provider": "codex"}` (`codex add`; no body or `claude` is Claude Code's
login). Alias, move and swap answer 404 for a Codex key, and `force` or
`confirmAuthChange` on a Codex switch 400: the terminal dashboard offers
switch, disable / enable and remove on a Codex row, and the page offers what
it does plus storing the current Codex login, which `tycswap codex add`
does. Without a Codex façade (no Codex accounts at launch) a Codex request
answers 503. A Codex request passes the same authentication and origin
checks as every other `/api` request and runs under the dashboard's
mutation lock like every mutation. Each Codex call also holds the Codex
store's lock (`codex/.lock`) from start to end: the switcher's switch and add
take it themselves, and `codexOps` took it around disable and remove (since
A48 the switcher does, for every caller), because the store's own writes
(`SetDisabled`, `RemoveSlot`) skipped the file lock while any goroutine of the
process held it, until A50 made every ordinary store write take the lock and
let only a transaction's own view reuse it. Without the lock around the whole
call, a remove could land between a Codex tick's capture and its write of
`auth.json` and leave `auth.json` on the removed account. A disable or remove
that waits 10 s for the lock answers 409; the switcher's own busy error on a
switch or add was a switch error (500) until A48 made it a lock error (409).

**One engine constructor.** The Codex engine's construction and loop live in
`internal/cli/codexauto.go`, shared by both hosts: `newCodexAutoEngineFor(sw,
settings)` holds the rules (no switcher, `codexEnabled` off or no Codex
accounts → no engine; a panic → no engine; bar `codexThreshold`, margin
`autoswitch.hysteresisPct`), `newCodexAutoEngine` is it over the command's own
switcher for `tycswap auto`, and `startCodexLoop` ticks at once and then
every interval. The tick filter (a switch or an error, every tick under
dry-run) and the `--json` fields are shared too, so `tycswap auto` prints
exactly what it printed. The dashboard's engine host builds the Codex engine
at Start with the same constructor and runs it on the same loop at the
engine's interval; every tick becomes `lastTick`, and what passes the filter
goes to the event log (the tray's notifications alone since A93, which drops
the page's log). Stop stops the Claude engine, cancels the Codex loop
and waits for both within the one `autoStopWait` budget; past it Stop answers
409 and Start refuses until both loops have returned, the A26 rule for the
Claude loop (since A83 Stop turns the key off and A93 removes the dashboard's
dry run, so only the app's exit ends the engines). The slider retargets the Claude 7d bar only: the Codex bar is
fixed for the run, as in `tycswap auto`, and the tile says so. There is no
Codex model setting, quarantine or state file. `autoswitch.codexEnabled` and
`codexThreshold` take effect when a Codex engine next starts: this page's,
when the dashboard started with Codex accounts, or `tycswap auto`'s. The
TUI's engine never runs one, and the Settings row says so.

**Wiring.** `newDashboard` decides Codex presence at launch, as the TUI does
(`codexIsPresent`): with Codex accounts it builds the quiet Codex switcher
(`newQuietCodexSwitcher`, the TUI's, whose stdout is discarded), the merged
snapshot over a context cancelled when `tycswap web` returns, the Codex
façade and an engine host over both switchers; without them the Codex parts
are nil and the dashboard is the Claude one.

**The page.** The account table lists the Codex rows under a Codex heading
after the Claude rows: slot, a `codex` chip, name, email, workspace, the 5h
and 7d meters, an empty model cell, *Switch* (disabled on the active row and
an API-key login) and a menu with *Disable* / *Enable* and *Remove*. A Codex
switch that leaves codex sessions running says to restart them, naming the
PIDs, as `codex switch` and the TUI do. *+ Add current Codex login* sits
beside *+ Add current login* while the server has Codex accounts, and the
card counts `N Claude · M Codex`. The Auto tab gains a *Codex engine* tile
(running, stopped or off; the bar and the last tick) and tags Codex ticks in
the log with a `codex` chip and `codex #n`. The header strip, the summary
tiles and *Next best* stay on the Claude rows: they describe Claude Code's
login and the Claude engine's ranking.

**Two stores, two loops.** No goroutine holds both stores' locks. Every
Codex call from the page and a Codex tick's switch serialise on
`codex/.lock` (above). The TUI's Codex disable and remove called the
switcher directly and could interleave with a refresh or switch holding the
lock in the same process; A48 moved the lock into the switcher, so they
serialise too. The state build every
interval now takes the Codex snapshot too: network-free unless an entry is
stale and due, but a token refresh of an inactive account takes the Codex
lock and can delay one broadcast. The rows use the decision-grade value
(unavailable past 300 s) while the Codex engine decides on the last good
measurement, as in `tycswap auto`.

**The tray.** The tray application (A35) renders the same document, so it
needs no data of its own. Its menu groups the rows by provider: the Claude
rows under *Accounts* as before, then a *Codex* heading and the Codex rows,
each group in slot order and past ten rows in its own submenu (*All N Codex
accounts*). Every row's id is `switch:` and its row key, and a click passes
the key: the app's switch dispatches on the provider (the Claude switcher,
or the dashboard's Codex façade), and the remote tray (A45) posts the key to
`/api/switch/{key}` as it is. A Codex switch notifies *Switched Codex to
account #n* with the codex sessions still running (or that the next codex
session uses it), and a Codex engine switch *Auto-switched Codex to account
#n*; the Codex engine's other ticks raise nothing, as the Claude engine's
non-switch events do not. The title, the icon, the API-key confirmation
(A33) and the threshold alert stay with the active Claude account (the alert
is remembered by row key); the tooltip adds the active Codex account and its
windows. The *Auto-switch* row's second line names the Codex bar (`codex
X%`) and says it rotates the Codex accounts too while the Codex engine is
enabled; its toggle is the same Start and Stop, so the A43 resume starts both
engines. A Claude-only state gives A37's menu, wording and notifications
unchanged; only the row ids now carry the provider (`switch:claude:2`).

**Tests.** `internal/web`: the exact document without Codex (`auto.codex`
null) and with it (the Codex rows, `auto.codex`); the Codex row's usage
against `codex list --json`'s projection and the list row's freshness keys;
token status and the endpoint profile on Claude rows only; the alias lookup
skipping a Codex row; the provider on the event stream; the provider-keyed
routes reaching the Codex façade for switch, disable, enable and remove and
refusing alias, move, swap (404) and the Claude switch flags (400); the 503s
without a Codex façade; add with a provider; the running PIDs a switch
answers; the page's Codex row offering exactly the TUI's actions, the add
button, the restart warning, the tile and the log tag, and the Claude-only
views reading Claude rows. `internal/cli`: the shared constructor's checks;
one Start running both engines and one Stop ending both; `codexEnabled` off
building no engine, and no Codex view without Codex accounts; the event per
outcome; Stop waiting for a Codex tick in flight and, past the wait, Start
refusing until it returns; the Codex façade over a real Codex store, its
disable and remove waiting for a holder of the Codex store lock and
answering a lock error when it stays held; and the
wired dashboard with and without Codex accounts (rows, `auto.codex`, a Codex
switch with its PIDs, a refused Codex alias, the 503). The `tycswap auto`
tests guard its output unchanged. The tray: the Codex rows under their own
heading and submenu, the title, tooltip and alert on the Claude account, the
Codex bar on the auto-switch row; the Claude-only menu, wording and
notifications unchanged; a click carrying the key to the right switcher,
with the Codex notification and its running PIDs; the Codex engine's switch
notification; the remote tray posting the key verbatim; the app's switch
dispatch; and the app resuming with both engines (`TestAppResumeStartsBothEngines`,
a fake Codex source through the `newCodexAutoEngineFor` seam).

## A48. The Codex store lock lives in the switcher; `tycswap web` ends with 0 when stopped

Three small fixes the reviews of A47 found, together because two share a
cause.

**Disable and remove hold the Codex store lock.** `Switcher.SetAccountDisabled`
and `Switcher.Remove` wrote through the store, whose writes then took the file
lock themselves unless this process already held it (A47, "Two stores, two
loops"; A50 replaced that rule with transaction-scoped views). The Codex
usage refresh holds that lock around its network call (it must be able to
persist a rotated token before it asks for one), and the TUI
runs that refresh in its own process. So a remove from the TUI during a
refresh of the same account skipped the file lock, deleted the slot and its
credentials, and the refresh then wrote the new token back under the removed
account's key: a live credential file for an account that no longer existed.
The dashboard had the same exposure through its refresh and its engine, and
A47 closed it there alone, in `codexOps`. The lock now sits where every caller
passes: both verbs take it through `withLock` like switch, add and move, so
`tycswap codex disable|enable|remove`, the TUI and the dashboard wait up to
10 s for a holder in this process or another and refuse when it stays held;
`codexOps`'s own wrapper is gone. `Remove` asks its question before it takes
the lock, so a prompt never holds the store.

**A busy store is a lock error.** The switcher's `acquire` reported a held
lock as a switch error, while the store's own `With` and the Claude side's
lock (port-spec 01, `LockError`) report a lock error; the dashboard maps the
kinds to 500 and 409. Every Codex verb now reports `LockError` with the
switcher's message (disable, enable and remove used to surface the store's own
`Failed to acquire lock` line), so a busy store answers 409 on switch and add
as it did on disable and remove. No Codex verb that can meet the lock takes
`--json`, so no error envelope changes.

**`tycswap web` and `tycswap app` end with 0 when stopped.** `web` returned
130 after Ctrl-C and SIGTERM alike, as the reference does, while `app` (A45)
means to return 0 on both because `systemctl --user stop` sends SIGTERM and a
stop it was asked for is not a failure; a service manager reads 130 as one.
Dropping `web`'s 130 branch (#22) fixed SIGTERM alone: the program-wide SIGINT
notifier (A7), which prints the cancellation note and exits 130 for every
other command, receives Ctrl-C beside the server's signal context and wins,
for `web` and for `app`, whose deferred token removal and engine stop it
skipped. The two servers (the remote tray included) now claim SIGINT once
their context is registered and before their own first line and the token
file (`claimSigint`; a Ctrl-C during the store's start-up notices, before the
claim, is still the notifier's): the claim stops the notifier's delivery
(`signal.Stop`), so
the outcome does not depend on when the notifier's goroutine runs; the serve
loop ends on the context, the defers run and the command returns 0. The
release, deferred before the cleanup defers, registers the notifier again when
the command returns, so nothing changes for any other command or for
`run()`-driven tests, where the notifier is not installed and a claim is a
no-op. A usage error stays 2 and a server that cannot start stays 1.

**Tests.** The switcher: disable and remove wait for a holder of the lock and
write once it is released, and report a lock error with nothing written when
it stays held; the three busy assertions name the lock kind. `internal/cli`:
the Codex façade's lock test runs against the switcher's lock; `tycswap web`
exits 0 through the notify-context seam, offline; with the A7 notifier
installed, a real SIGINT ends `tycswap web` with 0, the headless app with 0
and no token file, and `app --remote`, sent as its fake tray's `Run` starts,
with 0 and `remote.lock` free (Linux and macOS; the file is not built on
Windows).

## A49. The model windows sit beside their bar, picked from the reported windows

`autoswitch.model` decides which per-model weekly windows auto-switch counts
(until A97 removed it; the sessions decide since),
and `autoswitch.modelThreshold` is the bar for exactly those windows. The
registry (`settings.SettingSpecs`) listed the model key last, after the poll
interval, the Codex keys, the cooldown, the hysteresis, the strategy and the
unhealthy ticks, so every view that follows registry order (`tycswap config`,
the TUI's Settings screen, the dashboard's Settings tab, A27, A28) put the
choice at the bottom, eight rows away from its bar. Since accounts report a
Fable window that runs full before their 7d window does, that choice matters
as much as the bars. The registry now reads 5h threshold, 7d threshold, model
windows, model threshold, then the rest unchanged; no view sorts on its own,
so all three follow. Nothing reads the registry by position: the TUI and the
tests look keys up by name.

**A switch per reported window.** The Settings tab's text field asked for
model names to be typed as the usage API spells them, comma-separated. The
row now shows *All models* and a switch for every per-model window the
accounts report (`modelWindowNames`, the list the "limits ignored" warning
already uses), each ticked when the saved value names it, matched without
case and split on commas only, as `settings.ParseModelNames` does. A name the
saved value carries that no account reports keeps a ticked switch of its own:
the picker shows the whole saved value and a save never drops a name the user
did not untick. Ticking *All models* greys the named switches and saves
`all`; *Save* with nothing ticked sends the reset (`DELETE`), since the
server refuses an empty value. With no window reported and no name saved
there is nothing to tick, and the row keeps the text field. The value goes
through the same `POST /api/settings/{key}` route as before, so `ApplyModels`
retargets a running engine exactly as it did (A26). The tab repaints when the
reported windows change as well as when the settings do, so a window that
appears after the page loaded gets its switch. The Auto tab's *Count model
limits* switch and the tray's `<window> limit` switch (A39) are unchanged.
A93 removes the picker from both tabs: `tycswap config` alone sets the key.

*Tests.* The registry's first four keys are pinned by name. A node script
runs the picker's pure block of `app.js` (`modelPickerOptions`,
`modelPickerValue`): ticks for saved, case-folded, unreported, duplicated and
`all` values, comma-only splitting, and what Save sends, `null` for a reset.


## A50. Storage transaction ownership and scratch-login cleanup

Claude's lazy org-field migration is a write even when triggered by `list`,
`status`, or identifier resolution. `SequenceMigrated` probes read-only first,
then takes the store lock and re-reads before migrating. Already-locked callers
use `MigratedSequenceForUpdateLocked`; lifecycle resolution uses the roster
passed into the locked operation via `ResolveAccountFrom`, without another
migration or read. Import's write pass uses the locked entry too. Prompts stay
outside the lock.

Codex storage no longer infers ownership from a process-wide held counter.
`StoreLock.Store` and `WithLock` supply a transaction-scoped Store view. Normal
Store values always acquire the file lock, including writes to credential
snapshots. Only the supplied view reuses its transaction's lock; its mutex
serializes writes and release, and a retained view refuses writes after release.
Switching, refresh, capture and both importers pass this view through their
write spans. A released StoreLock is not reused for another transaction. This
replaces the storage bypass described in A47/A48; the switcher still owns its
whole multi-step operation and preserves its busy-error behavior.

`add --login` explicitly passes `--claudeai` and refuses `--console` (including
an equals form) before constructing the store or launching the child. Result
validation remains a second check. Rejecting a result after a Console flow has
already run cannot prevent key creation.

Scratch cleanup deletes only the OAuth Keychain item derived from its own
directory, never the shared Console key. Failures keep the private directory
and a `.cleanup-pending` marker containing only the cleanup backend. The next
`add --login` retries marked profiles under `.login-cleanup.lock`, leaving
unmarked, potentially active profiles alone. Cleanup remains retryable until
successful; success makes it idempotent. The recovery marker is removed last.
A failed cleanup warns independently of the save/switch result and cannot undo
a stored account. OAuth-before-managed-key read precedence is unchanged.

Tests cover migration contention and preservation of a competing writer's
changes, same-process registry/snapshot/transaction contention, expired views,
Console refusal before subprocess launch, failed deletion followed by retry,
shared-key and active-profile preservation, and reporting a cleanup failure
after a successful save. Existing import, switch, login and lock tests remain
in force.


## A51 — Persistent model controls and scheduler ownership

The Auto tab's Count model limits control (removed by A93 with the Settings
row's editor) replaces the boolean shortcut
with Off, All models, and Selected models plus Save. Both tabs use the same
editor and settings routes; Off or an empty selection unsets the override.
Reported names and unreported saved names are offered without case-sensitive
duplicates. Other names can be entered before usage has been fetched.

The Auto editor lives in static markup outside renderAuto's replaced nodes.
A focused editor, dirty draft, or pending mutation cannot have its options
or values repainted. Options rebuild only when names change. The Settings
model row retains its draft even after focus leaves and reported windows
change. Checkbox focus also participates in the settings render guard.

Every state build, GET and broadcast alike, allocates its sequence before
reading the snapshot. A completed mutation advances that same counter and
returns stateSequence. Clients discard older documents. The pending editor
remains protected through its confirming GET, with failed requests retaining
the attempted choice for retry. Sequence numbers are local to one server;
the existing session expiry/reload boundary handles server restarts.

Tray gauges, notifications, tooltips and account rows pass the actual model
names through their window calculations. They prefer the running engine's
model setting and match the names case-insensitively, including the all
sentinel. Excluded windows stay visible and are marked not counted.

TYCSWAP_AUTO_MANAGED_BY declares external scheduler ownership in the host's
environment. This is explicit configuration rather than platform-specific
process guessing. The facade refused every Start, including remembered
startup and dry runs, and exposed the owner with Available=false; the HTTP
route returned 409, and both dashboard and remote tray disabled Start (A83
replaced this: Start under an owner writes the setting). The
one-shot CLI is unaffected. Flakelab sets this owner on its dashboard service
whenever its autoswitch timer is configured. Disabling a UI control alone
would not protect old remote clients, direct requests, or app auto-resume.

Superseded by A83: Start under an external owner writes the setting instead.

## A52. The Windows tray adds its icon again after Explorer restarts

When Explorer restarts (a crash, ending it from Task Manager, some Windows
updates) it recreates the taskbar empty and broadcasts the registered window
message `TaskbarCreated` to every top-level window; a program that wants its
notification icon back adds it again. The Windows tray added its icon once,
in `Run`, and ignored the broadcast, so the icon was gone until the app
restarted while the app kept running unseen. The reference has no Windows
tray.

`Run` now registers `TaskbarCreated` before it creates the window, and the
window procedure answers it with the same add the start makes (`addIcon`),
with the current tooltip and click callback and an icon built again from the
latest PNG asked for: a `SetIcon` while Explorer was down (the update badge,
A44) failed its `NIM_MODIFY` and kept the older icon, which the re-add would
otherwise bring back. When that build fails, the icon there was is added.
The hidden window is a top-level window, not a message-only one, which is
what lets the broadcast reach it; the file's comments and A35 said
message-only and now say why it must not be. An elevated instance would not
get the broadcast (User Interface Privilege Isolation filters it without
`ChangeWindowMessageFilterEx`); the app runs unelevated, from the Run key or
a terminal, and is not changed for that. Every `Shell_NotifyIconW` call goes
through one function variable, which returns the call's error, so a failed
start names it again and the test can see what the tray asks the shell for.

**A `Quit` before the window.** `Quit` did nothing while there was no window
yet. The app and the remote tray turn a Ctrl-C into a `Quit` from their SIGINT
claim on (A48), before `Run` makes the window: a Ctrl-C in between was lost,
and the tray kept running and swallowed every later one. `Quit` now sets a
flag and reads the window handle under the tray's lock; `Run` sets the handle
under it and posts the quit when the flag is set, so none is lost or posted
twice. The Linux and macOS trays already kept a `Quit` from before `Run`.

**A failed re-add** destroyed the old HICON, which the shell may still show
(any process can broadcast `TaskbarCreated`). It now keeps it and frees the
new one. The add is not retried: a refused icon stays away until the next
`TaskbarCreated` or a restart of the app.

**Tests.** On Windows: `TaskbarCreated`, registered as `Run` registers it,
makes the window procedure add the icon once with the current tooltip and
callback and an icon built from the PNG; a tooltip update adds nothing. A
`Quit` before `Run` ends it within 5 s; a refused re-add keeps the shown icon
and frees the new one.

## A53. The log is opened for each record

The reference's `RotatingFileHandler` keeps its file open. tycswap's log is
written by the tray app (A35) and any command beside it, and the port's
handle per process went wrong twice: after one process rotated
`tycswap.log`, the other wrote on into the file it held, now
`tycswap.log.1`, and rotated by its own byte count; and on Windows, where Go
opens files without `FILE_SHARE_DELETE`, the held handle blocked the other's
rename and any delete of the log or the store directory.

Each record now opens the log, rolls it over first when the record would
bring the file's own size to 1 MB, appends and closes it. Format, limit,
backups, rollover order and lazy directory are unchanged. Two writers that
reach the limit at the same moment may both roll over and lose a backup,
possibly the newest.

**Tests.** `TestRotationByAnotherWriter`, `TestLogHoldsNoHandle`.

## A54. A state file for status lines (Go-side additive extension)

A Claude Code status line wants the live account's label and quota on every
render; running tycswap there costs a process start, and reading
`sequence.json` plus `cache/usage.json` means joining two slot-keyed files
whose identity guard lives in Go. tycswap therefore publishes
`<store root>/statusline.json`. The reference has no counterpart; there is no
command for it.

**Schema.** `{schemaVersion: 1, producer, producerVersion, writtenAt,
accounts: {"<email>|<organizationUuid>": {slot, label, usage | null}}}`, with
`usage` `{fetchedAt, pollIntervalS, fiveHour, sevenDay, scoped[{name, pct,
resetsAt}]}`, windows `{pct, resetsAt}`. Times are integer epochs. Windows come
through `oauth.NewUsage`, like every other surface. `pollIntervalS` defaults to
`usage.CandidateMaxIntervalS` when no plan exists. The label is alias >
organization name > email, stripped of control characters.

**Key.** `email|organizationUuid`, as `~/.claude.json`'s `oauthAccount` holds
them (null organization = ""); email alone is ambiguous across organizations.
A switch onto an API-key account writes that `oauthAccount` with a null
organization, so the reader's one rule finds it. No credential is an input to
the builder, so none can be published.

**Rebuilt, never patched.** The usage table and the roster are written under
different locks, so every write rebuilds the whole file from both sources
(`statusline.Publish`) under `<root>/.statusline.lock` and never reads it
back. That lock is always taken last and nothing is acquired under it; the
source reads are lock-free because both files are replaced by rename. Each
writer commits its source before publishing, so the last rebuild carries every
committed change. Write points: every `Store.WriteSequence` (roster changes, a
switch's commit and rollback) and `usage.Store`'s `SetOnChange` hook after
`Record` and `SetPollPlan`, outside the usage lock.

**Best-effort.** `Store.PublishStatusline` logs a failure or panic and
returns nothing. A write lost to a Windows sharing violation or a busy lock
heals on the next rebuild. An unreadable roster keeps the last file.
`tycswap migrate` neither copies an old store's file nor counts the new
store's own as content.

## A55. Claude Code lock parity: storage-write lock, per-lock staleness, Ctrl-C

Claude Code read-modify-writes its credential store (the credentials file or
the macOS Keychain item) under `<KS>/.storage-write.lock`, where KS is
`CLAUDE_SECURESTORAGE_CONFIG_DIR` when set (`~/.claude` when set but empty),
else the config home (`paths.GetSecureStorageHome`). Its credential-refresh
lock is `<KS>.lock`, so `CredentialsLockDir` follows KS too. An MCP token save
or a client registration landing mid-switch was lost, or wrote the previous
account back. A switch's
`withTripleLock` now takes that lock innermost, after the credentials and
config locks; both switch paths and their rollbacks run inside it.

Staleness is each lock's own, as Claude Code sets it: 60 s for the
credential-refresh lock (`AcquireCredentials`, a switch and the usage fetch),
15 s for storage-write (`AcquireStorageWrite`), 10 s for the config lock
(`Acquire`). At a flat 10 s tycswap took over a refresh lock Claude Code still
held, and both wrote `.credentials.json`. `breakStale` re-checks with the same
staleness the caller judged the lock by.

Ctrl-C exits from the SIGINT goroutine past every deferred `Release`. cclock
records held handles; the handler runs `lifecycle.RunCleanups` and then
`cclock.ReleaseAll`, last, so no lock directory outlives the process
(`sigintCleanup`). Claimed paths (`web`, `app`) unwind through their defers.

## A56. `add --login`: the scratch login's storage and the active slot

Claude Code names its credential storage, the Keychain items on macOS and
`.credentials.json` elsewhere, after `CLAUDE_SECURESTORAGE_CONFIG_DIR` when it
is set, in place of `CLAUDE_CONFIG_DIR`, and after the default `~/.claude`
storage (the unhashed items) when it is set but empty. `loginEnv` therefore
drops it beside the replaced config dir, so a scratch login can only write the
scratch profile's storage.

`--claudeai` and the `--console` refusal (A50) do not override a managed
`forceLoginMethod` of `console`, which still makes an API key, kept on macOS
in `Claude Code-<hash>` beside the login's `Claude Code-credentials-<hash>`.
Cleanup deletes both items hashed from the scratch path, and still never the
seat's unhashed ones. This amends A50's "only the OAuth item".

A login-directory add leaves `activeAccountNumber` on the live login, but a
`--slot` add can move or delete the record it names. `keepActive` makes it
follow the live identity when a migration moved it, and clears it when a
displacement deleted it, so it never names a slot now holding another account.

`tycswap run` and `tycswap env` drop the same variable for a pinned session (it is in `AuthOverrideEnvVars`), and `env` counts it as set even when empty.

## A57. `status` and `list` report a busy store lock

A roster with a record that lacks `organizationUuid` needs the org-field
backfill, and `SequenceMigrated` runs it under the store lock (e0eebfb). The
roster reads behind `status` and `list` (`buildStatusPayload`, `renderStatus`,
`BuildAccountsInfo`) discard that call's error, so when another tycswap held
the lock past its timeout they read the roster as absent: the live account
showed as "(not managed)" and `list` printed no accounts, in human and JSON
mode alike.

`reporting.Status` and `reporting.ListAccounts` now run `backfillLockError`
first: it calls `SequenceMigrated` and returns its error only when that error
is a `cerr.KindLock`, which the command reports like any other lock error.
Other errors keep their old handling, and a roster that needs no backfill
takes no lock, so the guard adds only one roster read. `statusline.json` (A54)
reads the roster without a lock and is unaffected.

## A58. The system proxy when the environment names none (Go-side additive extension)

`http.DefaultTransport` reads only `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY`; a
`tycswap app` started at login by launchd or the Run key has none, so behind a
proxy from a configuration profile or group policy every OAuth, usage, Codex
and update request went direct and failed. `internal/netproxy.Install`, called
first in `main`, sets the default transport's `Proxy`:

1. Either proxy variable set: `http.ProxyFromEnvironment`, unchanged. Its error
   quotes the raw value, password included, so it is replaced by one that
   does not.
2. Otherwise the system's settings, read once per process:
   `/usr/sbin/scutil --proxy` on macOS (absolute, so PATH cannot substitute
   it), HKCU `Internet Settings` on Windows, nothing elsewhere. Its bypass list
   and `NO_PROXY` both apply (host with subdomains, `.suffix`, glob, `<local>`,
   CIDR); loopback is never proxied; a malformed value means direct, never an
   error that echoes it. Nothing logs the proxy URL. Only the top-level
   `scutil` dictionary counts, not the per-interface copies under
   `__SCOPED__`. A running `tycswap app` keeps the setting it first read.

PAC scripts are not evaluated. The dashboard client (`cli/remote.go`) keeps its
own transport with `Proxy: nil`.

## A59. A slot backup holds the account part only

A slot stored before the A25 item 9 split kept the seat-wide keys
(`ccfile.SeatWideKeys`); the inactive refresh wrote them back and the `run`
bootstrap seeded them into its profile.

`credstore.WriteBackup` and `ReadBackup` now apply `oauth.AccountOnly`: every
slot write stores the account part, and a legacy slot read hands out the
account part while its stored bytes stay until the slot's next write. A blob
without seat-wide keys, and an API key, pass byte for byte. `.prev` is not
stripped on read; `KCReadBackup`/`KCWriteBackup` (migrations) stay verbatim.


## A60. Device appearance and visible usage freshness

The Windows tray reads `AppsUseLightTheme` whenever it opens. Dark menus draw
all rows against the same dark background; active accounts retain the brand
accent and bold system menu font. Settings/theme notifications close an open
menu whose mode changed. High contrast uses native menu rendering. Owner-drawn
rows retain their native labels and character navigation.

The dashboard defaults to CSS `prefers-color-scheme`. Settings offers explicit
Light and Dark overrides, saved in the non-secret `<brand>_appearance` cookie;
Auto clears it. A small same-origin script applies the choice before CSS loads.
This cookie carries no authentication data and grants no access.

Account rows expose usage fetch and next-attempt times. `usageRefresh` reports
the cached error classification and the later of the poll plan and failure
backoff. Retained usage can remain decision-grade after a failed fetch; the
page shows that failure beside its timestamp. This does not change quota,
rotation, retry delays, or the A31 polling policy.

## A61. Counted models must be reported by automatic targets

A target with no Fable window previously inherited its ordinary weekly
headroom, even when the active account exhausted Fable. The same target could
win on a 5h trigger or failover. Candidate qualification now requires numeric
usage for every configured model. `all` resolves to the union of reported model
names; without any names it fails closed. Turning model counting off retains
the ordinary quota policy. This overrides A34's missing-model fallback.

The guard applies before strategy ordering and proactive/at-limit/failover
selection. No qualifying compatible target means no switch. The dashboard identifies missing model data. This is conservative evidence of quota,
not a promise of entitlement: no session model is changed or discovered and no
session is restarted. The operator must keep the session's model counted.

The terminal candidate preview still ranks reported windows without this
compatibility label; its engine enforces the guard before any switch.

## A62. Compact Windows overview and shared scoped settings

The Windows tray opens a native Accounts / Settings popup on its existing UI
thread. A provider account appears once; shared and model quota columns stay
independent. Row focus reveals reset and freshness details. Sorting and focus
never switch an account. Switching requires an explicit activation
(double-click or Enter) and a destination chosen in a modal confirmation
that lists every destination, blocked ones disabled with their reason.
Nothing is preselected, so row navigation cannot carry a destination along.
Active styling is independent of menu item kind and toggle state. Only the
two font sizes are fixed (scaled for DPI); every other size derives from the
body font's line height or from measured text, so columns, buttons and tabs
fit their labels and the popup fits its columns and rows. The last column
runs to the list edge so a moved highlight repaints whole. Divider lines
separate the header, each table from its details, and the footer; detail
text sits in a frame.

The CLI shell builds the panel from the dashboard state. Local and remote
setting edits use the same serialized server mutation path as the browser.
Only an empty scope edits shared defaults; group routes validate a named
group before dispatch. Reset removes an override and restores inheritance.
Group model policy is read-only and follows confirmed sessions. Metadata
names each setting's source, runtime applicability and read-only reason.
An edit never migrates or restarts a session.

The popup retains row identity, scope and field drafts through
polls and sorting. Typed values are checked before submission and again on
the server. Successful saves reconcile with refreshed state; failures retain
the draft and show the error. The Windows app theme, system high contrast,
monitor work area and DPI govern native rendering without a browser runtime.
macOS and Linux keep their platform menus and use the dashboard for the form.

Flakelab owns scheduling, not policy values. Its one-shot checks reload shared
settings; continuous runners reload group overrides but take shared defaults
at startup. The group failed-poll counter currently starts anew each check,
so that ineffective override is excluded from group settings.

Superseded by A84: the Windows panel is one page with no tabs, settings, details, sorting or confirmation.

## A63. Human action menu and account setup

Bare invocation in a human terminal opens a searchable action menu, independently
of the tray process. `menu add` goes directly to provider setup. Agent shells,
pipes and complete CLI commands retain their existing noninteractive behavior.
TTY detection includes Cygwin terminals and rejects redirected MSYS pipes.
Automatic prompts require stdin, stdout and stderr to be terminals.

Menu actions execute the existing commands as child processes, returning terminal
ownership before browser sign-in or secret input. No credential is passed as a
command argument by the menu. The account roster is reread after selection and
identity changes refuse the action. Existing store locking and confirmations apply.

The web and tray Add account action opens that setup. A remote Windows tray targets
the WSL distro from its configured token-file UNC path. It can receive a fixed
account-setup event from the authenticated dashboard stream: only bearer-authenticated
subscribers may advertise the capability, one subscriber receives a request, and
reconnections do not replay requests. This allows a headless WSL service's browser
page to reach the Windows desktop. Without that bridge, the service reports the
missing desktop rather than claiming setup opened. New Codex accounts become
visible without restarting the dashboard.

WSL and Linux setup terminals load the user's configured login shell before
executing the menu. The fixed setup command quotes its executable path; provider
login still resolves and runs the provider binary directly. This preserves the
user's installation PATH when setup starts outside their usual terminal.
Windows and WSL callers use PowerShell's Start-Process with quoted arguments
to open a native console with terminal input/output. This avoids both Windows
Terminal's extra parsing and Go's default null standard handles for a new process.

The Windows tuning tab is Advanced. Setting descriptions and applicability notes
are concise; the source is shown once in its column. Defaults, including the 85%
5-hour switching threshold, are unchanged.

Superseded by A84 for the Windows panel, which has no Advanced tab and no setup actions.

## A64. Flakelab terminal prompts and incomplete commands

The action menu uses flakelab's pure-zsh `prompt_choose`, `prompt_input` and
`prompt_ran`, vendored byte-identically with provenance and its MIT license under
`internal/cli/promptzsh`. Both library and license are embedded in release binaries.
The adapter passes labels as arguments, captures only the answer on stdout, and
leaves the prompt on stderr. No user input becomes shell source. The existing
Go renderer remains the compatibility path when zsh is unavailable.

Before dispatch, a human terminal may complete missing required arguments for
account management and config commands. Only recognized incomplete grammars
enter this path; help, JSON, invalid flags and complete commands reach the
existing parsers. Existing argument-free defaults still execute normally.
Selection filters by provider and rereads the selected identity before dispatch.
The resulting command runs through the existing CLI with automatic prompting
disabled, preserving store locks, validation, confirmation and exit codes.
Cancelling before dispatch exits 130; no account or setting mutation occurs.

## A65. Complete guided workflows and dashboard reuse

The shared zsh menu exposes group operations and Claude/Codex import/export through
existing CLI commands. Provider-filtered account selections are checked again before
mutation; export paths resolve to files and existing destinations are refused.
The authenticated setup API accepts only fixed menu section names (main, add, manage,
groups, transfer), never shell commands. The tray receives that section in the existing
one-recipient, non-replayed setup event.

A running app publishes its loopback base URL and token-file path in a private
connection file, removed before restart. Dashboard reuse requires the matching app
lock to be held, validates the base URL, and uses the existing bearer-only launch API
and browser opener. Stale files do not count as a running app; connection failures
never start a duplicate server. The remote tray publishes separate connection metadata.

An explicit `add --login --switch` onto the current account uses the existing locked
activation path, so its new credential becomes live instead of returning already-active.
Plain `add --login` leaves the live login untouched for any other account. A login
as the current account (the slot the live credential resolves to, A101) takes the
same activation without `--switch`: the reference
names it as the repair for a dead login, the active row reads the live store, and
the renewed backup alone left that row at `no credentials` until a forced switch.

## A66. The engine keeps nothing between ticks

flakelab runs the engine as a systemd timer that starts `tycswap auto --once`
every two minutes, so every tick is a new process; the app hosts the engine in
a loop that starts again with the app; `tycswap auto` and the TUI loop in one
process. The failover strike count, the idle-hold start and the model-name
check lived in process memory and began again with each process: with the
active account's usage unknown, every timer tick reported `1/3 before
failover` and the engine never failed over.

A tick is a function of the store on disk and the clock. The engine keeps no
decision state from one tick to the next: `autoswitch_state.json` is its only
memory, and `--once`, the loop, the app's hosted engine and a dry run all run
the same tick. What a tick tells the loop about the next wait (a reset to
sleep until, the long blocked wait, the idle crawl) is returned with its
outcome, not kept on the engine.

Besides `lastSwitchAt`, `lastSwitchTo` and `quarantine`, the file holds two
additive keys; `schemaVersion` stays 1 and a malformed value reads as absent.

`"active": {"number": "<slot>", "unhealthyTicks": <n>, "idleHoldSince":
<seconds> | null}` is the live slot's failover count and idle-hold start.

- **Read:** at tick start, lock-free like the rest of the file. Anything but
  an object whose `number` is the live slot's, as a string, is no memory. A
  count that is not a number from 0 to 2147483647 (`math.MaxInt32`) reads as
  zero, and an idle-hold stamp in the future restarts the hold. While a hold
  is within `IdleHoldMaxS` no candidate is polled (05§9); an expired hold
  keeps its stamp, and candidates are polled again so the failover it falls
  through to has a target to compare.
- **Written:** when the active account's usage is unknown, as a
  read-modify-write under the state lock, before the `no-switch` event, so its
  `n/3` is the persisted count and two engines ticking at once each add their
  strike. On the token-expired branch the same write starts or keeps the idle
  hold (05§7).
- **Cleared:** when the active account's headroom is known, and inside the
  switch path's lock mutation that records `lastSwitchAt`, so a new active
  account starts at zero.

`"modelNamesChecked": "<list>"` (gone with the name check since A97, which
leaves a stored key unread) is the comma-joined, lower-cased
`autoswitch.model` list the name check (05§18) last warned or verified for.
The check completes on a tick where every switchable subscription account
outside quarantine has readable usage. When the stored value differs from the
current list, the completed check emits the `config-warning` if a name matches
no window and then records the list, so each list warns once per store rather
than once per process, and a changed list is checked again. The key is
advisory: when it cannot be written, the tick logs a WARNING and still reaches
its decision, and the check runs again next tick. An unchanged list
is never re-checked: the key is never cleared, so clearing the list or setting
`all` leaves it in place and restoring the same list later does not warn
again, and a list that once checked clean is not re-checked when the accounts'
window names change later. The compare reads the file lock-free at tick start,
so two engines that complete the check at the same moment may both warn once.
Two engines running with different lists overwrite each other's key, so each
re-checks on every tick and repeats any warning; that needs diverging lists,
for example a `--model` override on one of them, and is rare.

**Dry run:** reads the file and reports the decision a real tick would make
now, including the count it would reach (the persisted count plus one) and
the model-name warning. It writes nothing and does not simulate a sequence of
ticks: two dry ticks over an unchanged file report the same decision. Its one
memory is the model list it last checked, clean or not, so a long-lived dry
run (`auto --dry-run`, the TUI's) shows a list's warning once rather than on
every tick, and again after another list was checked in between. That memory
is presentation only: it never feeds a decision, which still comes from the
file alone, and a real engine keeps no such memory. A session group's dry
engine is built anew on every tick (`internal/cli/groupauto.go`), so it has
no such memory and a group scope's dry model warning repeats on every tick
(pre-existing).

Why: a timer deployment starts a process per tick and the app's engine
restarts with the app, so any memory outside the file is lost at the moment it
matters; one tick function for every mode leaves nothing for the modes to
disagree on.

## A67. A skipped usage fetch says why

When the active account's usage was not fetched, nothing recorded why: the
engine reported the account's usage as unknown for hours and no surface named
a cause. Every path in the active account's usage collection that returns
without a network request, and every path that discards a refresh result, now
writes a reason into the account's row in `cache/usage.json`. The row is read
by every process, so a reason recorded by the engine is the one the app and
`list --json` show.

The row gains `skipReason` (a sentence for humans, not a code) and `skipAt`
(epoch seconds). A skip writes them only when the reason changed, so `skipAt`
reads "skipped since" and an unchanged skip rewrites nothing.
The reasons:

- `owner holds an expired token; Claude Code refreshes it on next use`
- `live credential does not match slot N's backup; refresh skipped`
- `refresh discarded: an owner appeared mid-refresh`
- `refresh discarded: live credential changed mid-refresh`
- `refresh discarded: <error>` when persisting the refreshed credential failed
  under the lock
- `no credential in the live store`
- `credential owner unknown: <detail>`
- `expired token could not be refreshed` or `expired token could not be
  refreshed: <error>`
- the existing sentinel text, `keychain unavailable` or `re-login needed`, when
  the Keychain cannot be read or the token is quarantined as dead

Only the active account's skips are recorded; an inactive slot's skip stays
unrecorded. Any attempted fetch, successful or failed, removes both keys:
`skipReason` says why no usage was recorded and `lastError` says why a request
failed, so the two never describe the same moment.

The log records each change, never every tick: a WARNING `Usage for account N
not fetched: <reason>` when a reason is set or changes, and an INFO `Usage for
account N fetches again` when an attempt clears one.

The surfaces, all additive:

- `list --json` and `/api/state` account rows carry `usageRefresh.skipReason`
  and `usageRefresh.skipAt` (ISO-8601 UTC), each only when set.
- The tray's account details line reads `Usage: <status> · <reason>`; it is
  still shown only when the status is not `ok`.
- The dashboard's usage chip shows the reason in place of the status text it
  would show otherwise: `Sign in again to restore usage`, `Waiting for Claude
  to refresh its login`, `refresh failed` or `refresh rate-limited`. The check
  time before it and the next attempt after it stay, the chip keeps its
  warning style while the row carries a `lastError`, and its tooltip starts
  with the full reason.
- The engine's `no-switch` details under `active-usage-unknown` and
  `active-idle` end in ` · <reason>` while the active account's row carries
  one, for example `1/3 before failover · owner holds an expired token; …`.

Superseded by A84 for the tray: the panel shows the status as the row's warning; the dashboard keeps the reason.

## A68. The live slot follows the credential

A running Claude Code rewrites `~/.claude.json` from memory. With two slots of
one login in different organisations, a switch between them can leave the
file naming the old organisation while the credential file holds the new
slot's token, and every surface then showed the wrong account as live.

`Store.ResolveLiveSlot` decides the live slot. It maps the config identity to
slot X as before. A config identity that maps to no slot stays unmanaged
whatever the credential is, and an API-key X stays live. When X is a managed
OAuth slot and the live credential can be read, the first of these that
answers names the live slot, and `LiveSlot.Source` says which one did:

1. `backup`. A slot holds the live credential's lineage when its stored
   backup has the same refresh-token fingerprint, or the same account block
   once the seat-wide keys are set aside. X holding it keeps X; exactly one
   other managed OAuth slot Y holding it makes Y live. More than one other
   slot holding it (a copied backup) or an unreadable backup is no evidence.
2. `chain`. The chain record `live_lineage.json` in the backup root
   (`{"schemaVersion":1,"fingerprint":"<sha256>","slot":"5","identity":
   {"uuid","email","orgUuid"},"seenAt":<seconds>}`, 0600) holds the profile
   endpoint's last answer. It answers when its fingerprint is the live
   credential's and the roster still maps its identity to its slot, as step 3
   would. A slot removed or re-added under another uuid makes it stale: it is
   ignored, and the next profile hit overwrites it. A missing or malformed
   record is absent.
3. `profile`. The live access token goes to the OAuth profile endpoint, and
   `Store.SlotForIdentity` maps the identity it returns to a slot, the mapping
   `classifyOutgoing` uses: email and organisation unless both sides carry
   different uuids, else the organisation-scoped uuid. A managed OAuth slot
   other than X needs the roster's uuid to agree, the switch path's cross-slot
   rule. A hit is written to the chain record, atomically and only when the
   record changes, without a lock: last writer wins. A process asks once per
   live fingerprint, a miss included, so an offline box does not ask every
   pass (a switch's provenance asks again after a miss, below). It never
   asks while it holds the store lock, and never without an OAuth client; it
   then uses an answer it already has.
4. `config`. Otherwise X.

The chain record is the default login's; the Fable store keeps its own,
`fable/live_lineage.json` (A100). Only a resolution of the default
login reads or writes it: the default scope's `ResolveLiveSlot`, and the
ownership and handoff checks of the default login from any scope. A group
scope resolving its own login goes backup, profile, config, so its rotation
never overwrites the default login's record; it asks the profile once per
process until its own pass harvests the rotation.

`LiveSlot.Identity` carries the account the chain record or the profile
endpoint named, for those two sources, and nil otherwise.

The backup step alone is the earlier rule. The other two exist because a
running Claude Code also refreshes its token itself: the refresh token
rotates, no backup holds the lineage any more, and the resolver fell back to
X, the incident's conditions again. The chain record keeps the answer for
other processes and offline runs.

Each time the resolver overrides the config it logs a WARNING: `live config
names slot X (<email>, <org>) but the live credential is slot Y's (by
<source>); using Y. A running Claude Code may have rewritten ~/.claude.json`.
It names no certain cause: a switch writes the credential before the config,
and a resolve between the two writes sees the same mismatch. A process logs it
once per (X, Y) pair, however often it resolves or alternates between pairs.

A usage pass that may fetch the active slot (an OAuth client, and `fetch` nil
or naming it) first harvests. When chain or profile named the live slot and
that slot's backup has another fingerprint than the live credential, the live
credential's account part is written into the backup, under the store lock and
Claude Code's credential and config locks, after re-checking under them that
the live credential and the slot's record are unchanged; a slot with an
uncertain owner is left alone. It logs INFO `Harvested the rotated live
credential into Account-N's backup`. The live credential is never written.
From the next pass on, the backup step attributes the credential, offline too.
The harvest takes the pass's resolution and the live credential read for it,
so a pass reads the live credential once outside the lock, plus the re-check
under it when it harvests.

A dry-run engine (`auto --dry-run`, the TUI's and the dashboard's dry runs,
and the group scopes a dry tick runs, whose statuses resolve the default login
as well) and a store-only read (a fetch set that wants nothing: the engine's planning
read, the menu's and the dashboard's snapshots) resolve the same way but never
write the chain record and never harvest. They may still ask the profile
endpoint, and keep its answer in memory only.
`CurrentAccountNumber`, `list`, `status`, the tray's active row, the engine,
`switch`'s current slot and the switch-to already-active check all use the
resolver.

Credential ownership follows the same rule. The default login owns the slot
its credential's lineage names, and that claim is certain. It does not own
the slot its config names, so that slot is free like any other inactive slot.
The default profile's own config and credential drive the resolution, so a
group-scoped store sees the same owner. While the config named X, the default
claim on Y read as uncertain, so Y's usage was never fetched (`credential
owner unknown`), and after three ticks the engine failed over from the
account in use. Now Y's usage is fetched with its live credential as the
active slot's, and refresh, list and recovery see Y as the default login's.
Reading the owned credential re-checks that the default login still resolves
to Y, where it used to compare the config identity.

A normal switch backs up the resolved slot, not the config's. Taking the
outgoing slot from the config hid the mismatch while destroying a backup: a
switch onto X found a live credential that was not X's. When the profile
endpoint could not say whose it was (no network, an expired live token, the
bytes changing under the lock), the switch fell back to the pre-fix backup and
wrote Y's credential into X's slot, then installed that copy. The live login
stayed Y's, the config still named X, X's own credential survived only in
`.prev`, and the result read `already-active`. Now the switch backs the live
credential into Y, where it classifies as Y's own, and a switch onto X installs
X's stored credential and identity. The config backed up for Y keeps Y's own
`oauthAccount`, since the live config names X's. `switch --force X` still
activates X directly, without backing up an outgoing slot, and writes what it
wrote before. Its result now names the credential's slot: `from` is Y and
`switched` is true, since the live credential moved from Y's to X's.
A live credential the profile endpoint attributes to Y is Y's in the same way:
where the switch used to stash it as foreign to X, it now backs it into Y. The
foreign stash remains for credentials the resolver cannot attribute.

The switch's pre-lock provenance takes `LiveSlot.Identity`. An outgoing slot
the chain record or the profile attributed therefore classifies as
`own-rotated` and logs `Backed up account N`, not the `ownership could not be
verified — pre-fix backup` line. The provenance reuses the resolver's hit and
does not ask again. The miss memo gates the resolver only: when the resolver
named no account, the provenance asks the profile endpoint again before the
lock unless the miss is younger than a minute, so one switch asks an
unreachable endpoint once. One transient miss in a long-lived process would
otherwise disable the foreign-credential guard for that fingerprint until the
token rotated, and the switch would back the live credential into the
config's slot as unresolved. A hit from that question replaces the miss in
memory, and the next resolve records it in the chain record.
`switch --force` runs the same pre-lock provenance. Under the lock the
resolver asks no one, so without it a credential only the profile attributed
read as X's, and `--force Y` activated Y's older backup over the live token.

`switch Y` while the mismatch stands is a config repair, and so are `switch
--force Y` and a rotation that lands on Y. Before, `switch Y` reported
"Already on" and left the config wrong, and the ownership gate refused
`--force Y`.
The repair runs the normal switch under the triple lock. Step 1 backs up Y as
usual. The credential and Claude Code's `settings.json` are left alone, and
Y's stored `oauthAccount` goes into `~/.claude.json`, which keeps every other
key, and `activeAccountNumber` becomes Y. The human line is `Repaired the live
config for Account-N (<email>)`. The JSON result is `switched: true`, `reason:
config-repaired`, with `from` and `to` both Y. The log records `Repaired the
live config for account Y (it named account X)` instead of the switch-history
line, because nothing switched. The repair passes the ordinary ownership gate,
because the default login's claim on Y is certain. A group or session profile
that claims Y as well still refuses it, as conflicting owners.

The default-to-group handoff follows the same rule. Committing or rolling back
a handoff checks that the default config, read with the credential the handoff
took, still resolves to the target slot. It used to compare the config identity
with the target. Under a mismatch, a group can take Y from the default login:
the default credential and identity are cleared, and the group's claim becomes
certain. A rollback puts the default login back as it was, mismatch included.
Before, both the commit and the rollback refused, which left the journal and an
uncertain group claim behind. A group switch onto X is a plain switch from X's
stored backup, because the default login does not hold X: no default handoff
takes place, and the default login is left untouched.

Unchanged on purpose: an unmanaged config identity resolves to no slot, so
the unclaimed-credential and displaced-login capture keep working as before.
The repair does not stop a running Claude Code from writing the old identity
back. The next resolve warns again only in a new process.

## A69. A group releases its account

A group keeps its account in `groups/claims.json` and its `active.json` after
its sessions end, so the default login's rotation and the other group skip
it, even while the default account sits at its limit. `groups release
fable|opus` ends that claim, and A74 ends an idle group's claim on its own.
Sharing one account between a running group and another profile stays
refused: each refresh rotates the refresh token, and the profile that
refreshes second loses the login.

The release runs under the shared account lock and the group profile's
Claude locks, after reconciling an unfinished switch. It refuses while the
group's profile has live sessions, and while its config names an account
other than the recorded one. The profile's credential must be the account's
own lineage (`own-bytes`, `own-family` or `own-rotated`, as for an outgoing
group switch); its account-only part replaces the backup. The profile keeps
its MCP sign-ins (the seat-wide part) and loses `claudeAiOauth` and
`oauthAccount`; the claim and `active.json` go last, so an interrupted
release reruns to the same end. A group holding nothing reports
`released: null`. A profile left with only seat-wide keys holds no login:
neither the ownership scan nor the next group switch reads it as an
unrecorded credential.

## A70. Automatic account routing for Claude model requests

`tycswap claude` prepares a managed Claude process and loads its bundled Mods
module. The initial model selects a Fable or Opus/other credential profile;
subsequent model changes retain that profile and the running conversation.
Separate profiles retain separate account ownership. A routed resume restores
its saved profile and last successful main model unless a model is explicitly
selected. Existing group resumes recognize routed histories.

The launcher verifies the native version and runs the bundled offline mod
checks in the target profile and working directory before claiming an account.
Startup uses startup compatibility, including resumes that create a new native
process. It records a launch reservation before executing Claude. The mod's
`session.start` registers the actual conversation against that launch and
consumes the startup request reservation. Registration is idempotent for the
same process, launch and conversation; another conversation cannot replace an
active one without unregistering it first.

`routing.json` holds versioned `sessions`, `leases` and resume `history`, without
credentials. `routing.lock` serializes registration, selection, account changes
and request reservations. It precedes the shared account lock and the existing
Claude credential/config/storage locks. Usage polling happens outside those
locks; selection and dispatch revalidate ownership and liveness inside them.
Atomic writes preserve complete state records. Dead processes are pruned; a
live process's request reservation has no expiry timer.

Before each `turn.step`, the mod acquires a unique request reservation through
the internal JSON routing command. Selection requires fresh shared usage and
the applicable model windows, compatibility with the profile's current models,
and clear credential ownership. Ordinary accounts take precedence when Fable
is unnecessary; quota headroom and configured hysteresis select within that
class. An idle profile's account can be released and transferred through the
existing ownership protocol. Unknown or unusable candidates stop dispatch.

Concurrent requests share the held account when it remains eligible. A
different account cannot replace it until the response streams finish and their
reservations are released. Manual switching, automatic rotation and group
release use the same guard. Failed release leaves the reservation in place and
blocks further requests in that mod instance. Session changes cannot discard
pending requests. Purge rechecks managed and native liveness after confirmation
under the routing/account locks, and retains the empty lock files during
deletion.

Fable subagents require a Fable main model. Native team teammates, transparent
fallback models, custom identity/plugin settings and routing-variable overrides
are refused. A profile containing unmanaged live sessions or IDE instances
cannot participate in automatic routing. Main/subagent requests still use the
same physical credential profile.

Callback errors block dispatch through the mod's error handler. Native worker
unload is a separate boundary: Claude can continue on the last account without
the mod after its worker is unloaded. Restart through the managed launcher
after that event. The native crash characterization and production integration
tests record these behaviors using synthetic credentials and a local fake API.
A separate transport request gate remains a possible future extension; this
implementation does not install one.

The native runtime can run installed user mods after the command-line routing
mod. Model-rewriting and dispatch-replacing mods are unsupported alongside this
router: changing the request after acquisition invalidates its model/account
check. Regular plugins and observational mods remain supported.

## A71. The Opus / other group leaves Fable-capable accounts to Fable

The Opus / other group never needs Fable, and Fable-capable accounts are the
scarce ones: the Fable group and a Fable session on the default login can run
nowhere else. The group's rotation (`tycswap auto`, the flakelab timer) ranked
targets by their bars and headroom alone, so a failover from the group's
account landed on whichever account had the most room, Fable-capable or not,
and stayed there until that account hit a bar. `tycswap claude` already
preferred accounts that cannot run Fable for Opus/other requests (A70); the
rotation now does the same.

The engine takes a reservation seam (`WithReserved`): the Opus / other group's
store names every account whose recorded or plan-derived Fable `continue`
entitlement allows it, the same test routing applies, and every other profile
names none. A reserved account sorts after every unreserved account of its bar
tier under both strategies, so it is taken only when nothing else qualifies.
When the group's active account is reserved and every window is under its
bar, the tick moves with the trigger `reserved` to an unreserved account that
is below every bar, under the proactive cooldown, naming no window because
the account decided, not a window; with no such account the tick reports
`reserved-no-target` and stays, as it would without the reservation. At-limit
and failover moves keep their triggers: the reservation changes their order,
never their occurrence.

Unchanged on purpose: the default login reserves nothing, because the
operator's own session may need Fable there; the Fable group reserves
nothing, because every account it may use can run Fable; the group's first
account is still chosen by hand (`groups switch`, the menu, the tray); and an
account is never released from the group while a routed request holds it,
since the hand-over is an ordinary group switch under the routing guard.

## A72. Ultra and optional five-hour limits

Anthropic renamed Max 20x to Ultra and Max 5x to Max on October 30, 2026.
The [published plan](https://claude.com/pricing) removes the five-hour limit
for verified Ultra accounts, with removal staged per account. Weekly usage
and model entitlements remain. Plan names therefore establish Fable
compatibility but never determine which usage windows count.

Routing and recovery (since gone: A78, A95) require usable weekly usage and
validate a five-hour window only when reported. An omitted or null window is absent; a reported
window with unusable utilization is unknown and cannot authorize dispatch.
All reported windows retain their existing thresholds and model checks.
`groups.Entitlement` recognizes `ultra` alongside the legacy Max identifiers,
with recorded capability overrides retaining precedence.

The normalizer preserves malformed non-null five-hour data as unknown instead
of dropping it. Cache replacement, the auto-switch engine and rendered
surfaces already support absent windows. A successful weekly-only refresh
removes a cached five-hour window. Failed refreshes retain the previous
measurement under the existing freshness policy. JSON continues to omit an
absent window, while `statusline.json` publishes null; neither reports it as
zero percent or promises unlimited weekly usage. Codex policy is unchanged.

## A73. A login without a counted model's window hands over

A61 made targets report every counted model, but the active account was still
judged only on the windows it reports. A Pro login has no Fable window, so
with `autoswitch.model Fable` it read as below every bar and the tick stayed,
while the Fable sessions on it could not run (2026-11-04, account 2).

When the active account reports usage but no window for a counted model that
another account does report, the tick moves with the trigger `model-missing`.
Like `reserved` (A71), it names no window, runs under the proactive cooldown,
and lands only on an account that reports the model and is below every bar;
without one it reports `model-missing-no-target` and stays. A counted name no
account reports is a configuration problem, warned about by the model-name
check, and changes nothing here. Unchanged: an active account that reports the
model is judged on it as before, and `all` without reported names never
triggers.

## A74. An idle group gives its account back

A69 kept a group's claim after its sessions ended until someone ran
`groups release`. On 2026-11-04 the Opus / other group had held account 2 for
six days. Nothing refreshes an idle group's login: tycswap's own refresh skips
claimed accounts, and only a Claude process in the profile renews its token.
Its access token expired on 2026-10-29, after which every ownership check got
a 401. When the operator logged the same account into the default profile and
added it, the account had two owners: usage was held, the engine skipped it,
and `groups release` refused, because the group's credential matched neither
the newly stored backup nor a resolvable identity.

Three changes close that path. The scheduled group tick releases an enabled
group with no live sessions once its files show no activity for the group's
`autoswitch.cooldownSeconds` (session starts and exits, launches, switches and
token refreshes all touch them). Under the routing lock it also requires no
live or launching routed session, so a launch, which reserves its routing
session under the same lock that hands the group its account, never loses the
credential before Claude registers. Before verifying ownership, every release
renews a group login whose access token has expired, under the account lock
and the profile's credential lock, only while no Claude process runs in the
profile, and stores the rotated credential at once; a refresh the server
rejects leaves the login and the claim as they were. And `add`, from the live
login or a login directory, refuses an account a group claims, naming the
group, so a second login of that account can no longer replace the backup the
group's lineage is verified against. Unchanged: a release still requires the
group's own lineage, keeps its MCP sign-ins and drops the claim last; dry
ticks release nothing; legacy `run` profiles keep their own claim rules.

## A75. A session's status line can bring the next tick forward

The engine samples: the flakelab timer ticks every two minutes and the active
account's usage is fetched at most every three minutes outside the escalation
band. On 2026-11-04 account 2's five-hour window went from 42% to 100% within
356 seconds; the engine saw 42% twice (the second a cached reading) and 100%
seven seconds after the operator had switched by hand.

Claude Code passes the subscription's `rate_limits` to the status line after
every response. `tycswap auto --observe` reads that JSON on stdin. When a
five-hour or weekly window is within `EscalationMarginPct` (15 points) of its
bar, it marks the active account's usage poll due in the usage store and runs
one `--once` tick, which fetches it at once instead of waiting for the plan; a
spend limit and an absent window (Ultra accounts have no five-hour one) never
count. The mark goes through the store, not the engine: the collector fetches
only a row whose plan is due or whose reading is older than `ServeTTLS`, so
v0.8.4's engine-side nomination served the cached reading (2026-11-04 12:54,
a 137-second-old 91%), and a claimed fetch or a dead token still holds. One observation per
15 seconds passes across every session (a lock and a stamp in the backup
root): at the measured burn of at least 9.8 points a minute that leaves at
most 2.5 points between readings. The observation only triggers; the decision
uses the fetched usage, so a status line still showing the previous account
after a switch costs one fetch and no move. A backoff or a 429 within
`Post429MinIntervalS` leaves the plan alone. A status line running in a group
profile marks that group's account instead. Codex is not ticked from a status line. The path below the
margin parses and exits (12 ms measured).

## A76. The default login counts the models its sessions run

A group's model limits follow its live sessions; the default login counted
only `autoswitch.model`, which the operator set by hand. Counting Fable there
protects a Fable session but also refuses every target without Fable, so Opus
sessions stop at a weekly limit while an account without Fable has room.

`--observe` also records, for a status line in the default profile, the model
family its session runs (`default_models.json`, rewritten at most once a
minute per unchanged session, at once on a change, kept a day). A scheduled
tick (`auto --once`, as the timer, the quota hook and `--observe` run it)
counts, while `autoswitch.model` is unset, the reported per-model windows of
the families its live sessions run, matched against the default profile's
running session records, as `GroupSettings` does for a group. An explicit
`autoswitch.model` or `--model` always wins (until A97 removed both, so the
sessions always decide), and without records nothing
changes. A long-running `tycswap auto` loop and the dashboard's hosted engine
keep their configured models (since A97 none: they count 5h and 7d only). `config set autoswitch.model none` is refused
with a pointer to `unset`, and a stored `none` or `(none)` counts nothing,
since the help shows the unset value that way.

## A77. The default login follows Fable without being asked

A71 reserved Fable-capable accounts only in the Opus / other group, and A73's
hand-over waited out the cooldown. On the default login, where the operator
runs every session, that left two manual steps: a Fable-capable account
served Opus work until a bar moved it, and a Fable session started within five
minutes of a switch onto a login without Fable could not run until the
cooldown ended or the operator switched by hand.

The scheduled tick (`auto --once`, as the timer, the quota hook and
`--observe` run it) now builds the default engine with a reservation taken
from the same records A76 counts. While `autoswitch.model` is unset and at
least one default session runs, every live session has a recorded family and
none of them is Fable, an account whose plan or recorded `continue`
capability allows Fable (`Store.FableCapable`, the test A71 applies) is
reserved: it ranks last, and an active one hands over with the `reserved`
trigger, under the cooldown, to an account that cannot run Fable and is below
every bar. A live session without a record or a Fable session clears the
reservation, as does an explicit `autoswitch.model` (until A97 removed it);
with no live session the login stays where it is, because nothing burns.
While the Fable store is usable the reservation is A102's instead: always,
and only of the accounts the Fable store can use and its live account.

`--observe` reports whether its record was news: a session's first status line
or a change of family. News runs one tick at once on the cached usage, without
the 15-second fetch slot, and `model-missing` is no longer held by the
cooldown, since the sessions that run the model cannot run at all until the
login moves. The two hand-overs cannot alternate: `reserved` needs no live
Fable session and `model-missing` needs a counted one, and `reserved` keeps
the cooldown.

Limits, accepted: default sessions share one login, so one live Fable session
keeps every default session on a Fable-capable account; subagents run on
their main session's login and only the main model is recorded; a session
that draws no status line is never recorded and so holds the reservation off;
the first request of a Fable session started on a login without Fable can
race the tick its first status line runs; the loop and the dashboard's hosted
engine keep their configured models and reserve nothing.

## A78. Session groups and `tycswap claude` retire

A76 and A77 made the default login follow the models its sessions run, so
the operator starts plain `claude` and never picks an account. The Fable and
Opus / other session groups (A42, A62, A69, A71, A74) and the Mods router
(A70) existed to give Fable and Opus work different accounts by hand or per
request. What they still added is two accounts in use at once (Fable on one,
Opus on another) and per-request account choice with stream reservations; the
operator gave both up for one automatic login. Total weekly quota is the same
either way, since windows are per account; parallel throughput is what goes.

Removed: `tycswap claude`, the internal `routing` command, `routing.json`,
`routing.lock` and the bundled routing mod; `run --group`, the group launcher
and its hooks; every `groups` verb (switch, release, reconcile, config,
capability, list); group settings scopes, the group engine ticks and their
poll policies; the menu, tray and dashboard group views and endpoints; group
switching and its default and legacy handoffs. A switch no longer takes the
routing lock. The `group-release` event stays for the retirement.

Existing group state retires on its own. Each scheduled tick that is not a
dry run (`auto --once` from the timer or the quota hook, the loop, the hosted
engine; not a status line's `--observe` tick, which is about latency) asks,
per group, whether a claim remains or the group's directory is not yet marked
retired (`groups.Pending`). While a session runs in the group's profile it
waits. Otherwise it runs the A69 release under the account lock and the
profile's locks. An unfinished group switch is reconciled first, taking the
default login's or a legacy profile's locks when its journal recorded a
handoff from them; its credential is matched by refresh token, so an expired
login is refreshed (A74) only on a tick without a journal. The profile's
credential must be the account's own lineage before it replaces the backup,
with one exception: a login whose refresh the server rejects with
`invalid_grant` can never authenticate again, so it is dropped with its claim
and the backup keeps its own lineage. Then, with no session started since,
the profile's `projects/` conversations are streamed into the default profile
where missing, nothing there replaced and no symlink followed, and every file
beside `profile/` goes, with `retired.json` left as the marker. The profile
keeps its history and MCP sign-ins. Every step can rerun, so an interrupted
retirement finishes on a later tick. A login that cannot be verified for any
other reason (offline, an identity that does not resolve) keeps its claim: the
ownership scan still reads group claims, journals and profile credentials, so
no rotation takes the account, `add` still refuses it, and the warning names
it on each scheduled tick. A journal that recorded a handoff from the default
login reconciles only while no default session runs, as before.

Still-running group sessions keep working: `groups guard` (their
PreModelSwitch hook) and `recovery record --group` (their lifecycle hooks)
still answer (since A95 the hook records nothing and exits 0), and their
status lines mark the group's account due (A75).

The Claude side of the Claude / Codex handover now uses the default login:
its account checks follow the model class (`FableCapable` for Fable), a
rotation switches the default login, and a Claude destination is plain
`claude --model`. A recorded incident from a group session says the group is
retired. (A95 removed the handover.) Settings have one scope; a scoped save
is refused.

Kept on purpose: `groups/claims.json` also records the legacy `run` profiles'
claims, so the registry, its validation and `SafePath` stay; a recorded
`fableContinue` capability still counts for `FableCapable`. The retirement
code, the group scope of the store and the ownership scan's group branches
can go once no store can hold group state.

## A79. Plan names, and the organization nobody named

Nothing told the operator which plan a stored login is on: the dashboard,
the panel and `list` showed `oauth` and the organization name, and on
2026-11-09 the question whether an account "counts as Ultra" had no answer
on any surface. Claude Code records the plan it was told at login in its
config (`oauthAccount.planDisplayName`, "Ultra" for the account in question,
whose credential still says `subscriptionType: max`). The plan name is the
display truth; which windows apply still follows the reported usage (A72).

`add` records `plan` on the roster record from the login's config, a refresh
in place rewrites it, and a switch rewrites the outgoing account's from the
config it backs up. A record from before the key read its config backup (the
live config for the live login) on each read, and nothing writes the key
outside those paths: a roster a no-op command touches stays byte-identical.
`list --json` and `status --json` rows carry `plan` when it is known, the
dashboard shows it as a chip beside `oauth` (a Codex row shows the plan its
usage reports), the panel appends it to the row title, `list` appends it to
the organization tag, and `statusline.json` publishes it as `plan`.

Claude.ai names a personal account's organization "<email>'s Organization".
Shown as an organization it is noise, and as the status line label it put
the email in twice (`@you@example.com's Organization`). Every display
treats that exact name (either apostrophe, any case) as no organization:
the tag is `personal`, the dashboard row shows the email alone, and the
status line label falls back to the email. `organizationName` in JSON and
the roster keep the recorded value; identity and dedupe never changed.

## A80. Windows at a glance

Two rendering choices hid what A72 established. The dashboard drew the same
dashed "no data" meter for a window Anthropic did not report as for usage it
could not measure, so a verified Ultra login looked broken next to one still
carrying a 5-hour limit. And the Windows panel showed percentages only; the
reset countdown, the fetch time and the status sat in the details box under
the selected row, so reading any account meant clicking it first.

An absent window renders as a muted dash with its label and an explanation
on hover; the "no data" meter remains for usage that is unknown or stale,
and for a window reported without a usable share. The 5-hour column is left
out while no listed account reports one, as the model column already is
without counted models. Each panel cell carries the share and, when known,
the short time to the reset ("55% · 8m", "15% · 5d 5h"); sorting compares
the share before the dot, the details box keeps reset wording, fetch time
and status.

Superseded by A84 for the Windows panel: each limit's reset sits under its bar; no details box, no sorting.

## A81. Setup opens in Windows Terminal

**Add account…** and **Interactive menu…** opened the guided setup through a
hidden PowerShell `Start-Process`, which allocates a fresh console. From the
tray, started by the logon task, that console is the legacy console host even
where Windows Terminal is the default terminal: the operator saw a cmd-style
window every time (2026-11-09).

When `wt.exe` is on the PATH, the setup runs `wt.exe -w 0 new-tab --title
"<name> setup" <launcher> <args…>`: a new tab in the most recent Windows
Terminal window, under the default profile's look, or a new window when none
is open. Windows Terminal splits every argument at `;` and cannot carry an
embedded double quote (measured 2026-11-09: the `sh -lc 'exec "$SHELL" -lc
"$1"'` wrapper never ran), so the distro is entered through WSL's own login
shell instead: `wsl.exe --distribution <distro> --shell-type login -- <exe>
menu <section>`. wsl.exe quotes each of those words for the shell itself, so
argument boundaries survive and a quote in a word arrives literally (also
measured); the executable is therefore passed as is. On Windows itself the
tab runs `<exe> menu <section>`. The Start-Process path with the sh wrapper
remains for a machine without Windows Terminal. The tray's own hidden window
is not involved. The earlier setup design chose Start-Process to keep shell
text away from Windows Terminal's parser; the tab keeps that rule by handing
wt.exe no shell text at all.

## A82. A login is stored when it is complete

Claude Code ends `auth login` with "Login successful. Press Enter to
continue…" and keeps running until that Enter. `add --login` stored the
result only after the process exited, so an operator who closed the setup
window at that prompt lost the login: both processes ended with the console
and nothing was stored (2026-11-09; it "stuck" only when the menu was closed
through its own Close entry, because that path had pressed Enter).

While the login runs, `add --login` checks the scratch profile twice a second
with the same completeness test as before (a subscription credential and the
OAuth identity in the profile's config). As soon as it passes, the account is
stored, and the login process is ended with SIGTERM, which Claude Code leaves
on cleanly; `--switch` then switches at once, so the window can close at any
point after the browser flow. Where a signal cannot be sent (Windows), the
command says to press Enter and waits. A process that exits first is handled
as before: its result is stored when complete, otherwise the login is
reported incomplete. Scratch cleanup still runs on every exit path.

## A83. Auto-switch is a setting

On/off used to mean "does a scheduler exist". The app remembered its own
choice in `ui_prefs.json` (A43), which only the app read, and where an
external timer runs `tycswap auto --once`, A51 refused every Start, so the
tray and dashboard toggle was locked and nothing on disk could say off.

The rule: one key, `autoswitch.enabled` in `settings.json`, default off
(seeded once from the app's old remembered choice).

- Every engine reads it at the start of every tick (the timer's `--once`,
  the loop, the app's and the TUI's engine). Off, a tick asks the switcher
  nothing, reads and writes no state and emits one `no-switch` `disabled`
  event; `--once` exits 2. A dry run says it is off in a `config-warning`
  and decides as usual.
- A real `--once` that finds it off says so without building an engine: no
  process scan for the session policy, no Codex engine. A foreground
  `tycswap auto` (loop or `--once`) prints `Auto-switch is off; tycswap auto
  on turns it on.` once on stderr, and the loop's banner shows only while it
  is on.
- The Codex tick runs only while it is on, checked on every round in
  `tycswap auto` and in the app's loop; a dry run still ticks Codex.
  Retiring a former session group (A78) never moves the default login, and
  `add` tells its user the next tick does it, so it runs whatever the key.
- `tycswap auto --observe` while off records the session's model (A77),
  marks nothing due, runs no tick and exits 2 with the same `disabled` line;
  with `--dry-run` it decides as any dry run does.
- `tycswap auto on|off` write it, `auto on --if-unset` writes only an absent
  key (a provisioner's seed that never overrides a user's choice), and
  `auto status [--json]` reads it.
- With no external scheduler, the app's engine is a host the key gates: the
  app hosts it from launch whatever the key (it idles while off, every tick
  reporting `disabled`), so a shell's `tycswap auto on` or a save in Settings
  takes effect without the app. Start makes sure a real engine is hosted and
  only then writes on, waking the engine so it ticks at once (the Codex loop
  follows at its next round); Stop writes off and returns at once, leaving
  the engine hosted. A dry run writes nothing: while the key is off it takes
  the idle engine's place and Stop gives the host back; while the key is on
  it is refused, and so is a Start beside it (A93 removes the dashboard's dry
  run). Each write is one INFO log line.
  Quitting the app or `tycswap web` ends the hosted engine without writing.
  The view's `running` is a hosted engine that is a dry run or has the key
  on; `hosted` says an engine runs in this process, and model retargets
  (a model save; the tray's Model limits until A94) go to it whenever it is hosted.
- An external scheduler no longer locks the toggle. `TYCSWAP_AUTO_MANAGED_BY`
  now only means "do not host an engine here": Start and Stop write the key
  and start nothing, the view's `running` is the key, its `owner` names the
  scheduler, `hosted` is false, `available` is always true and `managedBy`
  always empty (kept for older clients). The controls of a hosted engine do
  not fail there: a model save has nothing to retarget, Wake nothing to
  wake, and the live 7d bar says to change the saved setting; the page turns
  Wake and the slider off, names the scheduler on the State tile, and keeps
  the Settings tab's scheduler notes. A dry run is offered there beside the
  scheduler's ticks. (A93 removes Wake, the slider, the dry run and the State
  tile; the heading says `checked by <owner>`.)

Supersedes A43's `ui_prefs.json` choice and A51's refusal of Start under an
external owner. Closes two review findings: "off" existed only inside the app
process, and a remembered "on" survived a managed period and resumed unasked.

## A84. The tray panel is the daily view

On 2026-11-11 the operator could not find where to turn auto-switch off: the
Windows panel's bottom line said "Auto-switch on" as plain text, and its two
switches were drawn only on the Advanced tab. They do not want settings in
the tray, and do not want to click an account to see when its limits reset.
They approved this design from a picture.

The rule: the Windows panel is one page.

- A title, the brand name. A list with a header row: `Account`, `5-hour`,
  `Weekly`, one column per model window any row reports (`Fable` first),
  and an unlabeled action column. One row per account, Claude rows first,
  then Codex, each in slot order.
- The account cell has two lines: the name (`#2 name`, `Codex #1 name`),
  semi-bold, cut with an ellipsis past the column's cap of 36 units (about
  34 average characters); then the plan in an outlined label, `not
  rotated` (muted) for an account excluded from rotation (on the Fable
  store's account a second outlined label `Fable`, A104), and in the limit
  colour, in the dashboard's words, a reading that failed (`token
  expired`, `re-login needed`, `usage unavailable`, …; none for an API-key
  account, which reports no usage by design).
- A limit cell is the share and a thin bar, and small under them that
  limit's own reset as clock time and time left: `15:04 · 3h 52m` for the
  5-hour window, `Today 15:04`, `Mon 15:04` within six days and `Mon 2 Jan`
  after that for the others (English names, local time). No reset known,
  a window that has not started or a reset already past: no second line.
  The share is in the text colour and the bar's fill in the accent; both
  take the limit colour when the share as shown (`97%`) is at or over the
  window's auto-switch bar (`autoswitch.fiveHourThreshold`,
  `sevenDayThreshold`, `modelThreshold`; for a Codex row the Codex engine's
  one bar). A muted dash stands where the
  account reports no such window, and in `Fable` for a plan
  `groups.Entitlement` knows cannot run it (Pro, free); an unknown plan
  shows what is reported.
- The action cell is a Switch button, disabled for an account that cannot
  be switched to; the active account shows `In use` in the accent instead.
  A104 adds `Use for Fable` beside it on every row the Fable store can take.
- One note line under the list: the last panel action's result or error,
  whichever button it was, or while the engine is out of reach why. Then
  Switch and `Auto-switch` are disabled; `Start at login` and the updates
  act on this machine and stay usable. Before the first state the list says
  `No data yet`.
- The footer, one row while the width holds it: `Quit` and, while one
  waits, `Update <brand>` and `Update Claude` on the left; `Auto-switch`
  and `Start at login` as switches (label, a pill filled with the knob
  right when on, outlined with the knob left when off, the word) and `Open
  dashboard` rightmost.
- The popup is as wide as its content and its footer's one row, never wider
  than the work area (the account column gives way first), and as tall as
  its rows; past the work area the list scrolls and the title, the note and
  the footer stay.
- Every Switch, switch and footer command is a real owner-drawn button, so
  Tab walks the Switch buttons top to bottom (A104: each row's `Use for
  Fable` just before its Switch) and then the footer, Space or
  Enter press, Esc closes. The popup itself has the focus when it opens, so
  a stray key presses nothing, and gets it back when the focused Switch
  becomes `In use` (disabled) or goes. A Switch's window text is its row in words
  (`Switch to #1 name, Ultra, Weekly 54%, Fable 50%`; `#5 name, in use,
  …`), and a switch's says its state (`Auto-switch, on`), since painted text
  is invisible to a screen reader.
- The shell lays the panel out again when no state has come for a minute,
  so an open panel's countdowns are never older: the app paints on every
  poll, but `--interval` goes up to an hour and a remote tray hears nothing
  while its engine is out of reach.

A switch from any tray (`appShell.click`, so the Linux and macOS menus too)
acts at once; only the API-key question (A33) stays, and its approval is
recorded only right before the switch is made. Replaced by A94, which leaves
auto-switch as it is and holds the engine instead: when auto-switch is on and
not a dry run (for a Codex row: and Codex rotation is on, since nothing else
moves a Codex login), the click turns it off first and does not switch when
that fails; a switch that then fails turns it on again. Switch clicks run
one at a time, so a first that fails cannot turn auto-switch on behind a
second. A tick already running when the click turns auto-switch off reads
the setting again under the state lock before it switches, and stops with
the `disabled` no-switch. The note reads
`Switched to #N. Auto-switch is now off, so the login stays there.` (or
`Switched to #N.`, `Switched to Codex #N…`) and the notification says so
too; the next action's result replaces it and a click on either switch
clears it. With auto-switch on, the next tick would move the login away
from the account the user picked.

The panel is Win32 through syscalls, as before: the popup paints the title,
the labels, the rules and the note; a child viewport paints the rows and
scrolls them with their buttons. Its geometry is a pure function, tested
with a fake text measure. The preview tool's `-png` draws it outside every
monitor, never activated and without a tray icon, and captures it with
`PrintWindow`.

Supersedes for the Windows panel: A62's Accounts / Settings popup, the
details revealed by row focus, sorting, the confirmation listing every
destination and the setting editor (settings live in the dashboard and
`tycswap config`); A63's Advanced tab and Add account… / Interactive
menu… (the dashboard and `tycswap add`, `tycswap menu`); A67's details line;
A79's plan in the row title; A80's `55% · 8m` cells and details box; and
the `Used by` column, Check for updates, the version text and the
"Auto-switch managed by …" status, which A83 left with nothing to say.
The Linux and macOS menus are unchanged (A37), but for A104's Fable items.

## A85. Start at login has one owner per box

On Windows the tray's *Start at login* read and wrote the HKCU `Run` value
alone (A35). A provisioner may start the tray from a scheduled task at
logon instead, named with the identifier the `Run` value would carry
(`io.github.tyclab.tycswap`, `….remote` for a remote tray, A45). There the
switch showed off although the tray started at every logon, and turning it
on added the `Run` value: a second start entry.

The rule: start at login has one owner per box. Where a task `\<label>`
exists, it is the owner: the state is the task's own enabled state, and on
and off enable and disable the task, then remove a `Run` value of the same
name. Without a task the `Run` value is the owner, as before. macOS and
Linux are unchanged.

- The task is read with `schtasks.exe /Query /TN \<label> /HRESULT /XML`
  and changed with `/Change /TN \<label> /ENABLE` or `/DISABLE`, each in a
  hidden window (`HideWindow`, `CREATE_NO_WINDOW`) from the system
  directory, ended after 10 seconds so a hung tool cannot freeze the tray.
- The state is the definition's `Settings/Enabled`, absent meaning enabled.
  No text is matched: each trigger has an `Enabled` of its own, and the
  tool's other output formats are translated.
- Written to a pipe, the definition declares UTF-16 but is often code-page
  bytes. A BOM or NUL bytes mean UTF-16; otherwise the bytes are read as
  they are, anything not UTF-8 replaced, and the declaration ignored.
- "No such task" is `/Query`'s non-zero exit, and only `/HRESULT` tells it
  apart without reading translated text: the exit code is then the HRESULT,
  and `0x80070002` or `0x80070003` (file or path not found) means no task.
  Any other failure (access denied, no answer within 10 seconds, a
  definition that is not a task) is an error naming the task with the
  tool's message. It is never read as no task, which would make the `Run`
  value a second owner.
- `app --autostart status` names the owner when it is the task: `Start at
  login: on (logon task <label>)`; the `Run` value's wording is as before.
- Every panel build asks for the state, and every read starts the tool, so
  the tray reads it at most every 30 seconds; a click on the switch drops
  that read and the repaint reads again at once.

Tests replace the tool and the `Run` value on every platform; a test binary
refuses the real tool as it refuses the real `Run` key. One test, on Windows
with `CI` set, drives a task it creates under a random name through the
real tool and deletes it.

## A86. The update check reads the release page, not the API

*Check for updates* answered `HTTP 403`. The check asked
`https://api.github.com/repos/tyclab/tycswap/releases/latest` (`update.Endpoint`,
A24) without a token, and GitHub allows 60 such requests an hour per public
IP address, shared by every program and device behind it; once they are
used, every answer is 403 until the hour is over. Measured on the reporting
box: the API answered 403 with `x-ratelimit-remaining: 0`, while
`https://github.com/tyclab/tycswap/releases/latest` answered `302` with
`location: https://github.com/tyclab/tycswap/releases/tag/v0.10.0`. The check
needs only that tag, and the downloads already use
`<ReleasesURL>/download/<tag>/…`, no API.

The rule: the latest release's tag comes from the release page's redirect.

- One GET of `<ReleasesURL>/latest` that does not follow redirects. A `301`,
  `302`, `303`, `307` or `308` whose `Location` path ends in
  `/releases/tag/<tag>` gives `<tag>`, unescaped; a relative `Location` is
  resolved against the request URL. The passive notice, `Checker.Latest`
  (the dashboard's card, the tray's check and offer) and the download
  upgrade all read it so.
- `update.Endpoint` is gone; `update.ReleasesURL` is the one address, still
  settable with `-ldflags -X`. Since the lookup now asks it too, the
  download upgrade refuses a `ReleasesURL` that is not https before any
  request, as it refused such downloads before.
- Errors say what happened and name no API. `429`, or `403` (GitHub's
  answer when it limits a network): `GitHub is limiting requests from this
  network; try again later`, with `(after HH:MM)` in local time from
  `Retry-After` (seconds) or else `x-ratelimit-reset` (Unix seconds). `404`,
  or a redirect that does not lead to a tag page (a repository without
  releases redirects to `/releases`): `no release found at <url>`. Any other
  status: `<url>: HTTP <code>`, where `<url>` is the page asked.
- Unchanged: `FetchTimeout`, the 24-hour cache with its negative results,
  the semver comparison and its pre-release handling (Deviation #1).

Supersedes A24's `Endpoint` (the API URL and its `tag_name`); A6's JSON
reading goes with it. The CLI package's tests point `ReleasesURL` at a
closed local port, so a command's passive notice in a test never reaches
github.com.

## A87. The Windows tray opens the dashboard's update check

The Windows panel's footer includes `Check for updates…` after `Quit`, even
when no update is known or scheduled checks are disabled. It starts the same
check as the dashboard's `Check now`, then opens the Updates section. It never
asks to install anything. The existing Linux and macOS update menu behavior
is unchanged.

A local tray calls its shared updates facade; a remote tray sends authenticated
`POST /api/updates/check` to its dashboard server. The remote shortcut is disabled
while that server is unreachable. A failed check request still attempts to open
the dashboard; check and browser errors appear in the panel note and notification.
The normal package-manager upgrade hints remain in the dashboard.

The single-use launch URL accepts only `show=updates` as a navigation target.
Its authenticated redirect preserves that target beside the CSRF fragment; the
client stores the CSRF token as before, removes the query and navigates to
`#updates`. After the first state arrives it unfolds and focuses the Updates
section. Later state events do not repeatedly move focus or scroll the page.

On switches use the same accent for label, track and state; an unavailable on
switch keeps a dimmed accent, while off remains neutral. High-contrast mode
continues using system colors. Footer commands wrap within the content width
instead of widening the account table, keeping the two switches and Open
dashboard together when they fit. This restores the manual shortcut removed
by A84 while retaining its daily view.

## A88. Explicit switches reclaim idle per-account profiles

`switch ACCOUNT` returns an idle legacy `run`/`env` profile's login to its
stored slot before the normal default-profile switch. A live CLI or IDE
session, unreadable process state, conflicting owners, mismatching identities,
or unverified credential keeps the claim held, including with `--force`.
The refusal for a live profile names its PIDs and the retry command.

The source snapshot is read under the shared store lock. A matching account
block or nonempty credential fingerprint proves local lineage; otherwise the
OAuth profile endpoint must identify the account outside the locks. Before
committing, the store lock and source credential, config and storage-write
locks are acquired and both ownership and source bytes are checked again.
The stored config must identify the same account too. An unverifiable rotation,
including an expired access token the endpoint cannot identify, is held for
reconciliation or a fresh login rather than overwritten from a stale backup.

A private `.tycswap-release.json` in the source profile records its version,
full account identity and SHA-256 digest of the account-only credential. It is
written before the backup. The verified credential is saved before removing
the source login; its seat-wide credentials and conversation files remain.
The claim is removed only after the source no longer serves the login, then
the marker is removed. A retry after source cleanup requires the marker's
identity and digest to match the saved backup. A new legacy claim removes any
old marker so a later session cannot inherit earlier release proof.

The normal switch still rechecks availability under its own locks. A failure
after releasing the source leaves a saved, unclaimed account for retry; it
does not repopulate a second rotating login. Default auto-switching remains
the account selection policy after a successful switch.

## A89. Automatic rotation and profile ownership

A88 only ran for an explicit account switch. Automatic candidate selection
discarded a legacy owner even after its last process exited, so it never
reached that release. Before polling, a real engine tick now prepares its
enabled subscription candidates through A88's verified release. This also
lets a released expired login refresh through the normal guarded backup path.
The selected target is checked again before freshening and switching. Dry
runs inspect release eligibility without changing credentials or claims.
Unverified, conflicting and live owners remain protected; disabled accounts
are neither released nor selected by this preparation.

The frozen engine interface gains no required method. The core adapter
provides optional preparation and ownership-reason capabilities. When no
target qualifies and a held account has remaining or unknown quota, the
engine emits `no-switch: accounts-in-use` with the actual ownership reasons.
It keeps the normal retry cadence instead of sleeping until a quota reset.
An account known to be exhausted does not produce that ownership diagnosis.
Snapshot eligibility applies the same ownership check; web rows expose an
optional `ownershipBlockReason`, and web/tray controls display it and refuse
the blocked switch before submission.

Ordinary parallel sessions share the default rotating login. Independent
`run`/`env` profiles remain explicit account pins, not an automatic worker
allocation mechanism. An idle prompt is still a running process and retains
its profile's login; reclaim never terminates it or copies a live refresh
token into another profile.


## A90. Startup login marks and shared-login handoff

`startLogin` is operator-owned account metadata, absent or non-boolean meaning
false. Mark writes resolve the account under the existing roster lock and use
the normal atomic sequence write. Re-add, re-login, add-token, move and swap
retain alias, disabled and startup metadata for the operation's existing
composite identity. Forced imports retain local flags and retain a local alias
when the envelope omits it. New identities do not inherit a displaced slot's
metadata; export format and new-import defaults are unchanged.

`start` selects an eligible marked account before a one-shot command handoff.
An explicit ID is the sole candidate and bypasses only mark/auto-on requirements.
Usage is cached; model coverage, quarantine, rotation and credential ownership
remain the engine's gates, and a login at its limit is a candidate after every
one with room (A105). No usage fetch or account mark is inferred from a
name, plan or policy result. The feature does not grant model access or change
managed settings. It creates no profile, claim or child-read handshake; a
session on a login at its limit gets a start hold (A105).

Preserve the incoming argv and environment before initialization. A set
`CLAUDE_CONFIG_DIR`, child `--bare`, or any `launch.AuthOverrideEnvVars` variable
skips launch selection without changing the inherited environment (except a
`CLAUDE_SECURESTORAGE_CONFIG_DIR` naming the Fable store, A103, and an
inherited `TYCSWAP_START_HOLD`, A105). Management forms still work
independently. No-mark and current-mark paths are silent and perform no
network request; they write nothing, except the start hold of a current mark
at its limit (A105). `store.Options.SkipMigrations` lets launch
initialization avoid otherwise automatic migration writes on these paths.

Freshen candidates before the autoswitch state lock. Use the current state →
store → Claude order, recheck enabled/current under the state lock, switch
through the guarded existing operation and stamp only a confirmed mutation.
Preserve other engine-state fields and never nest a state lock during stamp or
quarantine. A late stamp failure is reported after the switch, without rollback
or a second target. Proactive/reserved cooldown honors a start switch; urgent
at-limit/failover/model-missing behavior remains, but for a running start hold,
which holds every trigger (A105). Manual switches, enabled
writers and roster mutations do not all share this lock; the remaining narrow
race is accepted for this release. In-process filelock waits are unbounded.
Future E1 work must migrate all participants together to its final lock order.

Selection errors print a sanitized stderr diagnostic and hand off once on the
current login; unsafe-root failures bypass store access. Launch errors are
separate and never retry the child. POSIX execs; Windows spawns and waits with
child status and console signal ownership. Default command resolution and
provisioner launchers must reject their own canonical executable identity.
The child inherits its environment and arguments unchanged, except that A103
sets or removes `CLAUDE_SECURESTORAGE_CONFIG_DIR` by the session's model and
A105 sets or removes `TYCSWAP_START_HOLD`. A cached choice
can be stale or another switch can precede the child's credential read.

Reporting carries the mark through `AccountSnapshot`, optional true-only list
JSON and human tags. Dashboard Claude rows expose the boolean and authenticated
allow/disallow routes through AccountOps → core → lifecycle; Codex has no
startup control or field. Tray rows show the tag without a mark action.

This release retains `run`, `env`, `map`, `unmap` and their login ownership.
The later S5 retirement requires fleet acceptance and fresh review. In the
meantime an open `env` shell without a running Claude process does not keep
its claim from idle reclaim, and a newly starting legacy process can race it.

## A91. Servable models

On 2026-11-15 from 23:53 every scheduled tick reported `all-exhausted`. Eight
of 22 live default sessions ran Fable, so A76 counted the Fable window for the
whole login; the accounts with Fable room (3, 11) were at their 5-hour limits
and every account with 5-hour and weekly room (4, 7) had spent its Fable week,
so every account read headroom 0. Opus sessions stopped while 4 and 7 had Opus
room. A77 had accepted that one live Fable session keeps every default session
on a Fable-capable account; that limit holds only while some account can serve
Fable.

`reporting.DefaultSessionPolicyResult` decides it once from cached usage and
returns the settings, the reservation and the derived models it dropped. A
derived model stays counted while an account in the pool sits below the bars
the engine moves at on that model's window (`autoswitch.modelThreshold`) and
on its 5-hour and weekly windows (`fiveHourThreshold`, `sevenDayThreshold`):
judged at 100 %, the last account with Fable room kept Fable counted from its
bar up to its hard limit while the engine already refused it as a target. An
absent 5-hour window is room; an absent weekly one is not. Only a reading the
engine would still decide on serves: `UsageEntry.DecisionValue`, the value
the engine reads. A sentinel (no credential, an expired token an owner
holds) serves nothing; a reading younger than `usage.StaleOKS` serves, and so
does an older one up to `usage.TrustMaxAgeS` while the engine extends its
trust (failed fetches, a next poll not yet due), since the engine still
targets that account. The only account with Fable room drew http-429 on its
usage request many times a day, each with a 300-second backoff; refusing it
on any failed fetch dropped Fable as unservable for each of those windows.
Past `usage.TrustMaxAgeS` a reading no longer serves. The pool is every
enabled, rotation-eligible account not
quarantined in the engine state file, plus the live account whatever its
flags. A dropped model is named in the poll event as `unservable` (`not
counted: Fable` on the human line) and the engine counts 5h and 7d for it.

One room rule serves the policy and the engine: `oauth.AsOf` reads a window
whose reset time has passed as 0 % with its next reset unknown, since a
cached reading does not see a renewal. It drops the window's reset
time and countdown, so `soonest-reset` sorts a renewed account with the
unknown renewals, not first on a renewal in the past. The engine reads every
decision value through it, as do `start`'s ranking, the at-limit marks of
`list`, `status` and the account snapshot, and the dashboard's rows (and so
*Next best* and the tray's figures); the TUI's meters and *Next best* still
read the cached values. An account the policy finds serving after its reset is a
target before its next fetch only while its last reading is still trusted:
an account parked at its limit falls due for a poll at that reset, so in
practice while the reading is younger than `usage.StaleOKS`; otherwise the
move comes after the fetch that poll brings.

The reservation (A77) now covers only accounts that can run Fable, report a
Fable window and sit under `autoswitch.modelThreshold` on it, read the same
way. An account whose Fable week is spent is never reserved, so Opus work
fills it. While the Fable store is usable, A102 counts the default store's
sessions only and reserves the Fable pool's accounts with Fable room and the
Fable store's live account whatever runs.

A block the 5-hour and weekly bars do not explain emits `blocked-with-room`
and logs it as a warning, under a name no `poll`/`no-switch`/`all-exhausted`
filter drops: the live account is at or over a bar (trigger `at-limit` or
`proactive`, a counted model window included), the tick ends `Blocked`, and
enabled, rotation-eligible, unquarantined accounts below their 5-hour and
weekly bars were kept out by a counted model window (not reported, at
100 %, or over its bar when that window made the tick move) or by an
ownership hold met while preparing the target. A hold the hysteresis margin
or the cooldown explains names nobody; the first version warned on every
blocked tick, so a proactive tick held by the margin warned with each
status-line observation. The set is reported once per change: the engine
state file keeps the last one (`lastBlockedWithRoom`) and a tick without
such a block forgets it. A dry run shows the event and keeps nothing.

The scheduled tick (`auto --once`, `--observe`), `start`'s selection, and the
at-limit marks of `list`, `status` and the account snapshot (the dashboard's
and the TUI's rows) all read the one result, and since A98 so does a switch
by strategy. The dashboard state carries it as
`auto.counting` (the families of the live sessions, the counted, dropped and
reserved), with a one-line caption; the Auto tab, *Next best* and the tray's
figures render it, so an unset `autoswitch.model` reads *Follow sessions*, not
*Off* (A97 removes the setting: the policy always decides); the tray's *Model limits* switch (gone since A94) read `follows sessions — <caption>`
(the caption alone under a hosted engine, which does not follow sessions)
and says `off` only for an explicit `none`. Unchanged: an explicit
`autoswitch.model` or `--model`, which never drops a model (both removed
by A97); the long-running
loop and the hosted engines, which keep their configured models; and
one login for every default session, so while an account can serve Fable and a
Fable session runs, Opus sessions stay on a Fable-capable account too.

## A92. run, env and map retire

The operator ruled on 2026-11-10 that the per-account profile commands go
("Remove them") and on 2026-11-16, finding them still shipped, "FIX IT".
`tycswap run`, `env`, `map` and `unmap` are gone, with the session manager
(`internal/session`), the directory mappings (`internal/mappings`; a
`mappings.json` stays on disk unread, and `add`/`remove` no longer prune it)
and their help, docs, adapters and the env-only SIGINT routing. Each of the
four names exits 2 before the store is read, like an unknown command, with one
stderr line pointing at the shared default login and `claude --resume`. Every
Claude session shares the default login, which tycswap switches; A89's
"explicit account pins" no longer exist.

Why it took a second ruling: the removal waited behind the one-engine series
(behind E1, which never shipped) and then behind a fleet-acceptance gate
the ruling did not set (start-login S5); nothing asserted the commands gone.
`TestLegacyProfileCommandsAreGone` does now. A removal ruling ships in the
next release, pinned by a test, never behind a series or a gate the operator
did not set.

The helpers other features use moved out first: `launch.AuthOverrideEnvVars`,
`launch.ScrubAuthOverrides`, `launch.CLICommand` and `launch.CheckCmdShimArgs`
(add --login, codex login, the updater, start; `ScrubAuthOverrides` still
drops `CLAUDE_SECURESTORAGE_CONFIG_DIR`, and `cli.run` drops the Fable store's
value of it for tycswap's own process, A100) and `ccfile.KeychainServiceName`,
`ccfile.CredentialsFileName`, `ccfile.ReadProfileIdentity` (store, purge,
migrate). What stays until no profile is left: `internal/sessprofile` reads
the profiles under `<store>/sessions/`, `groups/claims.json` keeps their
claims, the ownership checks (A88, A89) still treat a profile as its account's
owner, a live session in one keeps its claim, and a shell whose
`CLAUDE_CONFIG_DIR` still names one runs every tycswap command on the default
login with one notice. `claude` itself, plain or through `tycswap start`
(which passes the environment on unchanged, A90), still reads that profile,
and `env --unset` is gone, so the notice and the removed commands' message
say how to unset the variable in sh, fish and PowerShell.

Leftovers retire on their own, beside A78's group retirement and on the same
rounds: scheduled and real (`auto --once`, each loop interval, the app's
hosted loop, with auto-switch on or off), never a dry run, a status line's
tick or `start`, and never under the engine's state lock. A89's pre-poll
release of candidates stays as it was. For every stored account, disabled ones
included, whose profile no Claude process uses (for a disabled account that
can mean a profile request or a refresh with its leftover login; retirement
never enables the account or switches to it):

1. `projects/` is copied into the default profile's where missing (A78's
   copy, never replacing a file; a namesake of another size is logged with
   both paths and sizes; a copied transcript keeps its modification time,
   by which Claude Code orders its session list), once, the marker written
   only after a complete copy: `.tycswap-history-copied` stops a transcript
   Claude Code later cleans up from coming back.
2. On the live default account the claim is released by dropping the
   profile's login when it is empty, the live login's own lineage, or stale.
   Neither the slot backup nor the live credential is written. A login of
   another lineage without the stale mark keeps its claim: the operator
   decides, by signing in again.
3. On other accounts a stale login is dropped the same way, and anything else
   takes A88's verified release. The stale mark (`postBackupWrite` set it
   while the profile ran, after its stored login was rewritten) is the proof:
   `run` itself discarded such a login on its next launch, while A88 would
   have saved it over the newer stored login once the profile endpoint named
   the account. This holds for explicit switches too.
4. Before any request, a profile login of another lineage than a slot backup
   not known dead (the usage store's `TokenDead`) is ordered by `expiresAt`.
   Expiring no later than the backup, it is the older copy: it is dropped the
   same way, without a request to the token or profile endpoint, and neither
   the backup nor the live credential is written. Expiring later, or with no
   backup or a dead one, it takes A88's release (item 5 when expired). Either
   expiry unreadable, it is held without a request. The order is taken again
   under the release's locks, before a refresh and before the save. Why:
   nothing records what Anthropic's token endpoint does when a rotated
   refresh token is presented again (for Codex a reused one is `invalid_grant`
   and can revoke the token family), and an unexpired older copy would pass
   the profile endpoint and be saved over the newer backup; so a profile
   login goes out only when it can be shown to be the newest. Such a pair
   needs a backup written without `postBackupWrite` dropping the profile's
   login: a failed or interrupted invalidation, a macOS Keychain delete that
   failed, a stale mark that could not be written, or a store an older
   version wrote.
5. RECLAIM-3: a rotated profile login whose access token expired is verified
   by refreshing its own token under the store lock and the profile's
   credential, config and storage-write locks; the new credential goes back
   into the profile before anything else, then the profile endpoint names
   it. `invalid_grant` drops the dead login and the claim and keeps the slot
   backup. A refreshed login the profile cannot store is the only live copy
   of its token family: when the token response names the account it is
   saved as the slot backup under the locks already held and the profile
   retires; otherwise it goes to `.creds-<n>-<email>.enc.recovered` beside
   the slot backup (0600, the backup's format, so moving it over the slot's
   `.enc` restores it), the warning names that file, and the spent profile
   login is held, never refreshed again. The slot's next stored login
   removes that file.
6. RECLAIM-4: a release refused by verification is stored in
   `claims.json` (`held`, reason and the login's digest), and rotation
   eligibility returns that reason until the login changes, so the dashboard,
   tray and engine agree. While that login stays, no round, preparation or
   `tycswap switch` asks about it again: the release returns the recorded
   reason without a profile request or a refresh. A refresh that failed in
   transport (`transient`: no answer, a timeout, a 5xx, a 429) is no refusal:
   it is held with `retryAt` and `failures` and tried again once the usage
   store's failure backoff (04§2.6, 30 s doubling to 600 s) has passed. The
   profile endpoint's silence is told apart the same way, through the
   optional `oauth.ProfileProber` the HTTP client implements beside the
   unchanged `oauth.Client`: an identity naming another account, a 401 or
   a 403 is a refusal, held until the login changes or a new sign-in drops
   it; a transport error, a timeout, a 429, a 5xx or a malformed reply is a
   failed request, held for the same backoff and asked again (before, one
   outage kept a good login out of rotation until the operator signed in
   again). A client that cannot tell counts its nil as a failed request.
   `Account-<n> profile retirement held` and `session release held` are
   warned once per login and reason, later rounds at debug; on the live
   account, whose drop asks no one, the hold (`live`) does only that.
   RECLAIM-5: a hand-over with nowhere to go emits only its `…-no-target`.

A drop marks the profile stale before clearing its login and unmarks it after
the claim is gone, so an interrupted drop finishes on the next round; an
interrupted verified release finishes through A88's marker. Running,
unreadable or misidentified profiles keep everything.

Liveness (SES-6): every check of a Claude Code session record, the session
list and the ownership checks alike, compares the record's `procStart` with
field 22 of `/proc/<pid>/stat` when both exist and the record's `pidDomain`
names this machine and PID namespace (`linux:<machine-id>:pid:[N]`, compared
whole: the namespace inode repeats across machines), so a reused PID neither
keeps a dead profile's claim nor lists a dead session. A record without
`procStart`, one from another machine or namespace, or a system without
`/proc` keeps the PID-only check. Two boxes cloned with one machine id cannot
be told apart by the record; profile folders are per box, so a record from
another box reaches this one only through a copied store.

## A93. One auto-switch switch; what is counted is shown, not chosen

**Why.** The operator runs `tycswap auto --once --json` from a systemd timer
every two minutes and the dashboard with `TYCSWAP_AUTO_MANAGED_BY` set, so
it hosts no engine. The Auto tab still offered what only a hosted engine
used, and some of it misled. *Count model limits* saved `autoswitch.model`;
only the unset value lets a scheduled tick follow sessions and move Opus work
to accounts whose Fable week is spent (`reporting/defaultmodels.go`), and an
explicit value switched that off for 38 hours without the page saying so.
Start dry-run started a second engine in the dashboard that counted
differently from the timer's ticks; Wake and the 7d slider reached no
engine; the event log stayed empty because the ticks print to the journal;
*Poll interval* is never read by `auto --once`. Settings carried a second
set of names in the script.

**What.**

- *One switch.* The Auto tab has one *Auto-switch* switch, the words of the
  Settings row and the tray: on is `POST /api/auto/start` (Start), off
  `POST /api/auto/stop` (Stop); both write `autoswitch.enabled` with and
  without an owner (A83). A start asking for a dry run is refused with the
  terminal command, `tycswap auto --dry-run`. Since 2026-11-18 (the
  operator: "why is on web frontend dashboard not an auto mode toggle?") the
  Dashboard tab's Accounts card shows the same switch in its head: the same
  state, the same two requests, both switches locked while a change is in
  flight and hidden where the Auto tab hides its own.
- *The heading says what is true.* `Auto-switch` with an `on` or `off` chip;
  the line under it is `checked by <owner>, last check <time ago>` under a
  scheduler and `checked every <interval> s by this dashboard` while an
  engine is hosted here; while off, `not checked while off` (with the last
  check under an owner). The tab badge says `on` or `off` with that line as
  its tooltip. No "engine" or "running" wording.
- *What is counted is shown.* `Counting now: <caption>` is
  `auto.counting.caption` (A91). While `autoswitch.model` is set (counting
  mode `setting`) one warning line says its value, that session following is
  off, and `tycswap config unset autoswitch.model`.
- *`autoswitch.model` is set through `tycswap config` only* (until A97
  removes it with the mark). The registry marks it (`Spec.ConfigOnly`) and every surface follows the mark: the
  dashboard's settings list and state leave it out, `POST`/`DELETE
  /api/settings/autoswitch.model` and `…/unset` answer 400 naming `tycswap
  config set|unset`, and the TUI's Settings screen does not list it. `config
  list|get|set|unset` and `auto --model` are unchanged.
- *Gone, with what only they used.* Start dry-run, Wake, the 7d slider and
  the event log leave the page; `/api/auto/wake`, `/api/auto/threshold` and
  `/api/auto/model` answer 404; `AutoFacade` keeps `View`, `Start()` and
  `Stop()`; the engine host drops its dry run, `Wake`, `ApplyThreshold` and
  its event ring (`AutoView.events`). The event stream stays: the tray turns
  it into notifications. With the tray's model switch gone (A94) the
  facade's `ApplyModels` has no caller and goes too. `AutoView` drops
  `dryRun` and `managedBy`: an older client reads the absent fields as the
  false and empty they always held. `available` stays, always true, because
  an older tray locks its toggle without it.
- *Poll interval* is listed only while no owner is named.
- *One name per setting.* The registry's labels (`Spec.Label`) are the names
  on the Settings rows, the toasts, the Auto tab's tiles (*5-hour threshold*,
  *Weekly threshold*, *Model threshold*, *Account order*, *Codex
  auto-switch* with its *Codex threshold*) and the Guide; the script's label
  table is gone. The help texts say that Codex auto-switch runs wherever
  auto-switch runs, `auto --once` included, that 0 for the Codex threshold
  means the weekly threshold, and that the model threshold covers every
  counted model window, session following's included.
- *Dead code and wrong texts.* `settings.LoadOver`, `SettingView.Scope`,
  `Source` and `ReadOnly`, and the page's read-only, `managedBy` and
  `!available` branches had no caller or could not run; the Guide and the
  Settings intro no longer say the engine runs inside `tycswap web`, name
  "± keys" or "Model windows", or that tray and dashboard edit the same
  settings.

**Tests.** A node script runs the production Auto tab and Settings rows over
a small DOM with the registry's labels: on and off in both modes, the three
counting modes' caption and warning, the switch's two requests, and every
tile and toast name. Go tests pin the 404s, the refused dry run and model
saves (store untouched), the hidden *Poll interval*, the facade's start, tick
and stop through the switch, and `config set|unset autoswitch.model`.

## A94. The tray sets auto-switch; a switch by hand holds for the cooldown

The operator accepted these rows on 2026-11-17 ("all rows as proposed, build
it"). Three things went wrong.

On 2026-11-17 16:39:25 (also 11-14 and 11-15) one tray process turned
auto-switch off and 0.6 s later on again. The click read the shell's cached
state and flipped it; panel clicks each run on their own goroutine, nothing
serialised them, and the button repainted only after a full state document,
so a second click landed on the flipped cache. Now a click carries the state
the row showed and sets the opposite of that: the row's `tray.Item` sets
`ShownState`, so its click is `Item.Choice`, `auto:on` or `auto:off`, on every
platform's menu and the Windows panel. Other toggles, and the public
`desktop/tray` package's users, keep a plain ID. A click that arrives while an
auto-switch change is still in flight is dropped, not queued.

A switch by hand meant two things. The tray turned auto-switch off before it
and left it off (A84), while one from the dashboard left it on, and the engine
could undo it: on 2026-11-16 a dashboard switch to account 4 at 16:37:17 was
followed at 16:37:20 by `Switched to account 3 by engine:at-limit`. The rule
now, on every surface (`tycswap switch`, the dashboard, both trays, the TUI):
a switch by hand leaves `autoswitch.enabled` as it is, and for
`autoswitch.cooldownSeconds` after it the engine makes no switch, whatever the
trigger. After that it decides as always.

- The hold is per store: a switch of the Fable store by hand stamps
  `fable/autoswitch_state.json` and holds only the Fable engine (A100).
- One place stamps the hold: `core.Switcher`'s `SwitchTo`, `SwitchToForce`
  and `Switch`, which every hand switch goes through. It runs the switch under
  the state lock (`.autoswitch_state.lock`) the engine's own switch and
  `tycswap start` hold for theirs, and when the roster's live slot or stamp
  moved, stamps it as they do (`lastSwitchAt`, `lastSwitchTo`, the failover
  memory cleared) plus `lastSwitchBy: "hand"` (`autoswitch.HandSwitch`). A
  tick that decided before the switch re-reads the state under the lock and
  sees the hold. A stamp that cannot be written is logged; the switch stands.
- The engine and `start` switch through `EngineSwitchTo` (their adapter
  shadows `SwitchTo`): they stamp under the lock they already hold, which a
  hand switch would wait on, and their stamp clears the mark, so their own
  cooldown holds `proactive` and `reserved` only, as before.
- A tick held by a hand switch emits `no-switch` with reason `cooldown` and
  the detail `switched by hand to account N; held for another Ss`.
- The tray no longer turns auto-switch off for a switch, nor back on after a
  failed one. Its switch clicks still run one at a time.
- The Codex engine has no cooldown and is unchanged: it moves only off a
  Codex account at or over its bar.

The tray menu's *Model limits* switch goes: on, it wrote `autoswitch.model =
all`, which switched off following the sessions (A91). The key is set with
`tycswap config`. Its menu item, click handler and the app's and remote
client's hooks (the latter called `/api/auto/model`) are gone. The menu's
"managed by" tooltip, sub line and grey-out also go: the server has left
`managedBy` empty since A83, so they could not show.

## A95. The handover after a limit is removed

The operator accepted on 2026-11-17: "Settings: *Handover wait*, the
'Continue after a limit' card, tray 'Session at limit' → Remove the feature.
Unused here: no recovery hook is installed, flakelab's wake-parked does this
job." The one-engine design of 2026-11-10 had listed the same removal.
Nothing installs `tycswap recovery record` since the group launcher went
(A78) and the operator's boxes run no such hook, while the dashboard's
recovery runtime still read the store on every state build and could switch
accounts on a `rotate` decision there.

Removed: `internal/recovery`; every `tycswap recovery` verb (`record`,
`observe`, `list`, `dismiss`, `prepare`, `plan`, `reclaim`); the dashboard's
*Continue after a limit* card, the sessions table's *Continue in Codex…*
button, the Guide's *Continue in the other tool* and its handover lines; the
`POST /api/recovery/{prepare,dismiss,start}` routes and the state document's
`recovery`; the tray's *Session at limit · review handover…* item and its
notification; the `TYCSWAP_HANDOVER_*` launch environment; the rotation on a
recovery decision; `autoswitch.handoverWaitMinutes`. The sessions table's
model label goes too: only the recovery store filled it (`sessionModels`),
from the hooks nothing installs.

`tycswap recovery …` exits 2 before the store is read with one line, as
`tycswap run` does (A92): it was removed, and a session parked on a limit
goes on when the login has room. `recovery record` prints the same line and
exits 0: it is the lifecycle hook a former group session may still run
(A78), and a Claude Code hook that exits 2 blocks the prompt and keeps a
Stop from ending the turn. `groups guard` stays and no longer writes the
recovery store. (Since 2026-11-25 only `recovery record` answers: nothing
called the other verbs, and the operator ordered such leftovers removed. Any
other `tycswap recovery` is an unrecognized argument, exit 2.)

A stored `handoverWaitMinutes` is read as nothing, `config` refuses it as
unknown, and every settings write drops it (`retiredKeys`), unlike an
unknown key such as `includeApiKeyAccounts`, which writes keep. The store's
`recovery/` directory (`state.json`, `state.json.lock`) is left on disk,
neither read nor deleted.

## A96. An absent 5-hour window is room on a 5-hour move

On 2026-11-17 from 21:08 (tycswap 0.13.1, `autoswitch.model` unset, Fable
dropped as unservable) the active account 12 sat at 93 %, then 98 %, of its
5-hour window with its week at 3 to 5 %. The bars were 5h 90, 7d 98, model 95,
hysteresis 10. Account 11 was at its 5-hour bar; accounts 3 (week 90 %) and 8
(week 97 %) report no 5-hour window, as Ultra accounts do once Anthropic has
verified them; 4 and 7 had used 99 % of their week. Every tick answered
`no-qualifying-candidate`, and the engine would have moved only at 100 %,
after the sessions hit the limit.

A34's proactive gate judged a candidate that reports no window of the
deciding class on its weekly figure. Written for a model-window move onto an
account that never used that model, it held account 3's week (10 points of
room) against the 5-hour bar and against 12's 5-hour room plus the margin
(10 − 7 < 10). An account without a 5-hour window has no 5-hour limit, yet it
could be a proactive 5-hour target only while its week was nearly unused.
A91 already counts an absent 5-hour window as room when it decides whether a
model is servable, and the README says such a window does not block
switching.

On a proactive move the 5-hour window made, a candidate that reports no
5-hour window is free on that axis: it is a target when it sits below every
bar of its own, the week and any counted model window. The 5-hour bar and the
margin against the active account's 5-hour room are not applied to its weekly
reading. It cannot bring the login back: below every bar, none of its windows
is hot, so the next tick on it reports `below-threshold`. Unchanged: a
candidate that reports a 5-hour window, moves the week or a model window made
(the weekly fallback still judges a week move onto an account that reports no
7d window, on its counted model window), `at-limit`, `failover`, `reserved`
and `model-missing`, and the strategies' order. The case the fallback was
written for no longer reaches the gate: since automatic targets must report
every counted model, a candidate without the model window is refused first
(`no-compatible-model-target`).

The warning `Blocked on Account-12 although 3, 8 are below their 5h and 7d
bars` in that evening's log came from a status-line tick at 21:06:29, while
Fable was still counted and 3 and 8 had spent their Fable week: A91's report,
correct there. The ticks after Fable was dropped named nobody.

Tests (`internal/autoswitch/absent5h_test.go`): the live pool switches
proactively on the 5h axis, to 3 under `best` and under `soonest-reset` when
3's week renews first, to 8 when 8's does, with no `blocked-with-room`, and the
next tick on the new account, past the cooldown, stays; a candidate without a
5-hour window at its weekly bar, or over a counted model window's bar, is no
target; one with a 5-hour window keeps the bar and the margin; a week move
still holds a candidate without a 5-hour window to the margin on the week and
refuses one that reports no week.

## A97. The model setting is removed; auto-switch always follows the sessions

**Why.** On 2026-11-17 the operator was asked whether `autoswitch.model` and
the `--model` flags should go, since no wanted behaviour needs them: left
unset, the default-session policy (A76, A77, A91) already counts Fable while
Fable sessions run, keeps accounts with Fable room free for them and lets Opus
fill the accounts whose Fable week is spent. His answer: "Remove it". Every
value someone had set caused `all-exhausted` while accounts still had Opus
room: one box stored `all`, another `Fable`. Any value skipped the policy (its
early return on a set setting): `all` or a list counts a model window even
when no account can serve it, so every account reads 0 % room; `none` counts
none, so Opus uses up the accounts that still have Fable room. A93 had already
taken it off the page and the tray and added a warning line for it.

**What.**

- *Session following is the only rule.* `reporting.DefaultSessionPolicyResult`
  always decides; it clears the base settings' `Model` before deriving, so
  nothing a caller passes replaces the counted set. Its rules are A91's
  unchanged: counted models from the live default sessions, a model no usable
  account can serve dropped and named `unservable`, the Fable reservation,
  `blocked-with-room`. `AutoSwitchSettings.Model` stays as the engine's input:
  the policy fills it, nothing loads it from `settings.json` and no write puts
  it there. The long-running loop and a hosted engine never followed sessions
  (A91); with nothing to configure they count 5h and 7d.
- *The key is retired.* `autoswitch.model` leaves the registry, so `config
  set|get|unset autoswitch.model` answer `unknown setting` and no list shows
  it. A file that holds it loads; the value is not read, and the next
  settings write drops it (`retiredKeys`, as `handoverWaitMinutes`, A95).
  `Spec.ConfigOnly` had no other key and goes, with the dashboard's 400 for
  it and the TUI's filter.
- *The flags go.* `tycswap auto --model` and `switch … --model` exit 2 with
  `--model was removed: auto-switch counts the models the sessions run.`;
  help and usage no longer list them. `--model-threshold` stays; the model
  threshold applies to every model window session following counts.
- *What existed only for a set value goes.* `switch --strategy` counts 5h
  and 7d only (until A98, which has it count the policy's set): its model list, the `Using configured model limits` line, the
  `--model` typo warning and the `models`/`modelSource` result fields go, and
  `POST /api/switch` ignores a `models` field. The engine's typo guard
  (`checkModelNames`, the state file's `modelNamesChecked`) and the unused
  `Engine.ApplyModels` go; `config-warning` stays for the dry run while off.
  The control-character handling of the free-text value (`TextValue`,
  `CleanText`, the `cleaned`/`notice` fields of `config list|get --json`, the
  warning lines of `config` and the menus, the notice on the TUI's and the
  dashboard's rows, the `string` setting kind) had no other setting. The
  dashboard's counting mode `setting` and `auto.counting.setting`, its
  warning line, its fallback to the setting and the `all` handling in its
  ranking go; the tray reads the counted models from `auto.counting` only.
  `Counting now: …` keeps its captions for `sessions` and `hosted`.

**Tests.** With a `settings.json` holding `"model"` set to `all`, `Fable` or
`none`, a scheduled tick on the 2026-11-17 shape (every account at 100 %
Fable, the live account at its 5-hour limit, another below its 5-hour and
weekly bars) polls the same windows as without the key and switches to that
account; `all` and `Fable` ended `all-exhausted` before. The key is refused
by `config set|get|unset`, survives a load and is gone after the next write;
neither `config list` nor the Settings tab nor the TUI lists it; both flags
exit 2 with the line; the served page has no warning line and does not name
the key. The policy tests that pinned "a set value wins" now pin that a
`Model` on the base decides nothing; engine tests that need a counted model
set it on the engine's settings, the seam the policy fills.

## A98. A switch by strategy counts what the session rule counts

**Why.** A97 left `switch --strategy` judging on 5h and 7d only, since the
model list it took came from the removed setting and flag. The review of
v0.15.0 reproduced the result with a Fable session live: accounts 1 (live,
Fable 20 %), 2 (most 5h and 7d room, Fable 100 %) and 5 (Fable 10 %). The
dashboard said `counting 5h + 7d + Fable (live Fable sessions)`, marked
account 2 `at limit: Fable` and named 5 its best candidate, and *Switch now*
with *Best* and with *Next available* both switched to account 2, onto a
login its Fable sessions cannot use. Asked whether the button and `tycswap
switch --strategy` should pick what the page shows, the operator said
"fix it" (2026-11-18).

**What.**

- *One counted set.* `switch --strategy best|next-available`, the
  dashboard's *Switch now* (`POST /api/switch`) and the TUI's best-switch all
  run `switching.Switch`, which asks the seam `switching.CountedModels` for
  the counted models; core wires it to `reporting.CountedForSwitch`, the
  default-session policy (A91) over the saved bars, the result `list`,
  `status`, `start`, the at-limit marks and the page's ranking read. *Best*
  judges headroom on 5h, 7d and those windows, and *Next available* skips an
  account at its limit on any of them. No flag, request field or setting
  names the set: `POST /api/switch` still ignores `models`. A plain rotation
  and `tycswap switch <id>` count nothing and are unchanged.
- *What the page does with the rest.* The page's ranking counts
  `auto.counting.models`: a model the policy drops as unservable is not in it,
  so the switch does not count it either and picks as before; the accounts
  the policy reserves for Fable are ranked like any other on the page, so the
  switch does not hold them back either (the reservation stays the engine's;
  since A99 all three rank them last).
- *Saying so.* In text mode, while a model window counts, one dimmed line
  gives the policy's caption, the dashboard's (`reporting.CountingCaption`,
  e.g. `Counting 5h + 7d + Fable (live Fable sessions)`), and a skipped
  account names its full windows (`Skipping Account-2 (at Fable limit)`; the
  JSON warning says the same), the all-at-limit line reads `usage limits`.
  `--json` gains no field.
- *Unchanged.* *Best* still moves only onto strictly more headroom than the
  live account has under that set, so where the live account is best it
  answers `already-best` while the page still names its best other account
  (until A99, which ranks by the account order).

**Tests.** The reproduction at the three entry points (`switch --strategy
--json`, `POST /api/switch`, `tui.Facade.Switch`), each failing on v0.15.0:
*Best* lands on 5 where the live account has less room, answers
`already-best` with the review's figures, where the live account has more;
*Next available* skips 2; with Fable unservable or no session reporting a
model the pick is the 5h and 7d one. One test renders the page over the
dashboard's state and compares its best candidate and at-limit marks with
where *Switch now* lands for the same state.

## A99. A switch by hand to the best account takes what the account order ranks first

**Why.** After A98 the three choices of a best account counted the same
windows but ranked by three rules: *Best* (`switching.selectBestSwitchable`)
took the most binding headroom, strictly more than the live account's; the
page's best candidate came from `app.js` (`lessBest`, `lessSoonest`, best
candidate only below every bar); the engine orders by `sortQualifying`. On
the operator's boxes the account order is `soonest-reset`, so the button and
the page named different accounts. Put to him: "make *Switch now · Best*
follow the *Account order* setting, so a switch by hand picks what the page
shows and what the engine would pick." His answer: "do it" (2026-11-18).

**Where they disagreed** (each a state of the test below, on A98's code):

- *Renewal against room*, `soonest-reset`: *Best* took the account with the
  most room, the page and the engine the one renewing first.
- *Over a bar, not at limit*, `best`: the engine ranks on weekly room without
  tiers and took the account over its 5-hour bar with the most weekly room;
  the page put that account in its *at threshold* group and named the other,
  and *Best*, on binding headroom, took the other too.
- *Reserved accounts*: the engine ranks accounts left to Fable work last;
  the page and *Best* ignored the reservation and took one.
- *Unservable model*, `soonest-reset`: as the first, once the model dropped.
- Ties: the page broke ties on 7d, model, then 5h usage, the engine on
  binding headroom.

**What.**

- *One ranking.* `autoswitch.RankTargets` orders candidates as the engine
  orders its targets: `targetQual` (the part of the qualification no trigger
  changes: every counted window reported, usage known, not at a limit, week
  and counted model week not spent), then `sortQualifying` by
  `autoswitch.strategy`, decision values read as of now. The engine's
  `selectCandidates` builds its targets with the same `targetQual` and adds
  only its trigger gates (the proactive landing gate, the hand-over filters).
- *The switch.* `selectBestSwitchable` ranks the live account with the
  rotation-eligible, unquarantined others, in sequence order, over A98's
  counted set and the policy's reservation (`switching.Counting`, filled by
  `reporting.CountedForSwitch`). It takes the first unless that is the live
  account (`already-best`); an unknown live reading still stays
  (`usage-unavailable`), as does a live account first while another account's
  usage is unknown. Quarantined accounts are no longer targets. Text mode
  names the order, beside A98's caption.
- *The page.* The state carries the ranking as `auto.ranking` (the other
  accounts, best first, over `auto.counting`'s models and reservation; the
  hosted engine's has neither). *Next best* lists those accounts in that
  order and the rest after them as before; *Best candidate* is the first.
  `app.js` keeps its own comparators only for the rows the engine could not
  take and for a state without `auto.ranking`.
- *Unchanged.* `next-available`, `tycswap switch <id>`, the engine's
  decisions, `--json`'s fields. An engine moves only when a trigger lets it,
  through its gates: a proactive move can skip the first account when it
  would not land below the triggering bar by the hysteresis margin, so the
  engine's target equals the page's best candidate where no gate refuses it
  (always on an at-limit move).

**Tests.** `TestSwitchNowAgreesWithThePage` renders the page over the
dashboard's state for seven states under both orders (renewals against
room, an account over a bar but not at limit, reserved accounts, a counted
Fable window, an unservable one, unknown usage, the live account first) and
asserts that *Switch now · Best*, the page's best candidate and the
scheduled tick's dry-run target are one account, that the live account first
stays, and that *Next available* never lands on an account the page marks at
limit; five of the fourteen failed on A98's code. The switching unit table
runs under both orders.

## A100. The Fable store: a second login per account beside the default login

**Why.** One shared login serves every Claude Code session on a box. While a
Fable session is alive and some account has Fable room, the login sits on that
account and the Opus sessions burn its 5-hour window there too; while none is
alive, Opus fills the Fable-spent accounts. The operator wants both at once:
"opus session with opus limit auto swich to fill the accounts with no fable
left", Fable sessions on the accounts that still have Fable. His design
request (2026-11-16): "we can have 2 logins into the same account so i think
that is the best idea. no need for handover and stuff". His order after the
pilot (2026-11-18): build it. Each account gets a second, independent sign-in;
sessions whose model is Fable run with Claude Code's
`CLAUDE_SECURESTORAGE_CONFIG_DIR` pointing at a second credential store;
tycswap rotates the two stores independently, and no login ever moves between
them.

**Pilot results (2026-11-17).** One box, Claude Code 2.1.326, tycswap 0.13.1;
times CET. Pilot account: slot 4, a personal Ultra account at 99 % of its week
with its reset three days out. Its second sign-in went into a scratch store
outside tycswap's store; the probes were `claude -p` runs with
`CLAUDE_SECURESTORAGE_CONFIG_DIR` set to it.

Not tested:

- P4 and P8: they need a second account's sign-in in the scratch store.
- P6: no login of a model-restricting organization was enabled.
- P9: it needs the launcher.
- P5 as planned (an hour with both classes): one probe only.
- P1's alternation: the default login was never refreshed on demand, because
  that means writing the live store. It was watched through tycswap instead.
  Its own token refresh after the second sign-in was due about 02:50 on
  2026-11-18 and is not in this record.
- More than two sign-ins of one account on one box, and how many sign-ins an
  account may hold at once.
- Interactive sessions: P2, P3 and P7 ran as print-mode probes.

| # | Result | Measured |
| --- | --- | --- |
| P1 | pass so far | Second sign-in at 20:13. The scratch login was refreshed 20 times, 20 s apart, at the real token endpoint: 20 times HTTP 200, each with a new refresh token and a new access token, and the profile and usage endpoints answered 200 after each. The default login's usage fetch succeeded 18 minutes after the sign-in and 3 minutes after the last refresh; tycswap logged nothing about the account. |
| P2 | pass | `strace` of one probe: 57 opens of the scratch store's credential file, all reads; no open of `~/.claude/.credentials.json`; no credential write. Transcript under `~/.claude/projects`, record under `~/.claude/sessions`. Also written under the shared config: `.claude.json`, plugin `.in_use` marks and the scratch login's synced skills (`~/.claude/skills/synced/`); a default-store session was offered that new skill a moment later. |
| P3 | pass | A default-store session listed the scratch-store session, and its message came back quoted. The scratch-store session listed twelve default-store sessions, and its message arrived. |
| P5 | one observation | `oauthAccount` named the default login's account before and after the probe; tycswap logged no live-login warning. |
| P7 | pass on this box | The scratch store held no `mcpOAuth` entry. `claude mcp list` on it: two stdio servers, one HTTP server and one claude.ai connector connected; a fifth server's line was cut off and is not judged. claude.ai connectors follow the store's account. |

Seen on the way: an access token lives 8 hours, and a new login's refresh
token carries an expiry 28 days out. Whether a refresh moves that expiry was
not read. Two sign-ins before the pilot: one account was signed in on one box
on 2026-11-13 22:52 and on a second box at 23:44; on 2026-11-17 both logins
answered the usage endpoint, each box having refreshed on its own since
(deduced from the 8-hour token life), and the later sign-in did not end the
earlier one. One `invalid_grant` on the second box on 2026-11-06 is
unexplained.

What the single login left unused, from the scheduled ticks on the pilot box,
2026-11-16 04:20 (v0.13.0) to 2026-11-17 19:52 CET, 899 ticks over 38 hours:

- By the ticks' own readings no enabled account was below its Fable, 5h and
  7d bars for 22.0 hours. The engine reported `all-exhausted` for 21.5 of
  them; for 18.3 another enabled account was below its 5h and 7d bars. Nine
  switches were made by hand from the dashboard. Moving there is what the
  busy-model rule exists to do, and it needs no second store.
- The rule never acted: every tick counted the Fable window and none named it
  unservable. By the rule's code that happens in those hours only while
  `autoswitch.model` holds an explicit value, which skips the rule; the value
  read `all` at 19:52. Who set it and when is not established. It is unset
  since 2026-11-17 20:11 (A97 removed the setting).
- The second store adds only the other 16 hours, when some account still
  serves Fable and the Opus sessions burn that account too. On the live
  account the weekly window moved 12.9 points an hour while it had Fable room
  and 1.3 while its Fable was spent (18 hours each; uncontrolled: nights and
  parked sessions fall on both sides). The total budget does not grow.

**Names.** The *default login* is the shared login in
`~/.claude/.credentials.json` that tycswap rotated before. A *Fable login* is
an account's second, independent sign-in, its own OAuth grant and refresh
token family; in code its lineage is `fable`, the other `default`. The *Fable
store* is the second credential store, which holds the one Fable login live
there; Fable sessions run on it.

**On disk.** `<root>` is the backup root.

| Path | Mode | Content |
| --- | --- | --- |
| `<root>/credentials-fable/` | 0700 | the accounts' Fable logins, `.creds-<n>-<email>.enc` (+ `.prev`), the format of `credentials/` |
| `<root>/fable/` | 0700 | the Fable store's scope root |
| `<root>/fable/autoswitch_state.json` (+ `.autoswitch_state.lock`) | 0600 | the Fable store's engine state, the root state file's schema (A66, A94) |
| `<root>/fable/live_lineage.json` | 0600 | the Fable store's chain record, A68's schema |
| `<root>/fable/store/` | 0700 | the Fable store: the value sessions get in `CLAUDE_SECURESTORAGE_CONFIG_DIR` (`paths.FableStoreDir`) |
| `<root>/fable/store/.credentials.json` | 0600 | the live Fable login plus the store's own seat-wide keys (`mcpOAuth`, `mcpOAuthClientConfig`) |
| `<root>/fable/store/.storage-write.lock/`, `<root>/fable/store.lock/` | dir | Claude Code's storage-write and credential-refresh locks of the Fable store |

Everything else is shared and single: the roster, `configs/`, the usage cache,
`settings.json`, `default_models.json`, the log. Nothing that acts on the
Fable store writes `.claude.json`, the roster, Claude Code's settings, the
default credential, `credentials/` or `configs/`.

In code: `credstore.Config` gains `ActivePath` (the active credential file;
empty is Claude Code's default) and `FileOnly` (no Keychain, no managed API
key, no global-config read or write). `Store.ForFable` returns the Fable
scope: a clone whose credential store is file-only with `credentials-fable/`
as its backups and the Fable store's file as its active credential, whose
`StatePath`, chain record and `ProfileDir` are the Fable ones, and which
shares the roster, the usage cache and the store lock. It touches no disk;
`SetupFableDirectories` creates the three directories and only writers call
it. A Fable login write invalidates no session profile. On the scope,
`GetCurrentAccount` names no account (it would read the shared
`.claude.json`), `HasLiveLogin` is whether the store holds a login, and
`ResolveLiveSlot` is the Fable resolver.

**On and off.** The store *has Fable logins* when a roster account has a
readable Fable login. It is *on* when it has Fable logins and its
`.credentials.json` holds a login (not seat-wide keys only, A29); *off* when
it has Fable logins and holds none. It is *usable* when on and no
quarantine entry in `fable/autoswitch_state.json`, whatever its reason
(`invalid_grant`, or `identity-conflict` when a refresh showed another
account's login), carries the live login's refresh-token fingerprint; the
reason is then `Account-N's Fable login: sign in again`, the answer the
login's own row gives. `Store.FableStore()` returns
`{On, Live, Usable, Reason}`, its live slot from backup or chain only, so a
caller without network can ask it. No setting turns the store off: `tycswap
switch --store fable --empty` empties it, and `tycswap switch --store fable
<id>` turns it on again (the operator kept this on 2026-11-18).

**The Fable resolver.** `Store.ResolveFableLiveSlot` never reads
`.claude.json`: every session writes its own account there (P2, P5). It
tries, in order, A68's sources on the Fable lineage: `backup`, exactly one
OAuth slot's Fable login holding the live credential's lineage (fingerprint
or the same account block); `chain`, `fable/live_lineage.json` naming this
fingerprint while the roster still maps its identity to its slot; `profile`,
the profile endpoint's account, written to the Fable chain record unless the
store is a `WithoutLineageWrites` view and asked once per fingerprint per
process; else no slot. `Store.HarvestFableLive` is A68's harvest on the
Fable lineage: when chain or profile named the live slot and its Fable login
has another fingerprint, the live login's account part is written into it,
under the store lock and the Fable store's credential-refresh and
storage-write locks, re-checked there. The live credential is never written.
`Store.RefreshFableBackupGuarded` is `RefreshBackupGuarded` on a Fable login:
under the store lock, declined while the Fable store is on that account, by
its last install too (`ResolveOwnerSlot`; Claude Code refreshes it, and the
stored file may hold a refresh token it rotated away), its lineage
re-checked, and only that login's
file written; `invalid_grant` comes back in the outcome.

**The last install.** Claude Code rotates the Fable store's login on its
own; until a harvest or the profile endpoint has named the rotated login, no
backup and no chain fingerprint holds it. When the profile endpoint then does
not answer (network, 429, 5xx), or the store lock keeps it out, or the
rotation lands while a switch waits for Claude Code's lock, the login reads
as nobody's. `Store.ResolveOwnerSlot`, which every caller that backs up,
replaces or keeps the live login asks, takes such a login for the account
tycswap last installed in the store: the slot the store's chain record
names, whatever fingerprint it carries, while the roster still maps the
identity it was written for to that OAuth slot (`Source` `last-install`,
no `Identity`); on the default store, below, the roster's active account.
Every Fable switch and renewal writes the record, and only
Claude Code's own refresh writes the store's login otherwise. Because this is
an assumption, a login backed up on it also goes to the unclaimed stash
(`{"lineage":"fable","reason":"last-install","slot":"N"}`, logged at INFO),
and the account's previous Fable login stays in `.prev`: a wrong assumption
loses nothing. `EmptyFable` removes the record with the login, so a login
that later appears in an emptied store has no last install and is stashed.
`ResolveFableLiveSlot`, `FableStore()`, the engine, `list`, `status` and the
dashboard keep their answer: no slot.

The default switch's outgoing backup asks `ResolveOwnerSlot` too. While some
account has a Fable login, a slot only `~/.claude.json` names (backup, chain
and profile missed it, `Source` `config`) is no evidence: every Fable session
writes its own account there, so the default login, rotated and not yet
harvested, was backed up into the Fable session's account's slot whenever
the profile endpoint missed. Such a login belongs to the default store's last
install: the roster's active account, which every default switch writes and
`move` and `swap` keep, when it is an OAuth account (the default chain record
keeps its A68 meaning: profile answers only). The switch backs the login up
into it as `unresolved` (A68's pre-fix backup,
`.prev` kept) after stashing a copy (`{"reason":"last-install","slot":"N"}`,
logged at INFO), and a target that is that slot repairs the config. With no
last install the login is stashed and the target activated without an
outgoing backup, as for an unmanaged live login (`from` null); that is the
case only when the roster names no active OAuth account. `switch N`'s
already-active check takes the same owner: with the config naming N and the
default login N's last install's, another account's, it switches instead of
answering `Already on`. `switch` and `switch --strategy` take it as their
current account too (2026-11-19: before, they read the config's slot, so
`next-available` counted on from the Fable session's account and the
rotation answered `Already on` that account;
`TestSwitchByStrategyTakesTheOwnerAsCurrent`). Without a
Fable login the switch is v0.15.1's: the config's slot takes the login
(`TestDefaultSwitchWithoutFableLoginsBacksUpIntoTheConfigsSlot`), and
`switch N` answers `already-active` and writes nothing
(`TestSwitchToTheConfigsAccountWithoutFableLoginsIsAlreadyActive`), each the
same result as the v0.15.1 tag.

The default resolver is unchanged except one line: when `.claude.json` names
the Fable store's live account (by backup or chain) and the default
credential is another slot's, A68's `live config names slot …` line is
logged at DEBUG, not WARNING. A Fable session writing its account into the
shared config is expected, and each timer tick is a new process.

**The switch.** `switching.SwitchFable(fable, id, opts)`:

1. Before the locks, the target must be a roster OAuth account with a Fable
   login whose plan offers Fable: `FableCapable`, read from the roster and the
   default login on every scope, so the sign-in, the pool and the switch give
   one answer. Refusals, nothing written: an unknown id,
   `Account-N is an API-key account; the Fable store holds OAuth logins
   only`, `Account-N has no Fable login: tycswap add --login --lineage fable
   --slot N`, `Account-N's plan offers no Fable`. When the target is already
   the live Fable slot (profile allowed), the result is `already-active`.
2. The locks, in order: the tycswap store lock `<root>/.lock`, then Claude
   Code's credential-refresh lock (`AcquireProfileCredentials`) and
   storage-write lock (`AcquireProfileStorageWrite`) of the Fable store
   (`Store.WithFableLocks`). No config lock; the default store's Claude Code
   locks are not taken.
3. Under them the roster is read again, and the target and the outgoing
   login are resolved from it, since a swap, move or `add --slot` may have
   committed while the switch waited. The target must still be the account
   it named, else `Account-N changed while the switch waited for the store
   lock; nothing switched. Retry the command.`, and still hold its Fable
   login. The live login is read again and its owner resolved
   (`ResolveOwnerSlot`: backup, chain, a profile answer asked before the
   locks, else the last install); when that is the target, nothing is
   written and the result is `already-active`, also before the locks. It
   goes into its account's Fable login (`.prev` kept, plus the stash copy
   for a last install), or, with no owner, into the unclaimed stash in
   `credentials-fable/` with the context `{"lineage":"fable"}`.
4. The target's Fable login is installed with the store's own seat-wide keys
   spliced in, by atomic rename, 0600: a running session reads it on its next
   request. The Fable chain record names it.
5. The result is `{"schemaVersion", "switched", "from", "to", "reason",
   "store": "fable"}`, with `from` and `to` as account refs (`from` null for
   an empty store or an unknown login).

`switching.EmptyFable` takes the same locks, backs the live login up as in
step 3, leaves the store's seat-wide keys only, or no file when it has
none, and removes the Fable chain record; its reason is `emptied`.

`core.Switcher.SwitchFable` is the switch by hand: under
`fable/.autoswitch_state.lock`, and a switch stamps the hold (`lastSwitchAt`,
`lastSwitchTo`, `lastSwitchBy: "hand"`) in `fable/autoswitch_state.json`
only: A94's hold is per store, and `byHand`'s roster mark cannot see a Fable
switch, since the roster does not change. Through `SwitchFableBy` it logs
`Switched the Fable store to account N by <actor>`, the caller's:
`cli(…)` for `switch --store fable` and `add --login --lineage fable`,
`tray` for the in-process tray; the dashboard passes none and logs the line
itself with its own (A104), so each move has one line. `EngineSwitchFable`
stamps nothing (the engine does, A102). `EmptyFable` runs under the same
state lock while the store holds a login, and `EmptyFableBy` logs `Emptied
the Fable store by <actor>` alike, `by hand` when `remove` or `add --slot`
empties the store (A101). On the command line: `tycswap switch --store fable
<id>`, `tycswap switch --store fable --empty`, and `--store default`, the
switch as without `--store`. With `--store fable` the id may stand anywhere
among the options (`translate.go` takes the first argument that is neither
an option nor an option's value); without it the id still comes first, as
before. `--store fable` with `--strategy`, `--force`, no id, or an id and
`--empty` exits 2 with one line in any order, `--store fable takes an
account or --empty`; another store name exits 2 with `unknown store: X`.

**Per-lineage quarantine.** A dead Fable login is quarantined in
`fable/autoswitch_state.json` only, in the root file's `quarantine` shape
(reason `invalid_grant`, `refreshTokenFingerprint`); the default store never
reads that file, and a dead default login stays in the root file. The engine
that writes it is A102's.

**The variable in tycswap's own processes.** Every child of a Fable-store
session inherits `CLAUDE_SECURESTORAGE_CONFIG_DIR`: its status line, its
quota hook, any `tycswap` an agent runs. `cclock` honours the variable, the
default credential path does not, so such a process would switch the default
login under the Fable store's Claude Code locks. At the top of `cli.run`,
and in `start` (A103's) right after it has captured the environment its child
gets, a value equal to `paths.FableStoreDir` (cleaned; case-insensitive on
Windows) is unset for the process and `cli.fableSessionProcess` set, so
`start`'s own start-login selection takes the default store's locks too. Any
other value is the user's own and left as it is. `launch.ScrubAuthOverrides` keeps the variable in its list (A92).

**macOS.** `Store.ForFable` returns `ErrFableUnsupported`: "The Fable store
needs a file-based credential store; macOS keeps Claude Code's login in the
Keychain". Claude Code's Keychain naming under the variable is unknown.
Windows uses the same file layout; it is built, not tested on a live box.

**No Fable login, no change.** A box where no account has a Fable login
behaves as v0.15.1: no command creates `fable/` or `credentials-fable/`, no
document gains a field, and the variable's removal (above) is the only
difference in `start`'s environment. `TestNoFableLoginNoChange` runs `list
--json`, `status --json`, `auto --once --json --dry-run`, `switch 2 --json`
and `switch 2` on the fixture store and checks that neither directory
appears and that each JSON document has v0.15.1's key paths; their
normalized output was byte-identical to origin/main's when it was written.

**Helpers for the other lanes.** `reporting.ModelFamily` (was
`modelFamily`), the `store` field of a `default_models.json` record
(`"fable"` for a Fable-store session, omitted for the default store's), and
`reporting.RecordedSession(root, id)`, a session's recorded family and store.

## A101. A Fable login is signed in once per account and box

**Why.** A Fable login is an account's second, independent sign-in (A100):
its own OAuth grant, never a copy of the default login, whose refresh token
would then rotate in two places. The operator signs each account in a second
time in the browser, once per account and box (design, "Limits"). `add
--login` already drives that browser sign-in in a scratch profile (A56, A82);
what it leaves is stored as the account's Fable login instead of its default
login. Every command that renames, removes or moves an account's files takes
the Fable login with them.

**The sign-in.** `tycswap add --login --lineage fable [--slot N] [--switch]
[-- LOGIN-ARGS...]`; `--lineage=VALUE` too. `--lineage default` is `add
--login` as before; another value exits 2 (`unknown lineage: X (default or
fable)`); `--lineage` without `--login` exits 1 (`--lineage goes with
--login`), and with `--alias` exits 1: an alias names a new account. On macOS
it exits 1 with `ErrFableUnsupported`'s text. Every one of these is refused
before Claude starts. With `--slot N` it first prints `Sign in as <email of N>
in the browser.`; an `N` that names no account is refused there. The scratch
profile, its cleanup and the store-as-soon-as-complete rule (A82) are those of
`add --login`.

**The identity rules.** `lifecycle.AddFableLogin(s, src, slot)`, through
`core.Switcher.AddFableLogin`, reads the scratch login's identity and
credential. It refuses, storing nothing, exit 1, one line each:

- an API key or an incomplete login: the `add --login` wording;
- an identity that names no OAuth roster account: `Account <email> is not
  stored yet: add it first with tycswap add --login.`;
- `--slot N` naming another account: `This sign-in is <email>, not Account-N
  (<email N>); nothing stored.` (without the parenthesis when slot N is empty);
- a plan without Fable (`FableCapable` on the default login): `Account-N's
  plan does not offer Fable.`;
- the refresh-token fingerprint of the account's default login or of the live
  default credential: `This is Account-N's default login, not a second
  sign-in; sign in again in the browser.`

On success, under the store lock and once the roster re-checked there still
holds the account, it calls `SetupFableDirectories` and writes the account-only
credential to the account's Fable login, keeping `.prev`; a refusal of that
re-check leaves a box without a Fable login without `fable/` and
`credentials-fable/`. Nothing else is written: `sequence.json`,
`configs/`, `credentials/`, `~/.claude/.credentials.json` and `~/.claude.json`
stay byte-identical. It prints `Stored the Fable login for Account-N
(<email>).`

**The first login turns the store on.** When the Fable store holds no login,
or with `--switch`, the command then calls `core.SwitchFable(N)`, a switch by
hand that holds the Fable engine for the cooldown (A94 per store), and prints
`The Fable store now uses Account-N.` A second account's Fable login is only
stored. When the signed-in account owns the Fable store's login, its last
install included (`ResolveOwnerSlot`, A100), the new login also replaces the
store's, under the Fable store's locks (A100) with the live login re-read
there, keeping the store's MCP logins, and the chain record names it; a login
replaced on the last install's assumption goes to the unclaimed stash
first. Otherwise the store keeps the older grant, and the next tick's harvest
(A100) would write that older grant back over the new file. This is how a dead
live Fable login (its `invalid_grant` quarantine names the live fingerprint) is
repaired: the store is usable again at once.

**Lifecycle.**

- `remove` deletes the account's Fable login and its `.prev`. When the account
  owns the Fable store's login (`ResolveOwnerSlot`, before the roster lock: a
  rotated login nothing else names is its last install's, A100), the store is
  emptied first by `core.EmptyFable` (under the Fable state lock); if that
  fails, nothing is removed and the error is named. Under the roster lock a
  store that took the account again meanwhile, by the same resolution,
  refuses.
- `move` and `swap` carry the Fable login with its account in
  `credentials-fable/`: written under the new key, staged in
  `credentials-fable/.swap-staging-creds-N.json` for a same-email swap (before
  anything changes), rolled back with the default backup, and the old key
  removed after the commit. When a moved account owns the Fable store's login
  (`ResolveOwnerSlot`), `fable/live_lineage.json` is rewritten to its new number
  under the store lock; for a login only its last install names, the record
  moves with its fingerprint, so the login stays that assumption (A100).
  Once the store lock is released, `move`, `swap`, `add --slot` and
  `add-token --slot` carry the renumbering into `fable/autoswitch_state.json`
  under its lock (`autoswitch.RenumberLive`): `lastSeenLive` and
  `lastSwitchTo` follow the account they name, every other field stays. No
  switch stamp is written, and the Fable tick trusts only that (A102), so
  before 2026-11-19 its next tick reported `live-login-changed` for an account
  that had only changed its number. A Fable tick between the roster's commit
  and that write may still report it once.
- The rule under both: a Fable login belongs to the identity it was signed in
  as. `add` and `add-token` with `--slot N` follow it too (added after lane
  B's report): an account the add moves from its old slot to N takes its Fable
  login, and the chain record when the store is on it; an identity the add
  displaces from N loses its Fable login, the store emptied first when it is
  on N, as for `remove`; a new identity in N takes none, so a login left under
  N's key goes; the identity already in N keeps its Fable login and `.prev`
  byte for byte (`add --slot N` of it renews its default login, which
  tycswap's own hints recommend). Each step runs beside the default side's:
  a displaced account's Fable login goes right after its default files, a
  moved one is written under the new key and leaves the old one once the
  roster is written, so an add that fails before either (a live session on
  the account, say) leaves the Fable logins where the roster has their
  accounts. `import` replaces no slot's identity (`--force` overwrites the
  slot of the same identity, whose Fable login stays); a new account it puts
  in a slot the roster does not hold deletes a Fable login left under that
  key, which an older tycswap's `remove` or `move` can leave.
- `purge` removes `credentials-fable/` and `fable/` with the rest of the store.
  Its session check covered tycswap's session profiles and the retired groups
  only, not `~/.claude/sessions`, where every session, a Fable-store one
  included, registers, and a session's record does not name its store. While
  the Fable store holds a login, purge therefore also refuses while any live
  Claude session is registered there.
- `export` reads the default logins only and never carries a Fable login;
  `import` never writes one and leaves `credentials-fable/` as it is.
- Without a Fable login none of these, nor `add` or `add --login`, creates
  `fable/` or `credentials-fable/`, and their output is v0.15.1's
  (`TestNoFableLoginLifecycleNoChange`, compared with origin/main 00d9582).

**Deciding from the resolved live slot.** A Fable session writes its account
into the shared `~/.claude.json` (pilot P5). `add --login` decided its
renewal of the live login (A65) from that config identity, so a default
sign-in of the Fable store's account force-switched the default login onto it.
It now decides from `ResolveLiveSlot(data).Slot`, the default credential's own
slot. A plain `add` of the live login stores the live credential under the
config identity; when that identity is the Fable store's live account and the
default credential resolves to another account, it now refuses (`~/.claude.json
names Account-N, the Fable store's account, but the default login is
Account-M's; nothing stored.`). Of the other `GetCurrentAccount` callers,
`switching/perform.go`, `switch.go` and `provenance.go` use it by A68's
design, the resolver reads it as its first input, the group profile ones read a
group's own config, and the status, dashboard callout, first-run prompt and
`add-token` hint only display it. The transfer adapter's `CurrentAccount`
answers the resolved slot's account too (none while Fable logins exist and
only the config names the slot), so `export` reads the live credential only
for the account it belongs to, and with that account's stored config when
`~/.claude.json` names another; before, exporting the Fable store's account
wrote the default login as its own. `authmode.go` gates the API-key
confirmation on the config's slot and is left (a Fable session never writes an
API-key account there).

## A102. One tick per store

**Why.** A100 gives each account a second login and the Fable store to run
it. The stores must rotate apart: "opus session with opus limit auto swich
to fill the accounts with no fable left" (the operator, 2026-11-16), while
the Fable store sits on an account that still has Fable. The timer runs
`tycswap auto --once --json` every 2 minutes, the quota hook the same, and
the status line `tycswap auto --observe --json`.

**Two ticks.** `auto --once` (the timer, the quota hook, `--observe`) runs
the default tick first, then, while auto-switch is on and the Fable store is
on (A100), the Fable tick. A dry run runs both dry, over
`WithoutLineageWrites` views. A real Fable tick first harvests the store's
rotated login (`HarvestFableLive`), and keeps one idle Fable login alive
after (below). The exit code is the more significant of the two outcomes: 1
if either errored, else 0 if either switched, else 3 if either was blocked,
else 2. With no Fable login, or with the store off, only the default tick
runs and the run's output is v0.15.1's byte for byte (`TestFableTickNoChangeWithoutAStore`;
compared with origin/main when written); with the store off a real run still
keeps one idle Fable login alive (below).
With auto-switch off no Fable tick runs. A real `--once` run then still
harvests the store's rotated login, as every read harvests the default
login's (A68): off stops switches, and the stored copy is tycswap's own. It
does nothing else for the Fable store, writes no Fable state and prints
nothing more (`TestAutoSwitchOffStillStoresTheFableStoresRotatedLogin`).

**The adapter.** The Fable engine is `autoswitch.NewEngine` over
`cli.fableAdapter`, which satisfies the frozen `autoswitch.Switcher` (A13):
the live account is `ResolveFableLiveSlot`, a live login whether the store
holds one, the logins and their refresh (`RefreshFableBackupGuarded`) and
writes the Fable logins, the switch `EngineSwitchFable`, the roster answers
(kind, identity, email, uuid backfill) the default store's, and the usage the
default store's collector, so one fetch path with the default login serves
both stores (A100, 1.7). It names no session owner and has no rotation
hooks: no profile or claim owns a Fable login. Its poll inputs are
registered under the Fable scope's key. Its `RecordedSwitch` is the Fable
state file's switch stamp (`lastSwitchTo`, `lastSwitchAt`), which every
tycswap switch of the Fable store writes, by hand or the engine's, and
nothing else does, so the engine tells a switch from a login made elsewhere
and records `lastSeenLive`/`lastSeenAt` in the Fable state file. The Fable
chain record is no such proof: the resolver writes it whenever the profile
endpoint names a login nobody switched in, and a real Fable tick resolves
before the engine asks, so a login made elsewhere (a `/login` in a
Fable-store session) went unreported until 2026-11-19; it is now reported
as `live-login-changed`, as the default store reports one. Its state file is
`fable/autoswitch_state.json`.

**The pool** (`reporting.FablePool`) is every account that is enabled and
rotation-eligible by the default rules, able to run Fable
(`Store.FableCapable`), holding a Fable login, and not quarantined in the
Fable state file (`reporting.FableQuarantine`, A104). The default store's
quarantine does not exclude.

**The Fable settings** are the merged settings with the model set to every
Fable window name an account's cached usage reports; they are counted
always, with no session following, no reservation and no `unservable`.

**The default store beside it.** While the Fable store is usable,
`reporting.DefaultSessionPolicyResult` counts the families of the sessions
whose record names no store, but for Fable: a Fable session on the default
login is not followed (A108; before 2026-11-20 it counted Fable); a session
without a record still counts as one of unknown family. It always reserves the Fable pool's accounts with Fable
room (A91's reading at `autoswitch.modelThreshold`) and the Fable store's
live account, whatever its room and whatever runs: they rank last (A71) and
an active one hands over under the cooldown (A77). Among them the Fable
store's live account ranks last as a target of the default tick
(`PolicyResult.ReservedRank`, `autoswitch.WithReservedRank`, A131), and a
default login on it hands over, under the same cooldown, to another account
below every bar, reserved or not, unless that account's Mythos login is in
use (A131); from a reserved account that is not the Fable
store's the hand-over still takes an unreserved one only, so it does not
come back (2026-11-19: before, the default tick took the Fable store's
account as readily as another reserved one and then stayed there with
`reserved-no-target`, and Opus sessions burned the windows the Fable
sessions ran on while another account was free). With no such account it
stays and says `the Fable store runs on the active account; no other
account is below every bar unless its Mythos login is in use`. An account
with Fable room but no Fable login is the default store's. Off, unusable or
without Fable logins, the policy is A91's. The dashboard's counting and
ranking, `start`'s ranking and `switch --strategy` read the same result, but
for `ReservedRank`, which orders the default tick and an idle Opus start
only.

**The status-line record.** `--observe` records `"store":"fable"` for a
session whose process runs under the Fable store (`cli.fableSessionProcess`,
A100); a default-store record has no store field, and a changed store is
news like a changed family. Near a bar in a Fable-store session it marks the
Fable store's live account due, not the default login's.

**Poll plans.** `AccountInfo.FableLive` marks the Fable store's live account
(resolved without a chain-record write), and the planner plans it as an
active account, on every scope's inputs, the Fable scope's Fable windows
included. An inactive account's plan is unchanged.

**Hold, cooldown and quarantine per store.** Each state file keeps its own
switch stamp: a switch of the Fable store by hand holds only the Fable tick
for `autoswitch.cooldownSeconds` (`switched by hand to account N; held for
another Ss`), a default one only the default tick. A start hold in the Fable
state file, from `start`'s selection on the Fable store (A105), holds the
Fable tick and the move off a dead Fable login as one in the root state file
holds the default tick. The Fable engine
freshens a target through `RefreshFableBackupGuarded`; `invalid_grant`
quarantines the account in the Fable state file only, through the engine's
own path (`account-quarantined`), and a new Fable login with another
fingerprint releases it (`credentials-replaced`) on the next real Fable
tick.

**Keep-alive.** A refresh token not used for 28 days may expire (measured in
the pilot). A real `auto --once` run with auto-switch on refreshes at most
one idle Fable login, after the Fable tick or, while the store is off, in
its place (2026-11-19: before, only a real Fable tick did, so after `switch
--store fable --empty` every Fable login went unused until it expired): not
the Fable store's account, its last install included (`ResolveOwnerSlot`,
A100; before 2026-11-19 only the resolved live slot was left, so a store
on a rotated, unattributed login made the keep-alive pick that account,
which the guarded refresh declines, and no other login was kept alive),
not quarantined, last refreshed more than 14 days ago, the oldest such. With the store off the run's stdout is unchanged unless a
refusal quarantines the login (`account-quarantined`, `store` `fable`). The last refresh is its `expiresAt` less 8 hours, the
measured access-token life; a credential carries only its expiry. The
refresh is `RefreshFableBackupGuarded`, logged at INFO as `Kept the Fable
login of account N alive`; `invalid_grant` quarantines as above, through
`autoswitch.Quarantine`.

**The live Fable login's own health** (added after lane C's report). The
Fable tick's usage is the default login's, so a dead live Fable login showed
nowhere and `FableStore().Usable` stayed true. A real Fable tick, before the
engine, now checks it: once its access token has expired (Claude Code
refreshes a login in use before that), `Store.RefreshFableLiveGuarded`
refreshes it in the store under the Fable store's locks, so Claude Code's own
refresh is kept out and a rejected grant is the login's verdict, not a lost
race; the result goes into the store and the account's Fable login file. An
`invalid_grant` or missing refresh token quarantines it per (account, fable)
as above, with the live credential's fingerprint. A live login quarantined so,
now or by an earlier keep-alive, is left at once: the store moves to the
account `autoswitch.StartTargets` ranks first in the Fable pool, else the
pool's first account not known to be at a limit
(`autoswitch.NotKnownAtALimit`: one whose usage is not read yet comes before
one whose cached reading shows a limit reached, 2026-11-19), else the pool's
first (`switch`, trigger `failover`, stamped as the engine's own by
`autoswitch.EngineSwitch`), or, with an empty pool, is emptied (`no-switch`,
`no-viable-target`, the tick blocked), and `start` then sends Fable sessions
to the default login with its notice. Under the Fable state lock the move
resolves the live slot again: a store that another tick (the timer's, a
status line's) has meanwhile moved off the dead login or emptied is left as
it is, with no event and no log line. A switch of the Fable store by hand
within the cooldown holds the move or the emptying as it holds a tick (A94,
`no-switch`, `cooldown`); the quarantine alone already makes the store
unusable, so `start` sends Fable sessions to the default login meanwhile.
Auto-switch turned off since the tick began holds them too: the key is
re-read under the state lock, as the engine's own switch re-reads it (A83),
and the engine then says `disabled`; the quarantine stands, as a dead
target's does. The default login is never read or written. This replaces the rule that the
live Fable login is Claude Code's alone to refresh (plan 1.7) for an expired
one.

**Events and log lines.** Every Fable tick event carries `"store":"fable"`
(`cli.fableEvent`, modelled on `groupEvent`); the default tick's are
unchanged. Human lines start with `Fable store: `. An `account-quarantined`
line names the Fable sign-in, not the default login's re-add: `Fable store:
Account-N (<email>) quarantined: <reason>. Sign its Fable login in again with
'tycswap add --login --lineage fable --slot N'.` (2026-11-19). The engine logs its
switch as `Switched the Fable store to account N by engine:<trigger>`
through the switcher's optional `LogSwitch`, and its other lines through a
logger prefixed `Fable store: `.

**Limits.** The `tycswap auto` loop and an engine hosted by `web` or `app`
run no Fable tick (the operator's boxes run the timer). Usage is one row per
account fetched with the default login, so a dead default login leaves the
account's usage unknown and blocks it for both stores until it is signed in
again.

## A103. tycswap start picks the store by the session's model

**Why.** flakelab routes every `claude` through `tycswap start -- <real
claude> "$@"`; on POSIX `start` execs, so the Claude process keeps its pid and
the environment `start` composes. A100's design puts sessions whose model is
Fable on the Fable store through `CLAUDE_SECURESTORAGE_CONFIG_DIR` and keeps
every other session on the default login. Before this entry `start` counted
that variable as a user override (A90): it skipped selection and handed the
variable on, so every `claude` started from a Fable session's shell (an
agent's, a restore's) stayed on the Fable store whatever its model.

**The session's model** (`cli.sessionFamily`). The first source that names a
model answers; its family is `reporting.ModelFamily` (aliases and full ids,
a `[1m]` suffix included, and a provider's id such as
`us.anthropic.claude-fable-5-2-v1:0`, which names its family as a whole
word; an id naming two families answers none), so an unknown model answers
with no family. The settings are, in Claude Code's precedence, the
`--settings` value (`--settings X` or `--settings=X`, the last before a
`--`: JSON when it starts with `{`, else a file, relative to the working
directory), then `<cwd>/.claude/settings.local.json`,
`<cwd>/.claude/settings.json` and `~/.claude/settings.json`; a source that
cannot be read or parsed, or sets no string, is skipped.

1. `--model X` or `--model=X` before a `--`; given twice, the last wins.
2. `ANTHROPIC_MODEL` (on Windows the name matches in any case).
3. `env.ANTHROPIC_MODEL` of the settings, in their order: Claude Code
   applies a settings file's `env` block as environment, so it beats every
   settings `model` (2026-11-19).
4. `--resume ID`, `-r ID` or `--resume=ID`, ID a UUID: the session's record
   in `default_models.json` (`reporting.RecordedSession`), else the model of
   the last assistant line of `~/.claude/projects/*/ID.jsonl`, reading at
   most the last 1 MiB. Claude Code's own `<synthetic>` lines and subagent
   (`isSidechain`) lines are skipped. A path that resolves out of
   `projects/` through a symlink is not read; only regular files are.
5. The `model` key of the settings, in their order (`--settings` since
   2026-11-19); the first that sets it wins.
6. Otherwise there is no family.

`RecordDefaultSessionModel` keeps the reading without provider ids: a
status line reporting a provider's id records nothing, as in v0.15.1, since
such a session runs on no login of tycswap's.

**Overrides keep A90.** With `CLAUDE_CONFIG_DIR` set, another
`launch.AuthOverrideEnvVars` variable set, `--bare` in the child's
arguments, or `CLAUDE_SECURESTORAGE_CONFIG_DIR` set to anything but the
Fable store directory (empty included), `start` reads nothing and changes
nothing but an inherited `TYCSWAP_START_HOLD`, which it removes (A105). A value equal to `paths.FableStoreDir` (cleaned; on Windows
case-insensitively, the name too) is tycswap's own, not an override.

**The store** (`cli.startEnvironment`):

| Family | Fable store | Child environment | Default start-login selection | Notice |
| --- | --- | --- | --- | --- |
| `fable` | on and usable | `CLAUDE_SECURESTORAGE_CONFIG_DIR=<FableStoreDir>`, in place of an inherited entry or appended; removed when no marked account has a usable Fable login (A105), or when no account the store can hold has room for Fable and the default login's account has (A112) | runs, and start's selection runs on the Fable store too (A105) | none, or A105's or A112's |
| `fable` | off or unusable, with Fable logins | the variable removed if it named the store | as before | `tycswap start: the Fable store has no usable login (<reason>); starting on the default login.` |
| other or none, or no Fable logins | – | the variable removed if it named the store | as before | none |

Every other variable passes unchanged and in order, but
`TYCSWAP_START_HOLD` (A105). Without a Fable login
the session's model is not read and the child environment is the input
environment, less a variable naming the Fable store (A100's one difference).
`--account N` with a Fable session on the store selects the default login and
is the Fable store's sole start-login candidate (A105); until 2026-11-19 it
selected the default login only and the Fable store did not move.

**No network, nothing created.** The state is `Store.FableStore()`: the live
slot from backup or chain only, and an unknown slot counts as usable. A box
without `credentials-fable/`, or a store `start` must not read (not a
directory, not private, run as root), opens nothing. No directory is created.

**Platforms.** macOS never sets the variable (`ForFable` refuses), so a
Fable session there starts as before, with no notice. Windows spawns with the
composed environment and matches the variable's name and value
case-insensitively (`cli.startWindows` is the tests' seam).

**Limits.** `--continue` and the `--resume` picker give no family (O10): such
a session starts by its settings' model. A session that changes model with
`/model` stays on the store it started on. Fable subagents of an Opus session
run on the default store. Not read, so a Fable session started by one of
these starts on the default login with no notice: no model named anywhere,
`--model default` or a settings `model` of `default` (Claude Code then takes
the login's own default, which its plan or organization decides);
`ANTHROPIC_DEFAULT_MODEL` and `ANTHROPIC_DEFAULT_<FAMILY>_MODEL` (`--model
opus` with `ANTHROPIC_DEFAULT_OPUS_MODEL=claude-fable-5-2` reads as Opus);
managed settings; and a wrapper command (`tycswap start -- bash -lc "claude
--model fable"`: only the child's own arguments are read).

**Tests.** `TestSessionFamilySources` drives each source, each precedence
pair, a last assistant line beyond 1 MiB and one just inside it, a malformed
transcript, a non-UUID id, a bare `--resume`, `--continue` and `-c`, the
settings' `env` block, `--settings` as a file and as JSON, and a provider's
id; `TestModelFamilyOfAProviderID` and `TestProviderIDRecordsNoSessionModel`
in `reporting` the provider ids;
`TestSessionFamilyTranscriptStaysInProjects` refuses file and directory
symlinks out of `projects/`. `TestStartPicksTheStoreBySessionModel` runs
`start` for every table row, inherited values kept or removed, an unknown
live slot and a profile endpoint that fails the test;
`TestStartOverridesKeepAFableSessionUnchanged` every override;
`TestStartAccountWithAFableSession` `--account` naming an account without a
usable Fable login and an unchanged Fable store; `TestStartOnWindowsMatchesTheVariableCaseInsensitively` and
`TestStartOnMacOSLeavesAFableSessionAsBefore` the platforms; and
`TestStartWithoutFableLoginsPassesTheEnvironmentUnchanged` the byte-identical
environment of a box without a Fable login.

## A104. Two live logins on every surface

**Why.** A100 gives each account a second login and the Fable store to run
it; the design says the dashboard and the tray show two live accounts, the
default login's and the Fable store's. Every surface took its one live
account from `store.ResolveLiveSlot` on the default store: `list` and
`status` through `reporting.BuildAccountsInfo`, `/api/state` as
`activeNumber` and the rows' `isActive`, the tray as `In use`, the TUI as
`ActiveNumber`. The operator's standing rules for the tray hold: one page,
every account's windows and reset times visible at once, no settings in the
tray (A84). On 2026-11-18 he decided the tray gets a Fable switch (open
point O11: "Add a tray button").

**The rule.** While some account has a Fable login, every surface below adds
the Fable store beside the default login; without one, nothing changes: no
field, line, chip, label, button or item (A100's no-change rule). The
default login's markers (`(active)`, `isActive`, `In use`, *Switch*, *Switch
now*, the best candidate) stay the default login's.

**One reading.** `reporting.readFableView` reads the Fable store once per
pass: `Store.FableStore()` for on, off, usable and the reason; the live slot
by `ResolveFableLiveSlot` on a `WithoutLineageWrites` view (backup, chain,
then the profile endpoint, never written), as `AccountInfo.FableLive` is
resolved (A102); and per account its Fable login, `sign in again` when the
Fable state file holds a quarantine entry for it, `invalid_grant` or the
engine's `identity-conflict` (the pool and the tray leave out either). Every
reader of that file on these surfaces, the pool (A102) and the tray included,
reads it through `reporting.FableQuarantine`: an entry whose fingerprint is
neither the account's stored Fable login's nor the Fable store's live
login's names a login signed in again since and is left out, so the row
reads `ok`, *Use for Fable* is there and the pool has the account right after
the sign-in, with auto-switch on or off; the engine releases the entry at its
next real tick as before. An entry without a fingerprint stands. The
store's state is `on`, `off` (no login held) or `unusable` (the live login's
grant quarantined).

**`list`.** `list --json` adds `fableAccountNumber` (int, or null while the
store is off or its slot unknown) and on each row with a Fable login
`fableLogin: {"live", "status"}` (`ok` or `sign in again`). The human list
adds, after every existing marker, `(Fable store)` on the store's account,
`(Fable login)` on another account with a usable login, and `(Fable login:
sign in again)` on a quarantined one.

**`status`.** `status --json` adds a top-level `fable`, whatever `active`
is: `state`, `storeDir` (the cleaned `paths.FableStoreDir`, the value
`start` gives a Fable session), `number` (null while unknown), `source`
(`backup`, `chain`, `profile`; null while unknown), `reason` while off or
unusable, and while the slot is known `email`, `organizationName`, `plan`
(when recorded), `usageStatus` and `usage` in the shapes of `active`. The
usage is the account's shared row read the way `activeAccountUsage` reads
the default one: one collector pass over that account's info row, which
fetches with its default login when due and never with the Fable login
(A100, 1.7). flakelab's wake-parked reads `.fable.number`, `.fable.storeDir`
and `.fable.usage.*`. The human output adds one line: `Fable store:
Account-N (<email>) · Fable 34% · 7d 61%` (the fullest Fable window and the
weekly window, each when known), `… · Fable login: sign in again`, `Fable
store: on, its account unknown`, or `Fable store: off`.

**The dashboard's state.** `reporting.Snapshot` carries the same object
(`AccountsSnapshot.Fable`) and each row's `FableLogin`; `providers.merge`
passes `Fable` from the Claude snapshot. `web.State` gains `fable` (omitted
when nil) and each Claude row `fableLogin`, a plain map so the in-process
tray reads what a remote one decodes. `auto.fable` (`web.FableAutoView`,
built by the facade in `cli/webfacades.go`) is the Fable store's engine:
`lastTickAt` and `quarantine` from `fable/autoswitch_state.json`
(`autoswitch.ReadLastTick`, `reporting.FableQuarantine`), `caption` `Counting
5h + 7d` plus every Fable window name an account reports, `ranking`
`autoswitch.RankTargets` over the Fable pool less the live slot with the
Fable settings, as the Fable tick ranks (A102), and `heldUntil` (epoch
seconds) while the state file's switch by hand (A94) holds the engine. The
usage both rankings read is one collector pass. Without a Fable login the
document is v0.15.1's byte for byte.

**The route.** `POST /api/switch/{key}?store=fable` calls
`core.Switcher.SwitchFableBy` without an actor for a `claude:` key, a switch
by hand that stamps the Fable store's hold; `&empty=1`, on any key, calls
`EmptyFableBy` alike. Both run through the authenticated mutation path
(cookie and CSRF, or the bearer) and log `Switched the Fable store to
account N` or `Emptied the Fable store` with the dashboard actor, the one
line of the move. Another `store` value (the empty `store=` included: only
an absent `store` is the default switch), a `codex:` key, `force` or
`confirmAuthChange` answer 400; a server without the seam (`web.Deps.Fable`)
answers 503. The default login, `sequence.json` and `.claude.json` are not
written.

**The page.** While `fable` is present the header adds a second line under
the active account, `Fable: #N <name>` (a *Fable login: sign in again* chip
while unusable), or `Fable store off` with the command that turns it on as
its tooltip. The account table gives the store's row a `Fable` chip,
another row with a usable Fable login a `Fable login` chip, and a
quarantined one `Fable login: sign in again` in the warning style with the
command that signs it in again as its tooltip; a row without a Fable login
gets no chip, and its menu names `add --login --lineage fable --slot N`
(open point O6: no second sign-in from the page). Each row with a usable
Fable login has *Use for Fable*, which posts the route; on the store's own
row it reads *Fable here* and is disabled. The Auto tab adds one line,
`Fable store: counting 5h + 7d + Fable · next #M` under a scheduler (an
`auto.owner`). No engine this page hosts runs a Fable tick (A102), so without
an owner the line says so instead (2026-11-18): `Fable store: this
dashboard's checks do not move it; a scheduler running tycswap auto --once
does`. Nor does a scheduler's tick while auto-switch is off or the store is
off (A102), and only a switch by hand turns the store on (A100), so the line
names no next account then either: `Fable store: off; tycswap switch --store
fable <account> turns it on` while the store is off, else `Fable store: not
checked while off` while auto-switch is off.

**The tray (O11).** The Windows panel's Fable store row carries a second
outlined label `Fable` after the plan label, and its screen-reader text
ends in `, Fable store`. Every row the Fable store can take (enabled, its
Fable login usable, a plan that runs Fable, not in the Fable quarantine, not
the store's own) has a second button, `Use for Fable`, beside *Switch*; it
switches the Fable store through the dashboard (the remote tray posts the
route, the in-process app calls `SwitchFable`), and a click while one is in
flight is dropped, as for the Auto-switch button (A94). The button sits
left of *Switch*, which keeps the column's right edge; Tab reaches it just
before its row's *Switch*; its window text is `Use <row name> for Fable`;
the column widens by it only while some row has one. The note reads
`Switched the Fable store to #N.` and a notification says so; a failure
reads `Fable switch failed: <error>`. The action cell's *Switch* and `In
use` stay the default login's. The Linux and macOS menus end the Fable store
row's second line in ` · Fable` and add, after the account rows, one `Use
for Fable: #N <name>` item per such account. The icon's tooltip reads `Default #N ·
Fable #M` (`Fable off`, `Fable account unknown`) in place of the default
account's name and windows, which the panel shows. A remote tray decodes the
new fields; an older dashboard sends none and the tray shows nothing new.

**Limits.** The TUI shows the default store only: its frozen facade (A13)
has no Fable store. The page shows neither the Fable store's hold nor its
quarantine beyond the chips. The tray tooltip names the two accounts, not
their windows.

**Tests.** `TestListNamesTheFableStoreAndItsLogins`,
`TestListWithTheFableStoreOff`, `TestListWithoutFableLogins`,
`TestStatusFableObject` (each state, the wake-parked paths),
`TestSnapshotCarriesTheFableStore` and
`TestFableQuarantineLeavesOutLoginsSignedInAgain` in `reporting`;
`TestMergePassesTheFableStoreFromClaude`; `TestStateCarriesTheFableStore`,
`TestStateRowFableLoginIsAMap`, `TestStateWithoutFable`,
`TestFableSwitchRoute`, `TestFableSwitchRouteRefusals`, the route in the
auth tables (401, 403) and `TestAppJSFable` (`testdata/fable.cjs`: every
page element with a Fable store, none without, the button's request) in
`web`; `TestDashboardStateWithoutFableLogins` (the state document against
`testdata/nofable-state.json`, recorded from the tree before these
surfaces), `TestDashboardSwitchesTheFableStoreOnly`,
`TestDashboardAutoFable`, `TestDashboardPageWithTheFableStore`,
`TestFableLoginSignedInAgainReadsOkAtOnce`,
`TestFableLoginSignedInAgainWithAutoSwitchOff` and
`TestFableQuarantineOfAnyReasonReadsSignInAgain` and
`TestFableSwitchRouteRefusesAnEmptyStore` over a real switcher in `cli`. The tray: `TestPanelFableLabelAndButton` (the label, the button and
the A84 fit, wide and narrow) and `TestPanelFableAccessibleTexts`
in `tray`, `TestNativePanelUseForFable` on Windows; `TestTrayFablePanelRows`,
`TestTrayFableTooltip`, `TestTrayFableMenu`, `TestTrayFableClick`,
`TestTrayFableClicksDropWhileOneIsInFlight`,
`TestTrayWithoutFableLoginsNothingChanges`,
`TestAppTraySwitchesTheFableStore` (the in-process switch over a real
store), `TestRemoteSwitchFablePostsTheStore`, `TestRemoteModeUsesForFable`
and `TestRemoteTrayDecodesTheFableFields` (with and without the fields) in
`cli`.

## A105. A start login is used at its limit, and auto-switch waits until the session is up

**Why.** The operator's order (2026-11-19): "i wanted that even account on
the limit is used to start the session and then switched away after it is
started". His marked start login was at its weekly limit, so A90's ranking
dropped it (`StartTargets` excluded a candidate whose binding headroom was
spent), `tycswap start` logged `start: no usable start login; continuing with
the current login`, and the session started on the current login. A start
login is needed for a session's first seconds only; its own usage does not
decide it. Measured on 2026-11-19 with Claude Code through the launcher:

- Claude Code settles what the login allows before the session takes its
  first prompt (a transcript: its notice about the login at 22:35:19.544,
  the first queue operation at 22:35:19.959, the SessionStart hook's output
  at 22:35:22.38, the first user message at 22:35:22.399).
- A SessionStart hook holds the first request: a hook that slept 6 s ended
  at 09:00:25.32, and the first user message was recorded at 09:00:25.733.
  Print mode (`claude -p`) runs SessionStart hooks too, and in both modes
  the first user message was recorded only after the hook had ended (hook
  end 09:07:55.88, first user message 09:07:56.07).
- From exec to the SessionStart hook: an interactive session started with a
  prompt 3.9 s, print mode 2.1 s plus Claude Code's own 3 s wait for stdin,
  on a 16-core box under load.
- The hook's stdin is JSON with `source` (`startup`, `resume`, `clear`,
  `compact`) and `session_id`.
- Start's switch stamp did not hold the engine's at-limit move
  (`heldByCooldown`, A94), and a tick can come at any moment: the 2-minute
  timer, and `auto --once --observe` from every session's status line. A
  tick would move the login away before Claude Code had read it.

**1. Selection.** A known counted window at 100 % no longer excludes a start
target. `autoswitch.rankStartTargets` puts the accounts below every counted
bar first, then those with room (unknown, or over a bar and below 100 %),
both as A90 ranked them, then the accounts at a limit (binding headroom at
or below 0), in sequence order whatever their reading. Every other gate
stays: marked (or the explicit `--account`), rotation-eligible, not
quarantined, the token not dead, the counted-model coverage, the guarded
preparation with its ownership checks, and the re-check under the state
lock, which ranks by the same rule. `autoswitch.StartTargets`, which the move
off a dead live Fable login ranks its pool with (`leaveDeadFableLogin`,
A102), is that ranking without the logins at a limit: its order is
unchanged; the move's fallback when it names none is A102's.

**2. The start hold.** When `start` settles on a login at a limit by that
ranking while auto-switch is on, by switching to it or because it is already
the current login (`already-start-login`, `already-active`, also when the
switch under the lock finds it current), it records a hold in
`autoswitch_state.json` under the state lock: `"startHolds": {"<token>":
<expiry>}`, the token 16 random bytes (`crypto/rand`) as hex, the expiry
epoch seconds, now plus `autoswitch.StartHoldS`. With a switch the hold is
in the same write as the switch stamp. Every write by `start` or `--started`
drops the holds that have ended. The child gets
`TYCSWAP_START_HOLD=<token>`; without a hold it gets no such variable, and
an inherited one (a nested `claude` in a session that was a held start) is
removed, on the override paths too.
- While any hold runs, the engine makes no switch, whatever the trigger:
  `cooldownHold` checks it first, so the tick's own check, the check under
  `perform`'s lock and the engine variant of `stampedSwitch` all hold, with
  the event a switch by hand produces, `no-switch` / `cooldown`, detail `a
  session is starting; held for another <n>s` (the longest hold left, rounded
  up). A tick stops there, before it prepares a target. A switch by hand and
  `start`'s own selection are not held.
- A hold that ends more than `StartHoldS` from now was not written by this
  clock and holds nothing: a clock that stepped back cannot hold the engine
  longer than the bound.
- A start on a login with room is v0.16.0's: no hold, no variable, the
  current-login path silent and writing nothing; it reads the cached usage
  and the default-session policy, files only, to tell. With auto-switch off,
  an explicit `--account` at its limit is switched to as asked, with no hold
  (nothing would act on one) and the plain notice.
- A hold that cannot be written says `Could not hold auto-switch for the
  starting session: <error>` and gives the child no token. A start login
  current at its limit when `start` looks but moved by the time it holds the
  lock is `selection-changed`, launched on the current login.

**3. `tycswap start --started`**, the release, run by a Claude Code
SessionStart hook. A management form like `--list`: exclusive with the
others and with launching. It runs after `start`'s line that drops the
Fable store's variable for its own process (A103). Without
`TYCSWAP_START_HOLD` it exits at once: the store is not opened and nothing
is read or written. It reads the hook's JSON on stdin unless stdin is a
terminal; an unreadable or empty input counts as `startup`, and for `clear`
or `compact` it does nothing, as with a store `start` must not read; without
a store there is no hold to release. Under each state file's lock, the root
one's and the Fable store's (6. below), it removes its own token and the
ended holds; a state without holds is not locked, and one that loses none is
not written. A token that is not there (released before, or ended) still
gets the tick: a session that needed longer than the bound to come up is the
one that most needs the login moved before its first request. It then waits
while another session is starting: it reads both state files every 100 ms on
the store's clock until neither holds a running hold, at the latest
`StartHoldS` from its start, by which every hold it found has ended.
Then it runs one real tick of both stores, the default store's and the
Fable store's, as `auto --once` and `auto --observe` do, with the
default-session policy, and without `auto --once`'s group retirement and
Codex tick, which wait for the scheduled one as for a status line (A78): the
session's first request waits for the hook. It prints nothing on stdout,
since a SessionStart hook's stdout goes into the session's context
(`oauth.Output`'s warning is discarded while it runs; it is logged too), and
exits 0 whatever happens: a failing hook must not break a session. Tick
errors and a panic go to `tycswap.log`; the token is never logged.

**4. The bound**, `autoswitch.StartHoldS = 30`: how long the login stays
where a session started when that session never reports, a command that is
not Claude Code, a session without the hook, a crashed start. 30 s is about
eight times the slower start measured above (3.9 s), and stays below the
hook's 45 s timeout with the tick after the wait.

**5. The notice.** After a switch onto a login at a limit with a hold the
stderr line is `Switched to start login Account-<n>, which is at its limit;
auto-switch moves on once the session is up.`; otherwise it stays `Switched
to start login Account-<n>.`

**What stays.** A box that marks no account, or only accounts with room, is
v0.16.0's byte for byte: the state file, the child environment, the outputs
and the log lines (`TestStartWithRoomRecordsNoHold` pins them, recorded from
the unchanged code), but for a Fable session on a usable Fable store with
marks, which 6. below moves. Every write keeps the state file's other fields (A90).
A v0.16.0 engine reading a state file with `startHolds` ignores the key and
keeps it: every engine write is a read-modify-write of the whole object. On
Windows `start` spawns and waits, and the child gets the variable the same
way.

**6. The Fable store's start login** (2026-11-19, review F2-1). The operator's
rule is that a session starts on a start login and may run on any account
afterwards; before, a Fable session on a usable Fable store skipped the
selection and started on whatever account the Fable store sat on, a work
account included. It was missed because the accounts it matters for were
disabled when the Fable store was planned. Whether Claude Code decides a
Fable-store session's model access from that store's login alone, or also
from files every session shares (`~/.claude.json` carries the default
login's `oauthAccount` and a `modelAccessCache`), is not measured, so such a
session's start satisfies both:
- The default selection runs for every session, Fable-store sessions
  included, as above, start hold and all: the shared files name a start
  login while the session comes up.
- Then the same selection runs on the Fable store (`cli.selectFableStartLogin`):
  the candidates are the marked accounts in the Fable pool
  (`reporting.FablePool`: enabled, eligible, a plan that offers Fable, a
  Fable login, not quarantined in the Fable state file), the current account
  is the Fable store's live one (backup or chain only, no request), the
  ranking and the at-limit tier are start's, counted on the Fable windows as
  the Fable tick counts them, the switch is the engine's Fable switch
  (`cli.fableAdapter`, `EngineSwitchFable`), and the stamp and the hold go
  into `fable/autoswitch_state.json` under its lock, so the Fable tick is held
  as the default one is. The lock order is the Fable paths' own: the Fable
  state lock, then the store lock, then Claude Code's two locks of the Fable
  store; the default state lock is released before. Its lines are the
  default's with `Fable store: ` before them (`tycswap start: Fable store:
  Switched to start login Account-N.`), and a switch logs `Switched the Fable
  store to account N by start`.
- With marks but none of them in the Fable pool the session starts on the
  default login, without `CLAUDE_SECURESTORAGE_CONFIG_DIR`, and stderr and the
  log say `tycswap start: no start login has a Fable login; starting on the
  default login.` Without marks, and with auto-switch off, the Fable store is
  left where it sits, as before. Before the selection, a session without
  `--account` follows the Fable placement rule (A112): while no account the
  Fable store can hold has room for Fable and the default login's account
  has, it starts on the default login, marks or not; with an account in the
  pool that has room the selection runs as above, a marked login at its
  limit included. A selection that fails otherwise (no
  candidate could be prepared, a change under the lock) says so and the
  session starts on the Fable store where it sits.
- `--account ID` keeps its meaning for the default login and is the Fable
  store's sole candidate when it is in the pool, else the notice above.
- One token serves both holds: the Fable selection records its hold under the
  default selection's token when that one holds (`autoswitch.WithStartHoldToken`),
  and the child gets the one token. `start --started` releases it in both
  state files, waits until neither holds an unexpired hold, and runs the one
  tick of both stores. A box without a Fable login reads the Fable state file
  for a hold and creates nothing.

**Tests.** In `autoswitch`: `TestStartRanksALoginAtItsLimitLast`,
`TestStartRanksLoginsAtALimitInSequenceOrder`,
`TestStartTakesAMarkedLoginAtItsLimit`, `TestStartPrefersAMarkedLoginWithRoom`,
`TestStartTakesAnExplicitAccountAtItsLimit` (auto-switch on and off),
`TestStartHoldsALoginAlreadyCurrentAtItsLimit`,
`TestStartHoldsATargetFoundCurrentUnderTheLock`,
`TestStartHoldsNothingOnceTheCurrentLoginMoved`, `TestStartDropsExpiredHolds`,
`TestStartHoldHoldsEveryTrigger`, `TestStartHoldPreparesNoTarget`,
`TestStartHoldHoldsASwitchUnderWay` (the check under `perform`'s lock),
`TestStartHoldHoldsTheEnginesOwnSwitchOnly` (`EngineSwitch`, `HandSwitch`),
`TestStartHoldBeyondTheBoundHoldsNothing`, `TestReleaseStartHold`,
`TestReleaseStartHoldWithoutAState` and `TestWaitStartHolds`. In `cli`:
`TestStartAtItsLimitHoldsTheEngineForTheSession`,
`TestStartWithRoomRecordsNoHold`,
`TestStartedMovesTheLoginOnOnceTheSessionIsUp` (every source, empty and
unreadable input), `TestStartedWithoutItsHoldDoesNothing`,
`TestStartedTicksAfterItsHoldEnded` (ended, released before),
`TestStartedWaitsForAnotherStartingSession` (the second session reports, or
its hold ends), `TestStartedNeverFails` (no store, a corrupt state, a
failing tick), `TestStartStartedIsAManagementForm`, `TestStartHelpNamesStarted`
and `TestDeadLiveFableLoginWithEveryAccountAtItsLimit`; for the Fable store's
start login `TestFableSessionStartsOnAStartLogin` (the reviewer's probe
turned around: the move, the hold, the default selection beside it and the
Fable tick after `--started`), `TestFableStartLoginRanksAndHolds`,
`TestFableStartWithoutAMarkedFableLogin`,
`TestFableStartWithoutMarksOrAutoSwitchIsAsBefore` (no file written),
`TestStartAccountWithAFableSession` (`--account`) and
`TestFableStartOneTokenHoldsBothStores`.

## A106. The login stays on an account that runs Mythos while a Mythos session runs

**Why.** Only some accounts may run Claude Mythos, and nothing tycswap reads
says which: every account reports the same per-model windows, and the
profile answer carries no entitlement. Claude Code checks Mythos access on
every request against the live login (measured 2026-11-18: a running Mythos
session got `There's an issue with the selected model (claude-mythos-5-2)`
while the shared login sat on another account for eleven seconds). tycswap
mapped `claude-mythos-5-2` to no family, so the status line recorded such a
session as nothing, the policy counted 5h and 7d only and the engine moved
the login to any account with room; `start` did the same, and a session that
changed from Fable to Mythos stayed recorded as Fable for up to a day. The
operator's order (2026-11-19): "to handle 3 accounts mythos better".

**The mark.** An operator-set roster key `mythos`; only a boolean `true`
counts (`store.MythosFrom`, `Store.MythosCapable`). `lifecycle.SetMythos`
sets and clears it under the roster lock, sharing `SetStartLogin`'s writer: an
unchanged mark writes nothing. It is carried like `startLogin` (A90) across a
re-add, re-login, move, swap and a forced import over the same identity, and
never to a new identity. `tycswap mythos --allow ID | --disallow ID | --list`
has the forms and exit codes of `start`'s management forms (0, 1 for a store
or account error with `tycswap mythos: <error>`, 2 for usage) and, like them,
no JSON. `list` tags `(Mythos)` after `(start login)`; list JSON and the
dashboard's Claude rows carry `mythos: true` only when marked, so a box
without a mark renders byte for byte as before; `reporting.AccountSnapshot`
carries `Mythos`. The dashboard has a `Mythos` chip and **Allow for Mythos** /
**Disallow for Mythos** (`POST /api/accounts/claude:ID/mythos/allow|disallow`,
authenticated like every mutation; the handler asks `AccountOps` for
`web.MythosMarker` by type assertion and answers 503 without it). The tray's
row tag and menu line add `Mythos`, as they add `start login`.

**The family.** `mythos` joins the recognised families
(`reporting.ModelFamily`), so the status line records a Mythos session, a
change of family is news (A77) and `start` learns the family of the session
it launches.

**The policy.** While a live default-store session is recorded as Mythos,
`PolicyResult.Mythos` lists the marked accounts, and Mythos restricts the
login only while it can be served, A91's rule for a model: an account of the
policy's pool (enabled, eligible, not quarantined, or the live one) is marked
and sits below the bars the engine moves at, on the reading the engine still
decides on (`DecisionValue`, its trust age), on its 5-hour window (absent is
room), its weekly window (absent is not) and every counted model window; or
the live account is marked and not at a hard limit (binding headroom over
those windows above 0). Then `PolicyResult.Serves` is
`Store.MythosCapable`. Otherwise `Serves` is nil, which serves every account,
and `Unservable` names `Mythos`: marks nobody set, or marks without room, must
never freeze rotation. The first version restricted while any marked account
was in the pool, so with every marked account at a limit the login stayed on
an unmarked one at its bar, `no-compatible-model-target`, and every default
session stopped while unmarked accounts had room (round-1 review, A91's fault
of 2026-11-15 again). The counted windows join the test because the engine
refuses a target over a counted window: a marked account with 5-hour and
weekly room but a spent Fable week, while Fable counts, would freeze the
login the same way. A Mythos session counts the account's Fable window
when the account reports no Mythos-named window (A111). A Mythos-only
session on the default login still reserves Fable-capable accounts with
Fable room under A77 and A91. Mythos sessions on the
Fable store are skipped as before (A102) and restrict nothing. Like every
followed session, a Mythos session counts while its process lives, idle or
not, and closing it lifts the restriction: its record outlives it for a day
but counts only for a live process.

**The engine** (`autoswitch.WithServes(model, serves)`, wired in
`cli.defaultEngine`, nil for every other engine):
- An account that does not serve is no target. A tick whose targets were all
  kept out so emits the existing `no-compatible-model-target`, detail `no
  qualifying target marked for Mythos; keeping the current account` (with
  ` with reported <models> usage` when a counted window kept some out too).
- A live account that does not serve hands over after the at-limit check and
  before the proactive one, with A73's trigger `model-missing`: below every
  bar, not held by the cooldown (only by a start hold), naming no window;
  without a target it stays with `model-missing-no-target`, detail `the
  active account is not marked for Mythos; no marked account is below every
  bar`. It never leaves a serving account for one that does not serve while
  Mythos can be served.
- A switch by hand onto an unmarked account holds that hand-over for
  `autoswitch.cooldownSeconds`, as it holds every trigger (A94); after it the
  login moves back to a marked account.
- The Fable reservation's hand-over (A77, A102) takes serving accounts only.

**Start.** `start` reads the session's model as for the store (A103), now
also on a box without a Fable login. For a Mythos session the candidates are
the accounts marked for Mythos, ranked as start ranks (A105: room first, then
at a limit, with a start hold); the start-login marks do not apply to it.
With no account marked, the session starts as any session does, after one
notice: `tycswap start: no account is marked for Mythos (tycswap mythos
--allow ID); selecting a start login as for any session.` Every other
session's candidates and current start login are also only those the policy
serves while a Mythos session runs; with none of them marked the selection
says `no usable start login`. The child of a Mythos session gets
`TYCSWAP_START_FAMILY=mythos`; every other child gets no such variable, an
inherited one removed as `TYCSWAP_START_HOLD` is, override paths included.
`start --started` records that family for the hook's `session_id` (Claude
Code's hook input carries it for every event, SessionStart included) before
its tick, so the session counts before its first status line, for the timer's
ticks and other sessions' starts too: not for `clear` or `compact`, and not on
a box without a store, which the record's lock would create. Without a start
hold it records and does nothing else; the session's first status line is
then no news. A held Mythos start sits on a marked account at a hard limit,
which start takes only when no marked account has room, so its `--started`
tick finds Mythos not served and moves the login as without the session; the
brief's test that this tick stays among the marked accounts no longer holds
after the round-1 correction.

**Switch by strategy.** `switch --strategy best` and the dashboard's *Next
best* rank only the accounts the policy serves (`switching.Counting.Serves`),
as A98 and A99 make them rank as the engine does. `next-available`, a rotation
with skips, and a switch by hand are not restricted.

**What the operator reads.** `reporting.CountingCaption` takes the marked
accounts: `counting 5h + 7d; live Mythos session: only accounts 2, 5; no live
Fable session`, `counting 5h + 7d; Mythos not served: marked accounts 2, 5
have no room; …` or `counting 5h + 7d; Mythos not served: no account marked;
…`. `auto.counting` carries them as `mythos` while a Mythos session runs
(omitted otherwise), and the `poll` event's `unservable` names `Mythos` while
it is not served.

**What stays.** A box without a mark and without a Mythos session is v0.16.2's:
the engine's option is nil, the policy's `Serves` nil, the state document, list
JSON and child environment unchanged (the existing no-change tests pass
unedited; `nofable-state.json` is untouched).

**Limits.** Not in scope: Mythos sessions on the Fable store, reading the
entitlement from an API, and the long-running `tycswap auto` loop and hosted
engines, which follow no session (A97). A Mythos session that draws no status
line and was not started through `start` and its hook is never recorded. A
session whose model changes to Mythos with `/model` counts from its next
status line. `start --started` finds the session only once Claude Code has
written its process record (`sessions/<pid>.json`, whose `startedAt` is the
process start); that it exists when the SessionStart hook runs is inferred
from that field, not measured.

**Tests.** In `autoswitch`: `TestMythosLoginStaysAmongMarkedAccounts` (the
live shape: the login on a marked account at its 5-hour bar and at 100 %, an
unmarked account with room), `TestMythosUnmarkedLoginMovesToAMarkedAccount`
(with and without the cooldown of the engine's own switch),
`TestMythosHandOverWaitsForASwitchByHand`, `TestMythosUnmarkedLoginStaysWithoutAServingTarget`,
`TestMythosBoundsTheReservedHandOver` and `TestMythosNilFilterServesEveryAccount`.
In `store`: `TestMythosFromOnlyAcceptsBooleanTrue`; in `lifecycle`:
`TestMythosMarksPreserveMetadata` and `TestMythosMarkFollowsIdentity`; in
`transfer`: `TestImportRetainsLocalMythosMarkOnlyForExistingIdentity`. In
`reporting`: `TestRecordDefaultSessionModelRecordsMythos` (a Fable-to-Mythos
change recorded and news), `TestPolicyRestrictsTheLoginToMythosAccounts` (no
account marked; a marked account disabled; the live one; every marked account
at a limit; a marked live account over its bar and at its hard limit; a
counted window spent; the Mythos session closed), `TestPolicyMythosKeepsTheOtherRules`,
`TestCountingCaptionNamesMythos` and `TestListAndSnapshotShowTheMythosMark`.
In `web`: `TestMythosRoutes` (auth, Codex, 503),
`TestStateRowMythosOnlyWhenMarked` and `TestDashboardMythosControls`. In
`cli`: `TestAutoOnceWithAMythosSession` (the tick with marks, with none: v0.16.2's
target and `unservable`, and with an Opus session),
`TestAutoOnceServesMythosOnlyWithRoom` (the round-1 states: every marked
account at a limit; a marked login over its bar and at its hard limit; a
marked account back below its bars),
`TestMythosSessionStartsOnAMythosAccount`, `TestMythosSessionWithoutAMarkedAccount`,
`TestStartWhileAMythosSessionRuns`, `TestMythosStartAtItsLimitIsRecordedBeforeItsTick`,
`TestStartedRecordsTheFamilyWithoutAHold`,
`TestStartRemovesAnInheritedFamily`, `TestMythosCommand`,
`TestAutoViewReportsMythos`, `TestSwitchBestWhileAMythosSessionRuns`,
`TestTrayMythosTag` and `TestDashboardSetsTheMythosMark`.

## A107. The Fable mark, and the Fable sign-in in the menu

**Why.** On 2026-11-19 `tycswap add --login --lineage fable --slot N` refused
two organization accounts that run Fable, after the browser sign-in, with
`Account-N's plan does not offer Fable.`: an organization's login often names
no plan (`subscriptionType` empty), and `Store.FableCapable` (A71, A77) then
answers no. It also honours a roster key `fableContinue`, which nothing but a
test wrote; set by hand in `sequence.json`, it let the sign-in through. The
operator, the same day: "interactive shell missing fable login": the `tycswap`
menu had no way to the Fable sign-in.

**What decides.** `Store.FableEntitlement(data, N)` is `FableCapable` with its
reason (`FableAnswer{Capable, By, Plan}`), and `FableCapable` now reads it, so
every caller (the sign-in, the Fable store's switch and pool, the default
login's reservation) gives one answer. In order: an API-key account never runs
Fable (`api-key`); a Pro or Free plan does not, whatever the mark (`plan`); a
boolean `fableContinue` decides (`mark`); a Max or Ultra plan does (`plan`);
anything else does not (`unknown`). The plan is the default login's
`subscriptionType`, else the roster's, as before. Only `By` is new; no answer
changes.

**The mark.** `tycswap fable --allow ID | --disallow ID | --list [--json]`, in
the forms and exit codes of `start --allow | --disallow | --list` (A90): the
three exclude each other, a missing account, `--json` without `--list`, an
unknown option or a positional argument exit 2 (`tycswap fable: error: …`);
an unknown account exits 1. `lifecycle.SetFableMark`, under the roster lock
like `SetStartLogin` and beside it, writes `fableContinue: true` or removes
the key, so the plan decides again, and writes nothing when the record already
says so. `--allow` refuses an API-key account (`Account-N is an API-key
account; a Fable mark is for OAuth logins only.`) and a Pro or Free plan
(`Account-N's plan (pro) cannot run Fable; nothing marked.`), where a mark
would change nothing. `--list` prints per roster account its number, email,
whether it runs Fable and why, and whether it holds a Fable login; `--json` is
`{"schemaVersion":1,"accounts":[{"number","email","fable","by","plan"?,"fableLogin"}]}`.
There was no `fable` command or abbreviation to shadow: the main parser
matches no prefixes, and `switch --store fable` stays as it is.

The key follows its identity as `startLogin` does: `carryAccountMetadata`
(re-add, re-login, `add --slot`, `add-token`) and the forced import's
`retainLocalMetadata` keep it; `move` and `swap` move the record whole; a new
identity in the slot or a newly imported one starts without it; `export`
carries no roster metadata. Before, `add --slot N` of the same identity and an
`add` that migrated it to another slot dropped a hand-set key.

**The refusal.** `lifecycle.FablePlanRefusal` is the sign-in's refusal:
`Account-N's plan does not offer Fable. If it runs Fable, mark it first:
tycswap fable --allow N`, the first sentence alone for a Pro or Free plan.
With `--slot N` the preflight asks it before `Sign in as …` and before Claude
starts, so the operator never signs in for nothing; an API-key account in slot
N is refused there too, with the sign-in's own `Account <email> is not stored
yet: add it first with tycswap add --login.` Without `--slot` the
account is known only from the sign-in's identity, so the refusal stays after
it, in `AddFableLogin`; the menu always passes `--slot`.

**The menu.** `menuFableStore` is whether the platform can hold a Fable
store: not on macOS (A100), where none of the entries appears. *Add account*
ends with `add --login --lineage fable  Sign in for Fable`; it reads `fable
--list`'s rows (`menuFableRows`, only once chosen) and asks for the account
among those that run Fable, without a Fable login first, then those with one
(`· sign in again`), each in roster order, and runs `add --login --lineage
fable --slot N`. With none it says `No account runs Fable yet. If one does,
choose Allow Fable in its menu (tycswap fable --allow N).` A Claude OAuth
account's own menu ends with *Sign in for Fable…* (*Sign in again for Fable…*
when the snapshot has its Fable login), the same command, and, only where it
changes the answer, *Allow Fable* (nothing decides yet, `unknown`) or
*Disallow Fable* (a mark decides), which asks first and runs `fable --allow
N` or `--disallow N`; an account whose plan decides gets neither. What
decides is read for the chosen account only, and only where its Fable
entries appear (`menuFableAnswer`, `FableEntitlement`'s `By`); without an
answer the menu offers the sign-in and no toggle. The account snapshot
carries none of it: it feeds every refresh of the dashboard, the tray and
the TUI, and `FableEntitlement` reads the account's stored login (on macOS
from the Keychain). The new entries come last, after every entry they could
shift.

**No mark, no change.** A box without the key reads every answer as before;
no document gains a field, the account snapshot reads what it read before; the
menu's other entries keep their places.

**Tests.** `TestFableEntitlementSaysWhy` (every basis, a Pro plan's mark, a
hand-set `false`, an unknown slot), `TestSetFableMarkWritesAndRemovesTheKey`
(the key, other metadata kept, no rewrite of an unchanged record, a hand-set
`false` removed), `TestSetFableMarkRefusals` (an unknown account, a Pro plan,
an API key; no file changes), `TestAddFableLoginOfAnUnknownPlanNamesTheMark`
(refused with the new line, stored after `SetFableMark`),
`TestFablePlanRefusalOfAPlanWithoutFableOffersNoMark` and
`TestFableMarkFollowsIdentity` (re-add, re-login, `add --slot` of the same
identity and to another slot, move, swap, another identity in the slot) in
`lifecycle`; `TestImportRetainsLocalFableMarkOnlyForExistingIdentity` in
`transfer`; in `cli` `TestFableCommandArguments` (each usage error, exit 2, no
write), `TestFableCommandAllowDisallowList` (the list, the key written and
removed, the JSON, no Fable directory created), `TestFableCommandUnknownAccount`,
`TestFableSignInOfAnUnknownPlanAccount` (refused before Claude starts, signed
in after `fable --allow`), `TestFableSignInOfAnAPIKeySlotOpensNoBrowser`,
`TestMenuAddFableSignInAsksForTheAccount` (the
entry, the order of the accounts, the command),
`TestMenuAddFableSignInWithoutAFableAccount`,
`TestMenuAddHasNoFableSignInWithoutAFableStore` and `TestMenuManageFableEntries`
(the entries and their order, each confirmation and its cancel, the sign-in, a
marked account, a plan that decides: the sign-in and no toggle, macOS, an API
key, Codex; the answer read for the chosen account only, and only there),
`TestMenuManageWithoutAFableAnswerOffersNoToggle`.

## A108. The default login does not follow a Fable session while the Fable store is usable

**Why.** The operator on 2026-11-20: "investigate tycswap switching seems
irrationals", "something is wrong we wasted this account quota for opus". His
order: "While the Fable store is usable, a Fable session that runs on the
default login no longer changes what the default login counts or where it
goes. The operator sees, where he looks at a session, that it runs on the
wrong login and how it gets onto the right one." From his box's journal and
`tycswap.log` (tycswap 0.17.0; times CET):

- 10:43:30, the default tick's `poll`: headroom counted on 5h and 7d only
  (account 12: 3 %, 3: 21 %, 9: 46 %).
- 10:45:08, `/model Fable` in a session (A) that ran on the default login
  (its record in `default_models.json` names no store). 10:45:10,
  `Switched to account 9 by engine:at-limit`. 10:46:29, the default `poll`:
  every account at 0 % but 9 (11 %): the default tick counted the Fable
  window, from that one session.
- 12:50:08, `Switched to account 10 by engine:at-limit`: another Fable
  session (B) on the default login made the default login count Fable, and
  account 10 was the only account with Fable room. 12:51:30, the default
  `poll`: 10 at 8 %, every other account at 0 %.

A102's policy skipped only records whose store is `fable`, so a Fable session
on the default login counted Fable, and the default login, which serves the
Opus sessions, went to the accounts the Fable store needs and burned their
windows. Session A got there by `/model` (a session stays on the store it
started on, A103); how B did is not established.

**The rule.** While the Fable store is usable (`Store.FableStore().Usable`,
A100), `reporting.DefaultSessionPolicyResult` skips a live session recorded as
family `fable` whatever store its record names, as it skips the Fable store's
sessions: it adds no family, so it changes neither the counted windows nor
`Sessions`, nor any reservation or restriction. Everything else stays: A102's
reservation of the Fable pool's accounts with Fable room and of the Fable
store's live account (`ReservedLast`), Mythos (A106), and a live session
without a record. With the store off, unusable or without Fable logins the
policy is as before (A77, A91): a Fable session on the default login is
followed and counts Fable, and `start` puts Fable sessions there then (A103).
The dashboard's counting, `start`'s ranking and `switch --strategy` read the
same result. The session keeps running on the default login, on whatever
account the Opus sessions need; the status line says so.

**What the operator reads.** `PolicyResult.FableOnDefault` counts the live
Fable sessions on the default login the policy does not follow, and the
counting caption (`reporting.CountingCaption`: the Auto tab, the tray; `switch
--strategy` prints its caption only while a model window is counted) names them in place of `no live session has reported
its model` or `no live Fable session`, which it read for them before (round-1
review): `counting 5h + 7d; 1 Fable session on the default login not followed
(start --check names it) — accounts with Fable room reserved: 3, 8, 11`.
`auto.counting` carries the count as `fableOnDefault`, absent when none, so
every other state's caption and document read as before.

**`tycswap start --check`.** A management form like `--started`: exclusive
with the other forms and with launching (exit 2, `management options are
exclusive` or `management options cannot launch a command`). The status line
runs it; flakelab adds it to its status-line wrapper. It reads the status-line
JSON Claude Code passes on stdin (`session_id`, `model.id`, at most 1 MiB)
unless stdin is a terminal. The session's store is the Fable store when the
process runs under it (`cli.fableSessionProcess`, A100), else the default
login. Its family is `reporting.ClaudeFamily` of `model.id`, the reading
`RecordDefaultSessionModel` records: a provider's id or an unknown model gives
none, and then nothing is printed. The Fable store's state is read as `start`
reads it (`startFableStore`: its files only, nothing on a store `start` must
not read).

| Session | Prints |
| --- | --- |
| Fable on the default login, the Fable store usable | `⚠ Fable session on the default login. Use the Fable store with an account approved for Fable.` |
| the same, while `start` sends Fable sessions to the default login (A105.6) | `⚠ Fable session on the default login (no start login has a Fable login). Use the Fable store with an account approved for Fable.` |
| the same, while `start` sends Fable sessions to the default login because no Fable login has room (A112) | `⚠ Fable session on the default login (no Fable login has room). Use the Fable store with an account approved for Fable.` |
| Opus, Mythos, Sonnet or Haiku on the Fable store | `⚠ <Family> session on the Fable store. Use the default login with an account approved for <Family>.` |
| any other, or a box without a Fable login | nothing |

Only the recognized family is used in the diagnostic. The session ID and model-version suffix
are unnecessary for reporting the store. A local login mark describes routing preferences;
the serving account's organization determines model access.

Under one of `start`'s overrides (`cli.startOverride` of the process's
environment: `CLAUDE_CONFIG_DIR`, a `launch.AuthOverrideEnvVars` variable, or
a `CLAUDE_SECURESTORAGE_CONFIG_DIR` that does not name the Fable store) it
prints nothing: such a session runs on no login `start` chose, and `start`
would launch a resume of it unchanged (round-1 review).

A105.6 sends every Fable session to the default login, with `no start login
has a Fable login; starting on the default login.`, while start logins are
marked, auto-switch is on and none of them is in the Fable pool.
`cli.fableStartsOnDefault` is shared by the launcher's Fable selection and this
diagnostic, so both use the same fallback condition; so is the Fable
placement rule, `cli.fableSessionOnDefault` (A112).

The form prints at most one line and exits 0 whatever happens, a panic
included. It creates no file or directory, takes no lock, makes no network
request and writes no log line of its own; it reads the Fable store's
credential files to tell its state, as `start` does, and prints none of them.

**What stays.** A box without a Fable login: the policy is unchanged, and
`start --check` stops when `credentials-fable/` is missing and prints nothing.
The no-change tests (`TestNoFableLoginNoChange`,
`TestFableTickNoChangeWithoutAStore`, `nofable-state.json`) pass unedited.
The status-line record is unchanged: a Fable session on the default login is
still recorded with no store.

**Limits.** Nothing moves a running direct session: only `/exit` and a resume
on the right model do (A103); a linked one moves by A109 and A112. The line
appears only where the status line runs
`start --check`. A credential file the state read cannot read is logged as for
any reader.

**A corrupt Fable login in `start`'s snapshots.** `start`'s snapshots of the
default login (`readStartSnapshotFor`) and of the Fable store
(`readFableStartSnapshot`), which the check now also takes, read through a
store view whose logger is nil, so that they write nothing (A90). The Fable
scope (`ForFable`) builds its credential store with that logger, and a Fable
login file that could not be decoded made it warn on nil: `start` panicked
before launching anything while start logins were marked and auto-switch was
on, and the check printed nothing (found in the round-1 review). A nil
`logging.Logger` now writes nothing. The views stay silent; the file is
logged by each read that meets it with the store's logger, as for any
reader (twice per `start` or `start --check` run), and such an account
has no usable Fable login, so `start` sends the Fable session to the default
login (A105.6) and the check names that cause. `--continue` and the `--resume` picker still give `start` no
family (A103), so a Fable session resumed so starts on the default login, and
the check names it.

**Tests.** In `reporting`: `TestDefaultPolicyIgnoresAFableSessionOnTheDefaultLogin`
(alone: no family, no `Sessions`, no counted model, the reservation kept;
beside an Opus session the Opus-only box's result; the store off: Fable
counted) and `TestCountingCaptionNamesFableSessionsOnTheDefaultLogin` (one
and two such sessions, alone and beside an Opus session; a Fable session on
its store and the store off read as before). In `cli`: `TestDefaultTickIgnoresAFableSessionOnTheDefaultLogin` (a
real `auto --once --json` on the reported shape: the default login stays and
counts 5h and 7d only, where the code before switched it to the account with
Fable room, `at-limit`; with the store off it follows the session as 0.17.0
did), `TestStartCheckNamesASessionOnTheWrongStore` (every row, a provider id,
an unknown model, a non-UUID or missing session id, empty and malformed input,
the store off, unusable and without Fable logins, the Fable store's variable;
the session ID and model version omitted from the diagnostic; the home and the store byte-identical after every run),
`TestStartCheckAgreesWithStartOnAFableSession` (a marked start login without
a Fable login, one whose Fable login is quarantined, one with a usable Fable
login, no marks, auto-switch off: the line and the store a resume with
`--model fable` starts on agree), `TestStoreTreeSeesALockTakenAndReleased`
(the no-write snapshot sees a lock directory made and removed),
`TestStartCheckPrintsNothingUnderAStartOverride` (every override, empty
values included, on either store),
`TestStartWithACorruptFableLoginOfAMarkedAccount` (no panic, the
default login, the cause, the file in `tycswap.log`),
`TestStartCheckReadsNoTerminal` (Linux, a pseudo-terminal),
`TestAutoViewNamesAFableSessionOnTheDefaultLogin` (the dashboard's
`auto.counting`),
`TestStartCheckIsAManagementForm` and `TestStartHelpNamesCheck`; in
`logging`, `TestNilLoggerWritesNothing`.

### Startup allocation, independent credentials and automatic placement

Each launch selects its own credential store. A dedicated Fable launch takes
a start hold in the Fable store alone. Older hold tokens can still be released
from either state file. A shared Mythos launch follows the account's explicit
model allocation. Resuming an independent login checks known shared and
model-specific limits at the current time. The dashboard labels the recorded
credential even when the model changes inside a session.

An automatic move can choose only a login allocated to every known premium
model it will serve. A local allocation exception applies to the individual
launch which requested it. With soonest-reset ordering, an Opus-only workload
below its thresholds can move to an eligible login whose binding window resets
strictly earlier. Unknown or equal resets leave it in place. An account whose
Mythos login a live session uses is reserved and ranks last in the default
rotation, after the Fable store's live account; a default login on it hands
over only to an account whose Mythos login is not in use (A131). Before
committing, the engine rechecks the
source and target under the switch lock. A drain also rechecks the workload.

## A109. Session links for native Claude on Linux and WSL

On a machine with managed Claude accounts, `start` gives each native
process a symlink under the private backup root, `links/<token>`, in
`CLAUDE_SECURESTORAGE_CONFIG_DIR`. Its target is the default profile, the
Fable store or one ready independent Mythos login. The SessionStart hook
binds the process identity, session UUID and target in `default_models.json`.
A resume gets a new link and a nested native launch gets its own. Hooks and
tycswap commands resolve inherited links before choosing their store.
The scheduled tick retires ended links. An independent Mythos binding
retains the identity needed to resume after a long absence. Direct sessions
keep the earlier placement behavior, including native Windows and macOS.
An unreadable or uncreatable launch link gives one notice and a direct launch.

**Older writers and rollback.** A 0.19.0 status-line writer rewrites the
whole session map using the fields it knows (`family`, `seenAt`, `store`
and `binding`). That write drops the `link` field even on other sessions'
rows. The running process still has its link. A current status-line
observation or `tycswap use` can bind it again, just as when SessionStart
never recorded it. Outside the session, `use --session ID` finds the
session's local process and matches its PID and start time to a live link.
If the native process record has only a PID, its timestamp must be at least
as recent as the link. A direct observation with the session UUID and link
can repair an older PID-only record's missing binding. A conflicting owner
or ambiguous process match refuses the repair. A rollback to 0.19.0
makes its status lines classify linked Fable and independent Mythos sessions
as default-login sessions until those sessions restart. Their links still
point at the same credentials.

**Moving a session.** A native refresh trace showed the client acquiring
`.oauth_refresh.lock` through its link before taking the lock beside the
resolved storage directory. It then re-read `.credentials.json` through the
link, refreshed, wrote the replacement grant and released the refresh lock
last. Replacing the link during that interval can send the grant to the
wrong store. A mover takes the refresh lock on the resolved source before
renaming a fresh symlink over the old one. A client already refreshing
finishes in the source. A client that retries after the rename resolves the
new link and reads the destination grant. The mover leaves the client's lock
in place regardless of its age.

Status-line and hook observers and the scheduled tick try the refresh lock
once. A busy source defers placement. Explicit `use` retries a busy client
lock for up to 9 s. Each attempt takes the tycswap store lock, then the
session-record lock, and rechecks the binding, source, eligible destination
and account allocation. Only a move ready to commit tries the source's
refresh lock. A busy attempt releases the tycswap locks before retrying.
The tick can rotate the selected Mythos login at a known limit when another
ready login has room. After a real Fable tick it places linked Fable
sessions by the Fable placement rule (A112), one refresh-lock try each.
An automatic allocation exception requires an affirmative answer
for this session and destination. A separate process owns that question and
rechecks the proposal after the answer. A later observation can replace an
asker that has died.

**Manual choice.** `tycswap use <mythos|fable|default> [--session ID]`
takes the session from the calling shell's link or its explicit UUID.
It holds the chosen login while the user enters the status line's
`/model claude-mythos-5-2` or `/model fable` command. The first report
of the requested family clears the hint. A later model change resumes
automatic placement. In-process subagents share the parent's link.
Remote Control belongs to the signed-in account and disconnects on an
account change. Run `/remote-control` again on the destination.

**Shared default and Mythos.** When a linked session on the default login
changes to Mythos and no independent login is ready, the status line reads
`no enabled, marked account with an independent Mythos login is available for this start`.
If every eligible login is at a known limit, it reads
`no available Mythos login has quota for mythos`. The link stays in place.
In 0.19.0 this change could trigger `model-missing` and switch the shared
default login for every session. The linked session no longer drives that
switch. A session initially launched on the shared Mythos fallback retains
the existing shared policy.

**Returning to default or Fable.** Placement checks the target store's
actual OAuth credential locally. After a native refresh rotates a login,
its backup and recorded fingerprint can both be out of date. Missing or
unreadable credentials can also leave the owner unresolved. The default
config can name a different session's account. The last installed slot
comes from the active roster on default or the Fable chain record on Fable.
Either clue alone leaves the link on its current login and the status line
says `cannot verify the current default login for the session` or
`cannot verify the current fable login for the session`. There is no
timer that makes the inference sufficient. A later observation can move
as soon as the credential matches an account backup or verified chain,
for example after an identity-resolving read and harvest or installation
of a known login. While that evidence is missing, the refusal can persist
indefinitely. Its frequency and duration on a live machine were not measured.

## A110. Selecting a Mythos login and starting once on Mythos

**Use for Mythos** resolves the requested account under the roster lock and
records the independent login's identity. A hand switch holds automatic
Mythos moves for the configured switch cooldown (A94). Each running linked
Mythos session moves through the A109 guard. If one source is busy, its link
stays in place and the result identifies the pending move. A retry against
the selected account refreshes the cooldown and can finish these moves. An
unreadable or incomplete cooldown leaves automatic movement available and
writes one warning when that condition begins. Disabling or unmarking the account
or losing its ready login invalidates that selection.

The CLI, dashboard and tray read the same account and usage snapshot. The
linked-session count follows the actual link targets. Unavailable state carries
a reason. A session's menu of destinations previews an explicit A109 move.
It excludes the current target and shows a single reason while the source is
refreshing. The command checks the binding, destination and refresh lock again
on click. The displayed owner comes from the bound Mythos directory or the
credential evidence for the bound default or Fable login. Insufficient evidence
displays as **unknown**. The move guard logs each committed move with both
owners and the actor, and remembers the last deferral reason on the binding.

The menu, dashboard and tray share one **Set next start** preference with
`start --next mythos` and `start --next clear`. A bare start claims the saved
request by identity. An unreadable request produces a diagnostic and leaves the
bare start on its ordinary path. An explicit set or clear repairs it. A failed launch can restore that request without
overwriting a newer choice. An explicit `start --mythos --account ID` uses
the selected account for that invocation. The terminal menu runs it in the
terminal that opened the menu.

## A111. The model window that meters Mythos

In a three-day transcript sample checked on 2026-11-21, 28 limit messages
from sessions running `claude-mythos-5-2` named Fable and zero named Mythos.
At 16:16 CET on November 21, a Mythos session reported
`You've hit your Fable limit` with a November 28, 10 am reset. At 18:40 CET
the same account's usage showed 53% of its shared week used and a single
scoped window, Fable, at 100% with a November 28, 09:59 reset. In v0.21.0
the Mythos login summary showed only `counting 7d` and a new Mythos start
selected the spent login.

On that release, `status --json` included the selected Mythos login and its
linked-session count, but the dashboard's `GET /api/state` omitted the summary
even though its account rows carried the Mythos sign-in status. The combined
provider snapshot now copies the Claude summary alongside the Fable summary,
including on installations that also have Codex accounts.

For each account, Mythos counts scoped windows whose names contain
`mythos` (case-insensitive). If no scoped name matches, it counts the account's
Fable window. The reported shared windows also count. This rule applies to
start placement, linked-session rotation, the Mythos login summary and
the shared default login's model-window decisions.

Reservations follow the session family. An independent Mythos start
selects among its eligible Mythos logins. On the shared default login,
a Mythos-only session still leaves Fable-capable accounts with Fable room
reserved when the Fable store is unavailable. With a usable Fable store,
the default login reserves that store's pool members with Fable room and
its live account regardless of room (A102).

## A112. A Fable session goes where Fable has room

**Why.** Observed on a live box on 2026-11-24 (tycswap 0.21.5): start
logins marked on six accounts, the Fable store on and usable, the Fable pool
three accounts with every Fable window at 100 %, and the default login's
account a marked start login with Fable at 8 % and no Fable login. A
`claude` on Fable went through `tycswap start` onto the Fable store and
answered every message with `You've hit your Fable limit`. A session
started as Opus on the default login and switched with `/model fable` kept
working; its status line logged `Session … move deferred by status line:
Account-4 has no remaining quota for this session`. The Fable tick reported
`all-exhausted` every 2 minutes while its own poll showed the default
login's account with 82 % headroom, and nothing named that this account had
Fable room but no Fable login. Three places decided which login serves a
Fable session, by three rules: start sent it to the Fable store whenever a
marked start login was in the Fable pool and never read quota (A105.6); the
status line sent it to the Fable store when its live account had room and
never looked at a session already there; the Fable tick rotated the store
inside the pool and never moved a linked session.

**The rule** (`cli.fableSessionOnDefault`). For an automatically placed
Fable session the Fable store would serve (on and usable, and A105.6 not
sending it to the default login):

1. The Fable store, when an account it can hold has room for Fable: its live
   account (backup or chain; an unknown one counts as room), or an account
   the Fable tick may switch it to (`reporting.FableSwitchable`, what
   `fableAdapter.SwitchableAccountNumbers` answers: the Fable pool less the
   accounts whose explicit model allocation leaves Fable out).
2. Otherwise the default login, when its current account has room for
   Fable: resolved from the credential as the status line resolves it, not
   from the shared config or the last install, and enabled
   (`cli.verifiedLoginOwner`, A109).
3. Otherwise the Fable store, as before.

Room is no limiting window for Fable in the cached usage as
`reporting.ModelLimitWindows(entry, "fable", now)` reads it, the one
`modelLimitWindows` now delegates to: no 5h, 7d or Fable window at 100 %. An
account without a reading has room. `reporting.ReadFableRoom` reads the
roster, the Fable store's state and the usage cache, and nothing else: no
request, no write, no lock.

What stays: the store off or unusable, no marked start login in the Fable
pool (A105.6) and a box without Fable logins send a Fable session where they
did. The parked or recovered Fable path (`recoveredFableStart`,
`store.FableAllocationParked`, the readers) is decided before the rule. An
explicit choice is not ruled: `start --account`, `tycswap use fable|default`
and a held `Wanted` keep their behaviour. The allocation question for an
account allocated away from the model is asked or not as before.

**Start.** `selectFableStartLogin` asks the rule after A105.6 and before the
Fable store's start-login selection, for a fresh Fable start and for a
resume whose saved link names the Fable store. When it answers the default
login, the child gets no Fable store, the default start-login selection runs
as for any default start, a linked start's link names the default login, and
stderr and `tycswap.log` say `tycswap start: Fable store: no Fable login has
room (first back: Account-N at <time>); starting on the default login.`,
followed by the hint below. `first back` is the account whose limit ends
first among those the store can hold (`oauth.ResetClockString`), left out
when no reset is known. With an account in the pool that has room, A105 is
unchanged: start still takes a marked login at its limit and the tick moves
on. The default selection ranks ordinary choices before allocation
exceptions under the rule as under A105.6 (`preferFableAllocations`), and
`start --check` names the cause as start decides it: `⚠ Fable session on the
default login (no Fable login has room). Use the Fable store with an account
approved for Fable.`

**Status line and hook.** `sessionDestination` takes the rule's target for an
automatic Fable placement. A linked session on the Fable store moves to the
default login when the rule says so, logged as every move (`Session <id>
moved from Fable store (Account-N) to default login (Account-M) by status
line`). A session on the default login behaves as before: it moves back once
an account the store can hold has room and the store's live account has.

**Tick.** After a real Fable tick (`auto --once` from the timer, the quota
hook or a status line, and `start --started`), `cli.placeFableSessions` gives
each live linked session recorded as Fable without a held choice the same
placement, actor `tick`, in either direction, through the A109 move guard
with one try of the refresh lock: a busy lock defers it (`Session <id> move
deferred by tick: source login is refreshing (.oauth_refresh.lock)`). A dry
run and auto-switch off move nothing, and auto-switch turned off since the
tick began holds the move. A move to an account allocated away from Fable is
left alone, the record untouched: the tick never asks. It writes no event;
the move guard's log line names the move. Like the Mythos tick it runs on
Linux only, where links exist.

**The hint.** While no account the Fable store can hold has room, the
accounts that are enabled, rotation-eligible, able to run Fable
(`FableCapable`), not allocated away from it, have room for Fable and hold no
Fable login could serve Fable once signed in
(`reporting.FableRoom.WithoutLogin`). `reporting.FableRoomHint` names them
and the command that signs them in (A114): `Account-11 has Fable room but no
Fable login: tycswap fable --login`, several in one sentence (`Accounts 7, 11
and 12 have …`). It ends start's notice; `status`
prints it under the Fable store line and `list` after its accounts, and their
JSON gains `fable.roomWithoutLogin` and `fableRoomWithoutLogin` (account
numbers, only when there is one). A real Fable tick logs it as one WARNING,
`Fable store: no Fable login has room; <hint>`, when the set changes: the set
is kept as `lastFableRoomWithoutLogin` in `fable/autoswitch_state.json` by
`autoswitch.RememberSet`, the limiter `Blocked on Account-N although …` uses,
and forgotten once there is room. Status and list name them with the store
off or unusable too, when no account in the pool has room: adding such a
login turns an empty store on (A101).

**Limits.** The rule does not make the default login follow the session:
while the Fable store is usable A108 still leaves a Fable session on the
default login uncounted, so the default tick may move the default login to an
account without Fable room for its Opus sessions. The rule then answers the
Fable store, and the move there waits for room (`Account-N has no remaining
quota for this session`). Start judges the default login's current account;
the default start-login selection after it ranks the marked accounts by 5h
and 7d, not by Fable, and may switch to one without Fable room. Direct
sessions (macOS, Windows, unlinked) are placed by start only. The cached
usage can be stale.

**Tests.** In `cli`: `TestFableStartPlacementByFableRoom` (the observed state:
the default login, the notice and its log line; a pool account other than the
store's with room, and no room anywhere: the Fable store and A105's hold),
`TestStartCheckAgreesWithStartOnFableRoom` (the three states),
`TestFableRoomWithoutALoginIsNamed` (`status` and `list`, human and JSON: an
account named; a disabled one, one allocated to Opus, one without Fable room,
one on a Pro plan not; none while a pool account has room),
`TestFableTickWarnsOnceAboutFableRoomWithoutALogin` (a dry run, two ticks,
a changed set); on Linux `TestFableStartWithoutFableRoomLinksTheDefaultLogin`
(a fresh start, and a resume whose saved link names the Fable store),
`TestStatusLinePlacesAFableSessionByFableRoom` (off the Fable store with the
move logged; back once the store's account has room, with no deferral before)
and `TestTickPlacesLinkedFableSessionsByFableRoom` (moved after a dry run;
auto-switch off; a busy refresh lock defers, then moves). The no-change tests
pass unedited. `TestStatusFableObject` in `reporting` reads the Fable store
line rather than the last line: its fixture has no room in the pool and
Account-11 with room and no Fable login, so the hint follows.

## A113. statusline.json names each login's current account

**Why.** A status line read the account from the shared `~/.claude.json`
`oauthAccount`, which names one account for every session and which any
client rewrites with its own. A session linked to the Fable store or an
independent Mythos login (A109) therefore showed the default login's account,
label and Fable week: on 2026-11-24 a session on a spent account displayed
another account at `fable:8%`. The status bar plugin now resolves the
session's own login directory and looks it up in `statusline.json`; this is
the producer side.

**The rule.** `statusline.json` (A54, `schemaVersion` still 1) gains an
optional `logins` object: the physical directory of each login a session link
can point at, symlinks resolved as `realpath` of `links/<token>` yields them,
to the `accounts` key (`statusline.Key`) of the account whose credential that
login holds now. The logins are the default login
(`store.DefaultProfileDir()`, the `paths.GetClaudeConfigHome()` start links),
the Fable store (`paths.FableStoreDir`) and each directory under
`mythos/accounts/`. Their owner is `Store.LoginOwnerSlot`, which
`reporting.SessionLoginOwner` now calls too: for the default login and the
Fable store the slot a unique backup or the chain record names for the live
credential (`LocalCredentialSlot`), for a Mythos login the one account its
directory's digest names (`MythosLoginOwner`); never `~/.claude.json`, never
the network. It reads the default login's credential from the default store
whatever scope publishes (`rootScope`). A login whose owner is not known, or
whose directory does not resolve, is absent; `statusline.Build` keeps only
values that are keys of `accounts`, so a duplicate identity names its lower
slot's key. Only Linux gathers logins, where session links exist; a box
without managed accounts, macOS and Windows write no `logins` key
(`omitempty`), and the rest of the file is as before. No credential, token or
fingerprint is written.

**Freshness.** Every `sequence.json` write and every usage change already
rebuilt the file. A login's account can change without either, so a rebuild
now also follows every write and removal of a chain record
(`writeLiveLineage`, `forgetLiveLineage`): a Fable store switch
(`RecordFableLive`), its emptying (`ForgetFableLive`) and a login the profile
endpoint re-attributes, after which a harvest only stores what the chain
record already says. A Mythos sign-in (`lifecycle.AddMythosLogin`) writes no
roster and now publishes too. Selecting another Mythos login changes no
login's account: each Mythos directory belongs to one identity, and the
selected one is published from its sign-in on.

**Limits.** A credential Claude Code rotated is unnamed until a resolution
re-attributes it (its chain record) or a harvest stores it; a rebuild in
between leaves that login out, and the status line falls back to
`~/.claude.json`. The default login's key follows the publishing process's
`CLAUDE_CONFIG_DIR`, which `start` treats as an override, so linked sessions
never run under one.

**Tests.** In `store` (Linux): `TestStatuslineNamesEachLoginsAccount` (three
logins held by three accounts, `~/.claude` a symlink, `~/.claude.json` naming
a fourth), `TestStatuslineLeavesOutAnUnverifiedLogin` (an unknown fingerprint;
no credential), `TestStatuslineLoginsOnlyWhereThereAreLogins` (the default
login only; no managed accounts: five keys),
`TestStatuslineLoginValueIsAnAccountsKey` (a duplicate identity) and
`TestStatuslineFollowsALoginItsProfileReattributes`; in `switching`,
`TestFableSwitchAndEmptyPublishTheStoresAccount`; in `lifecycle`,
`TestMythosSignInPublishesItsLogin` (and still after selecting it). In
`reporting`, `TestCollectReadsTheLiveCredentialOncePerPass` no longer counts
the reads of the rebuilds a pass's usage merges trigger.

## A114. Every account without a Fable login, signed in one after another

**Why.** An account serves the Fable store only after its second browser
sign-in (A101), one account per `add --login --lineage fable --slot N`. On
the operator's box three of six accounts that run Fable never got one, so the
Fable pool was half the roster and ran dry while an account with 82 % Fable
room stood outside it (A112). The operator on 2026-11-24, of the single
command: "is nice but need to manually know what account to add so is there
more user friednly way?", and of signing in every missing account in one go:
"yes, remember login one after another".

**The accounts.** `tycswap fable --login` asks, in roster order, for each
account that is enabled, runs Fable as `fable --list` reads it
(`FableEntitlement`'s `Capable`: never an API-key account or a Pro or Free
plan) and has no usable Fable login: none, or one that reads *sign in again*
(its quarantine entry, `reporting.FableQuarantine`, as every surface reads
it). `fableRows` marks them (`fableRow.Missing`), so the menu counts what the
command asks. With none it prints `Every account that runs Fable has a Fable
login.` and exits 0, with or without a terminal.

**The walk.** A fourth action of `tycswap fable`, exclusive with `--allow`,
`--disallow` and `--list`; `--json` is refused as with `--allow` (exit 2).
macOS refuses it with `ErrFableUnsupported`'s text, as `add --login --lineage
fable` does. With accounts to ask it needs a terminal on stdin and stdout
(`run`'s `stdinTTY && stdoutTTY`, not `humanTerminal`: the menu runs it as a
child with `TYCSWAP_NO_PROMPT`); without one it refuses with a
`ValidationError`, exit 1, before anything is asked. Per account it prints
`Fable sign-in <i> of <count>: <email> (Account-<n>).` and asks `Enter to
open the browser, s to skip, q to stop:`, so the operator can put the browser
on that account first. The answer is read a byte at a time: the sign-in's
`claude` reads the same stdin next. The end of input is `q`. Enter calls
`fableWalkSignIn`, which is `addCommand(prog, ["--login", "--lineage",
"fable", "--slot", N], s)`: that command's parser, `addLogin`,
`fableLoginPreflight`, scratch profile, `storeLoginWhenComplete`,
`AddFableLogin` and `activateFableLogin`, in this process, one call returning
before the next starts. Nothing of the sign-in is reimplemented: its output,
refusals, locks and log lines are the single command's, and the first Fable
login still turns the store on. A failed sign-in prints its own error and the
walk asks the next account.

**The reason.** The summary names each failure with its reason. The walk
hands the sign-in a stderr (`signInErrors`) that passes everything on and
keeps the text; the reason is its last `Error: ` line. `claude auth login`
gets the stream underneath (`loginStderr` in `startClaudeLogin`), so it keeps
the terminal: through a pipe, `exec.Cmd.Wait` would also wait for any process
that inherited it, a browser the login started included.

**The summary.** `Signed in for Fable: …`, `Skipped: …` (an `s`, and with `q`
that account and every one after it), one `Failed: <email> (Account-<n>):
<reason>` per failure, and `Run tycswap fable --login again for the rest.`
when any was skipped or failed. Exit 0 when none failed, 1 otherwise. A
second run reads the roster again and asks only for the accounts still
missing.

**Ctrl-C.** The sign-ins run in the walk's process, so Ctrl-C reaches
tycswap's SIGINT handler (A7): it runs the registered cleanups, the running
sign-in's scratch-profile removal among them (A56), releases Claude Code's
locks (A55) and exits 130. The walk sets the note that exit prints, `Fable
sign-ins stopped. Run tycswap fable --login again for the rest.`; no further
account is asked. A login stored before the Ctrl-C (A82) stays stored.

**The menu.** *Sign in for Fable* (`menuFableSignIn`) offers first, when two
or more accounts are `Missing`, `All <n> accounts without a Fable login, one
after another`, which runs `tycswap fable --login` as the menu runs every
command; the account entries after it are as before. The command browser
lists `fable --login` where a Fable store can exist.

**The hint.** A112's hint names this command instead of one `add --login
--lineage fable --slot N` per account: `Account-11 has Fable room but no Fable
login: tycswap fable --login`, `Accounts 7 and 11 have …`, `Accounts 7, 11
and 12 have …`. The walk signs in every missing account, not only those the
hint names.

**Limits.** The walk cannot see which account the browser is on: a sign-in as
another account is refused after the browser flow by `AddFableLogin`, as for
the single command, and the walk goes on. The passive update notice the
single command prints after a sign-in may follow each sign-in. A sign-in
waiting on its browser holds the walk, without a time limit, as `add --login`
does. The accounts are read once, at the start.

**Tests.** In `cli`: `TestFableLoginWalkSignsInEachAccountInTurn` (three
accounts in roster order, each through the single sign-in for its slot, each
login started only after the one before had removed its scratch profile; the
Ctrl-C note), `TestFableLoginWalkSkipsAndStops`,
`TestFableLoginWalkGoesOnAfterAFailedSignIn` (another account signed in, a
login that does not complete), `TestFableLoginWalkAsksOnlyAccountsWithoutAUsableFableLogin`
(a usable Fable login, a disabled account, an API key and a Pro plan not
asked; a quarantined Fable login asked), `TestFableLoginWalkWithEveryLoginInPlace`,
`TestFableLoginWalkRefusals` (no terminal, macOS, `--json`, the other
actions, help), `TestMenuFableSignInOffersTheWalk` and
`TestFableLoginWalkAgainAsksOnlyTheRest`; in `reporting`
`TestFableRoomHintNamesTheWalk`. `TestFableCommandArguments` names `--login`
in its two messages, and the cli tests' hint helper follows the new sentence.

## A115. The accounts that need a Fable sign-in are named before the pool runs dry

**Why.** A112 names accounts without a Fable login only in the urgent case:
no account the Fable store can hold has room, and the named account has. The
operator's box ran for days with three of six accounts that run Fable outside
the Fable pool, and nothing said so until the pool was spent. The operator on
2026-11-24, after signing them in with `tycswap fable --login`: "also there
should be notice/warning on dashboard and maybe also when starting claude?"
So the gap itself is named, not only its consequence, and the dashboard
shows it.

**The set** (`reporting.FableSignInNeeded`). In roster order, the accounts
that are enabled and rotation-eligible, able to run Fable (`FableCapable`)
and not allocated away from it by an explicit model allocation
(`fableServable`, the three conditions A112's `FableRoom.WithoutLogin`
applies, now one function), and that have no usable Fable login
(`reporting.NoUsableFableLogin`): none, or one that reads *sign in again*
because `FableQuarantine` holds it. That last test is the one definition the
walk's `fableRows` (`Missing`, A114) uses as well; the walk's own set is
unchanged (it still asks accounts allocated away from Fable, and works
without any Fable login). The set is empty unless an account has a Fable
login: a box that does not use the Fable store is not nagged. It reads the
roster, the Fable logins and the Fable quarantine only: no request, no write,
no lock. `reporting.FableSignInHint` is its sentence: `Account-7 needs a
Fable sign-in: tycswap fable --login`, `Accounts 7, 11 and 12 need a Fable
sign-in: tycswap fable --login`; it shares its wording of the accounts with
`FableRoomHint`.

**Where it shows.** `status` and `list` print, where A112's hint stands, A112's
hint while it applies and otherwise this sentence, in the same yellow; never
both lines (`fableLoginLine`). Their JSON carries the set as account numbers,
present only when not empty and independent of `roomWithoutLogin`:
`fable.withoutLogin` in `status --json`, built in the fable object itself
(`fableView.object`), so the dashboard's state, which takes that object from
the snapshot, carries it too; `fableWithoutLogin` in `list --json`. A start
that reaches the selection on the Fable store (`selectFableStartLogin`: the
store on and usable for a Fable start or a resume of a link to it) names the
set once, as its own notice line on stderr after that selection's notices,
through `startNotice`: never in `tycswap.log`. It does not when the placement
rule's notice ended in A112's hint (`fableSessionOnDefault`'s `hinted`); A112's
notice without a hint is followed by the sentence. No other start prints it.
The dashboard reads `fable.withoutLogin`: the Fable line in the header adds a
warn chip `N need a Fable sign-in` (`1 needs …`) whose tooltip is the whole
instruction, and the header's change signature includes the set, so the chip
comes and goes without a reload; a warn notice (`.notice-warn`, from the warn
colour variables) beside the auth-overrides notice names the accounts as the
page names them (`accountLabel`) with `tycswap fable --login` in `<code>`, and
is hidden without a set; a row of the set without a Fable login carries a
warn chip `no Fable login` whose tooltip is the command, while a row whose
Fable login reads *sign in again* keeps the chip it has. The account table
re-renders when the set changes. The Guide's *Something looks wrong* lists
the case. The tray and the terminal menu are unchanged.

**What stays from A112.** Its urgent hint, `fable.roomWithoutLogin` and
`fableRoomWithoutLogin`, the Fable tick's warning and `start --check` are as
they were.

**Limits.** The set is read when a surface is drawn, not watched: a start
names it only when it reaches the Fable store's selection, and a box whose
store is off or unusable hears of it from `status`, `list` and the dashboard
only. A parked Fable start without any Fable login shows a Fable store line
but no set. Whether an account can run Fable is the plan or mark
`FableCapable` reads; an account whose plan is unknown is not named.

**Tests.** In `reporting`: `TestAccountsThatNeedAFableSignIn` (a usable Fable
login, none, one to sign in again, a disabled account, an API key, a Pro plan
and an Opus-only allocation: the second and third; no Fable login anywhere:
none), `TestFableSignInHintNamesTheWalk` and
`TestSnapshotCarriesTheFableSignInSet` (the store off; every login usable).
In `cli`: `TestFableSignInIsNamedOnStatusAndList` (`status` and `list`, human
and JSON: a gap with room in the pool, A112's urgent case, no gap) and
`TestFableStartNamesTheAccountsThatNeedAFableSignIn` (room in the pool and a
gap: once, not logged; A112's notice with its hint: not again; A112's notice
without a hint: after it; no gap; not a Fable start). In `web`:
`TestStateCarriesTheFableSignInSet` and `TestAppJSFableSignIn` (the header
chip, singular and plural, gone on the next state without the set and kept
with the store off; the notice and its markup; the row chip on exactly the
rows of the set without a Fable login; the Guide's case; `.notice-warn`).
Every Fable start fixture leaves accounts that run Fable without a Fable
login, so the start tests that pin stderr whole now expect the sentence:
`TestFableStartPlacementByFableRoom` (two cases),
`TestFableSessionStartsOnAStartLogin`, `TestFableStartLoginRanksAndHolds`,
`TestFableStartWithoutAMarkedFableLogin`,
`TestFableStartWithoutMarksOrAutoSwitchIsAsBefore`, `TestFableStartAccount`
and, on Linux, `TestNativeSessionLinkFailureLaunchesWithOriginalStore` (two
notice lines for the Fable start). The walk's tests pass unedited.

## A116. One read operation reads each ownership input once

**Why.** On the operator's box (2026-11-25, 0.22.0: 8 accounts, four former
session profiles with a 200 to 300 KiB `.claude.json` each, about 9 live
sessions) `tycswap app --headless` used a full CPU core around the clock and a
scheduled `auto --once` 13 to 17 CPU-seconds. `CredentialOwner` read all its
inputs on every call: every profile's identity file, decoded whole into
`map[string]any` for two strings, and every profile's live PIDs. A state build
asks it once per account per caller, so it decoded the same few files several
hundred times; that was 74 % of its CPU.

**What is read once.** `Store.OneRead` is a copy of the store for one read
operation with a memo of two inputs: each config file's `oauthAccount`
identity, by path (`configIdentity`, `profileDirIdentity`: `credentialOwners`,
`GetCurrentAccount`, `defaultLiveSlot`, `verifyLegacyHandoff`,
`ReadOwnedCredentials`), and each profile's live Claude PIDs, by directory
(`livePIDs`: `credentialOwners`, `verifyLegacyHandoff`, `LegacyProfileOf`).
Asked again, the first answer is given. Every other input (roster, claims,
journals, group actives, profile, backup and default credentials, stale
markers) is read on every question as before. The memo dies with the copy:
nothing reaches another operation or stays in the dashboard or the engine.
The operations are `reporting.Snapshot`, `Status`, `ListAccounts`,
`BuildAccountsInfo`, `CollectUsageEntries`, `UsageByAccount`,
`UsageEntriesByAccount`, `ReplanCachedUsage`, `DefaultSessionPolicyResult`,
`FablePool`, `FableWindowNames`, `FableSwitchable`, `ReadFableRoom`,
`FableSignInNeeded`, the dashboard's Auto view, `SwitchableAccountNumbers`,
`RotationBlockReasons` and the choice `PrepareRotationCandidates` makes; one
inside another shares its reads. The functions a policy result keeps ask the
caller's store. A tick is one too, up to its candidate loop: the engine asks
its switcher's optional `BeginReads` at the start and ends it before it
prepares, rechecks or switches to a target. Meanwhile the default engine's
`tickAdapter` sends the reads that ask about ownership (current account, live
login, switchable accounts, block reasons, usage, which idle profiles to
release) and the Fable tick's `fableAdapter` its pool and usage to one
`OneRead` copy; every write and every other method go to the plain store. Inside the scope tycswap
writes no identity file and no session record (a release, a refresh, a
harvest, usage and the state file); the switch, which rewrites the default
config, comes after it. Whatever lands between a tick's first reads and its
selection is seen by the target's preparation or the switch, not by the
selection: the tick then reports the refusal (`account-unavailable`, or the
switch's error) where it reported the account held (`accounts-in-use`).

**Where a write path reads afresh.** `Store.Fresh` is the copy without the
memo. Every decision that guards a write reads through it whatever store it
is handed: `EnsureAccountAvailable`, `LegacyReleaseCandidate`,
`DiscardLegacyLogin`, `RefreshBackupGuarded`, `PersistBackupCredentials`,
`ClearLegacyForGroup`, the legacy handoff restore,
`switching.ReleaseIdleLegacy`, the harvest's owner check and a usage fetch's
choice of credential (`fetchAccountUsage`). `ReleaseLegacyAccount` reads
identities and live PIDs only through `LegacyReleaseCandidate`. `RotationOwnershipBlockReason`
asks the memo-reading `ensureAccountAvailable` and `legacyReleaseCandidate`.
A tick prepares, rechecks and switches to a target after its reads have
ended. `PrepareRotationCandidates` skips an account only when its reads say
the release would do nothing (`MayReleaseLegacy`), so an outdated read can
delay a release to the target's preparation but never runs one.

**The identity read.** `ccfile.ReadOAuthIdentityFrom` decodes the top level
into raw values and only `oauthAccount` further. It accepts and rejects what
the whole-file decode did: valid JSON with an object at the top (`null` or
another value is no identity), keys matched exactly, the last of a duplicated
key counting, and a file holding a number a float64 cannot hold refused
(`numbersFit`). A 250 KiB config reads in 0.9 ms with 31 allocations, 4.4 ms
with 39,500 before.

**Measured.** On the fixture of the tests (7 accounts, three former profiles,
an Opus group, a Fable store) one snapshot read an identity file up to 71
times (602 reads of 10 files) and scanned a profile up to 69 times (480 scans
of 8 directories), `status` and `list` the same, the Auto view 93 and 90, a
scheduled tick 51 and 49; now each once. `BenchmarkSnapshotOwnership` (8
accounts, four 250 KiB identity files): about 1.6 s, 536 MB and 11.2 million
allocations per snapshot before, 35 ms, 8.4 MB and 87,000 after.

**Limits.** The other inputs are still read per question: a snapshot opens
about 950 small files (3,000 before). `RecordPlan` still decodes the whole
live config for a record without a plan. `auto --once` is some ten
operations (the policy, the withdrawal retry, each tick, the Fable store's
checks, the retirements), so it reads a file up to 10 times (181 before).

**Tests.** `ownertest` builds the shared fixture and counts reads through two
seams (`ccfile.ObserveIdentityRead`, `store.ObservePIDScan`).
`TestReadOperationsReadOwnershipInputsOnce`,
`TestAutoViewReadsOwnershipInputsOnce` and
`TestOnceTickReadsOwnershipInputsOnce` count;
`TestTickEndsItsReadsBeforePreparingATarget` pins the scope's end;
`TestOneReadAnswersAsFreshReads` compares every ownership answer of one read
with a fresh one;
`TestWriteGuardsReadAfreshInOneRead`, `TestLegacyHandoffReadsAfreshInOneRead`,
`TestDiscardLegacyLoginReadsTheDefaultLoginAfresh`,
`TestReleaseIdleLegacyReadsAfreshInOneRead`,
`TestHarvestAndFetchReadTheOwnerAfresh` (each fails when its guard's `Fresh`
is removed), `TestSnapshotsDoNotShareOwnershipReads`,
`TestTickSeesAnIdentityChangedBeforeItSwitches` and
`TestTickSeesASessionEndedSinceTheLastTick` pin freshness;
`TestCredentialOwnersAsRecorded` the owners as recorded before;
`TestReadOAuthIdentityKeepsItsTolerance` the identity read against the
whole-file decode.

## A117. One state build is one read operation, and what no session owns is read once

**Why.** After A116, on the operator's box (2026-11-25, 0.23.1: 8 accounts,
four former session profiles with a 200 to 300 KiB `.claude.json` each, 12
linked sessions), `tycswap app --headless` still used 43 % of a core: one
dashboard state build cost about 2 CPU-seconds every 5 seconds, 77 % of it in
the session moves. `buildState` asked `SessionMoves` and `SessionLogin` once
per session; `sessionMoveChoices` tries three destinations, and
`sessionDestination` recomputed for each what does not depend on the session:
the Fable start snapshot, the Mythos selection, each login's verified owner.
Each of those was a read operation of its own, and the settings list asked the
Auto view a second time. The cost grew with sessions × destinations × accounts.

**One build, one read operation.** `web.Deps.BuildReads` gives each state
build its own `web.StateReads`: functions for the snapshot, the token-status
listing, the current login, the Auto view, the next start and each session's
login and moves. `buildState` asks for them once, at its start, and reads
through nothing else; the settings list takes the build's Auto view. The
dashboard's (`dashboard.buildReads`) are one `Store.OneRead` copy (A116) for
the snapshot, the listing, the current login and the Auto view
(`autoFacade.viewIn`), and one shared `placementInputs` over the same copy for
the sessions. Without `BuildReads` a build reads through the façades as before.

**Read once for every session.** Shared, `placementInputs` reads once the
Fable store's state, the Fable start snapshot's answer (`fableStartsOnDefault`
for no account), the Mythos move placement (`mythosMovePlacement`), each
login's roster, verified owner (`verifiedLoginOwner`; `loginOwner.of` names the
login in its refusal) and cached usage, by login (the default login, the
Fable store), and each link target's owner label
(`reporting.SessionLoginOwner`). Each session is judged against them with its
own link record: its target, held choice and family, the refresh lock of its
source (`InspectSessionLink`, `SessionTargetAvailable`) and the parked or
recovered Fable check. A preview is explicit and never asks the Fable room
rule, so that is not kept. `sessionMoveChoices` and `sessionLogin` (the
terminal menu) and every decision read through an unshared `placementInputs`,
which reads every input on every question as before.

**What stays apart.** A build's reads live in the `StateReads` its build holds
and end with it. Two builds that overlap read each their own, so the later
one reports what holds at its start; the hub still drops the older document
(A26). `StateReads` holds reads only: the mutation routes call the façades of
`Deps`, whose stores carry no memo, so a switch's guards and a session move's
decision read afresh as before.

**Every move stays per session.** `tycswap use`, the status line, the
dashboard's move route and the tick decide inside the A109 move guard on their
own reads (`placementFor(…, false)`), as before. A tick pass could share its
inputs only by checking them again under each session's guard: between two
sessions of a pass another process can switch the Fable store, or a fetch
rewrite the usage cache, and the second session must see it. So
`placeFableSessions` keeps one decision, with its own Fable start snapshot,
per session.

**Measured.** On the tests' fixture (8 accounts, three former profiles, an
Opus group, the Fable store, Mythos logins on two accounts, every account a
start login) one state build with 1, 4 and 12 linked sessions read an
identity file up to 8, 20 and 52 times (68, 170 and 442 reads), scanned a
profile up to 6, 15 and 39 times, read the roster 657, 951 and 1,735 times and
took 1, 4 and 12 Fable start snapshots; now each identity file and profile
once (11 and 9 reads), the roster 465 to 467 times and one snapshot, whatever
the number of sessions. `BenchmarkStateBuild` (four 250 KiB identity files,
12 linked sessions): 3.5 s, 167 MB and 1.11 million allocations per build
before, 0.37 s, 22 MB and 286,000 after, on a loaded box. On the operator's
store, the snapshot store-only, one whole build cost 2.9 CPU-seconds on
0.23.1 and 0.45 after, measured back to back under the same load.

**Limits.** A tick pass over 12 Fable sessions still takes 12 Fable start
snapshots (343 identity reads of 4 files and 877 roster reads on the tests'
Fable fixture, as before). What A116 reads per question is still read per
question: `CredentialOwner` is two thirds of the build's CPU left, and grows
with accounts, not sessions. Each session's link record is read twice per
build, for its login and for its moves, each time parsing `default_models.json`.

**Found while measuring.** A placement for Fable decided inside the move
guard waited for the store lock the guard held; A118 fixes it.

**Tests.** In `cli`, on Linux: `TestStateBuildReadsEachInputOncePerBuild` (1,
4 and 12 linked sessions, through `dashboardDeps`, newDashboard's wiring, with
the test's session list), `TestStateBuildWithTokenStatusReadsEachInputOnce`,
`TestStateBuildsDoNotShareTheirReads` (overlapping and in turn: a profile's
identity, the default config and the default login changed between the two),
`TestMutationsDuringAStateBuildReadAfresh` (a switch to an account a running
profile signed in as; a move to a default login no longer verified),
`TestStateBuildAnswersEachSessionAsAlone` (nine sessions on every kind of
login, held and not, one refreshing, one on a login no account owns, in ten
states, five of them against the answers recorded before),
`TestFablePlacementDecidesEachSessionOnItsOwnReads`,
`TestFablePlacementPassSeesAFableStoreSwitchBetweenSessions` and
`BenchmarkStateBuild`; in `web`, `TestEachStateBuildReadsThroughItsOwnReads`.
`ownertest` counts roster reads (`store.ObserveRosterRead`) and can mark start
logins and give accounts Mythos logins; `observeFableStartSnapshot` counts
Fable start snapshots.

## A118. A read inside the session move guard takes the guard's lock, not the file again

**Why.** The A109 move guard holds `<root>/.lock` through a `FileLock` of
its own (`reporting.withSessionRecords`), then `.default_models.lock`, and
decides the move inside. Since 0.20.0 a decision for a Fable destination
reads the Fable start snapshot there (`fableStartsOnDefault`), which builds
`BuildAccountsInfo` twice (`FableWindowNames`, `UsageEntriesByAccount`). For
an account a former session profile or a session group owns, that reads the
credential through `Store.ReadOwnedCredentials`, which takes `s.Lock`: the
same file through a second `FileLock`. `flock` refuses it to the process
that holds it, so the read polled for `filelock.DefaultTimeout` and came back
empty, per such account and twice per snapshot, with the store lock held all
the while. On the ownership fixture (two former profiles and the Opus group
own an account) one tick placement of one Fable session took 60.4 s, the
same snapshot outside the guard under 0.1 s. The tick, the status line
(`auto --observe`), `tycswap use fable`, the dashboard's move route and the
allocation answer reached it whenever the destination was Fable and the
Fable store usable.

**The guard lends its lock.** `FileLock.Lend` runs a function with a lock for
the same file that the code it calls takes instead of the file while the
lender holds it: its `Acquire` succeeds at once and asks the file nothing,
one holder at a time; once the function has returned it is an ordinary lock
again. `Store.UnderLock` is a copy of the store with such a lock as its
`Lock`; copies made from it (`OneRead`, `ForFable`, `WithoutLineageWrites`)
keep it. The move guard lends its hold of `<root>/.lock`, and `decide` now
receives `MoveSessionLink`'s view under it: the five entries and the
Mythos tick's and `switch --store mythos`'s moves read through that store.
`ReadOwnedCredentials` there runs under the guard's hold, and so does every
other store-lock taker the decision can reach (below).

**What the lock protects.** Inside the guard the file stays held by the
guard for the whole decision, so the owner and the credential
`ReadOwnedCredentials` reads still belong together against every other
process, and the lent lock serializes the decision's own holders as the
store's lock serializes a process's. Outside the guard nothing changes: each
store keeps its own lock and takes the file, and a holder in another process
or goroutine makes it wait.

**What the wait changed.** The credential `BuildAccountsInfo` reads feeds
only the usage collection's static sentinel (an API key, no credential, an
owned login expired), a fetch (none here: placement's views have no OAuth
client and fetch nothing), `list`'s token status and the duplicate-account
warnings. Plan (`RecordPlan`), usage identity (the roster's email and
organization) and "sign in again" (the Fable quarantine) come from
elsewhere. In the decisions the empty
credential reached only the snapshot's usage entries, as `no credentials`
for the owned accounts, and the policy view's serving readings behind
`FableWindowNames`, which takes the last good readings only. Start's answer
is the marked accounts, auto-switch and the candidates; the room rule and
the Mythos placement read the cached usage directly. No decision differed,
in nine states that vary each of these inputs. The wait did change two
outcomes: an explicit move retries a busy client for up to 9 s (A109) and
refuses to commit past that deadline, so `tycswap use fable` and the
dashboard's move to the Fable store failed with
`timed out preparing the session move` after about a minute. The tick, the
status line and the allocation answer moved the session, a minute late.

**A nested request fails at once.** `locknest` records which goroutine holds
each `filelock` and `cclock` lock, by its number in the goroutine's stack
header, and is asked before either waits. A goroutine that asks for a lock
file it holds, through the same lock, another lock on the path or the file
under another path, is refused at once with what its timeout would give
(`false`, Claude Code's lock timeout): it is the holder the wait would wait
for, so the wait could end only in that answer, 10 s later, unless another
goroutine released the holder's lock for it, which no tycswap code does (the
sweep below).
Another goroutine's request waits as before, and other processes see no
change. In a test binary a nesting panics on a fresh goroutine, which the
code under test cannot recover, naming the lock and both call sites, unless
a test observes nestings (`testutil.NestedLocks`). A test that stands in for
another holder holds the lock on a goroutine of its own (`testutil.HoldLock`);
25 tests did so on their own goroutine and passed only because the nested
request timed out. An acquisition and release take about 15 µs and 5
allocations more (the goroutine's number, 48 frames, a `Stat` of the lock
file): 34 µs against 16 µs on a loaded box.

**What the sweep found.** The suite run with every nesting recorded found 39
in those 25 tests and none in tycswap's own code. A call graph (VTA) from the
span of every holder of `<root>/.lock`, `.default_models.lock`, the Fable
store's locks and the engine state locks found a second acquisition of a
lock held only from the move guard's decisions: `ReadOwnedCredentials` (the
fault), `SequenceMigrated`'s organization backfill (only for a roster
without organization fields), the harvest's `withTripleLock` and a usage
fetch's `RefreshBackupGuarded` (neither runs without an OAuth client). All
four now run under the lent lock. The engine state lock reaches
`RenumberLive` and `stampedSwitch` only through the first-run setup, which
needs an empty roster in the middle of a switch, and they lock the Fable
engine's state file, not the root one. The other holders' spans call their
lock takers one after another, outside the held span.

**Rejected.** A reentrant `FileLock`, per goroutine: it hides the class
instead of catching it and is wrong for a decision that hands work to other
goroutines. Dropping the lock from `ReadOwnedCredentials`, a shorter
timeout, the decision outside the guard (A109). A store flag that skips the
lock in `ReadOwnedCredentials` alone: the lent lock covers every store-lock
taker on the view, in every package. Taking the guard's `.lock` through the
store's own `FileLock`: its in-process `hold` mutex ignores the move's
deadline (F3, §2.8) and the nested read would block on it for good. A
placement snapshot without credentials: start and placement would read
different inputs. Failing at once only in tests: production would keep a
10-second wait whose only possible end is the same refusal.

**Limits.** A holder that waits for a goroutine of its own, which asks for
the same file, still waits out that goroutine's timeout: the check knows
goroutines, not who waits for whom. A lent lock used after its lending ended
takes the file, as any lock does.

**Tests.** `TestFableMovesDecideWithoutANestedLock` (in `cli`, Linux: the
tick, the status line, `use fable`, the dashboard's move and the allocation
answer on the ownership fixture: no nested request, the Fable start snapshot
read inside the guard equals the one read outside, it is read while the
store lock is held, and the move goes where the decision outside the guard
sends it), `TestTheEmptyOwnedCredentialChangedNoDecision` (the nested read
reproduced inside the guard, its answers against those outside in nine
states), `TestTheFableStartSnapshotOutsideTheGuardTakesTheStoreLock` and, in
`store`, `TestReadOwnedCredentialsTakesTheStoreLock` (a holder elsewhere makes
the read wait); in `locknest`, `TestNestingNamesBothSites`,
`TestAnotherGoroutineIsNoNesting` and `TestNestingFailsTheTestBinary`; in
`filelock` and `cclock`, `TestNestedAcquisitionFailsAtOnce`, and in
`filelock` `TestContendingGoroutinesStillWait`. `Lend`'s contract, in
`filelock`: `TestALentLockTakesTheLendersHold` (the file stays held against
other locks meanwhile), `TestOnlyAHeldLockCanBeLent`,
`TestALentLockTakesTheFileOnceTheLendingEnds`,
`TestALentLockHasOneHolderAtATime` and `TestLendWaitsForAHolderOfTheLentLock`
(a goroutine still waiting for the lent lock as the lending ends goes to the
file); in `reporting`, `TestAStoreKeptFromTheGuardTakesTheStoreLockAfterIt`.
They wait on what a goroutine is doing, read from the goroutine dump
(`testutil.Parked`), never on a duration. The reproduction took 60.4 s
before and 0.08 to 0.11 s after, on a box at a load of about 100.

## A119. A merge that names a version releases itself

**Why.** Every release was cut by hand: an annotated tag on the merge commit
once CI passed there, notes written into the draft the Release workflow left,
`gh release edit --draft=false`. Four releases on 2026-11-25 (v0.23.0 to
v0.23.3) took that by hand, each one pull request, while macOS runners were
short all day: macOS jobs were cancelled with zero steps after about 15
minutes queued (`The job was not acquired by Runner of type hosted even after
multiple attempts`), an agent reran each with `gh run rerun --failed`, the
Release run's `publish` was skipped because `darwin` was, and the release was
put together by hand from the `linux-windows` artifact with macOS attached
hours later. The operator ruled that a release does not wait for macOS.

**The pull request decides.** `CHANGELOG.md` has `## Unreleased` (first, may
be empty), `## vMAJOR.MINOR.PATCH` sections, newest first, and
`## v0.23.3 and earlier` last: the entries written under the old single
`## Unreleased`, every one released by v0.23.3, with their words unchanged and
not split into versions they never had. A pull request that is to be released
renames `## Unreleased` to its version; the reviewer sees the version and the
notes in the diff. `release.CheckVersion(head, base, tags)` is the one rule:
it returns the version head names that base does not and refuses a level-two
heading that is none of the three kinds (a `## v0.24`, `## 0.24.0`, a
pre-release or a date after the version), a heading twice, versions out of
order, two new versions, a new one that is empty, one that equals a tag, one
not greater than every tag and every version base names, and any change to or
removal of a section base has besides `## Unreleased`. A heading inside a
fenced block is text. `make release-check` runs it locally against
`origin/main` and the working tree's file; CI's `test` job (required) runs it
with `RELEASE_BASE=HEAD^1`, the base side of the pull request's merge ref and
the previous main on a merge commit, after a full fetch for the tags.

**Two claims of one version.** The ruleset wants an up-to-date branch, so the
second pull request merges main first. Its check then meets the first one's
section on the base: git either conflicts, or merges the two headings into one
section (`changes ## v0.24.0, which the base already has`) or two (`names
## v0.24.0 twice`). `TestTheSecondClaimOfAVersionFailsBeforeItsMerge`.

**The merge releases.** `.github/workflows/release.yml` starts on
`workflow_run` when CI completes on main (only this repository's pushes: the
event reports pull request runs, forks' too). `decide` (contents and actions
read) runs `release.VersionAt` on the merge commit: `CheckVersion` of its
CHANGELOG.md against its first parent's, counting the tags merged into that
parent, so a later merge released first does not make this one late (it is
then not marked latest). No new version: nothing happens. Otherwise
`release.GateCI` reads the jobs of the CI attempt that completed
(`/attempts/<n>/jobs`, which a rerun started meanwhile does not change):
`test` and `windows` passed and `macos` passed or found no runner tag it,
unless the last earlier attempt in which `macos` ran ended otherwise (a
rerun that found no runner does not wash out a failure); a required job that
found no runner waits for its rerun, whose completion decides again; anything
else fails the run, naming the job. `tag` (contents
write) annotates the merge commit `tycswap vX.Y.Z` as `github-actions[bot]`
and pushes it; a tag already on that commit is success, one elsewhere an
error. The builds check out the tag. `publish` attaches the Linux and Windows
builds and `SHA256SUMS`, writes the notes into the draft and then publishes,
with `make_latest` false when a greater version is published already;
`attach-darwin` attaches macOS whenever it is built and never publishes. The
Linux/Windows chain and the macOS chain need nothing of each other, so each
reruns alone.

**The token's limit, and why one workflow.** A tag pushed with the workflow's
`GITHUB_TOKEN` starts no `push` workflow, so the run that tags also builds
and publishes. The update check reads `releases/latest` (A86); a release
becomes visible only through `publish`, after its Linux and Windows builds,
their lines and its notes are on the draft (`TestPublishThenAttachDarwin`
records the release as it was at that moment).

**SHA256SUMS.** `release.Publisher` never replaces a build: a name the release
holds is kept, whatever the rebuilt bytes, and an upload GitHub did not finish
(state not `uploaded`) is deleted and uploaded again. `SHA256SUMS` is computed
from the bytes attached (downloaded unless this run uploaded them), deleted and
uploaded again whenever it differs, and read back until a pass finds it equal
to the release's builds: a run that attaches meanwhile only adds lines. A line
that disagrees with its build stops the run, since a published hash never
changes; the Windows line is the same bytes before and after macOS arrives.
For the second between the delete and the upload a release has no
`SHA256SUMS`: clients offer nothing then, and `tycswap upgrade` refuses as it
did for an unreadable file.

**Clients wait for their build.** A release binary downloads its upgrade
(A36), so `update.Checker` takes a newer tag as the latest only once that
release's `SHA256SUMS` lists `AssetName(tag, platform, arch)`; until then
`Latest` returns `*update.BuildPendingError` (the card and the tray say the
build is not published yet), the passive notice prints nothing (its 24-hour
negative cache as for any failure), and `tycswap upgrade` prints `tycswap
<current> is the latest version for this machine: tycswap <tag> is released,
but its build for this machine (<asset>) is not published yet. Try again
later.` and exits 0. Before, a macOS release binary was told about the release
and `upgrade` failed with `SHA256SUMS has no line for <asset>` (exit 1). A
build from source (module, checkout) is told the tag alone and never asks for
`SHA256SUMS`.

**A runner shortage.** `release.Job.FoundNoRunner`: cancelled with no step
started (the recorded macOS job of run 45598548351 attempt 1 had no steps and
runner 0; one cancelled while running, run 45612794530, had run its first
steps). `.github/workflows/retry.yml` starts on `workflow_run` for CI and
Release; `release.DecideRetry` reruns the run's failed jobs only when every
job that did not succeed found no runner (a skipped dependant aside), the
completed attempt is the run's latest, and, for a pull request, the run is the
newest of its workflow on that branch: a newer push cancels the older run's
queued jobs with the same signature (run 44173229395). A job that ran and
failed leaves the run to a person, whose rerun of the failed jobs includes the
cancelled ones. `rerun-failed-jobs` reruns cancelled jobs (run 45598548351's
attempts 2 and 3 reran only macOS) and their dependants. Each rerun's
completion starts Retry again, until the job runs; GitHub reruns a run at most
50 times, within 30 days, and then refuses: the Retry run fails, saying a
person starts a new run. CI's concurrency group is now per commit on main
(per ref for pull requests, which still cancel), since a shared group cancels
a waiting main run when the next merge lands and that merge's version would
never be decided.

**Rerunning a release.** A fresh Release run for a merge commit that is
already tagged (a later CI attempt completing) does nothing; the run that
tagged it finishes it, rerun by Retry when a job found no runner. A rerun
(attempt > 1, **Re-run all jobs**) plans the builds the published release
lacks and changes nothing else: no second tag, no second upload, the notes of
a published release untouched (`TestRerunOfAFinishedReleaseChangesNothing`).
Two runs creating the release at once keep the older and delete the other's
draft.

**By hand.** A pushed `v*` tag runs as before and leaves a draft, with the tag
commit's section as its notes when it has one. `workflow_dispatch` with a tag
builds what its release lacks and publishes when the tag's commit has the
section: the way out past the rerun bound, and for a release cut out of band.

**Credentials.** Everything runs on `GITHUB_TOKEN`; `permissions: {}` at the
top and per job: `decide` contents and actions read, `tag`, `publish` and
`attach-darwin` contents write, the builds contents read, `retry` actions
write and contents read. Release and Retry decide in `internal/release`,
called as `go run ./internal/release/releasectl` from the default branch:
Retry never runs the code of the run it looks at, and a tag on a commit older
than this change still finds the tool. The builds alone check out the tag.

**GitHub's documented behaviour relied on.** Triggering a workflow:
"events triggered by the `GITHUB_TOKEN` will not create a new workflow run",
except `workflow_dispatch` and `repository_dispatch`. Events that trigger
workflows, `workflow_run`: it triggers "regardless of the conclusion of the
previous workflow", "only … if the workflow file exists on the default
branch", "You can't use `workflow_run` to chain together more than three
levels of workflows" (CI, Release, Retry is three), "The `requested` activity
type does not occur when a workflow is re-run" (`completed` does), and
GITHUB_SHA is the "Last commit on default branch". Re-running workflows and
jobs: "up to 30 days after its initial run", "A workflow run can be re-run a
maximum of 50 times", "Jobs that aren't included keep their outputs from the
previous attempt", "GitHub also re-runs the jobs that depend on them", re-runs
"use the same `GITHUB_SHA`". Concurrency: "any existing `pending` job or
workflow in the same concurrency group will be canceled". Releases: "Get a
published release with the specified tag"; "Only users with push access will
receive listings for draft releases"; `make_latest`, "Drafts and prereleases
cannot be set as latest". Release assets: a second upload of a name gets 422,
"you … must delete the old file before you can re-upload". Not documented and
taken from observation: a rerun started with `GITHUB_TOKEN` (triggering actor
`github-actions[bot]`) fires `workflow_run` on completion (a public
repository's release run 34162442065, attempts 2 and 3, each followed two
seconds later by its retry workflow's run).

**Rejected.** A release on every merge with a guessed bump; a bot commit that
moves entries into a version; a schedule that polls; a fixed retry count or
sleep. A separate tag-push workflow fed by a GitHub App or personal token (no
such credential exists, and none is needed). A reusable workflow called by
CI: its token can only keep or lose permissions ("permissions can only be
maintained or reduced"), so CI would need contents write on every pull
request. Holding publication until macOS (`make_latest` false until it
arrives): Linux and Windows would wait for macOS, against the ruling. A
concurrency group per release: a third run would cancel the waiting second,
with the no-runner signature. Recognising the shortage by its annotation text:
`FoundNoRunner` is structural, and a superseded pull request run is told apart
by the newer run. Rerunning the runner-less jobs of a run that also failed:
the run waits for a person anyway, and the reruns would take scarce macOS
runners.

**Limits.** A person who cancels a run while a job still queues gets that job
rerun: the signature cannot tell the cancel from the shortage; to stop a
release before it is tagged, disable the Retry workflow first. Retry watches
CI and Release, not itself. A release published while macOS is still building
has macOS untested on the merge commit when CI's `macos` found no runner; the
release's own macOS build smoke-tests the binaries, and CI's macOS rerun on
that commit does not take a release back. A job of Release that GitHub
cancelled for want of a runner is rerun alone with `rerun-failed-jobs`; when
it is `publish` or `attach-darwin`, it downloads its build from the earlier
attempt's artifact.

**Tests.** In `release`: `TestParse*`, `TestTheRepositoryChangelogParses`,
`TestCheckVersion` (each refusal above, nested lists, code spans and links,
numeric order), `TestTheSecondClaimOfAVersionFailsBeforeItsMerge`,
`TestCheckCountsEveryProblem`; `TestFoundNoRunnerOnRecordedJobs`,
`TestGateCI`, `TestGateCIAttempts` and `TestDecideRetry` on job listings
recorded from runs 45598548351 (macOS without a runner; passed), 45577203103
(macOS failed in `go test` after 11 minutes), 44173229395 (superseded) and
45612794530 (cancelled while running); `TestDecide`,
`TestVersionAtAndNotesAt`, `TestCreateTag` (git, a bare origin and two
clones), `TestPublishThenAttachDarwin`,
`TestRerunOfAFinishedReleaseChangesNothing`, `TestDarwinFirstLeavesADraft`,
`TestARerunFinishesAHalfRelease`, `TestAHandTagWithoutNotesStaysADraft`,
`TestPublishRefuses`, `TestTwoRunsCreatingTheReleaseKeepOne`,
`TestLatestOnlyForTheGreatestVersion`, `TestPublishedMissing`, `TestRetry`,
against an in-memory GitHub. In `update`:
`TestUpdateCheckWaitsForThisMachinesBuild`, `TestUpdateCheckWithoutSums`,
`TestUpdateCheckForASourceBuildIgnoresBinaries`,
`TestDownloadUpgrade_MacOSBuildNotPublishedYet`. What only GitHub can show
(the triggers, the token's push, a rerun's completion event) is checked on the
first release.

## A120. A Mythos session starts on a start login and moves on at its SessionStart hook

**Why.** Claude Code reads its login's organization settings, `availableModels`
among them, as it comes up and before its SessionStart hook runs (Claude Code
2.1.332, 2026-11-25). A session that comes up on a login whose organization
leaves Mythos out runs Opus instead (`Model "claude-mythos-5-2" is restricted
by your organization's settings`). A session that came up on Mythos keeps it
when its login moves to such an organization afterwards, at the hook or
later, in print mode too. So a Mythos session starts on a start login (A105)
and takes its quota from the selected Mythos login once it is up.

**The rule** (`startLoginInstead`, in `chooseMythosLogin`). It applies to
every linked Mythos start (`locateStartMythos` with a session link):
`start --mythos`, a launcher start with `--model claude-mythos-5-2`, a bare
start with next start Mythos, and a resume on a Mythos login (`--resume ID`
with or without a recorded login, the picker, `--continue`,
`--fork-session`, `--from-pr`). A start login is a ready Mythos login whose
account is marked (`store.StartLoginFrom`) and not allocated away from
Mythos. When the login the start would get, the ranked choice with room or
for a resume its recorded login, is no start login, the start takes:

1. the best ranked start login with room, the selected one first; notice
   `Starting on start login Account-N.`;
2. else, while a login with room asks no allocation question, a start login
   at its limit: the selected login if it is one, else the first in sequence
   order; notice `Starting on start login Account-N, which is at its limit;
   auto-switch moves the session to a Mythos login with room once it is up.`

The notice goes to stderr and, as `start: <line>`, to `tycswap.log`.
`--account N` of a start login at its limit starts there on the same
condition. A start on a start login seeds an empty Mythos selection with the
login it would have got. The rule does not apply when no Mythos login is a
start login, to an `--account` that is no start login, to a resume recorded
on a start login, when no login has room (the refusal names the first
return), when only logins with an allocation question have room (the
question comes before the launch), or to a direct launch (Windows, a failed
link), which nothing would move on.

**Auto-switch off** follows A105: a fresh start keeps its ranked choice, and
`--account` of a start login at its limit starts there with `Starting on
start login Account-N, which is at its limit.` A resume whose recorded login
has room still takes a start login: its return there is the resume binding,
not auto-switch.

**Moving on.** The SessionStart hook binds the link and places the session
as its status line would (`checkLinkedSession`, logged `by hook`): by the
model Claude Code names, else by the family `start` read for the launch
(`TYCSWAP_START_FAMILY`; A122). A resume returns to its recorded
login (`resumeMythos`, set by `bindSessionLink`) while that has room. Any
other session moves as a linked Mythos session (`mythosTickPlacement`): to
the selected login with known room, else to the best with known room, else
it stays and a tick moves it later. A print-mode session, which draws no
status line, moves the same way. With neither a model nor a family the first
status-line report places it. `SessionBinding` names an unreached `resumeMythos` before
the store a session ended on, so the next resume is placed by its recorded
login. Once moved, a report or tick moves the session to the selected login
when that differs and has known room, as for any linked Mythos session.

Until the hook binds a link, no session record names it, and every move goes
through `MoveSessionLink`, which finds a link by its record. The tick, the
quota hook, the timer, the `auto` loop, another session's held `--started`
or status line, Use for Mythos, `tycswap use` and the dashboard move a
session from its binding on; the dashboard's tick moves no link.

**Surfaces.** `start --check`, the dashboard and the tray name no login for
the next start.

**Limits.** Remote Control fails to connect on a start whose login moves
while it connects (`Session creation failed (server 400)`); `/remote-control`
connects it. A login whose room is unknown counts as room for the start but
not for the move: with no known room at the hook, a print-mode session on a
start login at its limit sends its requests there until a tick moves it. A
fresh session that ends on its start login before any move is recorded
there.

**Tests** (`cli`, Linux): `TestMythosResumeBeginsOnAStartLogin` (each resume
route), `TestMythosResumeReturnsToItsRecordedLogin`,
`TestMythosPrintModeSessionLeavesItsStartLogin` (no status line),
`TestMythosSessionLeavesItsStartLoginAtItsSessionStartHook`,
`TestMythosStartBeginsOnAStartLoginAtItsLimit`,
`TestMythosStartLoginExplicitAccount`,
`TestMythosStartWithoutAStartLoginChangeIsAsBefore`,
`TestMythosStartLoginSeedsTheSelectionWithItsRankedChoice`,
`TestMythosStartWithAFailedLinkTakesNoStartLogin` and
`TestMythosStartTakesAStartLoginWithRoomFirst`.

## A121. Every merge that adds an entry releases it, and a missing Mac runner delays nothing

**Why.** Under A119 a merge was released only when its pull request renamed
`## Unreleased` to `## vX.Y.Z` and put an empty `## Unreleased` above it. The
operator: "automatic should mean automatic not ad another manuial bullshit
chore". Naming the version was that chore; the entry a pull request writes is
all a release needs. And the Release workflow started when the whole CI
workflow completed, so a `macos` job queued for a Mac runner that never came
held v0.24.0 back about 15 minutes; the ruling: "Don't wait" for a missing
macOS runner, while a macOS job that ran and failed still blocks.

**The file.** `CHANGELOG.md` has one top section, `## Since v0.24.1`
(`release.TopHeading`), first and never renamed: every entry in it came after
v0.24.1, and the GitHub release of each version lists which of them it
carries. Below it stand `## v0.24.1`, `## v0.24.0` and `## v0.23.3 and
earlier`, frozen. A heading that names a version would need an edit on every
release, and one without a reference point (`## Changes`) would not say
where the history below starts. An entry is a top-level list item: a line
starting with `- ` outside a fenced block and every line up to the next such
line or heading (`release.Entry`); entries are told apart by their text with
trailing white space dropped, and counted, so two equal entries are two.

**What a merge releases.** `release.PendingAt(commit)`: the entries of the
commit's `CHANGELOG.md` that the `CHANGELOG.md` at the newest `v*` tag
reachable from its first parent lacks, in file order, are the release notes.
The version is that tag with the patch number raised when every new entry
starts with `**Fixed:**`, else the minor number; never a major one
(`release.NextVersion`). No new entry: nothing is released (a CI-only or
test-only merge). The entries of every section count, not the top one only:
for a file the check admits the new ones stand in the top section anyway,
and for the files before A121 it makes v0.24.1's merge compute v0.24.1 and a
hand-pushed or dispatched old tag get the entries it adds.

**The pull request check.** `release.CheckChange(head, base, tagged, since)`
runs as `make release-check` (against `origin/main`) and in CI's `test` job
(`RELEASE_BASE=HEAD^1`). It prints `Merging this releases v0.25.0 (minor over
v0.24.1: not every new entry is **Fixed:**), with this entry as its notes:`
and the entries, or `Merging this releases nothing: CHANGELOG.md has no entry
that v0.24.1 lacks.` It fails only on: a malformed file (A119's heading rules,
with the top section where `## Unreleased` stood); `## Unreleased` (`line N:
## Unreleased is gone (DESIGN A121): move its entries to the top of ## Since
v0.24.1 and delete the heading; …`, which is what an open pull request from
before A121 meets once it merges main); a version heading main does not have
(`… names a version, which a merge no longer does …`); an entry above the
top section; text in the top section that starts no entry, or an item marked
`*` or `+`; a change to or removal of a history section; and a change to or
removal of an entry main's top section has, named as `released in vX.Y.Z`
when the newest tag has it and as one `main's next release carries`
otherwise. A merged entry is frozen before its release too: whether a later
tag carries an earlier merge's entries is decided by their words. Where in
the top section a new entry stands is not checked; the notes follow the file.

**Merges released out of order.** The merge's Release run may come after a
later merge's. `release.MergeAt(commit)` reads, besides `PendingAt`, a `v*`
tag on the commit (its release, whatever its name) and the tags on later
commits (`git tag --contains`): when one of them has every new entry of the
commit, that release carries them and the run does nothing, quietly; a tag
of the computed name on such a commit is no error. So B released before A
(B's first parent) carries A's entries too, with the version their union
gets, and A's run then stops; C before B before A likewise. A name taken by
a commit that neither is the merge nor carries its entries is an error.

**The tag job claims the version.** Two Release runs can decide at once: the
CI and macOS runs of one merge, or the runs of two merges. `release.ClaimTag`
in the `tag` job fetches the tags, runs `MergeAt` again and pushes the tag;
when another run's push took the name meanwhile (`TagElsewhereError`), it
fetches and computes again, so the version moves past an earlier commit's
tag, and a later commit's tag that carries the entries ends the job with
nothing to release. A tag another run put on this commit leaves the release
to that run in attempt 1 and is finished by a rerun. The job's outputs
`tag` and `release` replace `decide`'s tag in the builds and `publish`, which
run only when `release` is true or the job was skipped (a rerun finishing a
tagged commit). The loop repeats only when a new tag appeared, so it ends.

**macOS on its own.** The `macos` job moved out of `ci.yml` into
`.github/workflows/macos.yml` (workflow `macOS`) with the same triggers,
steps, `contents: read` and concurrency expression (per commit on main, per
ref with cancel for pull requests); its job id and so its check run's name
stay `macos`. Ruleset 27211563 requires the contexts `test`, `windows` and
`macos` with no `integration_id`, and GitHub matches a required check by
that name, whatever workflow produced it.

**The gate.** Release starts on `workflow_run` for CI and macOS. `decide`
reads the newest push run of each on the merge commit
(`GET /actions/workflows/{file}/runs?head_sha=…&event=push`): CI's latest
attempt, every attempt of macOS (`release.ReleaseGate`, `GateRelease`).
`test` and `windows` not completed, not started yet, or found no runner:
wait; one that ended otherwise: block. `macos` passed, has no run on the
commit, is queued with no step started (a job not listed yet in a queued run
too), or found no runner: release now, unless the last attempt in which it
ran did not pass, which blocks (the latest found no runner) or waits (the
latest is a queued rerun, whose completion decides). `macos` started and
running: wait; its completion starts Release again. `macos` ended otherwise
(failed, cancelled while running): block. A block outweighs a wait. CI has
no run on the commit: wait. Whichever workflow completes second decides with
both known. When macOS completes first, its run waits on CI and CI's
completion decides; when macOS never ran for the commit, CI's completion
releases. When both complete within a run's decision time, both runs plan
the release and the tag job leaves it to the one that pushed the tag. A
macOS job that ran and failed after the release was decided (it was queued
then) does not take the release back, as under A119 for a job without a
runner; the pull request had passed `macos`.

**Retry** watches CI, macOS and Release. The `workflow_run` chain is at most
CI or macOS (push), Release, Retry: GitHub runs "A → B → C → D" and stops at
E. A rerun is the same run with a new attempt, keeping its trigger and its
place in the chain.

**By hand.** A tag pushed by hand builds a draft, and a dispatch publishes a
tag's release, as under A119; the notes of either are the entries the tag's
commit adds over the newest tag reachable from its first parent
(`release.NotesAt`), and with none the draft keeps the placeholder.
`releasectl decide` still accepts `--ci-run` and `--ci-attempt`, and `tag`
`--tag`, so a rerun of a Release run started before A121 finds its flags.

**Holding a release back** is not in the normal path, and nothing needs it:
a change that must not ship is reverted by the next pull request, whose
release then carries both entries. Should a merge ever have to wait, disable
the Release workflow before its CI completes (`gh workflow disable
release.yml`); once enabled again, the next merge's release carries every
entry no tag has, or a rerun of CI on the newest merge commit starts Release
for it.

**The transition.** This change's merge commit is decided by its own code
(Release checks out the default branch), while its first parent's file still
has `## Unreleased` above `## v0.24.1`: the check passes it and the merge
releases its entry over v0.24.1 as v0.25.0 (not a fix). Had v0.24.1 not been
tagged by then, the release would carry v0.24.1's entry too and v0.24.1's
run would then stop (`TestTheTransitionMerge`).

**GitHub's documented behaviour relied on**, besides A119's. Rulesets
(REST, required status checks): `context`, "The status check context name
that must be present on the commit"; `integration_id`, "The optional
integration ID that this status check must originate from". About protected
branches: "make sure that job names are unique across all workflows", since
the same job name in several workflows makes status check results ambiguous;
troubleshooting required status checks: "If a check and a commit status have
the same name, both must pass when that name is required". About status
checks: "GitHub Actions generates checks, not commit statuses, when
workflows are run". Not stated there in so many words, and observed: a job
without `name` reports a check run named by its id (`test`, `windows`,
`macos` on 4eaee2d, app `github-actions`, one check suite per workflow run).
`workflow_run`: "If you specify multiple `workflows` for the `workflow_run`
event, only one of the workflows needs to run"; "if you attempt to trigger
five workflows (named `B` to `F`) to run sequentially after an initial
workflow `A` has run (that is: `A` → `B` → `C` → `D` → `E` → `F`), workflows
`E` and `F` will not be run". List workflow runs for a workflow: `workflow_id`,
"You can also pass the workflow file name as a string"; `head_sha`, "Only
returns workflow runs that are associated with the specified head_sha";
`event`. Workflow jobs: `status` is one of `queued`, `in_progress`,
`completed`, `waiting`, `requested`, `pending`; a step's `started_at` is
"string or null". Observed: a step's `started_at` is null until it starts, a
rerun attempt's job listing carries the jobs it did not rerun with their
earlier results (run 45598548351 attempt 3), and a job queued for a Mac has
status `queued`, no runner and no steps (Release run 45694616515's
`darwin`).

**Rejected.** A bot commit that moves entries under a version heading after
the merge (a second commit per release, and the merge commit would not be
the release's). A version written by the pull request with a computed
default (still a field to fill). Holding the CI-triggered run until macOS
completes (the wait the ruling forbids). A concurrency group around tagging
(a third run cancels the pending second, with the no-runner signature).
Reading only the completed attempt of the triggering run: with two
workflows the other one's state is needed anyway, and a rerun started
meanwhile is in progress, so its completion decides.

**Limits.** An entry reworded after its merge is refused, so a correction
is a new entry. A Release run started
before this change whose `tag` job ran after it would tag the computed
version, not the named one, and its builds would not find their tag; no
pull request was open when this change was written.

**Supersedes** in A119: the pull request decides (the version heading,
`CheckVersion`, `## Unreleased`), two claims of one version (no version is
claimed), `VersionAt` and the gate on CI's completed attempt
(`GateCI`, `GateCIAttempts`, `CIGate`), a tag elsewhere always being an
error, the notes as the tag commit's version section, and the rejection of
"a release on every merge with a guessed bump" (the bump is a stated rule
now). Unchanged: tagging (annotated `tycswap vX.Y.Z`, `github-actions[bot]`),
publishing with Linux, Windows, `SHA256SUMS` and the notes in one step,
macOS attached later, the Retry rules, reruns of a half-done release,
`make_latest`, the update client's wait for its build, and the credentials.

**Tests** (`release`): `TestParseSectionsAndEntries`, `TestEntries`,
`TestTheRepositoryChangelogParses` (with the layout), `TestNewEntries`,
`TestNextVersion`, `TestCheckChange` (each refusal and its message),
`TestCheckCountsEveryProblem`, `TestTheRepositoryChangelogOverV0241`;
`TestGateRelease` on the recorded listings (macOS passed, without a runner,
failed, cancelled while running) and Release run 45694616515 (`darwin`
queued, `linux-windows` running) for queued and running macOS;
`TestReleaseGateReadsBothWorkflows`; `TestPendingAtAndNotesAt`;
`TestMergeOrders` (A then B, B then A, both fixes, C then B then A, a merge
without an entry), `TestTagRaces` (a push between a run's look and its push,
either way round, through a `pre-push` hook; the CI and macOS runs of one
merge; a name on an unrelated commit) and `TestTheTransitionMerge`, on git
repositories; `TestDecide`'s carried cases.

## A122. A linked Fable start comes up on a login that is already on a start login, and the hook places by the family start read

**Why.** The Fable store is one login for every Fable session on it. A start
that switches it to a start login (A105) puts all of them on that account,
one at its Fable limit included, for as long as the hold lasts, and they
answer `You've hit your Fable limit` meanwhile. The hold ends after 30 s; a
large resume reads its login's organization settings later than that, when
the store has moved on, and comes up without Fable. A session needs a start
login only to come up on, and one with a link of its own (A109) moves alone
afterwards, as a Mythos session does (A120): nothing shared has to move for
it.

Claude Code 2.1.332 names the model in the SessionStart hook's input of an
interactive session and not in print mode (its keys there, measured on
2026-11-25: `cwd`, `hook_event_name`, `session_id`, `source`,
`transcript_path`). A placement that waits for a model leaves a print-mode
session on the login it came up on: a Mythos one came up on Mythos and its
request answered `You've hit your weekly limit`.

**The rule** (`selectFableStartLogin`, start's one selection on the Fable
store). A session needs a start login only to come up on, so a start with a
link of its own, without `--account`, while start logins are marked and
auto-switch is on, takes a login that sits on one already:

1. the Fable store, when its account is a start login below its limits by
   start's ranking (`autoswitch.StartLoginHasRoom`): the session starts on
   the store;
2. else the default login, when its current account, verified by its
   credential and enabled, is one (`defaultIsStartLoginWithRoom`, counted on
   the 5-hour and weekly windows): the session comes up there, with `Fable
   store: Account-N is no start login; starting on the default login.` or
   `Fable store: start login Account-N is at its limit; starting on the
   default login.`

On both paths `start` switches nothing, holds nothing and writes no state,
on either store. On the default login it runs no start selection (A105,
A106) and asks no allocation question, since the session does not stay; the
hook's placement asks for the login that will serve it. With neither login
on a start login with room, nothing is there to come up on, and the start
selects on the Fable store as before, with the switch and the hold. A108's
and A112's answers come first and are unchanged: no start login in the Fable
pool, or no Fable room while the default login has some, start on the
default login through its selection. `--account N` still switches the store
to N as asked. A start without a link (Windows, a failed link, a command
that is not native Claude) is as before: nothing would move such a session
on. The link is created before the selection (`runStart`), so the rule
applies only when there is one.

**The hook.** `start --started` places every linked session it binds
(`checkLinkedSession`, logged `by hook`) by the model Claude Code names, else
by `TYCSWAP_START_FAMILY`, the family `start` read from the launch. That
family is what `start` placed the session by, so the hook's answer differs
from `start`'s only where `start` chose a login to come up on: a Fable
session on the default login goes to the Fable store when the Fable
placement rule says so (A112), a Mythos session leaves its start login
(A120). A print-mode session moves there too. With neither a model nor a
family (a picker or `--continue` without `--model`) the first status-line
report places it, as before.

**Limits.** With neither the Fable store nor the default login on a start
login with room, a Fable start still switches and holds the Fable store, for
every session on it. A session that reads its login's settings after the
default login has moved to an account whose organization leaves Fable out
comes up without Fable; the default login moves only at a limit or by hand.
While the store's account has no Fable room and another pool account has,
the session stays on the default login until the tick has moved the store.

**Tests** (`cli`, Linux): `TestLinkedFableStartLeavesTheFableStoreAlone`
(fresh, resume and print mode with another Fable session on the store: no
switch, no hold and no selection on either login, the hook alone moves the
session to the store), `TestLinkedFableStartOnAStartLoginsStore`,
`TestFableStartThatStillMovesTheFableStore` (`--account`, a failed link),
`TestLinkedFableStartWithoutAStartLoginToComeUpOnIsAsBefore`, and in
`TestMythosSessionLeavesItsStartLoginAtItsSessionStartHook` the hook without
a model.

## A123. The SessionStart hook's move waits for the client's token refresh

**Why.** Claude Code refreshes an expired token as it comes up and holds its
login's `.oauth_refresh.lock` meanwhile. A session link is not moved off a
login whose refresh lock is held (A109), and the hook's placement tried once:
on the first start after a login's token had expired the move was deferred
(`move deferred by hook: source login is refreshing`), and the session sent
its first request from the login it came up on, a start login at its limit
(A120) or the default login (A122). An interactive session was moved by its
first status-line report 4 s later, after a first message had failed with
the login's limit; a print-mode session has no status line.

**The rule.** The hook's placement (`checkLinkedSession` with `wait`) retries
while the client owns the source login's refresh lock, through
`cclock.RetryOAuthRefresh`, as `tycswap use` does: every attempt takes and
releases all locks, and the retries end after `cclock.DefaultTimeoutS` (9 s).
The session's first request follows the hook (A105), so it is sent from the
login the hook moved the session to. The status line, `start --check` and
the tick still try once and defer: they must not wait.

**Limits.** A refresh lock held longer than 9 s leaves the session where it
came up, with the deferral in `tycswap.log`; its status line or the tick
moves it. The hook's own time grows by the client's refresh, about a second.

**Tests** (`cli`, Linux): `TestMythosSessionStartHookWaitsForTheClientsRefresh`
(the lock held for 0.4 s: the hook returns after it and the session has
moved; before, it returned at once and the session stayed);
`TestStatusLineDefersDuringClientRefresh` and
`TestTickDefersDuringClientRefresh` still pass within a second.

## A124. A published release starts flakelab's pin

**Why.** A release is pinned in flakelab by its `tycswap-pin.yml`, which
opens a pin pull request that approves its own CI run and merges itself. That
workflow's quarter-hour schedule has never fired (no scheduled run since it
merged on 2026-11-25, also after its minutes were moved), so every pin so far
was started by hand. The operator wants the release and its pin to need no
person.

**How.** The Release workflow's last job, `pin`, runs after `publish` when
the release was published (`decide` says `publish=true`: a merge, or a
dispatch with notes; a hand-pushed tag's draft starts nothing) and runs
`gh workflow run tycswap-pin.yml --repo tyclab/flakelab`. The workflow's own
`GITHUB_TOKEN` can reach only this repository, and GitHub has no project
token; GitHub's documentation names a GitHub App for access across
repositories. The `tyclab-release` App, installed on tycswap and flakelab,
gives the job a token limited to flakelab and `actions: write`
(`actions/create-github-app-token`, from the Actions variable
`RELEASE_APP_CLIENT_ID` and secret `RELEASE_APP_PRIVATE_KEY`). The pin
workflow reads the newest release itself and does nothing for one that is
pinned already, so a rerun of the job is harmless.

**Failure.** While the variable `RELEASE_APP_CLIENT_ID` is not set the job
is skipped: the App is not set up, and a red run on every release would say
nothing new. With it set, a missing secret or an App that is not installed
on flakelab fails the job red after the release is published. Either way the
release stands and the pin is started by hand as before. The job runs no
code of this repository.

## A125. A linked start that would switch or hold a shared login comes up on a start login's own login

**Why.** The default login and the Fable store are each one login for every
session on them. A start whose shared login sits on no start login switches
it to one, one at its limit included, and holds it; a start on a start login
at its limit holds it (A105). Every session on that login runs on that
account meanwhile and answers with its limit. A122 ended that for a Fable
start only while the default login sits on a start login with room: with the
default login on an account that is no start login, or on one at its limit,
the Fable store was still switched, and a start of any other family switched
or held the default login as before. A session needs a start login only to
come up on (A120, A122), and a Mythos login (A109) is one account's own
login, which no selection switches.

**The rule.** A start with a link of its own, without `--account`, whose
selection on a shared login would switch or hold that login, comes up on a
start login's own login instead: the ready Mythos login of an account marked
as a start login, one below its limits by start's ranking first, else the
first in sequence order (`ownStartLogin`). `start` then switches nothing,
holds nothing, writes no state and asks no allocation question, and says
`<why>; starting on start login Account-N's Mythos login.`

- On the Fable store (`selectFableStartLogin`) this follows A122's two
  steps: the store on a start login with room, then the default login on
  one, come first.
- On the default login (`ownLoginInstead`, before `selectStartLoginFor`) it
  holds for every family, also for a Fable start the placement rule sends
  there (A112) and a Mythos start that reaches it: whenever
  `autoswitch.StartKeepsLogin` says the selection would not leave the login
  as it is.

With no start login's Mythos login ready, with `--account`, without a link,
without marks and with auto-switch off, a start is as before.

**The hook.** `start --started` places the session by its model, else its
family (A122): on the default login, on the Fable store by the placement
rule (A112), or on a Mythos login (A120). A session of another family leaves
a Mythos login at its hook also when its destination is at a limit
(`placementInputs.leaving`): it only came up there, and that account's quota
may be allocated to Mythos alone. The hook then runs one tick of both
stores, as after a start hold (A105), so auto-switch moves the shared login
on. The other deferrals stay: an allocation question, a parked Fable login,
a refresh lock held beyond A123's wait.

**Limits.** A box needs the ready Mythos login of a start login; without one
a start selects as before and says nothing. If the hook does not run, or its
move is deferred, that session's requests go out on the own login's account
until its status line or the tick moves it; no other session is affected.
Sessions on Windows and macOS have no link.

## A126. A start that moves a shared login for want of an own login says so

**Why.** A125 protects a shared login only while a start login has a ready
Mythos login. When that login is lost (signed out, its account disabled, its
Mythos mark removed), `ownStartLogin` finds none and the start selects as
before A125: it switches the default login or the Fable store to a start
login, one at its limit included, or holds it, and every session on it runs
on that account. Nothing said so, and on a box with one such login a single
lost sign-in brought the old behaviour back unnoticed.

**The rule.** A start login is enrolled while it is an OAuth login and
carries the Mythos mark or holds a Mythos login directory: the protection was
there, and its own commands give it back. An account of another kind can have
no Mythos login and is never enrolled. `store.OwnStartLogins` is the one rule for both facts, the ready
own logins and the enrolled start logins, each in sequence order;
`ownStartLogin` ranks the ready ones as A125 does, and the warning below reads
the same function. `reporting.OwnStartLoginHint` names, for the enrolled start
login that needs the fewest, the commands it is missing, in this order:
`tycswap enable <n>` while it is disabled, `tycswap mythos --allow <n>`
without the Mythos mark (`add --login --lineage mythos` asks for the mark),
`tycswap add --login --lineage mythos --slot <n>` while its Mythos login is
not signed in: `Give Account-<n> one with <commands>.`

- **At the start.** Where A125 would have come up on an own login (a link of
  its own, no `--account`, marks set, auto-switch on, a selection that would
  switch or hold) and finds none ready while a start login is enrolled, start
  says, before its selection, `<why>; no start login has a Mythos login of its
  own, so this start may switch or hold the default login for every session
  on it. Give Account-<n> one with <commands>.` (`the Fable store` with A122's
  `Fable store: ` reason on the Fable store), to stderr and `tycswap.log`
  through `reportStartSelection`. The notice names no outcome: the selection
  has not run, and with no usable start login it moves nothing. The selection
  then runs and reports as before; a hold says nothing of its own, a switch
  names its target (`Switched to start login Account-N.`).
- **Standing warning.** `reporting.OwnStartLoginWarning` holds while start
  links exist on this platform (`reporting.SessionLinksSupported`, the
  platform half of the condition `runStart` creates links by, which
  `CreateSessionLink` uses too), auto-switch is on, a start login is enrolled
  and none has a ready Mythos login. `status` and `list` print it in yellow
  after their Fable and Mythos lines; `status --json`, `list --json` and the
  dashboard's state carry it as `ownStartLoginWarning`, present only while it
  holds, and the dashboard shows it as a notice above the accounts, beside
  A115's Fable sign-in notice. It is evaluated where the state is built, so a
  page drawing a WSL box's state shows that box's answer.

A box whose start logins never had a Mythos mark or login is not nagged, as
A115 ruled for the Fable sign-in: its only remedy would be a Mythos mark,
which declares that the account runs Mythos. Which login a start takes is
unchanged.

**Limits.** The tray draws no line for the field.

**Tests.** `cli` (Linux): `TestStartWithoutAnOwnLoginSaysItMovesTheSharedLogin`
(disabled, signed out and unmarked for Mythos, each with its command, on the
default login and the Fable store, a hold, and a selection that finds no
usable start login and moves nothing), `TestStartThatTakesNoOwnLogin`
(an own login of no start login says nothing). `reporting`:
`TestStatusAndListWarnWithoutAnOwnStartLogin` (status and list, text and JSON:
present with an enrolled start login and no ready one on Linux, naming the
sign-in, or the Mythos mark once it was removed; absent with a ready one,
without marks, with auto-switch off, and without a Mythos mark or login),
`TestOwnStartLoginWarningSkipsAnAccountThatIsNoOAuthLogin`.
`web`: the `signin` harness of `TestAppJSFableSignIn` shows and hides the
dashboard notice.

## A127. The account-mismatch line is logged at DEBUG

**Why.** A68's line `live config names slot X (…) but the live credential is
slot Y's (by …); using Y` is deduplicated within one process only, and every
tick, status line and hook is a new process. A100 lowered it to DEBUG when
the config names the Fable store's live account. Since A109 a session can run
on an account's own Mythos login, and since A125 any start can come up on one;
such a client writes that account into the shared `~/.claude.json` as a Fable
session does. With one Mythos session open a box logged the line 102 times in
35 minutes.

**The rule.** The line is logged at DEBUG in every case, still once per
process and pair. The resolver takes the slot from the credential whichever
client wrote the config: a Fable session, a Mythos session, or a default-login
client that wrote its old account back after a switch (A68). The line asks
nothing of the user, so a warning was noise; on a box where every account has
a Mythos login a second special case beside A100's would have left no
mismatch at WARNING anyway. This supersedes A100 in that point:
`configNamesFableLive` and its special case are gone.

**Limits.** A68's case is no longer visible at the default log level; the
resolution is unchanged, and `--debug` shows the line.

## A128. The tick removes the temp files of killed writes

**Why.** `atomicfile` writes through a temp file `.atomic-*.tmp` beside the
target and removes it on any error, but a process killed between create and
rename (a status line the client times out, a SIGKILL) leaves it. One box
held 15 such files from three weeks in the data root and its `cache/`, mode
0600, copies of state and config files.

**The rule.** `atomicfile.SweepStale(root, olderThan, now)` removes nothing
for an empty `root` (which `filepath.EvalSymlinks` would read as the working
directory), else resolves `root` once (a data root that is itself a symlink is swept), walks it with
`filepath.WalkDir`, which follows no symlink below it (`links/` points at
login directories outside the root), and removes the regular files named like
`.atomic-*.tmp` whose mtime is older than `olderThan`; a file it cannot read
or remove is skipped. `retireProfiles`, the round every scheduled real tick
runs (`auto --once` on or off, the `auto` loop and the app's hosted loop;
never a dry run or a status line's tick), calls it on the data root
(`BackupDir`, which holds `tycswap.log`) with `staleTempAge`, one hour, and
the store's clock, and logs `Removed N stale temporary files` at INFO when N
is above zero. A write takes milliseconds, so an hour-old temp file has no
writer; a frozen writer that resumes after its temp was swept gets the
rename's error back from `Write`, as for any failed write. The walk starts at
the data root only, never at the home directory or `~/.claude`; the profile
and login directories inside the data root are walked, and there too only
`atomicfile`'s name is removed.

**Limits.** Only `atomicfile`'s name is swept: nothing else uses it. Other
temp names can leak the same way and are left as they are. Slot credentials
are not written through `atomicfile`: a killed slot write leaves a `*.tmp` of
`credstore` in `credentials/` or `credentials-fable/`, inside the swept root
and outside the swept name; the box that held the 15 `.atomic` files had no
such leftover, so it has no measured case. The others are the `*.tmp` of
`ccfile` and `switching` (in the home and Claude config directory),
`.settings-*.tmp`, `.tycswap-*.tmp`, `.tycswap-codex-*.tmp`,
`.tycswap-copy-*` and `.tycswap-upgrade-*`; `.tycswap-migrate-*.tmp` and
`login.*` have their own cleanups. A temp file outside the data root is never
removed.

## A129. A refresh waits for the reply once its request is written

**Why.** A refresh grant is single-use: once the token endpoint processed the
request, the old refresh token is dead. The guarded refreshes cut the whole
request at 5 s (and the client at 10 s), so a reply slower than that was
dropped after the server had rotated the token; the successor was lost, and
the account showed `re-login needed` later with no line in the log that
explained it. Claude Code itself waits 30 s for the same request.

**The rule.** `oauth.HTTPClient.Refresh` has two bounds, both fields of the
client (`RefreshSendTimeout`, `RefreshReplyTimeout`; zero takes
`DefaultRefreshSendTimeout`, 5 s, and `DefaultRefreshReplyTimeout`, 30 s):

- Until the request is written (DNS, connect, TLS, write; httptrace's
  `WroteRequest`) the send bound holds. A refresh runs under the store lock,
  which a waiting command gives up on after `filelock.DefaultTimeout` (10 s),
  so on a dead network a refresh holds the lock no longer than before. A
  failure here spent nothing and is `transient` as before.
- Once it is written the reply is waited for up to the reply bound. Only a
  parsed success or a 4xx is a verdict on the grant; a timeout, a reset, a
  3xx or 5xx, or a 2xx that is cut off or cannot be read is `transient` with
  `RefreshOutcome.Unanswered` set: the grant may be spent.

The guarded refreshes (`RefreshBackupGuarded`, `RefreshFableBackupGuarded`,
`RefreshFableLiveGuarded`, `RefreshLegacyLogin`, the group login's
`refreshExpiredGroupLogin`) no longer cut the context; `GuardedRefreshTimeout`
is gone. They send through `Store.SendRefresh`, and the active login's own
refresh while no client runs (`oauth.TryFetchUsageForAccount`) sends the
same way; both are `oauth.LoggedRefresh`. It logs INFO `Refreshing the token
of <subject>` once the request has been written (the client calls it from
its `WroteRequest` hook, handed over in the context), so a refresh whose
process is killed during the wait leaves a trace; WARNING when the outcome is
unanswered (the refresh token may be spent, and a later `re-login needed`
comes from this) and for `invalid_grant`; DEBUG for any other failure. A
request that never went out writes nothing above DEBUG: an offline box adds
no line per account per poll to the usage fetch's own warning. The subject
is an account number, never an address, and no line carries a token. The
send bound and the reply wait are tested over HTTP/1.1 and over HTTP/2, the
token endpoint's protocol; `WroteRequest` fires on both.

A successor that was already received is stored under the locks; that wait
is as long as one guarded refresh can hold the store lock: the Fable store's
live refresh waits inside it for Claude Code's two locks of that store
(`cclock.DefaultTimeoutS` each), then its send and reply bounds run, 53 s in
all (`store.GuardedRefreshHold`, `persistLockWait` in `reporting`, from the
existing constants). Until 2026-11-28 the wait was the send and reply bounds
alone, 35 s, and a successor received while such a refresh still waited for
a Claude Code lock was discarded with its refresh token spent. The active
login's refresh without a running client is sent before it takes the locks,
so a guarded refresh of another account that awaits its reply no longer
makes it drop the successor after 10 s. Each Claude Code lock wait can run
one backoff step (at most 0.5 s) past its bound, which the wait does not
cover.

The other callers pass `context.Background()` and so get the full reply wait:
the usage fetch (`oauth.TryFetchUsageForAccount` from `reporting`), the
auto-switch freshen, the Fable tick and `start`. Codex has its own client
(`internal/codex/api`) and is unchanged.

**Limits.** A refresh whose request went out on a network that then swallows
packets holds the store lock up to 35 s. `tycswap start` refreshes the start
target it selects before it launches, one candidate after another, so a token
endpoint that accepts and does not answer delays a launch up to 35 s per
candidate instead of 5 s; giving up earlier could cost the start login's
sign-in. A command that waits for the lock
meanwhile gives up after 10 s, as it did for any long holder before; none of
them crashes. The auto-switch tick skips that round (a freshen that could not
take the lock reports `transient`, and with every candidate so the tick says
`could not freshen any candidate (network?)`; `auto --once` exits 1 on a switch
that could not take it). `start --check` exits 0 and may show the lock's
message in the status line. `start` launches on the current login and says
`could not refresh the login; check the connection or renew it with tycswap
add --login` or `Could not select a start login: …; continuing`; a parked or
recovered Fable start exits 1 without launching, as on any lock it cannot
take: it refuses rather than launch on an unverified login. No code changed
for these: the only new long holder is a sent refresh whose reply the network
holds back, which the tick's `(network?)` and `start`'s `check the connection`
name, and the lock's own message says another instance holds it. The active
login's refresh without a running client still runs outside the locks and
drops its successor when they stay busy longer than a refresh can hold them;
refreshing it under the locks, as the guarded refreshes do (upstream #167),
is the open item.

## A130. A login whose tokens Claude Code wiped never replaces a stored login

**Why.** When the server rejects a refresh, Claude Code empties
`accessToken` and `refreshToken` in place and keeps the credential. Such a
login has no token to resolve its identity with, so a switch away classified
it `unresolved` and its fail-open backup replaced the outgoing slot's stored
login (the previous one stays in `.prev`); a switch back then installed the
wiped login, and the account needed a new sign-in although its stored login
may still have worked. `add` of the live login stored it as well.

**The rule.** `oauth.TokensWiped` is the one predicate: a JSON object that
carries neither an access nor a refresh token under `claudeAiOauth`, the
object absent included. Not wiped: a blank access token with a refresh
token, a refresh token alone, a setup token (access token only), a raw API
key. Where a live login would be written into a slot it is checked:

- The switch's ownership oracle returns `wiped` after `own-bytes` and
  `own-family` (an identical backup is wiped already). The switch backs up
  the outgoing config only, as for `own-bytes`, keeps the stored login, and
  warns; a wiped login the last install attributes is not stashed first
  either `The live login carries no tokens (…); Account-N's stored login was
  kept.` A group or legacy release that meets `wiped` holds the login as it
  held `unresolved` before.
- `add` of the live login refuses it with a validation error that asks for a
  new sign-in (`tycswap add --login`), before it asks to overwrite a slot or
  changes anything: until 2026-11-28 the refusal came after the Fable store's
  live login of the displaced account had been emptied.
- The Fable store's outgoing login (`backUpFableOutgoing`, which also takes
  the last install's account) is not written into the Fable login, and with
  no account named for it it is not stashed either; a warning is logged.
- `export` of the active account whose live login is wiped exports the
  account's stored login, as for an inactive account, and says so on stderr.
- `import` skips an entry whose credential is wiped, with a warning that
  names the account, and imports the others: such an entry is never written
  into a slot, and one dead login does not block an archive made before this
  rule or a stored login the old defect replaced.

The harvest of a rotated live login (`harvestLiveCredential`,
`HarvestFableLive`) and the `own-rotated` branch cannot reach a wiped login:
they need the chain record or a profile answer, and both need an access
token. Disabling the account whose login is live cannot either: before
`parkDefaultLogin` writes, `WithdrawLogin` refuses a login only the config
or the last install attributes, and a wiped login has no other attribution.
A `--login` sign-in is checked by `CheckLogin`; renumbering and rollback move
stored logins only.

**Limits.** A login with only one of its two tokens left is not wiped and is
handled as before. A live JSON object with no `claudeAiOauth` at all counts
as wiped, so `add` refuses it.

## A131. A reserved hand-over goes only to a lower rank

**Why.** A102 let a default login on the account reserved last hand over to
any other account below every bar, and the policy made two kinds of account
last: the Fable store's live account, and every account whose Mythos login a
live session uses (A108's startup allocation). With two such accounts below
every bar and every other at a limit or disabled, and no live session on the
default login, each was a target from the other: the `reserved` trigger
moved the default login from one to the other and back once per cooldown,
four switches in 18 minutes, from the tick or from a status line's
observation. Nothing was gained by either move, and each one moved a shared
login. While a session ran on the default login an allocation could hide
one of the two and the tick said `reserved-no-target`; the cycle began once
that session ended.

**The rule.** Every account has a rank in the default rotation,
`PolicyResult.ReservedRank`, the one place that decides it:

| Rank | Account |
|---|---|
| 0 `RankFree` | not reserved |
| 1 `RankReserved` | reserved (A71, A77, A91, A102) |
| 2 `RankFableStore` | the Fable store's live account (A102) |
| 3 `RankMythos` | a live session uses its Mythos login, also when the Fable store runs on it |

The engine takes it through `autoswitch.WithReservedRank`; without the
option a reserved account is rank 1 and any other rank 0. A `reserved`
hand-over goes only to an account below every bar of a strictly lower rank:
from rank 1 to rank 0 as before, from rank 2 to rank 0 or 1, from rank 3 to
rank 0, 1 or 2, never between two accounts of the same rank. With no target
the tick says `reserved-no-target`, rank 2 with `the Fable store runs on the
active account; no other account is below every bar unless its Mythos login
is in use`, rank 3 with `the active account's Mythos login is in use; no
other account is below every bar unless its Mythos login is in use too`.
Within a bar tier a target of a higher rank sorts after one of a lower rank
under both strategies, and an idle Opus start (A105) takes a rank 3 account
after a rank 2 one, both after the others and before the logins at a limit.
No other trigger changes.

**Why 3 after 2.** Mythos room is the scarcest a box has, and the account
whose Mythos login runs sessions is the one that needs it; the Fable
store's account is the better place for the default login to rest.

**Limits.** With no session on the default login the policy follows no
family, so an account whose explicit model list names only Mythos stays a
target of the `at-limit` and `proactive` triggers; it ranks last there
too. `RankTargets` (a switch by hand, the dashboard's best candidate) ranks
by reservation alone, as before.
