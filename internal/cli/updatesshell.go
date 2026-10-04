// updatesshell.go — "an update is waiting" (DESIGN A44): the one answer the
// tray icon's badge, the tooltip, the menu's Updates section and the
// dashboard's Updates card share, and "Check for updates…", which looks at
// everything that answer is made of: tycswap's own release and Claude Code.
package cli

import (
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/tyclab/tycswap/internal/ccversion"
	"github.com/tyclab/tycswap/internal/tray"
)

// updateWaiting reports whether the menu offers an update: a newer tycswap
// release, or a Claude Code update the app can run.
func (a *appShell) updateWaiting() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending != "" || a.claudeCodeUpdateLocked()
}

// The icons the badge switches between, each made once: the plain mark and
// the badged one per count (1–9, then "9+").
var (
	plainTrayIcon = sync.OnceValue(appIcon)
	badgeIcons    sync.Map // count → tray.Icon
)

// maxBadge is the count from which the badge says "9+".
const maxBadge = 10

// badgeTrayIcon is the icon with the badge for n things that wait.
func badgeTrayIcon(n int) tray.Icon {
	n = max(1, min(n, maxBadge))
	if ic, ok := badgeIcons.Load(n); ok {
		return ic.(tray.Icon)
	}
	ic, _ := badgeIcons.LoadOrStore(n, appIconBadge(n))
	return ic.(tray.Icon)
}

// attentionCount is the number on the badge (A44): one per update the user
// would run — tycswap, Claude Code. The dashboard's header badge counts the
// updates the same way (updateCount in app.js). 0 while nothing waits.
func (a *appShell) attentionCount() int {
	if !a.updateWaiting() {
		return 0
	}
	return max(a.updateCount(), 1) // something waits even when nothing is itemised
}

// syncIcon puts the badge on the icon with its count, or takes it off, when
// that changed; the icon is not touched otherwise. The decision and the call
// happen under iconMu, so of two repaints that race the later one wins.
func (a *appShell) syncIcon() {
	a.iconMu.Lock()
	defer a.iconMu.Unlock()
	n := min(a.attentionCount(), maxBadge)
	if n == a.badge {
		return
	}
	a.badge = n
	if n > 0 {
		a.tray.SetIcon(badgeTrayIcon(n))
	} else {
		a.tray.SetIcon(plainTrayIcon())
	}
}

// updatesSection is the menu's Updates section right under "Open
// dashboard": what can be updated, nothing while nothing waits.
func (a *appShell) updatesSection() []tray.Item {
	var rows []tray.Item
	a.mu.Lock()
	pending := a.pending
	a.mu.Unlock()
	if pending != "" {
		shown, have := strings.TrimPrefix(pending, "v"), strings.TrimPrefix(a.act.Current, "v")
		it := tray.Item{ID: "install-update", Kind: tray.KindUpdate,
			Title: "Install " + appName() + " " + shown + "…",
			Sub:   "you have " + have + " · restarts by itself"}
		if hint := a.upgradeHint(); hint != "" {
			// The tray cannot install this build (A36): the row says how.
			it.Title, it.Sub = appName()+" "+shown+" is available…", "you have "+have+" · update with "+hint
		}
		rows = append(rows, it)
	}
	if a.claudeCodeUpdate() {
		if it, ok := a.claudeCodeItem(); ok {
			rows = append(rows, it)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return append([]tray.Item{tray.Separator(), tray.Header(updatesHeader(a.updateCount()))}, rows...)
}

// updateCount is the number of updates, as the dashboard's header badge
// counts them.
func (a *appShell) updateCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	if a.pending != "" {
		n++
	}
	if a.claudeCodeUpdateLocked() {
		n++
	}
	return n
}

// updatesHeader heads the Updates section with how many there are (A44), in
// the words of the dashboard's header badge.
func updatesHeader(n int) string {
	if n <= 1 {
		return "Update available"
	}
	return strconv.Itoa(n) + " updates available"
}

// updatesChanged repaints menu, tooltip and badge after what can be updated
// changed, and tells the dashboard.
func (a *appShell) updatesChanged() {
	a.repaint()
	a.dashboardChanged()
}

// dashboardChanged pushes the change to the app's dashboard. The server only
// signals its serve loop, so this is safe from any goroutine; it is never
// called from update itself, which the broadcast it causes calls back.
func (a *appShell) dashboardChanged() {
	if a.act.UpdatesChanged != nil {
		a.act.UpdatesChanged()
	}
}

// beginCheck counts a check that asks the network, so the dashboard shows
// "Checking…" while one runs; done ends it (once).
func (a *appShell) beginCheck() (done func()) {
	a.mu.Lock()
	a.checking++
	a.mu.Unlock()
	a.dashboardChanged()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.checking--
			a.mu.Unlock()
			a.dashboardChanged()
		})
	}
}

// checkEverything runs the checks at once and waits for them: the release
// endpoint and Claude Code. Their announcements are only marked as made,
// since the caller reports the outcome itself — a newer release included, so
// the periodic check does not open the A36 dialog later for a release the
// dashboard's Check now already showed. newer is a release newer than this
// build.
func (a *appShell) checkEverything() (newer string, releaseErr error) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); a.checkClaudeCode(false) }()
	newer, releaseErr = a.checkRelease()
	if newer != "" {
		a.mu.Lock()
		a.offered = newer
		a.mu.Unlock()
	}
	wg.Wait()
	return newer, releaseErr
}

// releaseQuestion is the dialog that offers release latest.
func (a *appShell) releaseQuestion(latest string) string {
	return appName() + " " + strings.TrimPrefix(latest, "v") + " is available (you have " + strings.TrimPrefix(a.act.Current, "v") +
		"). Install now? " + appName() + " restarts by itself once it is installed."
}

// checkForUpdatesClick is "Check for updates…": everything at once, then a
// newer release is offered as the periodic check offers it — asked again even
// when that version was offered before, since the click is the request — and
// one notification says what the check found.
func (a *appShell) checkForUpdatesClick() {
	newer, releaseErr := a.checkEverything()
	if newer != "" && a.act.Ask != nil && a.upgradeHint() == "" {
		ok, err := a.act.Ask(appName()+" update", a.releaseQuestion(newer), "Install", "Later")
		if err == nil && ok {
			a.installUpdate()
			return
		}
		// Later, or no dialog after all: the outcome below names it.
	}
	a.notify(a.checkOutcome(releaseErr))
}

// checkOutcome is the notification "Check for updates…" ends with: what can
// be updated, else what is up to date, and what could not be checked — in the
// words the dashboard's card uses for the same state (A44). A release found
// before is named even when the re-check failed: it is still waiting in the
// menu. A Claude Code that is missing is said too, and then nothing is
// called "everything up to date".
func (a *appShell) checkOutcome(releaseErr error) (title, body string) {
	a.mu.Lock()
	pending := a.pending
	cc, ccKnown := a.claude, a.claudeKnown
	a.mu.Unlock()
	name := appName()
	var found, current, notes []string
	var failed []unchecked
	if pending != "" {
		found = append(found, name+" "+strings.TrimPrefix(pending, "v"))
	}
	switch {
	case releaseErr != nil && pending != "":
		failed = append(failed, unchecked{"for a " + name + " newer than " + strings.TrimPrefix(pending, "v"), releaseErr.Error()})
	case releaseErr != nil:
		failed = append(failed, unchecked{"for a new " + name, releaseErr.Error()})
	case pending == "" && a.act.LatestVersion != nil:
		current = append(current, name+" "+strings.TrimPrefix(a.act.Current, "v"))
	}
	if ccKnown {
		switch cc.State() {
		case ccversion.UpdateAvailable:
			found = append(found, "Claude Code "+cc.Latest)
		case ccversion.UpToDate:
			if cc.Err == nil {
				current = append(current, "Claude Code "+cc.Installed.Version)
			}
		case ccversion.Missing:
			notes = append(notes, "Claude Code is not installed on this machine.")
		}
		if err := claudeCodeCheckErr(cc); err != nil {
			failed = append(failed, unchecked{"Claude Code", err.Error()})
		}
	}
	// What could not be checked, with each reason while the text fits a
	// notification, else only what: the dashboard's card lists every reason.
	couldNot := func(why bool) string {
		if len(failed) == 0 {
			return ""
		}
		var parts []string
		for _, f := range failed {
			if why {
				parts = append(parts, f.what+" ("+f.why+")")
			} else {
				parts = append(parts, f.what)
			}
		}
		s := "Could not check " + joinAnd(parts) + "."
		if !why {
			s += " The dashboard's Updates card says why."
		}
		return s
	}
	note := strings.Join(notes, " ")
	switch {
	case len(found) > 0:
		title, body = "Updates available", strings.Join(found, ", ")+". Choose them under Updates in the "+name+" menu."
	case len(failed) > 0:
		title, body = "Could not check for every update", upToDate(current)
	case len(notes) > 0:
		title, body = "No updates found", upToDate(current)
	case len(current) == 0:
		return "Everything is up to date", "No update was found."
	default:
		return "Everything is up to date", upToDate(current)
	}
	join := func(parts ...string) string {
		var kept []string
		for _, p := range parts {
			if p != "" {
				kept = append(kept, p)
			}
		}
		return strings.Join(kept, " ")
	}
	return title, fitNotification(join(body, note, couldNot(true)), join(body, note, couldNot(false)))
}

// unchecked is one thing "Check for updates…" could not check, and why.
type unchecked struct{ what, why string }

// notifyLimit is how much of a notification's text Windows shows: it cuts the
// body at 255 UTF-16 units (A41), wherever that falls.
const notifyLimit = 255

// fitNotification is full when all of it fits a notification, else short:
// a shorter text that ends where it should rather than one cut mid-word.
func fitNotification(full, short string) string {
	if len(utf16.Encode([]rune(full))) <= notifyLimit {
		return full
	}
	return short
}

// upToDate is "<a> and <b> are up to date.", "" for nothing.
func upToDate(items []string) string {
	if len(items) == 0 {
		return ""
	}
	verb := " are up to date."
	if len(items) == 1 {
		verb = " is up to date."
	}
	return joinAnd(items) + verb
}

// joinAnd is "a", "a and b", "a, b and c".
func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
