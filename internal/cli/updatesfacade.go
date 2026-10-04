// updatesfacade.go — the app dashboard's Updates card (DESIGN A44): the tray
// shell's update state as a web.UpdatesView, and the page's "Check now" and
// apply buttons bound to the same checks and runs as the tray's rows. The
// shell checks on its own clock, so the server does not ask it to
// (web.Deps.UpdatesOwnSchedule); a separate `tycswap web`, and an app without
// a tray icon, keep A27's updates host.
package cli

import (
	"sync/atomic"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/web"
)

// updatesFacade is handed to the dashboard before the tray shell exists (the
// server is built first) and meets the shell once it does.
type updatesFacade struct{ sh atomic.Pointer[appShell] }

var _ web.UpdatesFacade = (*updatesFacade)(nil)

// errNoShell: the shell is not wired yet.
var errNoShell = cerr.Validation("the tray is still starting; try again in a moment")

func (f *updatesFacade) View() web.UpdatesView {
	sh := f.sh.Load()
	if sh == nil {
		return web.UpdatesView{}
	}
	return sh.updatesView()
}

// Check starts every check and returns at once. The count goes up first, so
// the state the 202 carries already says "Checking…".
func (f *updatesFacade) Check() error {
	sh := f.sh.Load()
	if sh == nil {
		return errNoShell
	}
	done := sh.beginCheck()
	go func() {
		defer done()
		_, _ = sh.checkEverything() // the shell keeps each failure for the card
	}()
	return nil
}

// Apply runs one update the page has confirmed, the way the tray's row runs
// it but without the tray's dialog.
func (f *updatesFacade) Apply(target string) (web.UpdateResult, error) {
	sh := f.sh.Load()
	if sh == nil {
		return web.UpdateResult{}, errNoShell
	}
	switch target {
	case "app":
		msg, restart, err := sh.upgradeApp()
		if err != nil {
			return web.UpdateResult{}, err
		}
		if restart != nil {
			// Quitting only cancels the serve context, and the server waits
			// for this request, so the answer still reaches the page.
			defer restart()
		}
		return web.UpdateResult{Message: msg}, nil
	case "claude-code":
		return sh.applyClaudeCode()
	}
	return web.UpdateResult{}, cerr.Validation("unknown update target %q", target)
}

// updatesView is what the dashboard's Updates card shows; Available is the
// answer the icon's badge gives.
func (a *appShell) updatesView() web.UpdatesView {
	available := a.updateWaiting()
	hint := a.upgradeHint()
	a.mu.Lock()
	pending, latest, checkedAt, checking := a.pending, a.appLatest, a.checkedAt, a.checking
	releaseErr := a.releaseErr
	cc, ccKnown := a.claude, a.claudeKnown
	local, localKnown := a.claudeLocal, a.claudeLocalKnown
	a.mu.Unlock()
	v := web.UpdatesView{Available: available, Checking: checking > 0}
	if !checkedAt.IsZero() {
		v.CheckedAt = &checkedAt
	}
	if pending != "" {
		latest = pending
	}
	v.App = &web.AppUpdateView{Current: a.act.Current, Latest: latest, Available: pending != ""}
	if pending != "" {
		v.App.Hint = hint
	}
	if releaseErr != nil {
		v.App.Error = releaseErr.Error()
	}
	switch {
	case ccKnown:
		v.ClaudeCode = claudeCodeView(cc)
		// Available only for an update the app can run: the card's button.
		v.ClaudeCode.Available = v.ClaudeCode.Available && a.act.RunClaudeCode != nil
	case localKnown && local != nil:
		v.ClaudeCode = &web.ClaudeCodeUpdateView{Installed: local.Version, Method: local.Method.Label(), State: "checking"}
	case localKnown:
		v.ClaudeCode = &web.ClaudeCodeUpdateView{State: "checking"}
	}
	return v
}
