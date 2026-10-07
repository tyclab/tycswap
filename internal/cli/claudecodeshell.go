// claudecodeshell.go — the tray's Claude Code row: which Claude Code this
// machine runs, and the newest one its own installer offers (DESIGN A42).
// What counts as newest, how an update runs and what a missing Claude Code
// gets are A27's rules, which the dashboard's Updates card already follows.
package cli

import (
	"errors"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/ccversion"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/web"
)

// storeClaudeCode takes a fresh Claude Code check and repaints the menu. A
// newly available version is announced once per version; a missing Claude
// Code only shows in the menu, so a machine that has its reasons is not told
// again at every start. announce false only marks a new version as
// announced, for a check whose caller reports the outcome itself (A44).
//
// A check that could not learn the newest version keeps the one the check
// before it learnt, as long as the same Claude Code is installed, and brings
// its error along (claudeCodeCheckErr): being offline says nothing about
// whether the update is still waiting, and forgetting it would take the row
// and the badge away until the next check, six hours later.
func (a *appShell) storeClaudeCode(st ccversion.Status, announce bool) {
	a.mu.Lock()
	reached := st.Latest != ""
	if !reached && st.Err != nil && a.claudeKnown && a.claude.Latest != "" && sameClaudeCode(a.claude, st) {
		st.Latest = a.claude.Latest
	}
	a.claude = st
	a.claudeKnown = true
	if reached {
		a.checkedAt = time.Now()
	}
	fresh := st.State() == ccversion.UpdateAvailable && a.claudeAnnounced != st.Latest
	if fresh {
		a.claudeAnnounced = st.Latest
	}
	a.mu.Unlock()
	a.updatesChanged()
	if fresh && announce {
		next := "Choose \"Update Claude Code\" in the " + brandName() + " menu."
		if _, panel := a.tray.(tray.PanelTray); panel {
			next = "Choose Update Claude in the tray panel."
		}
		a.notify("Claude Code "+st.Latest+" is available", "You have "+st.Installed.Version+". "+next)
	}
}

// claudeCodeCheckErr is why the last Claude Code check could not look for a
// newer version, nil when it could or had nothing to look for (Claude Code
// missing). The tray's "Check for updates…" and the dashboard name it in the
// same words (A44). With an update or "latest" kept from the check before
// (storeClaudeCode), it is that check's error.
func claudeCodeCheckErr(st ccversion.Status) error {
	switch st.State() {
	case ccversion.CannotCheck:
		switch {
		case st.Err != nil:
			return st.Err
		case st.Installed != nil && st.Installed.Version == "":
			return errors.New("the installed version is unknown")
		}
		return errors.New("the newest version is not known")
	case ccversion.UpdateAvailable, ccversion.UpToDate:
		return st.Err
	}
	return nil
}

// checkClaudeCode runs a full check and stores it (A42); the dashboard shows
// "Checking…" meanwhile (A44).
func (a *appShell) checkClaudeCode(announce bool) {
	if a.act.CheckClaudeCode == nil {
		return
	}
	done := a.beginCheck()
	defer done()
	a.storeClaudeCode(a.act.CheckClaudeCode(), announce)
}

// claudeCodeUpdate reports whether the menu offers a Claude Code update: one
// is available and the app can run it. That row belongs to the Updates
// section and badges the icon (A44).
func (a *appShell) claudeCodeUpdate() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.claudeCodeUpdateLocked()
}

func (a *appShell) claudeCodeUpdateLocked() bool {
	return a.claudeKnown && a.act.RunClaudeCode != nil && a.claude.State() == ccversion.UpdateAvailable
}

// setClaudeCodeInstalled takes the quick look at start — which Claude Code,
// no network — so the brand row names the version at once. A full check that
// already arrived is newer and is kept.
func (a *appShell) setClaudeCodeInstalled(in *ccversion.Installed) {
	a.mu.Lock()
	if a.claudeKnown {
		a.mu.Unlock()
		return
	}
	a.claudeLocal, a.claudeLocalKnown = in, true
	a.mu.Unlock()
	a.updatesChanged() // the dashboard names the installed version too
}

// claudeCodeLine is the brand row's second line: the installed Claude Code,
// and after a full check whether it is the latest; "" before the app looked.
func (a *appShell) claudeCodeLine() string {
	a.mu.Lock()
	st, known := a.claude, a.claudeKnown
	in, local := a.claudeLocal, a.claudeLocalKnown
	a.mu.Unlock()
	if known {
		in = st.Installed
	} else if !local {
		return ""
	}
	switch {
	case in == nil:
		return "Claude Code not installed"
	case in.Version == "":
		return "Claude Code (version unknown)"
	}
	line := "Claude Code " + in.Version
	if known {
		switch st.State() {
		case ccversion.UpToDate:
			line += " · latest"
		case ccversion.UpdateAvailable:
			line += " · " + st.Latest + " available"
		}
	}
	return line
}

// claudeCodeItem is the menu row; false while there is nothing to show (no
// check yet, or the app has no way to run the commands).
func (a *appShell) claudeCodeItem() (tray.Item, bool) {
	a.mu.Lock()
	st, known := a.claude, a.claudeKnown
	a.mu.Unlock()
	if !known || a.act.RunClaudeCode == nil {
		return tray.Item{}, false
	}
	in := st.Installed
	switch st.State() {
	case ccversion.Missing:
		// A27: a missing Claude Code is said, with the line to type, never
		// installed from here.
		return tray.Item{ID: "claude-code", Title: "Claude Code is not installed — how to install…"}, true
	case ccversion.UpdateAvailable:
		return tray.Item{ID: "claude-code", Kind: tray.KindUpdate, Title: "Update Claude Code " + in.Version + " → " + st.Latest + "…",
			Sub: "with " + in.Method.Label() + " · running sessions keep the old one"}, true
	case ccversion.UpToDate:
		return tray.Item{ID: "claude-code", Title: "Claude Code " + in.Version + " — latest", Disabled: true}, true
	case ccversion.CannotCheck:
		v := in.Version
		if v == "" {
			v = "(version unknown)"
		}
		return tray.Item{ID: "claude-code", Title: "Claude Code " + v + " — could not check for updates", Disabled: true}, true
	}
	return tray.Item{}, false
}

// claudeCodeClick updates Claude Code with its own installer, after asking;
// a missing one is told how to install (A27).
func (a *appShell) claudeCodeClick() {
	a.mu.Lock()
	st := a.claude
	a.mu.Unlock()
	switch st.State() {
	case ccversion.Missing:
		a.notify("Install Claude Code", "Claude Code is not installed on this machine. Install it with: "+ccversion.InstallHint(platformGOOS()))
		return
	case ccversion.UpdateAvailable:
	default:
		return
	}
	cmd := ccversion.UpgradeCommand(st.Installed)
	if a.act.Ask != nil {
		ok, err := a.act.Ask("Update Claude Code?",
			"Update Claude Code from "+st.Installed.Version+" to "+st.Latest+" with:\n\n"+cmd.String()+
				"\n\nRunning Claude Code sessions keep the old version until you restart them.", "Update", "Cancel")
		if err == nil && !ok {
			return
		}
		// No dialog on this platform: the click was the request.
	}
	a.notify("Claude Code", "Updating Claude Code — this can take a minute.")
	if _, err := a.runClaudeCode(st); err != nil {
		a.notify("Claude Code update failed", err.Error())
	} else {
		a.notify("Claude Code update done", "Restart running Claude Code sessions to use the new version.")
	}
}

// runClaudeCode runs the update the check st found, for the tray and for the
// dashboard (A44), one update at a time, then looks at Claude Code again. A
// zero exit status alone is not success (A27): the version installed now must
// be the one offered, or newer. The error is the command's most telling line;
// the result still carries all of its output, for the dashboard's Output
// disclosure.
func (a *appShell) runClaudeCode(st ccversion.Status) (web.UpdateResult, error) {
	if !a.applyMu.TryLock() {
		return web.UpdateResult{}, errUpdateRunning
	}
	out, err := a.act.RunClaudeCode(ccversion.UpgradeCommand(st.Installed), st.Installed)
	a.applyMu.Unlock()
	out = strings.TrimSpace(out)
	var after ccversion.Status
	if a.act.CheckClaudeCode != nil {
		done := a.beginCheck()
		after = a.act.CheckClaudeCode()
		a.storeClaudeCode(after, false)
		done()
	}
	if err != nil {
		return web.UpdateResult{Output: out}, errors.New(lastOutputLine(out, err.Error()))
	}
	if a.act.CheckClaudeCode != nil {
		if after.Installed == nil || after.Installed.Version == "" {
			return web.UpdateResult{Output: out}, errors.New("the installer finished, but the installed Claude Code version could not be verified")
		}
		if after.Installed.Version != st.Latest && !ccversion.Newer(after.Installed.Version, st.Latest) {
			return web.UpdateResult{Output: out}, errors.New("the installer finished, but Claude Code is still at " + after.Installed.Version + " (expected " + st.Latest + " or newer)")
		}
	}
	return web.UpdateResult{Message: "Claude Code is updated. Restart running Claude Code sessions to use the new version.", Output: out}, nil
}

// applyClaudeCode is the dashboard's "Update Claude Code": the tray's run
// without its dialog, since the page has asked (A44).
func (a *appShell) applyClaudeCode() (web.UpdateResult, error) {
	a.mu.Lock()
	st, known := a.claude, a.claudeKnown
	a.mu.Unlock()
	if !known || a.act.RunClaudeCode == nil || st.State() == ccversion.Checking {
		return web.UpdateResult{}, cerr.Validation("Claude Code has not been checked yet; try again in a moment")
	}
	if st.State() != ccversion.UpdateAvailable {
		return web.UpdateResult{}, cerr.Validation("Claude Code has no update to install")
	}
	return a.runClaudeCode(st)
}
