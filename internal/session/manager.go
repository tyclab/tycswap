package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/printer"
)

// These make claude bypass account OAuth (verified against claude 2.1.175); scrubbed from launch and probe env, not the fast path.
var AuthOverrideEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
	"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
	// Not an auth override, but Claude Code keeps its credentials under this
	// dir instead of the session profile's, and in the default ~/.claude
	// storage when it is set but empty: the live login's (DESIGN A56).
	"CLAUDE_SECURESTORAGE_CONFIG_DIR",
}

// authStatusTimeout bounds the `claude auth status --json` probe (a local check
// that still spawns the full CLI).
const authStatusTimeout = 10 * time.Second

// bootstrapLockTimeout is the FileLock acquire budget for bootstrap — larger
// than the switch paths' 10s because bootstrap may hold the lock across one
// token refresh (a 10s network call) plus auth-status probes.
const bootstrapLockTimeout = 30 * time.Second

// Manager bootstraps per-account session profiles and launches claude into them.
type Manager struct {
	accounts Accounts
	oauth    oauth.Client
	kc       keychain.KeychainClient
	clk      clock.Clock
	log      *logging.Logger
	stdout   io.Writer
	runner   Runner

	getenv         func(string) string
	environ        func() []string
	lockConfig     func(lockDir string) (func(), error)
	lockTimeout    time.Duration
	hookExecutable string
}

// Options configures a Manager. Every field is optional except a working
// Accounts (passed to NewManager); zero-value seams fall back to production
// defaults.
type Options struct {
	// OAuth performs the one proactive bootstrap refresh. Required for a real
	// launch; a nil client makes every refresh a transient failure (warn +
	// keep stored creds).
	OAuth oauth.Client
	// Keychain is the macOS keychain seam (session-profile hashed entries).
	Keychain keychain.KeychainClient
	// Clock drives the MCP mirror's proper-lockfile staleness check.
	Clock clock.Clock
	// Logger receives the module's info/warning lines (nil-safe).
	Logger *logging.Logger
	// Stdout receives the user-facing dimmed/warning/accent messages.
	Stdout io.Writer
	// Runner is the process seam (LookPath / auth-status probe / exec handoff).
	Runner Runner
	// Getenv / Environ inject the environment (default os.Getenv / os.Environ).
	Getenv  func(string) string
	Environ func() []string
	// LockConfig acquires Claude Code's <config>.lock for the MCP splice,
	// returning a release closure. Default is cclock-backed.
	LockConfig func(lockDir string) (func(), error)
	// LockTimeout overrides the bootstrap FileLock acquire budget (default 30s).
	LockTimeout    time.Duration
	HookExecutable string
}

// NewManager builds a Manager over the given account store and options.
func NewManager(accounts Accounts, opts Options) *Manager {
	m := &Manager{
		accounts:       accounts,
		oauth:          opts.OAuth,
		kc:             opts.Keychain,
		clk:            opts.Clock,
		log:            opts.Logger,
		stdout:         opts.Stdout,
		runner:         opts.Runner,
		getenv:         opts.Getenv,
		environ:        opts.Environ,
		lockConfig:     opts.LockConfig,
		lockTimeout:    opts.LockTimeout,
		hookExecutable: opts.HookExecutable,
	}
	if m.kc == nil {
		m.kc = keychain.NewFake()
	}
	if m.clk == nil {
		m.clk = clock.System{}
	}
	if m.stdout == nil {
		m.stdout = os.Stdout
	}
	if m.runner == nil {
		m.runner = osRunner{}
	}
	if m.getenv == nil {
		m.getenv = os.Getenv
	}
	if m.environ == nil {
		m.environ = os.Environ
	}
	if m.lockTimeout <= 0 {
		m.lockTimeout = bootstrapLockTimeout
	}
	if m.lockConfig == nil {
		m.lockConfig = func(lockDir string) (func(), error) {
			h, err := clockLockConfig(lockDir, m.clk)
			if err != nil {
				return nil, err
			}
			return h, nil
		}
	}
	return m
}

// setupMode captures the handful of run-vs-env differences the shared setup
// path (setupPreamble + setupBootstrap) is parameterized by: the status-notice
// verb, and the message templates whose wording differs between a launch
// ("this session") and an export ("this shell"). See setupPreamble.
type setupMode struct {
	verb           string // status-notice verb ("Launching" / "Prepared")
	presetOverride string // CLAUDE_CONFIG_DIR-preset warning; one %s (the preset)
	sameActiveNote string // note when the account is already the active default and no preset; two %s (num, email)
	scrubIgnore    string // auth-override scrub warning; one %s (the joined var names)
}

var (
	runMode = setupMode{
		verb:           "Launching",
		presetOverride: "CLAUDE_CONFIG_DIR is already set (%s); overriding it for this launch.",
		sameActiveNote: "Account-%s (%s) is already the active default login — launching claude directly.",
		scrubIgnore:    "Ignoring %s for this session — it would override the selected account inside Claude Code.",
	}
	envMode = setupMode{
		verb:           "Prepared",
		presetOverride: "CLAUDE_CONFIG_DIR is already set (%s); this export overrides it for this shell.",
		sameActiveNote: "Account-%s (%s) is the active default login — an unpinned shell already uses it; nothing exported.",
		scrubIgnore: "Removing %s for this shell — these override the selected account inside Claude Code. " +
			"They are unset for the WHOLE shell (not just tycswap); re-export them to restore.",
	}
)

// setupPreamble runs the shared Run/SetupEnv front matter: locate claude, apply
// the Windows share-history guard, resolve the account, reject API-key accounts,
// and classify the CLAUDE_CONFIG_DIR-preset / same-active-default situation. It
// emits the preset-override warning (when a preset is present) or the
// same-active-default note (when the requested account is already the live
// default login and no preset is set) — the two points where Run execs directly
// and env no-ops (D1 / FINDING 1). sameActive reports that second case; the
// caller owns the exec-vs-no-op decision, and the note has already been printed.
// Nothing here bootstraps a profile.
func (m *Manager) setupPreamble(identifier string, claudeArgs []string, shareHistory bool, mode setupMode) (claudeBin, accountNum, email string, sameActive bool, err error) {
	bin, lookErr := m.runner.LookPath("claude")
	if lookErr != nil || bin == "" {
		return "", "", "", false, errClaudeNotFound()
	}
	if err := CheckCmdShimArgs(bin, claudeArgs); err != nil {
		return "", "", "", false, err
	}
	if shareHistory && m.accounts.Platform() == platform.Windows {
		return "", "", "", false, errShareHistoryWindows()
	}
	num, mail, _, rerr := m.accounts.ResolveAccount(identifier)
	if rerr != nil {
		return "", "", "", false, rerr
	}
	// Guard before any exec/no-op decision and before setup_session.
	if aerr := m.ensureNotAPIKey(num, mail); aerr != nil {
		return "", "", "", false, aerr
	}

	if preset := m.getenv("CLAUDE_CONFIG_DIR"); preset != "" {
		// "current default account" is meaningless once CLAUDE_CONFIG_DIR is set
		// (we may already be inside a session), so neither the fast path nor the
		// D1 no-op triggers even on an identity match — the preset is overridden
		// and the profile prepared.
		m.warn(fmt.Sprintf(mode.presetOverride, preset))
	} else if cur := m.accounts.CurrentAccountNumber(); cur != nil && *cur == num {
		m.println(printer.Dimmed(fmt.Sprintf(mode.sameActiveNote, num, mail)))
		return bin, num, mail, true, nil
	}
	return bin, num, mail, false, nil
}

func (m *Manager) setupBootstrap(identifier string, share, shareHistory bool, mode setupMode) (sessionDir, accountNum, email string, scrubbed []string, err error) {
	scrubbed = m.scrubbedPresent()
	if len(scrubbed) > 0 {
		m.warn(fmt.Sprintf(mode.scrubIgnore, strings.Join(scrubbed, ", ")))
	}
	dir, num, mail, serr := m.SetupSession(identifier, share, shareHistory)
	if serr != nil {
		return "", "", "", nil, serr
	}
	m.println(fmt.Sprintf("%s Account-%s (%s) %s",
		printer.Accent(mode.verb), num, mail, printer.Muted("[session mode]")))
	return dir, num, mail, scrubbed, nil
}

// Run launches Claude Code as the given account in the current terminal. On
// POSIX it execs and never returns on success; with a mocked runner it returns
// nil after recording the exec.
func (m *Manager) Run(identifier string, claudeArgs []string, share, shareHistory bool) error {
	claudeBin, _, _, sameActive, err := m.setupPreamble(identifier, claudeArgs, shareHistory, runMode)
	if err != nil {
		return err
	}
	if sameActive {
		// Same-account fast path: never create a second credential copy for the
		// account that is already the active default login (two copies can drift
		// on refresh-token rotation). The note was printed by the preamble.
		return m.exec(claudeBin, claudeArgs, m.environ())
	}

	sessionDir, _, _, _, err := m.setupBootstrap(identifier, share, shareHistory, runMode)
	if err != nil {
		return err
	}

	env := setEnvVar(scrubEnv(m.environ(), AuthOverrideEnvVars), "CLAUDE_CONFIG_DIR", sessionDir)
	return m.exec(claudeBin, claudeArgs, env)
}

// ExecDefault launches plain Claude Code with the current default login: no
// session profile, no auth-override scrubbing, the unmodified environment
// passed through — identical to typing `claude` directly.
func (m *Manager) ExecDefault(claudeArgs []string) error {
	claudeBin, err := m.runner.LookPath("claude")
	if err != nil || claudeBin == "" {
		return errClaudeNotFound()
	}
	return m.exec(claudeBin, claudeArgs, m.environ())
}

// exec hands the terminal to claude via the runner. POSIX replaces the process
// image (never returns on success); Windows spawns, waits, and exits with
// claude's own return code.
func (m *Manager) exec(claudeBin string, claudeArgs []string, env []string) error {
	if err := CheckCmdShimArgs(claudeBin, claudeArgs); err != nil {
		return err
	}
	argv := append([]string{claudeBin}, claudeArgs...)
	return m.runner.Exec(claudeBin, argv, env)
}

// ensureNotAPIKey rejects API-key accounts in session mode (unsupported yet):
// bootstrap is OAuth-shaped and _is_session_valid requires authMethod
// "claude.ai", so an api_key account would fail validation opaquely.
func (m *Manager) ensureNotAPIKey(accountNum, email string) error {
	if m.accounts.AccountKindFor(accountNum) == "api_key" {
		return cerr.Session(
			"Account-%s (%s) is an API-key account; 'tycswap run' (session mode) "+
				"does not support API-key accounts yet. Use 'tycswap --switch-to' "+
				"to make it your default login instead.", accountNum, email)
	}
	return nil
}

func errClaudeNotFound() error {
	return cerr.Session("'claude' was not found on PATH. Install Claude Code first.")
}

// errShareHistoryWindows is the shared Run/SetupEnv rejection of --share-history
// on Windows (sharing re-syncs copies there, which would fork history).
func errShareHistoryWindows() error {
	return cerr.Session(
		"--share-history is not supported on Windows yet: sharing uses " +
			"re-synced copies there, which would fork the history instead " +
			"of sharing it.")
}

// scrubbedPresent returns the AUTH_OVERRIDE_ENV_VARS that are currently set
// (non-empty), in declaration order.
func (m *Manager) scrubbedPresent() []string {
	set := make(map[string]bool)
	for _, e := range m.environ() {
		set[envKey(e)] = true
	}
	var out []string
	for _, v := range AuthOverrideEnvVars {
		if set[v] { // set at all: an empty value still counts (see the list)
			out = append(out, v)
		}
	}
	return out
}

// probeEnv builds the auth-status probe env: the session config dir, with the
// auth-override vars dropped.
func (m *Manager) probeEnv(sessionDir string) []string {
	return setEnvVar(scrubEnv(m.environ(), AuthOverrideEnvVars), "CLAUDE_CONFIG_DIR", sessionDir)
}

// -- output helpers (session.py print(dimmed(...)) / warning(...) → stdout) --

func (m *Manager) println(s string) { fmt.Fprintln(m.stdout, s) }

func (m *Manager) warn(s string) { fmt.Fprintln(m.stdout, printer.Yellowed(s)) }

func (m *Manager) logInfof(format string, a ...any) {
	if m.log != nil {
		m.log.Infof(format, a...)
	}
}

func (m *Manager) logWarnf(format string, a ...any) {
	if m.log != nil {
		m.log.Warningf(format, a...)
	}
}

// -- environment helpers -----------------------------------------------------

func envKey(entry string) string {
	if i := strings.IndexByte(entry, '='); i >= 0 {
		return entry[:i]
	}
	return entry
}

// ScrubAuthOverrides returns environ without the AuthOverrideEnvVars, the
// scrub run and env apply, for other commands that start claude.
func ScrubAuthOverrides(environ []string) []string { return scrubEnv(environ, AuthOverrideEnvVars) }

// scrubEnv returns a fresh slice with every entry whose key is in drop removed.
func scrubEnv(environ, drop []string) []string {
	dropSet := make(map[string]bool, len(drop))
	for _, d := range drop {
		dropSet[d] = true
	}
	out := make([]string, 0, len(environ))
	for _, e := range environ {
		if dropSet[envKey(e)] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// setEnvVar upserts key=value into environ (replacing in place if present, else
// appending). The input slice is mutated when the key already exists.
func setEnvVar(environ []string, key, value string) []string {
	kv := key + "=" + value
	for i, e := range environ {
		if envKey(e) == key {
			environ[i] = kv
			return environ
		}
	}
	return append(environ, kv)
}

// -- credential-shape helpers ------------------------------------------------

// hasRefreshToken mirrors SessionManager._has_refresh_token: an unparsable or
// unknown-shape blob returns true ("let the refresh attempt decide"); a
// setup-token account (parsed, no refreshToken) returns false (skip silently).
func hasRefreshToken(creds string) bool {
	var v any
	if err := json.Unmarshal([]byte(creds), &v); err != nil {
		return true // JSONDecodeError → unknown shape
	}
	m, ok := v.(map[string]any)
	if !ok {
		return true // .get on a non-dict → AttributeError
	}
	ca, present := m["claudeAiOauth"]
	if !present {
		return false // .get("claudeAiOauth", {}) → {} → .get("refreshToken") → None
	}
	caMap, ok := ca.(map[string]any)
	if !ok {
		return true // claudeAiOauth present but not an object → AttributeError
	}
	rt, present := caMap["refreshToken"]
	if !present {
		return false
	}
	return pyTruthy(rt)
}

// pyTruthy mirrors Python bool(x) for JSON-decoded values.
func pyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case json.Number:
		return t.String() != "0" && t.String() != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// -- JSON / filesystem helpers -----------------------------------------------

// encodeJSONIndent marshals v as json.dumps(indent=2) does: two-space indent,
// no HTML escaping (so URLs with & or < survive), no trailing newline.
func encodeJSONIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p) // follows symlinks (Path.exists parity)
	return err == nil
}

func isSymlink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// chmodPosix applies mode on non-Windows hosts (every session.py chmod is gated
// on sys.platform != "win32").
func chmodPosix(path string, mode os.FileMode) {
	if !platform.IsWindows() {
		_ = os.Chmod(path, mode)
	}
}

// backgroundCtx is a package-level seam kept trivial; the oauth client applies
// its own 10s refresh deadline.
func backgroundCtx() context.Context { return context.Background() }
