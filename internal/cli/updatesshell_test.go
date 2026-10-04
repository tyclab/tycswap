package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/ccversion"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/web"
)

// updatesRig is a shell with both sources wired (DESIGN A44): a release
// endpoint and Claude Code, each answering what the test sets.
type updatesRig struct {
	mu          sync.Mutex
	latest      string // "" → the endpoint cannot be read
	claude      ccversion.Status
	calls       []string
	asked       []string
	refreshes   atomic.Int32
	releaseGate chan struct{} // when set, LatestVersion waits for it
}

func updatesShell(t *testing.T, answer bool) (*appShell, *fakeTray, *updatesRig) {
	t.Helper()
	sh, ft, _ := newTestShell(t)
	rig := &updatesRig{latest: "v2.1.0", claude: brewSeat("2.1.281", "2.1.281")}
	sh.act.Ask = func(title, body, ok, cancel string) (bool, error) {
		rig.mu.Lock()
		rig.asked = append(rig.asked, title+"|"+body)
		rig.mu.Unlock()
		return answer, nil
	}
	sh.act.Current = "v2.1.0"
	sh.act.LatestVersion = func() (string, error) {
		if rig.releaseGate != nil {
			<-rig.releaseGate
		}
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.calls = append(rig.calls, "release")
		if rig.latest == "" {
			return "", errors.New("offline")
		}
		return rig.latest, nil
	}
	sh.act.CheckClaudeCode = func() ccversion.Status {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.calls = append(rig.calls, "claude-code")
		return rig.claude
	}
	sh.act.RunClaudeCode = func(c ccversion.Command, in *ccversion.Installed) (string, error) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.calls = append(rig.calls, "run:"+c.String())
		// The installer brings Claude Code to the version it offered.
		rig.claude = brewSeat(rig.claude.Latest, rig.claude.Latest)
		return "==> Upgrading claude-code\n", nil
	}
	sh.act.UpdatesChanged = func() { rig.refreshes.Add(1) }
	sh.update(sampleState())
	return sh, ft, rig
}

func (r *updatesRig) callList() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, ",")
}

var (
	plainIcon = plainTrayIcon()
	badgeIcon = badgeTrayIcon(1)
)

// iconKind names an icon: "plain", "badge" for a count of 1, "badge:N" for
// another count (A44).
func iconKind(ic tray.Icon) string {
	same := func(a, b tray.Icon) bool {
		return bytes.Equal(a.PNG, b.PNG) && bytes.Equal(a.LargePNG, b.LargePNG) && bytes.Equal(a.ARGB32, b.ARGB32)
	}
	if same(ic, plainIcon) {
		return "plain"
	}
	for n := 1; n <= maxBadge; n++ {
		if same(ic, badgeTrayIcon(n)) {
			if n == 1 {
				return "badge"
			}
			return "badge:" + strconv.Itoa(n)
		}
	}
	return "other"
}

func iconKinds(ft *fakeTray) string {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var out []string
	for _, ic := range ft.icons {
		out = append(out, iconKind(ic))
	}
	return strings.Join(out, ",")
}

func TestBadgeIconIsTheMarkWithADot(t *testing.T) {
	if badgeIcon.Size != plainIcon.Size || len(badgeIcon.ARGB32) != badgeIcon.Size*badgeIcon.Size*4 {
		t.Fatalf("badge icon forms: size %d, ARGB32 %d bytes", badgeIcon.Size, len(badgeIcon.ARGB32))
	}
	if bytes.Equal(badgeIcon.PNG, plainIcon.PNG) || bytes.Equal(badgeIcon.LargePNG, plainIcon.LargePNG) || bytes.Equal(badgeIcon.ARGB32, plainIcon.ARGB32) {
		t.Error("the badge icon should differ from the plain one in every form")
	}
	if len(plainIcon.PNG) == 0 || len(plainIcon.ARGB32) != plainIcon.Size*plainIcon.Size*4 || (runtime.GOOS == "darwin") != (len(plainIcon.LargePNG) > 0) {
		t.Errorf("plain icon forms: %d, %d, %d bytes (the 128 px mark on macOS only)", len(plainIcon.PNG), len(plainIcon.LargePNG), len(plainIcon.ARGB32))
	}
}

// Each source puts the badge on the icon and the note in the tooltip, and
// takes both off again; the icon is only set when that changes.
func TestUpdateBadgeFollowsEachSource(t *testing.T) {
	for _, tc := range []struct {
		name         string
		waiting, off func(sh *appShell)
	}{
		{"tycswap",
			func(sh *appShell) {
				sh.act.LatestVersion = func() (string, error) { return "v2.2.0", nil }
				_, _ = sh.checkRelease()
			},
			func(sh *appShell) { sh.click("install-update") }},
		{"Claude Code",
			func(sh *appShell) { sh.setClaudeCode(brewSeat("2.1.280", "2.1.281")) },
			func(sh *appShell) { sh.setClaudeCode(brewSeat("2.1.281", "2.1.281")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh, ft, rig := updatesShell(t, true)
			sh.update(sampleState())
			if got := iconKinds(ft); got != "" {
				t.Fatalf("icon set with nothing to update: %s", got)
			}
			tc.waiting(sh)
			sh.update(sampleState()) // the next poll tick: no second SetIcon
			if got := iconKinds(ft); got != "badge" {
				t.Fatalf("icons = %s, want badge", got)
			}
			// Up front: Windows cuts the tooltip at 127 characters (A41).
			if !strings.HasPrefix(ft.tooltip, "tycswap · updates available — #1 ") || ft.title != "#1 · 45%" {
				t.Errorf("title %q, tooltip %q", ft.title, ft.tooltip)
			}
			if _, ok := ft.item("update"); !ok {
				t.Error("Check for updates… is gone")
			}
			if rig.refreshes.Load() == 0 {
				t.Error("the dashboard was not told")
			}
			tc.off(sh)
			sh.update(sampleState())
			if got := iconKinds(ft); got != "badge,plain" {
				t.Errorf("icons = %s, want badge,plain", got)
			}
			if strings.Contains(ft.tooltip, "updates available") {
				t.Errorf("tooltip still says so: %q", ft.tooltip)
			}
		})
	}
}

// With both waiting, the Updates section comes right after "Open dashboard",
// tycswap first, then Claude Code; the Claude Code row returns to the App
// section for its other states.
func TestUpdatesSectionRowsAndOrder(t *testing.T) {
	sh, ft, _ := updatesShell(t, true)
	sh.act.LatestVersion = func() (string, error) { return "v2.2.0", nil }
	_, _ = sh.checkRelease()
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))

	ids, headers := menuShape(ft)
	if ids != "brand,open,install-update,claude-code,switch:1,switch:2,auto,autostart,update,quit" {
		t.Errorf("menu = %s", ids)
	}
	if headers != "2 updates available,Accounts,Automation,App" {
		t.Errorf("headers = %s", headers)
	}
	// Two things wait: the badge counts them (A44), and each row says what it
	// brings on its second line.
	if got := iconKinds(ft); !strings.HasSuffix(got, "badge:2") {
		t.Errorf("icons = %s, want the last to be badge:2", got)
	}
	for id, want := range map[string]string{
		"install-update": "you have 2.1.0 · restarts by itself",
		"claude-code":    "with Homebrew · running sessions keep the old one",
	} {
		if it, _ := ft.item(id); it.Kind != tray.KindUpdate || it.Sub != want {
			t.Errorf("%s: kind %d, sub %q, want %q", id, it.Kind, it.Sub, want)
		}
	}
	for id, want := range map[string]string{
		"install-update": "Install tycswap 2.2.0…",
		"claude-code":    "Update Claude Code 2.1.280 → 2.1.281…",
	} {
		if it, _ := ft.item(id); it.Title != want || !it.Clickable() {
			t.Errorf("%s = %+v, want %q", id, it, want)
		}
	}

	sh.setClaudeCode(brewSeat("2.1.281", "2.1.281"))
	if ids, headers := menuShape(ft); ids != "brand,open,install-update,switch:1,switch:2,auto,claude-code,autostart,update,quit" || !strings.HasPrefix(headers, "Update available,") {
		t.Errorf("latest Claude Code belongs to App: %s / %s", ids, headers)
	}
}

func menuShape(ft *fakeTray) (ids, headers string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var i, h []string
	for _, it := range ft.menu {
		switch {
		case it.Kind == tray.KindHeader:
			h = append(h, it.Title)
		case !it.Separator:
			i = append(i, it.ID)
		}
	}
	return strings.Join(i, ","), strings.Join(h, ",")
}

// "Check for updates…" asks both sources, then says in one notification that
// everything is current.
func TestCheckForUpdatesChecksBoth(t *testing.T) {
	sh, ft, rig := updatesShell(t, true)
	sh.click("update")
	calls := rig.callList()
	if !strings.Contains(calls, "release") || !strings.Contains(calls, "claude-code") {
		t.Errorf("calls = %s", calls)
	}
	if len(ft.notes) != 1 || ft.notes[0] != "Everything is up to date | tycswap 2.1.0 and Claude Code 2.1.281 are up to date." {
		t.Errorf("notes = %v", ft.notes)
	}
	if len(rig.asked) != 0 {
		t.Errorf("asked %v with nothing to install", rig.asked)
	}
	if v := sh.updatesView(); v.Checking || v.CheckedAt == nil || v.Available {
		t.Errorf("view after the check: %+v", v)
	}
}

// With updates everywhere: the new release is offered in the dialog, and the
// one notification names both — no separate announcements, and the periodic
// checks do not announce them again.
func TestCheckForUpdatesReportsWhatWaits(t *testing.T) {
	sh, ft, rig := updatesShell(t, false) // "Later"
	rig.latest = "v2.2.0"
	rig.claude = brewSeat("2.1.280", "2.1.281")
	sh.click("update")
	if len(rig.asked) != 1 || !strings.HasPrefix(rig.asked[0], "tycswap update|tycswap 2.2.0 is available (you have 2.1.0)") {
		t.Errorf("asked = %v", rig.asked)
	}
	want := "Updates available | tycswap 2.2.0, Claude Code 2.1.281. Choose them under Updates in the tycswap menu."
	if len(ft.notes) != 1 || ft.notes[0] != want {
		t.Fatalf("notes = %v\nwant %q", ft.notes, want)
	}
	sh.checkClaudeCode(true)
	sh.offerUpdate() // the dialog was shown for this version already
	if len(ft.notes) != 1 || len(rig.asked) != 1 {
		t.Errorf("announced again: notes %v, asked %v", ft.notes, rig.asked)
	}
}

// A yes in the check's dialog installs the release at once.
func TestCheckForUpdatesInstallsOnYes(t *testing.T) {
	sh, ft, rig := updatesShell(t, true)
	rig.latest = "v2.2.0"
	var restartedTo string
	sh.act.Restart = func(tag string) { restartedTo = tag }
	sh.click("update")
	if restartedTo != "v2.2.0" {
		t.Errorf("restart tag = %q", restartedTo)
	}
	if last := ft.notes[len(ft.notes)-1]; !strings.Contains(last, "Restarting…") {
		t.Errorf("notes = %v", ft.notes)
	}
}

func TestCheckForUpdatesSaysWhatCouldNotBeChecked(t *testing.T) {
	sh, ft, rig := updatesShell(t, true)
	rig.latest = ""
	rig.claude = brewSeat("2.1.281", "")
	rig.claude.Err = errors.New("HTTP 502")
	sh.click("update")
	want := "Could not check for every update | Could not check for a new tycswap (offline) and Claude Code (HTTP 502)."
	if len(ft.notes) != 1 || ft.notes[0] != want {
		t.Errorf("notes = %v\nwant %q", ft.notes, want)
	}
}

// The dashboard's view: the same answer as the badge, and each source's
// details.
func TestUpdatesFacadeView(t *testing.T) {
	sh, _, _ := updatesShell(t, true)
	f := &updatesFacade{}
	if v := f.View(); v.Available || v.App != nil {
		t.Errorf("no shell yet: %+v", v)
	}
	f.sh.Store(sh)
	sh.act.LatestVersion = func() (string, error) { return "v2.2.0", nil }
	_, _ = sh.checkRelease()
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))

	v := f.View()
	if !v.Available || v.Checking || v.CheckedAt == nil {
		t.Errorf("flags: %+v", v)
	}
	if *v.App != (web.AppUpdateView{Current: "v2.1.0", Latest: "v2.2.0", Available: true}) {
		t.Errorf("app = %+v", *v.App)
	}
	wantCC := web.ClaudeCodeUpdateView{Installed: "2.1.280", Latest: "2.1.281", State: "update", Method: "Homebrew",
		Command: "brew upgrade --cask claude-code", Available: true}
	if *v.ClaudeCode != wantCC {
		t.Errorf("claude code = %+v", *v.ClaudeCode)
	}
	if _, err := json.Marshal(v); err != nil {
		t.Error(err)
	}
	// An update the app cannot run is shown, not offered.
	sh.act.RunClaudeCode = nil
	if cc := f.View().ClaudeCode; cc.State != "update" || cc.Available {
		t.Errorf("without a runner: %+v", *cc)
	}
}

// Check returns at once and the state already says "Checking…"; the result
// arrives through the dashboard refreshes.
func TestUpdatesFacadeCheck(t *testing.T) {
	sh, _, rig := updatesShell(t, true)
	f := &updatesFacade{}
	if err := f.Check(); !errors.Is(err, errNoShell) {
		t.Errorf("no shell: %v", err)
	}
	f.sh.Store(sh)
	rig.releaseGate = make(chan struct{})
	rig.latest = "v2.2.0"
	if err := f.Check(); err != nil {
		t.Fatal(err)
	}
	if !f.View().Checking {
		t.Error("not checking right after Check")
	}
	close(rig.releaseGate)
	waitNotChecking(t, f)
	v := f.View()
	if v.Checking || !v.Available || !v.App.Available {
		t.Errorf("after the check: %+v", v)
	}
	if len(rig.asked) != 0 {
		t.Errorf("the dashboard's check must not open the tray's dialog: %v", rig.asked)
	}
}

// Apply runs the tray's runs without the tray's dialogs; one at a time.
func TestUpdatesFacadeApply(t *testing.T) {
	sh, ft, rig := updatesShell(t, true)
	f := &updatesFacade{}
	if _, err := f.Apply("app"); !errors.Is(err, errNoShell) {
		t.Errorf("no shell: %v", err)
	}
	f.sh.Store(sh)

	// tycswap: the self-upgrade, then the restart.
	sh.act.LatestVersion = func() (string, error) { return "v2.2.0", nil }
	_, _ = sh.checkRelease()
	var restartedTo string
	sh.act.Restart = func(tag string) { restartedTo = tag }
	res, err := f.Apply("app")
	if err != nil || res.Message != "Updated tycswap 2.0.0 → 2.1.0 Restarting…" || restartedTo != "v2.2.0" {
		t.Errorf("app: %+v, %v, restart %q", res, err, restartedTo)
	}
	if sh.updatesView().App.Available {
		t.Error("the release is still pending after the upgrade")
	}

	// Claude Code: the installer's own upgrade, then a fresh check.
	rig.mu.Lock()
	rig.claude = brewSeat("2.1.280", "2.1.281")
	rig.mu.Unlock()
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))
	res, err = f.Apply("claude-code")
	if err != nil || !strings.HasPrefix(res.Message, "Claude Code is updated.") || res.Output != "==> Upgrading claude-code" {
		t.Errorf("claude-code: %+v, %v", res, err)
	}
	if calls := rig.callList(); !strings.Contains(calls, "run:brew upgrade --cask claude-code,claude-code") {
		t.Errorf("calls = %s", calls)
	}
	if sh.claudeCodeUpdate() {
		t.Error("the update is still offered after the run")
	}
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))
	sh.act.RunClaudeCode = func(ccversion.Command, *ccversion.Installed) (string, error) {
		return "==> Downloading claude-code\nError: Download failed\n", errors.New("exit status 1")
	}
	// A failure still hands the page what the command printed.
	if res, err := f.Apply("claude-code"); err == nil || err.Error() != "Error: Download failed" || res.Output != "==> Downloading claude-code\nError: Download failed" {
		t.Errorf("failed run: %+v, %v", res, err)
	}
	if len(rig.asked) != 0 {
		t.Errorf("the page asked already; the tray asked again: %v", rig.asked)
	}
	for _, n := range ft.notesNow() {
		if strings.HasPrefix(n, "Claude Code update") || strings.HasPrefix(n, "tycswap update") {
			t.Errorf("the page reports the result; the tray notified too: %q", n)
		}
	}

	// One update at a time, whoever started the other.
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))
	sh.act.LatestVersion = func() (string, error) { return "v2.3.0", nil }
	_, _ = sh.checkRelease()
	sh.applyMu.Lock()
	for _, target := range []string{"app", "claude-code"} {
		if _, err := f.Apply(target); !errors.Is(err, errUpdateRunning) {
			t.Errorf("%s while another runs: %v", target, err)
		}
	}
	sh.applyMu.Unlock()

	if _, err := f.Apply("everything"); err == nil {
		t.Error("unknown target accepted")
	}
}

// A release the dashboard's Check now found is not offered again in the
// periodic check's dialog: the page showed it already.
func TestDashboardCheckMarksTheReleaseOffered(t *testing.T) {
	sh, ft, rig := updatesShell(t, true)
	rig.latest = "v2.2.0"
	f := &updatesFacade{}
	f.sh.Store(sh)
	if err := f.Check(); err != nil {
		t.Fatal(err)
	}
	waitNotChecking(t, f)
	sh.offerUpdate()
	if len(rig.asked) != 0 || len(ft.notes) != 0 {
		t.Errorf("offered again: asked %v, notes %v", rig.asked, ft.notes)
	}
	if !sh.updatesView().App.Available {
		t.Error("the release is no longer pending")
	}
}

func waitNotChecking(t *testing.T, f *updatesFacade) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.View().Checking && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.View().Checking {
		t.Fatal("the check did not finish")
	}
}

// A release found before that the re-check cannot confirm is still named as
// waiting, next to the failure.
func TestCheckForUpdatesKeepsAPendingRelease(t *testing.T) {
	sh, ft, rig := updatesShell(t, false)
	sh.act.LatestVersion = func() (string, error) { return "v2.2.0", nil }
	_, _ = sh.checkRelease()
	sh.act.LatestVersion = func() (string, error) { return "", errors.New("offline") }
	rig.asked = nil
	sh.checkForUpdatesClick()
	want := "Updates available | tycswap 2.2.0. Choose them under Updates in the tycswap menu. Could not check for a tycswap newer than 2.2.0 (offline)."
	if len(ft.notes) != 1 || ft.notes[0] != want {
		t.Errorf("notes = %v\nwant %q", ft.notes, want)
	}
	if v := sh.updatesView(); !v.App.Available || v.App.Error != "offline" {
		t.Errorf("app = %+v", v.App)
	}
}

// A missing Claude Code is said, and then nothing is called up to date as a
// whole — on the tray and on the card.
func TestCheckForUpdatesNamesAMissingClaudeCode(t *testing.T) {
	missing := brewSeat("", "")
	missing.Installed = nil
	sh, ft, rig := updatesShell(t, true)
	rig.claude = missing
	sh.click("update")
	want := "No updates found | tycswap 2.1.0 is up to date. Claude Code is not installed on this machine."
	if len(ft.notes) != 1 || ft.notes[0] != want {
		t.Errorf("notes = %v\nwant %q", ft.notes, want)
	}
	if cc := sh.updatesView().ClaudeCode; cc.State != "missing" || cc.Command == "" {
		t.Errorf("card = %+v", *cc)
	}
}

// What could not be checked reaches the card too: the release check's error
// and Claude Code's. "checked …" is only for a check that reached the
// network.
func TestUpdatesViewCarriesEveryError(t *testing.T) {
	sh, _, rig := updatesShell(t, true)
	rig.latest = ""
	rig.claude = brewSeat("2.1.281", "")
	rig.claude.Err = errors.New("HTTP 502")
	f := &updatesFacade{}
	f.sh.Store(sh)
	if err := f.Check(); err != nil {
		t.Fatal(err)
	}
	waitNotChecking(t, f)
	v := f.View()
	if v.CheckedAt != nil {
		t.Errorf("nothing reached the network, yet checked at %v", v.CheckedAt)
	}
	if v.App.Error != "offline" || v.ClaudeCode.State != "unknown" || v.ClaudeCode.Error != "HTTP 502" || v.ClaudeCode.Detail != "Could not check for a newer Claude Code: HTTP 502" {
		t.Errorf("view = %+v, app %+v, claude code %+v", v, *v.App, *v.ClaudeCode)
	}
	rig.latest = "v2.1.0"
	if _, err := sh.checkRelease(); err != nil {
		t.Fatal(err)
	}
	if v := f.View(); v.CheckedAt == nil || v.App.Error != "" {
		t.Errorf("after a release check that worked: %+v", v)
	}
}

// A Claude Code check that cannot learn the newest version keeps the update
// the check before it found, with its error, so the row and the badge stay.
func TestClaudeCodeFailedCheckKeepsTheUpdate(t *testing.T) {
	sh, _, _ := updatesShell(t, true)
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))
	offline := brewSeat("2.1.280", "")
	offline.Err = errors.New("HTTP 502")
	sh.setClaudeCode(offline)
	if !sh.claudeCodeUpdate() || !sh.updateWaiting() {
		t.Fatal("a failed check forgot the Claude Code update")
	}
	if v := sh.updatesView().ClaudeCode; v.State != "update" || v.Latest != "2.1.281" || v.Error != "HTTP 502" || !v.Available {
		t.Errorf("claude code = %+v", *v)
	}
	title, body := sh.checkOutcome(nil)
	if title != "Updates available" || !strings.Contains(body, "Claude Code 2.1.281") || !strings.Contains(body, "Could not check Claude Code (HTTP 502).") {
		t.Errorf("outcome = %q | %q", title, body)
	}
	// Another Claude Code now: the old finding says nothing about it.
	other := brewSeat("2.1.281", "")
	other.Err = offline.Err
	sh.setClaudeCode(other)
	if sh.claudeCodeUpdate() {
		t.Error("the update was kept for a different version")
	}
}

// Too much to say for a notification: the reasons go, what could not be
// checked stays, and the dashboard's card says why.
func TestCheckOutcomeFitsANotification(t *testing.T) {
	sh, ft, rig := updatesShell(t, true)
	long := errors.New("Get \"https://downloads.example/claude-code-releases/latest\": dial tcp: lookup downloads.example on 127.0.0.53:53: read udp 127.0.0.1:40000->127.0.0.53:53: i/o timeout after 75003 ms")
	rig.latest = ""
	rig.claude = brewSeat("2.1.281", "")
	rig.claude.Err = long
	sh.act.LatestVersion = func() (string, error) { return "", long }
	sh.click("update")
	want := "Could not check for every update | Could not check for a new tycswap and Claude Code. The dashboard's Updates card says why."
	if len(ft.notes) != 1 || ft.notes[0] != want {
		t.Errorf("notes = %v\nwant %q", ft.notes, want)
	}
}
