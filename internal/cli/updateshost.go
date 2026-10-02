// updateshost.go — the dashboard's Updates card behind `tycswap web` (DESIGN
// A27): the release check `tycswap upgrade` already has and the Claude Code
// check, as a web.UpdatesFacade. The server's loop asks it to check at start
// and every six hours; the page's Check now asks again; each check runs in
// the background and pushes a fresh state through Server.Refresh when it has
// learnt something. Apply runs `tycswap upgrade`'s own path (go install for a
// go-installed binary; guidance for a checkout build) or Claude Code's own
// installer, and hands back what they printed.
package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tyclab/tycswap/internal/brand"
	"github.com/tyclab/tycswap/internal/ccversion"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/update"
	"github.com/tyclab/tycswap/internal/version"
	"github.com/tyclab/tycswap/internal/web"
)

// updateCheckTimeout bounds one background check (both lookups together).
const updateCheckTimeout = 2 * time.Minute

// updateApplyTimeout bounds one apply: a Claude Code update can take a
// while; go install, longer on a cold module cache.
const updateApplyTimeout = 25 * time.Minute

// updatesHost is the web.UpdatesFacade `tycswap web` wires.
type updatesHost struct {
	// Seams; tests replace them. latest is the release lookup, check the
	// Claude Code one, upgrade runs tycswap's self-upgrade into the writers
	// and returns its exit code, runClaude runs one Claude Code command.
	latest    func(ctx context.Context) (string, error)
	check     func(ctx context.Context) ccversion.Status
	upgrade   func(stdout, stderr *bytes.Buffer) int
	runClaude func(ctx context.Context, c ccversion.Command, in *ccversion.Installed) (string, error)
	hint      func() string // how this build upgrades when the dashboard cannot do it, "" when it can
	current   string        // the running version (v-prefixed)
	now       func() time.Time
	// onChange is Server.Refresh once the server exists; nil before.
	onChange func()

	mu         sync.Mutex
	checking   int
	checkedAt  time.Time
	latestTag  string // the newest release known, "" for none
	releaseErr error  // why the last release check failed
	appDone    string // the release the dashboard installed, "" for none
	cc         ccversion.Status
	ccKnown    bool
}

var _ web.UpdatesFacade = (*updatesHost)(nil)

// newUpdatesHost builds the host over the backup root's cache directory.
func newUpdatesHost(backupDir string) *updatesHost {
	checker := update.Checker{CacheDir: filepath.Join(backupDir, "cache")}
	env := ccversion.DefaultEnv()
	plat := platform.Detect()
	h := &updatesHost{
		latest: checker.Latest,
		check:  func(ctx context.Context) ccversion.Status { return ccversion.Check(ctx, env) },
		upgrade: func(stdout, stderr *bytes.Buffer) int {
			return update.Upgrader{Stdout: stdout, Stderr: stderr}.SelfUpgrade(exePath(), plat)
		},
		runClaude: func(ctx context.Context, c ccversion.Command, in *ccversion.Installed) (string, error) {
			return ccversion.Run(ctx, env, c, in)
		},
		hint:    func() string { return upgradeHint(exePath(), plat) },
		current: version.Version,
		now:     time.Now,
	}
	return h
}

// checkoutCommand is what a checkout build is upgraded with, as `tycswap
// upgrade` says it.
var checkoutCommand = strings.TrimPrefix(update.CheckoutHint, "built from a checkout: ")

// upgradeHint is the command a build the dashboard cannot upgrade is told:
// a checkout build (the checkout upgrades it), a binary outside a Go bin
// directory (manual), Windows (the running .exe is locked). "" means
// SelfUpgrade would run `go install` here.
func upgradeHint(exe string, plat platform.Platform) string {
	if update.DetectBuildSource() == update.SourceCheckout {
		return checkoutCommand
	}
	return shapeHint(installShape(exe), plat)
}

// installShape reads the layout from the environment and home directory
// SelfUpgrade reads (Upgrader's defaults), so the card offers the button
// exactly when the apply would run `go install`.
func installShape(exe string) update.InstallShape {
	home, _ := os.UserHomeDir()
	return update.DetectInstallShape(exe, os.Getenv, home)
}

// shapeHint is upgradeHint past the checkout test: "" for a go-installed
// binary off Windows, else the line to type.
func shapeHint(shape update.InstallShape, plat platform.Platform) string {
	if shape == update.ShapeGoInstall && plat != platform.Windows {
		return ""
	}
	return "go install " + update.ModulePath + "@latest"
}

func (h *updatesHost) changed() {
	if h.onChange != nil {
		h.onChange()
	}
}

// Check starts both lookups in the background and returns at once; the
// count goes up first, so the state the caller broadcasts already says
// "Checking…".
func (h *updatesHost) Check() error {
	h.mu.Lock()
	h.checking++
	h.mu.Unlock()
	go h.runCheck()
	return nil
}

// runCheck is one check. A failed lookup forgets nothing: being offline says
// nothing about whether an update still waits, so the release found before
// stays, the Claude Code version learnt before stays for the same installed
// Claude Code, and the error comes along for the card to name.
func (h *updatesHost) runCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	var wg sync.WaitGroup
	var tag string
	var rerr error
	var st ccversion.Status
	wg.Add(2)
	go func() { defer wg.Done(); tag, rerr = h.latest(ctx) }()
	go func() { defer wg.Done(); st = h.check(ctx) }()
	wg.Wait()

	h.mu.Lock()
	reached := false
	if rerr == nil {
		h.latestTag, h.releaseErr, reached = tag, nil, true
	} else {
		h.releaseErr = rerr
	}
	if st.Latest != "" {
		reached = true
	} else if st.Err != nil && h.ccKnown && h.cc.Latest != "" && sameClaudeCode(h.cc, st) {
		st.Latest = h.cc.Latest
	}
	h.cc, h.ccKnown = st, true
	if reached {
		h.checkedAt = h.now()
	}
	h.checking--
	h.mu.Unlock()
	h.changed()
}

// sameClaudeCode reports whether two checks found the same Claude Code at
// the same version: then the newest version one of them learnt holds for
// the other.
func sameClaudeCode(a, b ccversion.Status) bool {
	x, y := a.Installed, b.Installed
	return x != nil && y != nil && x.Version != "" && x.Path == y.Path && x.Version == y.Version && x.Method == y.Method
}

// View is what the card shows.
func (h *updatesHost) View() web.UpdatesView {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := web.UpdatesView{Checking: h.checking > 0}
	if !h.checkedAt.IsZero() {
		t := h.checkedAt
		v.CheckedAt = &t
	}
	app := &web.AppUpdateView{Current: h.current, Latest: h.latestTag}
	if h.latestTag != "" && newerRelease(h.latestTag, h.current) {
		if h.appDone == h.latestTag {
			app.Installed = true
		} else {
			app.Available = true
			app.Hint = h.hint()
		}
	}
	if h.releaseErr != nil {
		app.Error = h.releaseErr.Error()
	}
	v.App = app
	if h.ccKnown {
		v.ClaudeCode = claudeCodeView(h.cc)
	} else {
		v.ClaudeCode = &web.ClaudeCodeUpdateView{State: "checking"}
	}
	v.Available = app.Available || v.ClaudeCode.Available
	return v
}

// newerRelease compares two v-prefixed versions (update's comparator rules:
// a pre-release build is told about its release).
func newerRelease(latest, current string) bool {
	return ccversion.Newer(strings.TrimPrefix(latest, "v"), strings.TrimPrefix(current, "v"))
}

// claudeCodeView is a Claude Code check as the card shows it. Available only
// for an update the dashboard can run.
func claudeCodeView(st ccversion.Status) *web.ClaudeCodeUpdateView {
	v := &web.ClaudeCodeUpdateView{Latest: st.Latest}
	if in := st.Installed; in != nil {
		v.Installed, v.Method = in.Version, in.Method.Label()
	}
	switch st.State() {
	case ccversion.Checking:
		v.State = "checking"
	case ccversion.Missing:
		v.State, v.Command = "missing", ccversion.InstallHint(platformGOOS())
		v.Detail = "Claude Code is not installed on this machine."
	case ccversion.UpdateAvailable:
		v.State, v.Command, v.Available = "update", ccversion.UpgradeCommand(st.Installed).String(), true
		if st.Err != nil {
			v.Error = st.Err.Error() // the version is the one learnt before
		}
	case ccversion.UpToDate:
		v.State = "latest"
		if st.Err != nil {
			v.Error = st.Err.Error()
		}
	default:
		v.State = "unknown"
		err := st.Err
		if err == nil {
			err = errors.New("the newest version is not known")
			if st.Installed != nil && st.Installed.Version == "" {
				err = errors.New("the installed version is unknown")
			}
		}
		v.Error = err.Error()
		v.Detail = "Could not check for a newer Claude Code: " + err.Error()
	}
	return v
}

// platformGOOS is the install hint's platform.
func platformGOOS() string {
	if platform.IsWindows() {
		return "windows"
	}
	return "linux"
}

// Apply runs one update the page has confirmed. The server runs one apply
// at a time (409 otherwise).
func (h *updatesHost) Apply(target string) (web.UpdateResult, error) {
	switch target {
	case "app":
		return h.applyApp()
	case "claude-code":
		return h.applyClaudeCode()
	}
	return web.UpdateResult{}, cerr.Validation("unknown update target %q", target)
}

// applyApp is `tycswap upgrade` for the dashboard: go install for a
// go-installed binary; a checkout build or an unknown layout prints the
// guidance `tycswap upgrade` prints, which is the error's output.
func (h *updatesHost) applyApp() (web.UpdateResult, error) {
	h.mu.Lock()
	latest, current := h.latestTag, h.current
	h.mu.Unlock()
	if latest == "" || !newerRelease(latest, current) {
		return web.UpdateResult{}, cerr.Validation("no newer %s release is known; check for updates first", brandName())
	}
	var stdout, stderr bytes.Buffer
	code := h.upgrade(&stdout, &stderr)
	out := strings.TrimSpace(strings.TrimSpace(stdout.String()) + "\n" + strings.TrimSpace(stderr.String()))
	if code != 0 {
		return web.UpdateResult{Output: out}, cerr.Validation("%s", lastOutputLine(out, "the upgrade did not complete"))
	}
	h.mu.Lock()
	h.appDone = latest
	h.mu.Unlock()
	h.changed()
	return web.UpdateResult{
		Message: brandName() + " " + strings.TrimPrefix(latest, "v") + " is installed. This server still runs " + strings.TrimPrefix(current, "v") + ": stop it and start " + brandName() + " web again to use the new version.",
		Output:  out,
	}, nil
}

// applyClaudeCode runs Claude Code's own installer for the update the last
// check found, then looks at Claude Code again.
func (h *updatesHost) applyClaudeCode() (web.UpdateResult, error) {
	h.mu.Lock()
	st, known := h.cc, h.ccKnown
	h.mu.Unlock()
	if !known || st.State() == ccversion.Checking {
		return web.UpdateResult{}, cerr.Validation("Claude Code has not been checked yet; try again in a moment")
	}
	if st.State() != ccversion.UpdateAvailable {
		return web.UpdateResult{}, cerr.Validation("Claude Code has no update to install")
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateApplyTimeout)
	defer cancel()
	out, err := h.runClaude(ctx, ccversion.UpgradeCommand(st.Installed), st.Installed)
	out = strings.TrimSpace(out)
	// Look again either way, so the card shows the version now installed.
	h.recheckClaudeCode()
	if err != nil {
		return web.UpdateResult{Output: out}, cerr.Validation("%s", lastOutputLine(out, err.Error()))
	}
	return web.UpdateResult{Message: "Claude Code is updated. Restart running Claude Code sessions to use the new version.", Output: out}, nil
}

// recheckClaudeCode runs the Claude Code check once more, now.
func (h *updatesHost) recheckClaudeCode() {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	st := h.check(ctx)
	h.mu.Lock()
	if st.Latest == "" && st.Err != nil && h.cc.Latest != "" && sameClaudeCode(h.cc, st) {
		st.Latest = h.cc.Latest
	}
	h.cc, h.ccKnown = st, true
	if st.Latest != "" {
		h.checkedAt = h.now()
	}
	h.mu.Unlock()
	h.changed()
}

// brandName is the program name the card's sentences use.
func brandName() string { return brand.Sanitized().Name }

// lastOutputLine is the most telling line of a command's output, else fallback.
func lastOutputLine(out, fallback string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return fallback
}
