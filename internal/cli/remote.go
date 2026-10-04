// remote.go — `tycswap app --remote URL`: the tray for a dashboard that runs
// in another process (DESIGN A45).
//
// On a WSL machine Claude Code and tycswap live inside the distro, where
// there is no StatusNotifierWatcher, so `app` runs headless and the machine
// has no tray. The Windows binary has a complete tray and reaches the distro's
// dashboard on 127.0.0.1:<port>. The tray shell (appshell.go) only ever
// renders web.State and acts through shellActions, so remote mode is a
// second set of those hooks, over the dashboard's HTTP API, plus the SSE
// stream in place of the in-process observers.
//
// The token. The app in the distro mints a 128-bit token per start, hands it
// to its web server (web.Deps.RemoteToken) and writes it to
// <backup_root>/remote.token, 0600, removed on exit. Per start on purpose: a
// token dies with the process that honours it, nothing has to be rotated or
// revoked, and a remote that gets 401 simply re-reads the file — the new
// process has written a new one by then. Windows reads the file over
// \\wsl.localhost\<distro>\…, where the distro's permissions apply. The URL
// is loopback only and plain http, because the token travels in clear text.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/autostart"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/update"
	"github.com/tyclab/tycswap/internal/version"
	"github.com/tyclab/tycswap/internal/web"
)

// ── the server side: token file and URL rules ─────────────────────────────

// remoteTokenBytes is the token's entropy: 128 bits, like the web server's
// own three tokens.
const remoteTokenBytes = 16

// mintRemoteToken draws a fresh token from r as 32 hex characters.
func mintRemoteToken(r io.Reader) (string, error) {
	buf := make([]byte, remoteTokenBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("minting the remote token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// remoteTokenPath is <backup_root>/remote.token.
func remoteTokenPath(backupDir string) string { return filepath.Join(backupDir, "remote.token") }

// writeRemoteToken puts the token on disk for a remote tray to read: a temp
// file in the same directory, 0600, renamed into place, so a reader never
// sees a half-written token. The parent (0700) exists already.
func writeRemoteToken(path, token string) error {
	return atomicfile.Write(path, []byte(token+"\n"), atomicfile.Opts{FileMode: 0o600, DirMode: 0o700})
}

// removeRemoteToken takes the file away again. A file that is already gone,
// or one that cannot be removed, changes nothing: the token dies with this
// process either way.
func removeRemoteToken(path string) { _ = os.Remove(path) }

// remoteBaseURL checks and normalises --remote: http on 127.0.0.1 or
// localhost with an explicit port and nothing else, returned as
// "http://<host>:<port>". Anything beyond loopback is refused — the token
// travels in clear text, which is acceptable on one machine and nowhere else.
func remoteBaseURL(raw string) (string, error) {
	const want = "expected http://127.0.0.1:<port> or http://localhost:<port>"
	u, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%s, not %q", want, raw)
	}
	if u.Scheme != "http" {
		return "", fmt.Errorf("%s, not %q (plain http on loopback only: the token travels in clear text)", want, raw)
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" {
		return "", fmt.Errorf("%s, not %q (loopback only: the token travels in clear text)", want, raw)
	}
	port := u.Port()
	n, perr := strconv.Atoi(port)
	if port == "" || perr != nil || n <= 0 || n > 65535 {
		return "", fmt.Errorf("%s with a port, not %q", want, raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("%s and nothing after the port, not %q", want, raw)
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// remoteAutostartLabel names the remote tray's start-at-login entry, apart
// from the local app's, so a machine that runs both keeps both.
func remoteAutostartLabel() string { return autostart.Label + ".remote" }

// remoteAutostartArgs are the arguments a start-at-login entry carries so
// the remote tray comes back as one: `app --remote URL --token-file PATH`.
func remoteAutostartArgs(base, tokenFile string) []string {
	return []string{"app", "--remote", base, "--token-file", tokenFile}
}

// ── the single-instance rule on this side ─────────────────────────────────

// remoteLockPath is the remote tray's own lock, beside app.lock (A38): a
// remote tray and a local app do not exclude each other, two remote trays
// do. On Windows — the side a remote tray runs on — the backup root is
// %USERPROFILE%\.tycswap, created with the lock if it is not there yet.
func remoteLockPath() string { return filepath.Join(paths.GetBackupRoot(), "remote.lock") }

// acquireRemoteLock takes the remote tray's lock, with acquireAppLock's
// contract: held is false when another remote tray owns it, err is a real
// filesystem problem.
func acquireRemoteLock() (lock *filelock.FileLock, held bool, err error) {
	return acquireLockAt(remoteLockPath())
}

// ── the client ────────────────────────────────────────────────────────────

// Reconnect cadence for the event stream: a second, doubling to half a
// minute. Package variables so tests run the loop in milliseconds.
var (
	remoteRetryMin = time.Second
	remoteRetryMax = 30 * time.Second
	// remoteStaleAfter is how long the stream may stay silent before it
	// counts as dead; the server pings every 15 s.
	remoteStaleAfter = 60 * time.Second
	// remoteCallTimeout bounds every API call.
	remoteCallTimeout = 30 * time.Second
	// remoteMaxFrame bounds one SSE line and one frame's accumulated data:
	// a state document is kilobytes, and a server that never sends a
	// newline must not grow the tray's memory until the watchdog gives up
	// on it — which it never does while bytes trickle in.
	remoteMaxFrame = 4 << 20
)

// errRemoteRefused is a 401 on the stream after the token was re-read: the
// file does not hold the token the engine honours. Told apart from a
// connection error so the tray says which file to check instead of asking
// whether the distro's app runs — it does.
var errRemoteRefused = errors.New("the engine refused the token")

// remoteClient drives one dashboard over its HTTP API with the remote token.
type remoteClient struct {
	base      string // http://127.0.0.1:<port>
	tokenFile string
	calls     *http.Client // bounded: every call but the stream
	stream    *http.Client // unbounded body: the SSE stream

	// debug, when set, logs every answer and every stream end (--debug).
	debug func(format string, args ...any)

	mu      sync.Mutex
	token   string
	last    web.State
	hasLast bool
	// down and why are what the shell's Offline hook reports; refused says
	// why is the token, not the connection. outage says an outage has been
	// announced and not yet taken back, so each outage is notified once on
	// the way out and once on the way back; everUp gates the first
	// announcement, because an engine that is not up yet when the tray
	// starts at login is no outage — only a link that worked and broke is.
	down    bool
	why     string
	refused bool
	outage  bool
	everUp  bool
}

// newRemoteClient builds the client; the token is read with loadToken.
//
// One transport for both clients: loopback, never through a proxy, and no
// redirect is ever followed. The default client would follow up to ten and
// keep the Authorization header for any destination on the same host — port
// changes included — so a listener on the configured port (a wrong one, or
// one another local user bound first) could point the tray at another
// loopback port with the token attached, or off the box with the request
// body. A 3xx is therefore an answer like any other and reported as such.
func newRemoteClient(base, tokenFile string) *remoteClient {
	transport := &http.Transport{
		Proxy:                 nil,
		ResponseHeaderTimeout: remoteCallTimeout,
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &remoteClient{
		base:      base,
		tokenFile: tokenFile,
		calls:     &http.Client{Transport: transport, CheckRedirect: noRedirect, Timeout: remoteCallTimeout},
		stream:    &http.Client{Transport: transport, CheckRedirect: noRedirect},
		down:      true,
		why:       "engine unreachable at " + base,
	}
}

// logf writes one --debug line, or nothing.
func (c *remoteClient) logf(format string, args ...any) {
	if c.debug != nil {
		c.debug(format, args...)
	}
}

// loadToken (re)reads the token file. Called at start and on every 401: the
// engine was restarted and wrote a new token.
func (c *remoteClient) loadToken() error {
	raw, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return fmt.Errorf("reading the remote token: %w", err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return errors.New("reading the remote token: " + c.tokenFile + " is empty")
	}
	c.mu.Lock()
	c.token = tok
	c.mu.Unlock()
	return nil
}

func (c *remoteClient) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// Offline is the shell's hook: whether the engine is out of reach, and why.
func (c *remoteClient) Offline() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.down, c.why
}

// AutoRunning answers from the last state the client saw, as the shell's
// AutoRunning hook; false before any state arrived.
func (c *remoteClient) AutoRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hasLast && c.last.Auto != nil && c.last.Auto.Running
}

func (c *remoteClient) remember(st web.State) {
	c.mu.Lock()
	c.last, c.hasLast = st, true
	c.mu.Unlock()
}

// send performs one authenticated request. A 401 means the token on file
// is not the one the engine holds any more: re-read it and try once more.
func (c *remoteClient) send(ctx context.Context, client *http.Client, method, path string, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.currentToken())
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			c.logf("%s %s: %v", method, path, err)
			return nil, err
		}
		c.logf("%s %s: %s", method, path, resp.Status)
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err := c.loadToken(); err != nil {
			return nil, fmt.Errorf("the engine refused the token, and it could not be re-read: %w", err)
		}
	}
}

// call sends a JSON request and decodes a 2xx answer into out (when non-nil).
// Errors carry the server's {"error": …} message, or the HTTP status when
// the body has none.
func (c *remoteClient) call(method, path string, body any, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteCallTimeout)
	defer cancel()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	resp, err := c.send(ctx, c.calls, method, path, raw)
	if err != nil {
		return fmt.Errorf("engine at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("the engine refused the token in " + c.tokenFile + "; is it the remote.token of the app running in the distro?")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errors.New(remoteErrorMessage(resp.Status, data))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("engine answered %s %s with something other than JSON: %w", method, path, err)
		}
	}
	return nil
}

// remoteErrorMessage is the server's error message, or the status line when
// the body carries none.
func remoteErrorMessage(status string, body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && strings.TrimSpace(e.Error) != "" {
		return e.Error
	}
	return "engine answered " + status
}

// State fetches the state document and remembers it.
func (c *remoteClient) State() (web.State, error) {
	var st web.State
	if err := c.call(http.MethodGet, "/api/state", nil, &st); err != nil {
		return web.State{}, err
	}
	c.remember(st)
	return st, nil
}

// mutate sends one state-changing call and, when it succeeded, refreshes the
// cached state at once. The shell repaints right after a click and reads
// AutoRunning from that cache; the server's broadcast of the new state
// reaches the stream a moment later, which is a moment too late for that
// paint — and for a second quick click, which would act on the old state.
// A refresh that fails leaves the cache to the stream: the mutation itself
// went through.
func (c *remoteClient) mutate(path string, body any) error {
	if err := c.call(http.MethodPost, path, body, nil); err != nil {
		return err
	}
	_, _ = c.State()
	return nil
}

// Switch is POST /api/switch/{key}: the shell names a Claude account by its
// slot, the dashboard by its key ("claude:<slot>", A26).
func (c *remoteClient) Switch(num string) error {
	return c.mutate("/api/switch/"+neturl.PathEscape("claude:"+num), nil)
}

// AutoStart starts the engine for real (never a dry run from the tray).
func (c *remoteClient) AutoStart() error {
	return c.mutate("/api/auto/start", map[string]any{"dryRun": false})
}

// AutoStop is POST /api/auto/stop.
func (c *remoteClient) AutoStop() error {
	return c.mutate("/api/auto/stop", nil)
}

// SetModel mirrors the local SetModelLimits (appcmd.go, A39): write the
// autoswitch.model setting, then retarget a running engine. The retarget's
// error only matters while the engine runs; otherwise the server says
// "not running", which is no failure of the switch.
func (c *remoteClient) SetModel(on bool) error {
	model := ""
	var err error
	if on {
		model = "all"
		err = c.call(http.MethodPost, "/api/settings/autoswitch.model", map[string]any{"value": "all"}, nil)
	} else {
		err = c.call(http.MethodPost, "/api/settings/autoswitch.model/unset", nil, nil)
	}
	if err != nil {
		return err
	}
	if aerr := c.call(http.MethodPost, "/api/auto/model", map[string]any{"model": model}, nil); aerr != nil && c.AutoRunning() {
		return aerr
	}
	return nil
}

// Launch is POST /api/launch: the one-time dashboard URL, checked and
// rebuilt before it is returned — see checkLaunchURL.
func (c *remoteClient) Launch() (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.call(http.MethodPost, "/api/launch", nil, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("engine returned no dashboard URL")
	}
	return checkLaunchURL(c.base, out.URL)
}

// checkLaunchURL admits only what web.Server.LaunchURL hands out — http,
// loopback, the engine's own port, the root path and a 32-hex launch token
// — and returns the URL rebuilt from those parts. The answer goes to the
// platform's browser opener (A41), which on Windows is ShellExecute: it launches
// whatever a URL's scheme is registered for, so a listener on the port that
// is not the engine must not get to choose what "Open dashboard" opens.
func checkLaunchURL(base, raw string) (string, error) {
	const bad = "engine returned an unexpected dashboard URL"
	b, err := neturl.Parse(base)
	if err != nil {
		return "", errors.New(bad)
	}
	u, err := neturl.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return "", errors.New(bad)
	}
	host := u.Hostname()
	if (host != "127.0.0.1" && host != "localhost") || u.Port() != b.Port() || (u.Path != "" && u.Path != "/") {
		return "", errors.New(bad)
	}
	q := u.Query()
	tok := q.Get("token")
	if len(q) != 1 || len(q["token"]) != 1 || len(tok) != 2*remoteTokenBytes || strings.Trim(tok, "0123456789abcdef") != "" {
		return "", errors.New(bad)
	}
	return "http://" + net.JoinHostPort(host, u.Port()) + "/?token=" + tok, nil
}

// run follows the event stream until ctx ends, delivering `state` frames to
// onState and `auto` frames to onAuto, and reconnecting with backoff. onLink
// is called once with false when an outage begins and once with true when
// the stream is back; the shell turns those into a repaint and a notification.
func (c *remoteClient) run(ctx context.Context, onState func(web.State), onAuto func(web.AutoEventView), onLink func(up bool)) {
	delay := remoteRetryMin
	for {
		// The error itself is not shown unless --debug asks for it: the
		// tray says where the engine should be and what to check, which is
		// all a user can act on.
		got, err := c.follow(ctx, onState, onAuto, onLink)
		if ctx.Err() != nil {
			return
		}
		c.logf("GET /api/events ended: %v", err)
		if got {
			delay = remoteRetryMin // a stream that worked earns a quick retry
		}
		c.markDown(onLink, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > remoteRetryMax {
			delay = remoteRetryMax
		}
	}
}

// markDown records why the engine is out of reach and announces an outage
// once — and only after the link has worked at some point: the Run key
// starts the tray before the distro's user service is up, and that is not
// an outage, so the first paint says unreachable without a notification.
func (c *remoteClient) markDown(onLink func(up bool), err error) {
	c.mu.Lock()
	c.down = true
	c.refused = errors.Is(err, errRemoteRefused)
	if c.refused {
		c.why = "engine at " + c.base + " refused the token in " + c.tokenFile
	} else {
		c.why = "engine unreachable at " + c.base
	}
	announce := c.everUp && !c.outage
	if c.everUp {
		c.outage = true
	}
	c.mu.Unlock()
	if announce && onLink != nil {
		onLink(false)
	}
}

// markUp records the link as working and takes an announced outage back.
func (c *remoteClient) markUp(onLink func(up bool)) {
	c.mu.Lock()
	c.down = false
	c.refused = false
	c.everUp = true
	back := c.outage
	c.outage = false
	c.mu.Unlock()
	if back && onLink != nil {
		onLink(true)
	}
}

// tokenRefused reports whether the engine answers but not to this token:
// the notification then names the file instead of asking whether the
// distro's app runs.
func (c *remoteClient) tokenRefused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refused
}

// follow opens GET /api/events once and parses frames until the stream ends.
// got reports whether at least one frame arrived (the connection worked).
func (c *remoteClient) follow(ctx context.Context, onState func(web.State), onAuto func(web.AutoEventView), onLink func(up bool)) (got bool, err error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resp, err := c.send(sctx, c.stream, http.MethodGet, "/api/events", nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return false, errRemoteRefused
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return false, errors.New(remoteErrorMessage(resp.Status, data))
	}
	// A silent stream is a dead one: the server pings every 15 s.
	watchdog := time.AfterFunc(remoteStaleAfter, cancel)
	defer watchdog.Stop()

	// Line by line, each line and each frame's data bounded by
	// remoteMaxFrame; a longer one ends the stream with an error, which
	// lands in run's reconnect.
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), remoteMaxFrame)
	errTooLarge := errors.New("stream frame too large")
	var name string
	var data []byte
	for sc.Scan() {
		watchdog.Reset(remoteStaleAfter)
		line := sc.Text()
		switch {
		case line == "":
			if name != "" {
				got = true
				c.dispatch(name, data, onState, onAuto, onLink)
			}
			name, data = "", nil
		case strings.HasPrefix(line, ":"):
			// a ping comment: the watchdog was reset above, nothing to deliver
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			if len(data)+len(line) > remoteMaxFrame {
				return got, errTooLarge
			}
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")...)
		}
	}
	err = sc.Err()
	switch {
	case errors.Is(err, bufio.ErrTooLong):
		return got, errTooLarge
	case err == nil:
		return got, io.ErrUnexpectedEOF // the server closed the stream
	case sctx.Err() != nil && ctx.Err() == nil:
		return got, errors.New("stream went silent")
	}
	return got, err
}

// dispatch hands one frame to the shell. A state frame marks the link up
// BEFORE the shell paints it, so the paint already sees the engine as
// reachable.
func (c *remoteClient) dispatch(name string, data []byte, onState func(web.State), onAuto func(web.AutoEventView), onLink func(up bool)) {
	switch name {
	case "state":
		var st web.State
		if json.Unmarshal(data, &st) != nil {
			return
		}
		c.remember(st)
		c.markUp(onLink)
		if onState != nil {
			onState(st)
		}
	case "auto":
		var ev web.AutoEventView
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		if onAuto != nil {
			onAuto(ev)
		}
	}
}

// ── the command ───────────────────────────────────────────────────────────

// runRemoteApp is `app --remote`: the tray only, driven over the API. It
// takes no store lock, builds no dashboard, resumes no engine, fetches no
// settings and looks at no Claude Code — all of that is the distro's.
func runRemoteApp(o appOptions, s ioStreams) int {
	lock, got, lerr := acquireRemoteLock()
	switch {
	case lerr != nil:
		fmt.Fprintln(s.err, "could not take the single-instance lock: "+lerr.Error())
	case !got:
		errorTo(s.err, brandName()+" app --remote is already running on this machine (its icon is in the menu bar / tray). Quit it first.")
		return 1
	default:
		// Released early on a restart hand-over (lock = nil below), so the
		// deferred release must not touch a nil lock.
		defer func() {
			if lock != nil {
				_ = lock.Release()
			}
		}()
	}

	var sh *appShell
	t, err := newTray(plainTrayIcon(), tray.Options{
		Tooltip: brandName(),
		OnClick: func(id string) {
			if sh != nil {
				sh.click(id)
			}
		},
		OnActivate: func() {
			if sh != nil {
				sh.click("open")
			}
		},
	})
	if errors.Is(err, tray.ErrUnsupported) || (err == nil && t == nil) {
		// A remote without a tray is pointless: the dashboard it would show
		// is already reachable in the browser from the distro's URL.
		errorTo(s.err, "remote mode needs a tray; run "+brandName()+" app in the distro instead")
		return 1
	}
	if err != nil {
		errorTo(s.err, "Error: "+err.Error())
		return 1
	}
	ctx, cancel := notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	rc := newRemoteClient(o.remote, o.tokenFile)
	if o.debug {
		rc.debug = func(format string, args ...any) { fmt.Fprintf(s.err, "remote: "+format+"\n", args...) }
	}
	if err := rc.loadToken(); err != nil {
		// Not fatal: the distro may simply not be up yet. The tray shows the
		// engine as unreachable and the next 401 re-reads the file.
		fmt.Fprintln(s.err, "remote token: "+err.Error()+" — the engine stays unreachable until the file can be read")
	}
	fmt.Fprintf(s.err, "Remote engine: %s (token from %s)\n", o.remote, o.tokenFile)

	outputPlace := appLogPath()
	if f, ok := s.err.(*os.File); ok && isTTY(f) {
		outputPlace = "the terminal running " + brandName() + " app"
	}
	openDashboard := func() error {
		u, err := rc.Launch()
		if err != nil {
			return err
		}
		if err := openBrowser(u); err != nil {
			fmt.Fprintln(s.err, "Dashboard: "+u)
			return fmt.Errorf("open the Dashboard address from %s in your browser (%v)", outputPlace, err)
		}
		return nil
	}
	var quitOnce sync.Once
	quit := func() { quitOnce.Do(func() { cancel(); t.Quit() }) }
	var restart atomic.Bool
	var restartTag atomic.Pointer[string]
	cfg := autostartConfig(o)
	update.RemoveStaleBinary(exePath()) // a Windows upgrade's leftover from last time
	// How this build is upgraded, worked out once: it probes the binary's
	// directory with a file, and the menu, the card and every check ask.
	buildHint := appUpgradeHint()
	sh = newAppShell(t, shellActions{
		OpenDashboard:  openDashboard,
		SwitchTo:       rc.Switch,
		AutoRunning:    rc.AutoRunning,
		AutoStart:      rc.AutoStart,
		AutoStop:       rc.AutoStop,
		Upgrade:        func() (string, error) { return upgradeForShell() },
		UpgradeHint:    func() string { return buildHint },
		Autostart:      func() (bool, error) { return autostart.Enabled(cfg) },
		SetAutostart:   func(on bool) error { return setAutostart(cfg, on) },
		SetModelLimits: rc.SetModel,
		// ApproveAPIKey / RestartNotice stay nil: the tray refuses an API-key
		// switch and points at the dashboard (A33). RunClaudeCode and
		// CheckClaudeCode stay nil: Claude Code lives in the distro, not here
		// (A42). AddCurrent stays nil: the login to add is the distro's.
		Offline:       rc.Offline,
		Quit:          quit,
		Current:       version.Version,
		LatestVersion: latestReleaseTag(paths.GetBackupRoot()),
		Ask:           askVia(t),
		Restart:       func(tag string) { restartTag.Store(&tag); restart.Store(true); quit() },
		SkipTag:       os.Getenv(justInstalledEnv()),
	})
	// First paint before anything arrived: the engine shows as unreachable
	// until the first state frame, which follows within the moment when the
	// distro is up.
	sh.update(web.State{})
	go rc.run(ctx, sh.update, sh.autoEvent, func(up bool) {
		switch {
		case up:
			sh.notify("WSL engine reachable again", brandName()+" at "+o.remote+" answers; the tray is live.")
		case rc.tokenRefused():
			sh.notify("WSL engine refused the token", brandName()+" at "+o.remote+" does not accept the token in "+o.tokenFile+"; is it the remote.token of the app running in the distro?")
		default:
			sh.notify("WSL engine unreachable", "WSL engine unreachable at "+o.remote+"; is "+brandName()+" app running in the distro?")
		}
		sh.repaint()
	})
	go func() {
		<-ctx.Done()
		t.Quit()
	}()
	if o.open {
		_ = openDashboard()
	}
	if !o.noUpdates {
		// The same test hooks as the local app (appcmd.go).
		applyTestHooks(sh)
		go updateCheckLoop(ctx, sh.offerUpdate)
	}
	fmt.Fprintln(s.err, "Running in the menu bar / tray. Quit from its menu or press Ctrl-C.")
	runErr := t.Run()
	cancel()
	if runErr != nil {
		errorTo(s.err, "Error: "+runErr.Error())
		return 1
	}
	if restart.Load() {
		// Hand the single-instance lock over to the successor (A38), which
		// comes back with the same arguments — the remote flags included.
		if lock != nil {
			_ = lock.Release()
			lock = nil
		}
		tag := ""
		if p := restartTag.Load(); p != nil {
			tag = *p
		}
		if err := restartSelf(tag); err != nil {
			errorTo(s.err, "Could not restart automatically ("+err.Error()+"); start "+brandName()+" app again.")
			return 1
		}
	}
	return 0
}
