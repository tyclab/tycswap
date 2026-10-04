// web.go — the local dashboard server: consumer-defined seams, token/cookie
// security model, loopback-only bind, and the poll loop that drives SSE.
//
// Package web implements DESIGN A26 (`internal/web` — the local dashboard):
// an embedded single-page UI on 127.0.0.1:<port> backed by a small JSON API
// and a Server-Sent-Events stream. Every seam is consumer-defined here (A2
// style; Facade is the frozen tui.Facade method set), so this package never
// imports internal/core, internal/tui or internal/cli.
//
// Security model (A26): three independent 128-bit secrets are minted per
// launch. The one-time launch token rides in the printed URL; GET /?token=<t>
// redeems it once, sets the HttpOnly, SameSite=Lax, Path=/ session cookie and
// redirects to /#csrf=<token>. The CSRF token travels in that URL fragment
// alone — never in the page, which the cookie alone fetches, and never to
// the server — and the page keeps it in sessionStorage. Every /api/*
// request needs the cookie AND X-CSRF-Token (reads included; EventSource
// sends ?csrf= instead); every mutating request also needs an Origin /
// Sec-Fetch-Site header that is absent or same-origin. The server binds
// loopback only and answers 421 to any Host header other than
// 127.0.0.1:<port> / localhost:<port>. Credential material is never
// serialised.
package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tyclab/tycswap/internal/brand"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/reporting"
)

//go:embed static
var staticFS embed.FS

// Facade is the account-operation seam the dashboard drives — the frozen
// tui.Facade method set (DESIGN §2.20/A13), copied verbatim so *core.Switcher
// satisfies it structurally without this package importing tui or core.
type Facade interface {
	AccountsSnapshot(fetch map[string]bool) *reporting.AccountsSnapshot
	SwitchTo(id string, jsonOut bool) (map[string]any, error)
	Switch(strategy *string, jsonOut bool, models []string, modelSrc *string) (map[string]any, error)
	SetAccountDisabled(id string, disabled bool) error
	RemoveAccount(id string, yes bool) error
	AddAccount(slot *int, assumeYes bool, alias *string) error
	AddAccountFromToken(token string, email, slotArg *string, assumeYes bool) error
	BackupDir() string
	SetPollPolicyInputs(threshold float64, models []string)
	ClearPollPolicyInputs()
}

// BaseURLAdder is add-token with a base URL (DESIGN A46). It is not on the
// frozen Facade: the handler asks the facade for it by type assertion, and
// *core.Switcher provides it.
type BaseURLAdder interface {
	AddAccountFromTokenWithBaseURL(token, baseURL string, email, slotArg *string, assumeYes bool) error
}

// AccountOps is the account lifecycle beyond Facade (alias, move, swap, force
// switch, token-status listing, the API-key switch approval). *core.Switcher
// satisfies it.
type AccountOps interface {
	SetAlias(id, alias string) (num, normalized string, err error)
	UnsetAlias(id string) (num string, err error)
	MoveAccount(account, target string) (srcNum, tgtNum string, swapped bool, err error)
	SwapAccounts(first, second string) (numA, numB string, err error)
	SwitchToForce(id string, jsonOut, force bool) (map[string]any, error)
	// ApproveAPIKeySwitch records the user's explicit yes to switching onto
	// the API-key account id names: a change of how Claude Code
	// authenticates that a running session does not pick up, so the switch
	// layer refuses it without one (DESIGN A33).
	ApproveAPIKeySwitch(id string)
	// ListAccounts with showTokenStatus=true, jsonOut=true yields the
	// `tycswap list --token-status --json` payload; /api/state?tokenStatus=1
	// lifts each row's "tokenStatus" string from it.
	ListAccounts(showTokenStatus, jsonOut bool, fetch map[string]bool) (any, error)
}

// CodexOps is the Codex account surface the dashboard drives (DESIGN A47):
// what the terminal dashboard does with a Codex row, plus storing the login
// the codex CLI has, as `tycswap codex add` does. It is its own narrow seam
// because the Claude signatures do not fit: a Codex switch reports the codex
// sessions still running on the old account. id is a Codex account reference
// (slot number, email or alias); every call takes the Codex store's own lock.
type CodexOps interface {
	// SwitchTo activates the account and answers {"number", "email",
	// "runningPids": [...], "alreadyActive"}.
	SwitchTo(id string) (map[string]any, error)
	SetAccountDisabled(id string, disabled bool) error
	// RemoveAccount forgets the account without a prompt (`codex remove -y`).
	RemoveAccount(id string) error
	// AddCurrent stores the current Codex login and answers {"number", "email"}.
	AddCurrent() (map[string]any, error)
}

// SettingView is one effective setting (`tycswap config`).
type SettingView struct {
	Key         string   `json:"key"`   // dotted, e.g. "autoswitch.sevenDayThreshold"
	Kind        string   `json:"kind"`  // "float"|"int"|"bool"|"choice"|"string"
	Value       any      `json:"value"` // effective value
	Default     any      `json:"default"`
	IsDefault   bool     `json:"isDefault"`
	Choices     []string `json:"choices,omitempty"`
	Description string   `json:"description"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	// Applies says when a saved value takes effect for this dashboard. The
	// server sets it (settingApplies, A27); a facade's value is replaced.
	Applies string `json:"applies"`
}

// SettingsFacade is the settings.json surface.
type SettingsFacade interface {
	Effective() []SettingView
	Set(dotted, raw string) (any, error)
	Unset(dotted string) (bool, error)
}

// AutoEventView is one auto-switch engine event. Provider is "" for the
// Claude engine's events, so their JSON is unchanged, and "codex" for a
// Codex tick, whose Kind is one of the Claude engine's kinds (DESIGN A47).
type AutoEventView struct {
	At       float64        `json:"at"`
	Kind     string         `json:"kind"`
	Message  string         `json:"message"`
	Account  string         `json:"account,omitempty"`
	Fields   map[string]any `json:"fields,omitempty"`
	Provider string         `json:"provider,omitempty"`
}

// AutoView is the auto-switch engine's state as the dashboard shows it.
type AutoView struct {
	Available  bool            `json:"available"`
	Running    bool            `json:"running"`
	DryRun     bool            `json:"dryRun"`
	StartedAt  *float64        `json:"startedAt"`
	Threshold  float64         `json:"threshold"`  // live autoswitch.sevenDayThreshold, the 7d bar (ApplyThreshold-adjusted)
	Settings   map[string]any  `json:"settings"`   // effective autoswitch settings the engine started with
	Events     []AutoEventView `json:"events"`     // most recent last, ring of <= 200
	Quarantine map[string]any  `json:"quarantine"` // contents of autoswitch_state.json (may be nil)
	// Codex is the Codex engine that runs beside the Claude one; null on an
	// install without Codex accounts (DESIGN A47).
	Codex *CodexAutoView `json:"codex"`
}

// CodexAutoView is the Codex auto-switch engine as the dashboard shows it.
// Threshold is its one bar (autoswitch.codexThreshold, or the 7d bar when
// that is 0), fixed when the engine starts: the slider never moves it.
type CodexAutoView struct {
	Enabled   bool           `json:"enabled"` // autoswitch.codexEnabled
	Running   bool           `json:"running"`
	Threshold float64        `json:"threshold"`
	LastTick  *CodexTickView `json:"lastTick"` // null before the first tick
}

// CodexTickView is one Codex engine tick, in the names of `tycswap auto
// --json`'s codex line.
type CodexTickView struct {
	At          float64 `json:"at"`
	Outcome     string  `json:"outcome"` // ok | switched | blocked | no-accounts | error
	Detail      string  `json:"detail"`
	SwitchedTo  *string `json:"switchedTo"` // null unless outcome is switched
	RunningPIDs []int   `json:"runningPids"`
}

// AutoFacade drives the hosted auto-switch engine.
type AutoFacade interface {
	View() AutoView
	Start(dryRun bool) error
	Stop() error
	Wake() error
	ApplyThreshold(threshold float64) error // the 7d bar, within the autoswitch.sevenDayThreshold bounds (50–100)
	// ApplyModels retargets which per-model weekly windows the RUNNING engine
	// counts ("all", a comma-separated list, or "" for 5h + 7d only).
	ApplyModels(model string) error
}

// Strategies are the manual `switch --strategy` choices the UI offers.
// "soonest-reset" is deliberately NOT here: switching.Switch treats any
// string outside this set as plain rotation (next slot, usage ignored), and
// soonest-reset exists only as the AUTO-switch ordering (autoswitch.strategy,
// DESIGN A17), so offering it would rotate without looking at usage.
var Strategies = []string{"best", "next-available"}

// SessionsView is one probe of procdetect's running Claude Code sessions and
// IDE instances. ConfigDir and Profile are keyed by PID: the Claude config
// directory the session was found in (its transcripts live there too) and,
// for a session started with `tycswap run` / `tycswap env`, the slot of the
// account its session profile belongs to ("" for the default login).
type SessionsView struct {
	Claude    []procdetect.ClaudeSession
	IDE       []procdetect.IdeInstance
	ConfigDir map[int]string
	Profile   map[int]string
}

// DefaultSessions probes procdetect's on-disk state under the default Claude
// config directory only. It is the Deps.Sessions default; SessionsIn also
// covers the session profiles.
func DefaultSessions() SessionsView {
	return probeSessions(procdetect.GetClaudeDir(), "")
}

// SessionsIn returns a Deps.Sessions probe over the default Claude config
// directory AND every session profile under <backupDir>/sessions/ (the
// CLAUDE_CONFIG_DIR of `tycswap run` and `tycswap env`), so a session started
// as another account is listed and can be stopped. Profile directories are
// named <slot>-<email slug> (sessprofile.SessionDirFor). A session PID seen
// twice is listed once, and so is an IDE instance (its lock file names a
// port and carries a PID) found under more than one directory; the default
// directory wins.
func SessionsIn(backupDir string) func() SessionsView {
	return func() SessionsView {
		v := probeSessions(procdetect.GetClaudeDir(), "")
		if backupDir == "" {
			return v
		}
		entries, err := os.ReadDir(filepath.Join(backupDir, "sessions"))
		if err != nil {
			return v
		}
		seen := map[int]bool{}
		for _, c := range v.Claude {
			seen[c.PID] = true
		}
		seenIDE := map[[2]int]bool{}
		for _, i := range v.IDE {
			seenIDE[[2]int{i.PID, i.Port}] = true
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			slot, _, _ := strings.Cut(e.Name(), "-")
			p := probeSessions(filepath.Join(backupDir, "sessions", e.Name()), slot)
			for _, c := range p.Claude {
				if seen[c.PID] {
					continue
				}
				seen[c.PID] = true
				v.Claude = append(v.Claude, c)
				v.ConfigDir[c.PID], v.Profile[c.PID] = p.ConfigDir[c.PID], slot
			}
			for _, i := range p.IDE {
				k := [2]int{i.PID, i.Port}
				if seenIDE[k] {
					continue
				}
				seenIDE[k] = true
				v.IDE = append(v.IDE, i)
			}
		}
		return v
	}
}

// probeSessions lists dir's sessions, keeping only records whose file is
// named after the pid they carry: Claude Code writes sessions/<pid>.json, so
// a record whose file name disagrees with its pid field was not written by
// the process it names, and must not be shown or stopped as that process.
func probeSessions(dir, slot string) SessionsView {
	listed, ide := procdetect.GetRunningInstances(dir)
	claude := make([]procdetect.ClaudeSession, 0, len(listed))
	for _, c := range listed {
		if filepath.Base(c.Path) == strconv.Itoa(c.PID)+".json" {
			claude = append(claude, c)
		}
	}
	v := SessionsView{Claude: claude, IDE: ide, ConfigDir: map[int]string{}, Profile: map[int]string{}}
	for _, c := range claude {
		v.ConfigDir[c.PID], v.Profile[c.PID] = dir, slot
	}
	return v
}

// DefaultSessionTitle reads the title from the transcript under claudeDir
// (the default Claude config directory when ""). It is the Deps.SessionTitle
// default.
func DefaultSessionTitle(claudeDir, cwd, sessionID string) string {
	if claudeDir == "" {
		claudeDir = procdetect.GetClaudeDir()
	}
	return SessionTitle(claudeDir, cwd, sessionID)
}

// ErrNotTheProcess reports that the process holding a listed PID is not the
// one the session file describes — its start time disagrees with the file's
// startedAt, or cannot be read — so nothing was signalled. A session file
// left behind by a crash names a PID the system may since have given to an
// unrelated process.
var ErrNotTheProcess = errors.New("the pid now belongs to another process, or its start time cannot be verified")

// startTolerance is how far the process start time read from the system may
// lie from the session file's startedAt and still count as the same process.
// Claude Code records startedAt moments after it starts, and the system's
// clock for process starts has second granularity on Linux (boot time), so
// a few seconds is normal; a reused PID differs by the old process's whole
// lifetime.
const startTolerance = 20 * time.Second

// startMatches is the decision: recorded (the session file's startedAt) and
// actual (the process's start time from the system) within startTolerance of
// each other.
func startMatches(recorded, actual time.Time) bool {
	d := recorded.Sub(actual)
	if d < 0 {
		d = -d
	}
	return d <= startTolerance
}

// DefaultKill stops pid, but only after verifying that the process holding
// it is the one the session file describes: its start time (Linux
// /proc/<pid>/stat, macOS kinfo_proc, Windows GetProcessTimes on the handle
// that is then terminated) must match startedAt (epoch milliseconds) within
// startTolerance; otherwise ErrNotTheProcess and no signal. The stop is
// SIGTERM, or TerminateProcess on Windows, which has no SIGTERM for another
// process (kill_*.go). It is the Deps.Kill default; the stop handler only
// ever calls it for a PID procdetect currently lists.
func DefaultKill(pid int, startedAt int64) error {
	if startedAt <= 0 {
		return fmt.Errorf("%w: the session file records no start time", ErrNotTheProcess)
	}
	return terminateVerified(pid, time.UnixMilli(startedAt))
}

// SnapshotSource is the read model the state document is built from: the
// Facade's own, or a merged one over every provider (providers.
// MultiSnapshotSource) whose rows carry provider and key.
type SnapshotSource interface {
	AccountsSnapshot(fetch map[string]bool) *reporting.AccountsSnapshot
}

// Deps are the server's injectable seams. Facade is required; every other
// field has a production default (see New) or is optional.
type Deps struct {
	Facade Facade
	// Snapshot is what the account rows are read from; nil → Facade. A
	// merged source lists the Codex rows after the Claude ones, and its
	// ActiveNumber stays the Claude active slot (DESIGN A47).
	Snapshot SnapshotSource
	Sessions func() SessionsView
	// SessionTitle names a running session from its transcript under the
	// config directory it was found in; nil → DefaultSessionTitle.
	SessionTitle func(claudeDir, cwd, sessionID string) string
	// Kill stops the Claude Code session with this pid, which the session
	// file says started at startedAt (epoch milliseconds); nil →
	// DefaultKill, which verifies the process's start time first.
	Kill     func(pid int, startedAt int64) error
	Clock    clock.Clock
	Rand     io.Reader     // token entropy; default crypto/rand
	Interval time.Duration // poll tick; default 5 s
	Logger   func(string)  // default discards
	// Ticker builds the poll ticker; default time.NewTicker. Tests inject a
	// channel they drive by hand.
	Ticker func(d time.Duration) (<-chan time.Time, func())

	// Optional surfaces: nil → the section is null in the state document and
	// its routes answer 503.
	Accounts AccountOps
	// Codex drives the Codex rows; nil (no Codex accounts at launch) makes
	// every route given a codex: key answer 503 (DESIGN A47).
	Codex    CodexOps
	Settings SettingsFacade
	Auto     AutoFacade
	// AutoEvents, when non-nil, is fanned out as SSE `auto` events; each one
	// also triggers a state broadcast. A closed channel ends the auto stream.
	AutoEvents <-chan AutoEventView

	// Updates is what the host found to update (A27). nil: state.updates is
	// null and the update routes answer 503. With one, Serve asks it to
	// check at start and every UpdateInterval.
	Updates UpdatesFacade
	// UpdateInterval is how often Serve asks Updates to check again;
	// default six hours.
	UpdateInterval time.Duration
	// UpdateTicker builds the update ticker; default time.NewTicker. Tests
	// inject a channel they drive by hand.
	UpdateTicker func(d time.Duration) (<-chan time.Time, func())
	// UIPrefs keeps the page's view choices for the machine (A27); nil: the
	// page keeps them for its own lifetime only and state.ui is null.
	UIPrefs UIPrefs
	// CurrentLogin is the account Claude Code is signed in with (the email
	// of its OAuth identity); false when there is none. nil leaves
	// state.currentLogin null (A27).
	CurrentLogin func() (email string, ok bool)
	// AuthOverrides lists what makes Claude Code ignore its stored login
	// (A27); nil → DetectAuthOverrides over this process's environment and
	// the live config home's settings.json.
	AuthOverrides func() AuthOverridesView
}

// Server is one dashboard instance. Construct with New, bind with Start, run
// with Serve.
type Server struct {
	d      Deps
	token  string // CSRF token: handed to the page once in the redirect's fragment, required on every /api call
	cookie string // session cookie value: distinct from token, so a cookie leaked
	//                 to another 127.0.0.1 port (cookies are not port-scoped) is useless alone
	cookieBase string // brand.SessionCookie, validated; the port is appended once bound
	// launchMu guards launch and launchUsed together: the check and the
	// redemption are one critical section, so concurrent redeem attempts
	// agree on exactly one winner.
	launchMu   sync.Mutex
	launch     string // one-time bootstrap token carried in the printed URL
	launchUsed bool
	index      *template.Template
	handler    http.Handler
	hub        *hub
	httpSrv    *http.Server

	pingInterval time.Duration

	mutMu sync.Mutex // serialises façade (store) mutations

	// stateSeq numbers the state documents in the order their builds began;
	// the hub drops a document older than the newest one it has published
	// (see broadcast).
	stateSeq atomic.Uint64

	// refresh carries Refresh requests to the serve loop; one slot, so a
	// burst coalesces into one broadcast and no caller ever blocks.
	refresh chan struct{}
	// applying: one update apply runs at a time (handleUpdatesApply).
	applying atomic.Bool

	mu   sync.Mutex
	ln   net.Listener
	port int
	done chan struct{} // closed when Serve winds down; ends SSE streams
}

const (
	csrfHeader    = "X-CSRF-Token"
	defaultPeriod = 5 * time.Second
	defaultPing   = 15 * time.Second
	tokenBytes    = 16 // 128 bits
)

// New builds a Server from d, minting the per-launch token. It errors when
// Facade is nil or the token cannot be read from Rand.
func New(d Deps) (*Server, error) {
	if d.Facade == nil {
		return nil, errors.New("web: Deps.Facade is required")
	}
	if d.Snapshot == nil {
		d.Snapshot = d.Facade
	}
	if d.Clock == nil {
		d.Clock = clock.System{}
	}
	if d.Rand == nil {
		d.Rand = rand.Reader
	}
	if d.Interval <= 0 {
		d.Interval = defaultPeriod
	}
	if d.Logger == nil {
		d.Logger = func(string) {}
	}
	if d.Sessions == nil {
		d.Sessions = DefaultSessions
	}
	if d.Kill == nil {
		d.Kill = DefaultKill
	}
	if d.SessionTitle == nil {
		d.SessionTitle = DefaultSessionTitle
	}
	if d.Ticker == nil {
		d.Ticker = func(dur time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(dur)
			return t.C, t.Stop
		}
	}
	if d.UpdateInterval <= 0 {
		d.UpdateInterval = defaultUpdateInterval
	}
	if d.UpdateTicker == nil {
		d.UpdateTicker = func(dur time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(dur)
			return t.C, t.Stop
		}
	}
	if d.AuthOverrides == nil {
		backupDir := ""
		if d.Facade != nil {
			backupDir = d.Facade.BackupDir()
		}
		d.AuthOverrides = func() AuthOverridesView { return defaultAuthOverrides(backupDir) }
	}
	// Three independent secrets, in this order: the CSRF token (the redirect
	// fragment), the session cookie value and the one-time launch token in
	// the printed URL. See the Server fields for why they must differ.
	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(d.Rand, buf); err != nil {
		return nil, fmt.Errorf("web: minting session token: %w", err)
	}
	cookieBuf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(d.Rand, cookieBuf); err != nil {
		return nil, fmt.Errorf("web: minting session cookie: %w", err)
	}
	launchBuf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(d.Rand, launchBuf); err != nil {
		return nil, fmt.Errorf("web: minting launch token: %w", err)
	}
	tmpl, err := template.ParseFS(staticFS, "static/index.html")
	if err != nil {
		return nil, fmt.Errorf("web: parsing index.html: %w", err)
	}
	s := &Server{
		d:            d,
		cookieBase:   brand.Sanitized().SessionCookie,
		token:        hex.EncodeToString(buf),
		cookie:       hex.EncodeToString(cookieBuf),
		launch:       hex.EncodeToString(launchBuf),
		index:        tmpl,
		hub:          newHub(),
		pingInterval: defaultPing,
		done:         make(chan struct{}),
		refresh:      make(chan struct{}, 1),
	}
	s.handler = s.routes()
	return s, nil
}

// Token returns the per-launch CSRF token (the value the redirect's fragment
// hands the page).
func (s *Server) Token() string { return s.token }

// consumeLaunch redeems the one-time bootstrap token: true exactly once for
// the right value. A replay (browser history, shell scrollback, a `ps`
// snapshot of the launcher's argv) is refused.
func (s *Server) consumeLaunch(t string) bool {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	if s.launchUsed || !tokenEqual(t, s.launch) {
		return false
	}
	s.launchUsed = true
	return true
}

func launchURL(port int, token string) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/?token=" + token
}

// cookieName is the session cookie's name: brand.SessionCookie plus the bound
// port. Cookies are not port-scoped, so with one name a second dashboard on
// another port would overwrite the first one's cookie and log it out.
func (s *Server) cookieName() string {
	if p := s.Port(); p != 0 {
		return s.cookieBase + "_" + strconv.Itoa(p)
	}
	return s.cookieBase
}

// Start binds addr, which must resolve to a loopback interface (an empty addr
// means 127.0.0.1:0), and returns the bootstrap URL http://127.0.0.1:<port>/?token=<t>.
// A non-loopback bind is refused before any byte is served.
func (s *Server) Start(addr string) (string, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("web: bad listen address %q: %w", addr, err)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("web: refusing non-loopback listen address %q", addr)
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return "", fmt.Errorf("web: listener %s is not loopback", ln.Addr())
	}
	s.mu.Lock()
	s.ln = ln
	s.port = tcp.Port
	s.mu.Unlock()
	return s.URL(), nil
}

// URL returns the bootstrap URL after Start ("" before), with the current
// launch token whether or not it was redeemed.
func (s *Server) URL() string {
	port := s.Port()
	if port == 0 {
		return ""
	}
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	return launchURL(port, s.launch)
}

// Port returns the bound port after Start (0 before).
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// Serve runs the HTTP server on the Start listener and the poll loop that
// broadcasts a state event every Interval, until ctx is cancelled. It returns
// nil on a clean shutdown; Start must have succeeded first.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		return errors.New("web: Serve before Start")
	}
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          nil,
	}
	s.httpSrv = srv
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	tick, stop := s.d.Ticker(s.d.Interval)
	defer stop()
	// The update check runs on the server's own clock (A27): once at start
	// and then every UpdateInterval; the facade checks in the background
	// and calls Refresh when it has learnt something.
	var updTick <-chan time.Time
	if s.d.Updates != nil {
		var stopUpd func()
		updTick, stopUpd = s.d.UpdateTicker(s.d.UpdateInterval)
		defer stopUpd()
		s.checkUpdates()
	}
	autoCh := s.d.AutoEvents
	for {
		select {
		case <-s.refresh:
			s.broadcast()
		case <-updTick:
			s.checkUpdates()
		case ev, ok := <-autoCh:
			if !ok {
				autoCh = nil // closed: stop selecting on it, keep serving
				continue
			}
			// Coalesce: every event already queued goes out as its own `auto`
			// frame, followed by ONE state document for the whole batch.
			s.publishAuto(ev)
		drain:
			for {
				select {
				case more, ok := <-autoCh:
					if !ok {
						autoCh = nil
						break drain
					}
					s.publishAuto(more)
				default:
					break drain
				}
			}
			s.broadcast()
		case <-ctx.Done():
			close(s.done)
			shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := srv.Shutdown(shutCtx)
			cancel()
			<-serveErr
			if err != nil && !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return nil
		case err := <-serveErr:
			close(s.done)
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-tick:
			s.broadcast()
		}
	}
}

// broadcast rebuilds the state document and pushes it to every SSE
// subscriber. Subscribers that asked for token status (?tokenStatus=1) get
// the enriched document; it is built only while one of them is connected,
// and the plain document is the same state with the tokenStatus keys
// dropped, so both come from one snapshot.
//
// It runs on the serve loop (ticks, engine-event batches) and on request
// goroutines (after every mutation), so two builds can overlap. The snapshot
// itself is safe to take concurrently: it is a set of file reads, the usage
// cache is read lock-free from atomic replaces and written under its file
// lock behind a reservation step, and the credential store has its own
// mutex. What overlapping builds could do is publish out of order — the
// build that started first, with the older store state, finishing last —
// so every document takes a sequence number before its build starts and the
// hub drops any document older than the newest it has published.
func (s *Server) broadcast() {
	seq := s.stateSeq.Add(1)
	wantTS := s.hub.wantsTokenStatus()
	st := s.buildState(stateOpts{tokenStatus: wantTS})
	var withTS []byte
	if wantTS {
		b, err := json.Marshal(st)
		if err != nil {
			s.d.Logger("web: state: " + err.Error())
			return
		}
		withTS = b
		st = withoutTokenStatus(st)
	}
	body, err := json.Marshal(st)
	if err != nil {
		s.d.Logger("web: state: " + err.Error())
		return
	}
	s.hub.publishState(seq, body, withTS)
}

// publishAuto fans one engine event out as an `auto` SSE event. The caller
// follows a batch of them with one broadcast.
func (s *Server) publishAuto(ev AutoEventView) {
	body, err := json.Marshal(ev)
	if err != nil {
		s.d.Logger("web: auto event: " + err.Error())
		return
	}
	s.hub.publish("auto", body)
}
