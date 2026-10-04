// appcmd.go — `tycswap app`: the dashboard as a menu-bar / tray application
// that starts minimized (DESIGN A35).
//
// The tray owns the process's UI thread, so this command runs the web server
// on a goroutine and hands the main goroutine to tray.Run. Without a tray
// (a darwin build without cgo, a Linux session without a StatusNotifierWatcher,
// --headless) it degrades to `web --no-open`. With --remote the tray drives a
// dashboard in another process instead — the one in a WSL distro — over its
// HTTP API (remote.go, DESIGN A45). With --detach it starts itself in the
// background and gives the terminal back (A40).
package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tyclab/tycswap/internal/appicon"
	"github.com/tyclab/tycswap/internal/autostart"
	"github.com/tyclab/tycswap/internal/brand"
	"github.com/tyclab/tycswap/internal/ccversion"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/update"
	"github.com/tyclab/tycswap/internal/version"
	"github.com/tyclab/tycswap/internal/web"
)

// newTray is the seam tests replace with a fake.
var newTray = tray.New

// The self-upgrade behind the tray's install row and how this build is
// upgraded when the tray cannot do it: seams, so a test can take the
// update→restart path without installing anything.
var (
	upgradeForShell = runUpgradeForShell
	appUpgradeHint  = func() string { return upgradeHint(exePath(), platform.Detect()) }
)

type appOptions struct {
	port      int
	interval  float64
	debug     bool
	open      bool
	headless  bool
	detach    bool
	autostart string // "", "on", "off", "status"
	noUpdates bool
	// remote is the engine's base URL (http://127.0.0.1:<port>, normalised)
	// when this app is the tray for a dashboard in another process (A45);
	// tokenFile is where that dashboard wrote its remote.token.
	remote    string
	tokenFile string
	// rest is argv without --detach: what the detached child is started with.
	rest []string
}

// remoteTokenFileEnv names the token file when --token-file is not given,
// so a start-at-login entry can carry it in the environment instead.
func remoteTokenFileEnv() string { return brand.Sanitized().EnvPrefix + "_REMOTE_TOKEN_FILE" }

// parseAppArgs parses `app` flags; a non-nil error carries the usage message.
func parseAppArgs(argv []string) (appOptions, error) {
	o := appOptions{interval: 5}
	var portSet, intervalSet bool
	for i := 0; i < len(argv); i++ {
		tok, start := argv[i], i
		val := func(name string) (string, bool) {
			if strings.HasPrefix(tok, name+"=") {
				return tok[len(name)+1:], true
			}
			if tok == name {
				if i+1 >= len(argv) {
					return "", false
				}
				i++
				return argv[i], true
			}
			return "", false
		}
		switch {
		case tok == "--open":
			o.open = true
		case tok == "--headless":
			o.headless = true
		case tok == "--detach":
			o.detach = true
		case tok == "--no-update-check":
			o.noUpdates = true
		case tok == "--debug":
			o.debug = true
		case tok == "--port" || strings.HasPrefix(tok, "--port="):
			v, ok := val("--port")
			if !ok {
				return o, errors.New("argument --port: expected one argument")
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 65535 {
				return o, errors.New("argument --port: invalid int value: '" + v + "' (0-65535)")
			}
			o.port, portSet = n, true
		case tok == "--interval" || strings.HasPrefix(tok, "--interval="):
			v, ok := val("--interval")
			if !ok {
				return o, errors.New("argument --interval: expected one argument")
			}
			// At least a second, as for `tycswap web`: every tick rebuilds the
			// whole state.
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 1 || f > 3600 {
				return o, errors.New("argument --interval: invalid number of seconds: '" + v + "' (1-3600)")
			}
			o.interval, intervalSet = f, true
		case tok == "--autostart" || strings.HasPrefix(tok, "--autostart="):
			v, ok := val("--autostart")
			if !ok || (v != "on" && v != "off" && v != "status") {
				return o, errors.New("argument --autostart: expected on, off or status")
			}
			o.autostart = v
		case tok == "--remote" || strings.HasPrefix(tok, "--remote="):
			v, ok := val("--remote")
			if !ok {
				return o, errors.New("argument --remote: expected one argument")
			}
			o.remote = v
		case tok == "--token-file" || strings.HasPrefix(tok, "--token-file="):
			v, ok := val("--token-file")
			if !ok {
				return o, errors.New("argument --token-file: expected one argument")
			}
			o.tokenFile = v
		case tok == "-h" || tok == "--help":
			return o, errHelp
		default:
			return o, errors.New("unrecognized arguments: " + tok)
		}
		// The flag and the value val read with it, for the detached child.
		if tok != "--detach" {
			o.rest = append(o.rest, argv[start:i+1]...)
		}
	}
	if o.detach && o.autostart != "" {
		return o, errors.New("argument --detach: not allowed with --autostart (it registers the entry and exits)")
	}
	if o.remote == "" {
		if o.tokenFile != "" {
			return o, errors.New("argument --token-file: only meaningful with --remote")
		}
		return o, nil
	}
	// The dashboard runs in the other process: every flag that shapes a
	// dashboard here contradicts --remote, and the error says which.
	for _, c := range []struct {
		set  bool
		name string
	}{{o.headless, "--headless"}, {portSet, "--port"}, {intervalSet, "--interval"}} {
		if c.set {
			return o, errors.New("argument --remote: not allowed with " + c.name + " (the dashboard runs in the distro, not here)")
		}
	}
	base, err := remoteBaseURL(o.remote)
	if err != nil {
		return o, errors.New("argument --remote: " + err.Error())
	}
	o.remote = base
	if o.tokenFile == "" {
		o.tokenFile = os.Getenv(remoteTokenFileEnv())
	}
	if o.tokenFile == "" {
		return o, errors.New("argument --remote: needs --token-file PATH or " + remoteTokenFileEnv() + " in the environment (the remote.token the app in the distro writes)")
	}
	return o, nil
}

var errHelp = errors.New("help")

// appCommand handles `tycswap app [flags]`.
func appCommand(prog string, argv []string, s ioStreams) int {
	aprog := prog + " app"
	o, err := parseAppArgs(argv)
	if errors.Is(err, errHelp) {
		printer.ForceUTF8Output()
		return renderAppHelp(prog, s.out)
	}
	if err != nil {
		return subError(aprog, s.err, err.Error())
	}
	if o.autostart != "" {
		return appAutostart(o.autostart, autostartConfig(o), s)
	}
	// In the background, with the same flags; confirmed through the lock the
	// child takes (A40).
	if o.detach {
		lockPath := appLockPath()
		if o.remote != "" {
			lockPath = remoteLockPath()
		}
		return startDetached(prog, append([]string{"app"}, o.rest...), lockPath, s)
	}
	// A console window Windows opened only for this process (start at login,
	// a double-click) would sit in the taskbar and end the app when closed;
	// give it back and write to the log instead (A41). Not for --headless,
	// which has no tray icon to quit it from.
	if !o.headless {
		s = releaseOwnConsole(s)
	}
	// The tray for an engine in another process: no dashboard, no store, no
	// engine here (A45). Everything below is the local app.
	if o.remote != "" {
		return runRemoteApp(o, s)
	}

	// One tray icon per machine (A38). The lock lives for the whole process;
	// the OS releases it if we are killed.
	lock, got, lerr := acquireAppLock()
	switch {
	case lerr != nil:
		// A filesystem problem here must not stop the app from starting.
		fmt.Fprintln(s.err, "could not take the single-instance lock: "+lerr.Error())
	case !got:
		errorTo(s.err, appName()+" app is already running on this machine (its icon is in the menu bar / tray). Quit it first.")
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

	// The shell and the tray refer to each other: the tray's callbacks land in
	// the shell, the shell paints through the tray. Build the tray first with
	// no shell, hand the shell's methods to it, then plug the shell in. The
	// tray comes before the dashboard here because it decides who checks for
	// updates: the shell when there is a tray, the A27 host otherwise.
	var sh *appShell
	var t tray.Tray
	if !o.headless {
		t, err = newTray(plainTrayIcon(), tray.Options{
			Tooltip: appName(),
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
		if err != nil && !errors.Is(err, tray.ErrUnsupported) {
			errorTo(s.err, "Error: "+err.Error())
			return 1
		}
	}
	// The dashboard's Updates card reads the tray shell (A44), which does not
	// exist yet: the facade is handed over now and meets the shell below.
	// Without a tray the card keeps A27's host, as `tycswap web` has it.
	var updates *updatesFacade
	// --no-update-check holds back the A27 host's own schedule too.
	opts := dashboardOptions{noUpdateSchedule: o.noUpdates}
	if t != nil {
		updates = &updatesFacade{}
		opts.updates = updates
	}
	// The remote token (A45): minted per start, handed to the server and
	// written where a tray on the other side of a WSL boundary reads it.
	opts.remoteToken, err = mintRemoteToken(rand.Reader)
	if err != nil {
		errorTo(s.err, "Error: "+err.Error())
		return 1
	}
	d, code := newDashboard(o.interval, o.debug, s, opts)
	if code != 0 {
		return code
	}
	// The app — and only the app — remembers whether auto-switch was on (A43).
	d.auto.statePath = appStatePath(d.sw.BackupDir())
	// The token file lives exactly as long as the process that honours the
	// token. A write failure is said and not fatal, like the lock: the
	// local app works without a remote.
	tokenPath := remoteTokenPath(d.sw.BackupDir())
	if err := writeRemoteToken(tokenPath, opts.remoteToken); err != nil {
		fmt.Fprintln(s.err, "remote token: "+err.Error())
		tokenPath = ""
	}
	removeToken := func() {
		if tokenPath != "" {
			removeRemoteToken(tokenPath)
			tokenPath = ""
		}
	}
	defer removeToken()
	srv := d.srv
	url, err := srv.Start(net.JoinHostPort("127.0.0.1", strconv.Itoa(o.port)))
	if err != nil {
		errorTo(s.err, "Error: "+err.Error())
		return 1
	}
	fmt.Fprintf(s.err, "Dashboard: %s\n", url)

	// Ctrl-C and SIGTERM alike: the distro side runs as a user service
	// (A45), and `systemctl --user stop` sends SIGTERM, which would
	// otherwise end the process without the deferred token removal.
	ctx, cancel := notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if t == nil {
		if o.headless {
			fmt.Fprintln(s.err, "Running without a tray (--headless). Press Ctrl-C to stop.")
		} else {
			fmt.Fprintln(s.err, "No system tray is available here; running the dashboard server only. Press Ctrl-C to stop.")
		}
		// Where a tray on the Windows side of a WSL boundary finds its way in (A45).
		if tokenPath != "" {
			fmt.Fprintf(s.err, "Remote token: %s (for %s app --remote)\n", tokenPath, appName())
		}
		if o.open {
			_ = openBrowser(url)
		}
		resumeAutoSwitch(d, s)
		serveErr := srv.Serve(ctx)
		stopEngineKeepingChoice(d.auto)
		if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
			errorTo(s.err, "Error: "+serveErr.Error())
			return 1
		}
		return 0
	}

	// Where this app's output goes: the terminal it runs in, or the log a
	// background or start-at-login app writes to.
	outputPlace := appLogPath()
	if f, ok := s.err.(*os.File); ok && isTTY(f) {
		outputPlace = "the terminal running " + appName() + " app"
	}
	openDashboard := func() error {
		u, err := srv.LaunchURL()
		if err != nil {
			return err
		}
		if err := openBrowser(u); err != nil {
			// Leave a way in (A41): the address, single-use like the one
			// printed at start, goes to the app's output, and the message
			// leads with where to find it — Windows cuts a notification's
			// text at 255 characters.
			fmt.Fprintln(s.err, "Dashboard: "+u)
			return fmt.Errorf("open the Dashboard address from %s in your browser (%v)", outputPlace, err)
		}
		return nil
	}
	var quitOnce sync.Once
	quit := func() { quitOnce.Do(func() { cancel(); t.Quit() }) }
	var restart atomic.Bool
	var restartTag atomic.Pointer[string]
	// Claude Code itself (A42): which one this machine runs, and the newest
	// one its installer offers.
	ccEnv := ccversion.DefaultEnv()
	checkClaudeCode := func() {
		if sh != nil {
			sh.checkClaudeCode(true)
		}
	}
	sh = newAppShell(t, shellActions{
		OpenDashboard: openDashboard,
		SwitchTo: func(id string) error {
			_, err := d.sw.SwitchTo(id, false)
			return err
		},
		AddCurrent: func() (web.AddLoginResult, error) {
			if email, _, ok := d.sw.GetCurrentAccount(); !ok || email == "" {
				return web.AddLoginResult{}, errNoLogin
			}
			return srv.AddCurrentLogin()
		},
		AutoRunning:  func() bool { return d.auto.View().Running },
		AutoStart:    func() error { return d.auto.Start(false) },
		AutoStop:     func() error { return d.auto.Stop() },
		Upgrade:      func() (string, error) { return upgradeForShell() },
		UpgradeHint:  func() string { return appUpgradeHint() },
		Autostart:    func() (bool, error) { return autostart.Enabled(autostart.Config{}) },
		SetAutostart: func(on bool) error { return setAutostart(autostart.Config{}, on) },
		SetModelLimits: func(on bool) error {
			sf := settingsFacade{root: d.sw.BackupDir()}
			var err error
			if on {
				_, err = sf.Set("autoswitch.model", "all")
			} else {
				_, err = sf.Unset("autoswitch.model")
			}
			if err != nil {
				return err
			}
			// Retarget the running engine too: leaving it on the models it
			// started with is what made this switch look broken (A39).
			model := ""
			if on {
				model = "all"
			}
			if aerr := d.auto.ApplyModels(model); aerr != nil && d.auto.View().Running {
				return aerr
			}
			return nil
		},
		SetThreshold:  d.auto.ApplyThreshold,
		ApproveAPIKey: d.sw.ApproveAPIKeySwitch,
		RestartNotice: restartNotice,
		RunClaudeCode: func(c ccversion.Command, in *ccversion.Installed) (string, error) {
			rctx, rcancel := context.WithTimeout(ctx, updateApplyTimeout) // brew may update itself first
			defer rcancel()
			return ccversion.Run(rctx, ccEnv, c, in)
		},
		CheckClaudeCode: func() ccversion.Status {
			cctx, ccancel := context.WithTimeout(ctx, updateCheckTimeout)
			defer ccancel()
			return ccversion.Check(cctx, ccEnv)
		},
		UpdatesChanged: srv.Refresh,
		Quit:           quit,
		Current:        version.Version,
		LatestVersion:  latestReleaseTag(d.sw.BackupDir()),
		Ask:            askVia(t),
		Restart:        func(tag string) { restartTag.Store(&tag); restart.Store(true); quit() },
		SkipTag:        os.Getenv(justInstalledEnv()),
	})
	updates.sh.Store(sh)
	srv.OnState(sh.update)
	srv.OnAuto(sh.autoEvent)
	// Resume auto-switch before the first paint, so the icon shows it at once
	// and its first events reach the tray (A43).
	resumeAutoSwitch(d, s)
	sh.update(srv.Snapshot())

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()
	go func() {
		<-ctx.Done()
		t.Quit()
	}()
	if o.open {
		_ = openDashboard()
	}
	// The brand row names the installed Claude Code at once; the full check
	// (network) follows later, and only with updates on.
	go func() {
		fctx, fcancel := context.WithTimeout(ctx, 30*time.Second)
		defer fcancel()
		in, _ := ccversion.Find(fctx, ccEnv)
		sh.setClaudeCodeInstalled(in)
	}()
	if !o.noUpdates {
		// Test hooks (never documented for users): a shorter first check and
		// auto-approval let the update→restart path run without a click.
		applyTestHooks(sh)
		go updateCheckLoop(ctx, sh.offerUpdate)
		go checkLoop(ctx, claudeCodeCheckFirst, updateCheckEvery, checkClaudeCode)
	}
	fmt.Fprintln(s.err, "Running in the menu bar / tray. Quit from its menu or press Ctrl-C.")
	runErr := t.Run()
	cancel()
	if err := <-serveErr; err != nil && !errors.Is(err, context.Canceled) {
		errorTo(s.err, "Error: "+err.Error())
		return 1
	}
	stopEngineKeepingChoice(d.auto)
	if runErr != nil {
		errorTo(s.err, "Error: "+runErr.Error())
		return 1
	}
	if restart.Load() {
		// Hand the single-instance lock over: the successor is a detached
		// process that takes it within startGrace, and holding it here until
		// the deferred release would make that a coin flip (A38).
		if lock != nil {
			_ = lock.Release()
			lock = nil
		}
		// The token file too, now rather than in the deferred remove: the
		// successor writes its own token to the same path, and a remove
		// that ran after it would take the new token away (A45).
		removeToken()
		// The binary on disk is the new release; come back as it, same
		// arguments, same environment (A36). Only returns when that failed.
		tag := ""
		if p := restartTag.Load(); p != nil {
			tag = *p
		}
		if err := restartSelf(tag); err != nil {
			errorTo(s.err, "Could not restart automatically ("+err.Error()+"); start "+appName()+" app again.")
			return 1
		}
		return 0
	}
	return 0
}

// resumeAutoSwitch turns auto-switch back on when the user left it on (A43),
// and says so on the app's output.
func resumeAutoSwitch(d *dashboard, s ioStreams) {
	started, err := d.auto.resume()
	switch {
	case err != nil:
		fmt.Fprintln(s.err, "auto-switch was on when the app last ran, but could not resume: "+err.Error())
	case started:
		fmt.Fprintln(s.err, "Auto-switch resumed: it was on when the app last ran.")
	}
}

// stopEngineKeepingChoice ends a running engine as the app exits, the way
// `tycswap web` does, without recording "off": quitting the app is not
// turning auto-switch off, and the next start resumes it (A43).
func stopEngineKeepingChoice(a *autoFacade) {
	a.mu.Lock()
	a.statePath = ""
	a.mu.Unlock()
	if a.View().Running {
		_ = a.Stop()
	}
}

// justInstalledEnv tells the restarted process which release it was just
// updated to, so it never offers that tag again (loop guard, A36).
func justInstalledEnv() string { return brand.Sanitized().EnvPrefix + "_JUST_INSTALLED" }

// applyTestHooks reads the two test hooks: <PREFIX>_TEST_UPDATE_FIRST, a
// duration for the first release check, and <PREFIX>_TEST_AUTO_APPROVE=1,
// which answers every dialog with yes.
func applyTestHooks(sh *appShell) {
	p := brand.Sanitized().EnvPrefix
	if v := os.Getenv(p + "_TEST_UPDATE_FIRST"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			updateCheckFirst = d
		}
	}
	if os.Getenv(p+"_TEST_AUTO_APPROVE") == "1" {
		sh.act.Ask = func(string, string, string, string) (bool, error) { return true, nil }
	}
}

// Update-check cadence (A36): soon after start, then four times a day.
var (
	updateCheckFirst = 90 * time.Second
	updateCheckEvery = 6 * time.Hour
)

// claudeCodeCheckFirst is when the app first looks at Claude Code (A42): soon,
// but after the start-up work.
var claudeCodeCheckFirst = 30 * time.Second

// updateCheckLoop calls check on the cadence until ctx ends.
func updateCheckLoop(ctx context.Context, check func()) {
	checkLoop(ctx, updateCheckFirst, updateCheckEvery, check)
}

// checkLoop calls check after first, then every every, until ctx ends.
func checkLoop(ctx context.Context, first, every time.Duration, check func()) {
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			check()
			timer.Reset(every)
		}
	}
}

// latestReleaseTag reads the newest release's tag from the release endpoint
// `tycswap upgrade` uses; a tag read refreshes the passive notice's cache
// under backupDir (A27).
func latestReleaseTag(backupDir string) func() (string, error) {
	checker := update.Checker{CacheDir: filepath.Join(backupDir, "cache")}
	return func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return checker.Latest(ctx)
	}
}

// askVia returns the tray's dialog when it has one.
func askVia(t tray.Tray) func(title, body, ok, cancel string) (bool, error) {
	asker, has := t.(tray.Asker)
	if !has {
		return nil
	}
	return asker.Ask
}

// appIcon is the tray's images, all pre-rendered (appicon's embedded
// files): the macOS menu bar and the menu's brand row show the coloured mark
// at 128 px (tray.Icon.LargePNG), Windows the 32 px PNG, Linux the 22 px
// pixmap.
func appIcon() tray.Icon {
	return tray.Icon{
		PNG:      appicon.PNG(32, false),
		LargePNG: appicon.PNG(128, false),
		ARGB32:   appicon.ARGB32(22),
		Size:     22,
	}
}

// appIconBadge is appIcon with the badge (A44) for n things that wait: on
// macOS the count beside the mark (the bar takes a wider image), on Windows
// and Linux the dot, which a 16–22 px square has no room to number.
func appIconBadge(n int) tray.Icon {
	return tray.Icon{
		PNG:      appicon.PNGBadge(32),
		LargePNG: appicon.PNGCounted(128, n),
		ARGB32:   appicon.ARGB32Badge(22),
		Size:     22,
	}
}

// runUpgradeForShell runs the same self-upgrade as `tycswap upgrade` (`go
// install` for a go-installed binary, A24) and says what it did in one line.
// "Updated …" means the running binary was replaced, so the app can restart
// into it; a `go install` that put the new build into another Go bin directory
// says where to start it instead.
func runUpgradeForShell() (string, error) {
	exe := exePath()
	before := modTime(exe)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), updateApplyTimeout)
	defer cancel()
	run := func(_ context.Context, name string, args []string, o, e io.Writer) (int, error) {
		return update.RunCommand(ctx, name, args, o, e)
	}
	up := update.Upgrader{Stdout: &out, Stderr: &out, Run: run}
	code := up.SelfUpgrade(exe, platform.Detect())
	text := strings.TrimSpace(out.String())
	if code != 0 {
		return "", errors.New(lastOutputLine(text, "the upgrade did not complete"))
	}
	if after := modTime(exe); !after.IsZero() && !after.Equal(before) {
		return "Updated " + appName() + ".", nil
	}
	return "Installed the new " + appName() + ", but not over " + exe + ". " + restartByHand(), nil
}

// modTime is path's modification time, zero when it cannot be read.
func modTime(path string) time.Time {
	if path == "" {
		return time.Time{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// autostartConfig is the start-at-login entry for these options: plain
// `app`, or `app --remote URL --token-file PATH` so the entry carries the
// remote arguments (A45).
func autostartConfig(o appOptions) autostart.Config {
	if o.remote == "" {
		return autostart.Config{}
	}
	return autostart.Config{Label: remoteAutostartLabel(), Args: remoteAutostartArgs(o.remote, o.tokenFile)}
}

// setAutostart registers or removes the entry.
func setAutostart(cfg autostart.Config, on bool) error {
	if on {
		return autostart.Enable(cfg)
	}
	return autostart.Disable(cfg)
}

func appAutostart(mode string, cfg autostart.Config, s ioStreams) int {
	var err error
	switch mode {
	case "on":
		err = setAutostart(cfg, true)
	case "off":
		err = setAutostart(cfg, false)
	}
	if err != nil {
		errorTo(s.err, "Error: "+err.Error())
		return 1
	}
	on, err := autostart.Enabled(cfg)
	if err != nil {
		errorTo(s.err, "Error: "+err.Error())
		return 1
	}
	if on {
		fmt.Fprintln(s.out, "Start at login: on")
	} else {
		fmt.Fprintln(s.out, "Start at login: off")
	}
	return 0
}

func renderAppHelp(prog string, out io.Writer) int {
	fmt.Fprintf(out, `usage: %[1]s app [--open] [--headless] [--port N] [--interval SECONDS] [--no-update-check] [--debug] [--detach]
       %[1]s app --remote URL [--token-file PATH] [--open] [--no-update-check] [--debug] [--detach]
       %[1]s app [--remote URL --token-file PATH] --autostart on|off|status

Run the dashboard as a menu-bar / tray application. It starts minimized: the
icon shows the active account and its fullest usage window, the menu switches
accounts, toggles auto-switch, moves the 7d threshold while auto-switch runs
and checks for updates, and notifications tell you about switches,
quarantines and an account nearing its limit. A badge on the icon means an
update of %[1]s or Claude Code waits; the menu's Updates section installs it.

This command runs in the foreground; --detach starts it in the background and
gives the terminal back. Plain "%[1]s" in a terminal does the same as
"%[1]s app --detach".

With --remote the tray drives a dashboard that runs in another process — the
app inside a WSL distro, which has no tray of its own — over its HTTP API.
Nothing is stored or switched on this side; the token comes from the
remote.token the app in the distro writes next to its data.

options:
  --open              also open the dashboard window at start
  --headless          no tray; serve the dashboard only (like "web --no-open")
  --detach            start in the background, output to the app log, and return
  --no-update-check   do not look for new releases of %[1]s or Claude Code (default:
                      90 s / 30 s after start, then every 6 h; asks before installing either)
  --port N            fixed dashboard port (default 0 = ephemeral)
  --interval S        live-state poll interval in seconds (1-3600, default 5)
  --debug             log requests and errors to stderr (with --remote: every call's status and
                      every end of the event stream)
  --remote URL        be the tray for the dashboard at URL (http://127.0.0.1:<port> or
                      http://localhost:<port> only); not with --headless, --port, --interval
  --token-file PATH   the remote.token of that dashboard (default $%[2]s)
  --autostart MODE    register (on) / remove (off) / show (status) start at login, then exit;
                      with --remote the entry carries the remote arguments
`, prog, remoteTokenFileEnv())
	return 0
}
