// Tests for the tray's Fable store (row E5): the Fable label, Use for Fable
// on the panel and in the menu, the tooltip, and both trays' Fable switch.
package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/tray"
	"github.com/tyclab/tycswap/internal/web"
)

// fableTrayState: the Fable store on account 1, the default login on 2.
// Fable logins: 1 live, 2 usable, 3 to sign in again, 4 on a Pro plan, 5 not
// rotated, 6 quarantined by the Fable engine (plan unknown); 7 has none.
func fableTrayState(number any) web.State {
	row := func(num int, alias, plan string, live bool, status string) map[string]any {
		r := panelRow("claude", num, alias, plan, limitWindow(10.0, ""), limitWindow(float64(num), ""), nil)
		if status != "" {
			r["fableLogin"] = map[string]any{"live": live, "status": status}
		}
		return r
	}
	rows := []map[string]any{
		row(1, "first", "Ultra", true, "ok"),
		row(2, "second", "Ultra", false, "ok"),
		row(3, "third", "Ultra", false, "sign in again"),
		row(4, "fourth", "Pro", false, "ok"),
		row(5, "fifth", "Ultra", false, "ok"),
		row(6, "sixth", "", false, "ok"),
		row(7, "seventh", "Ultra", false, ""),
		panelRow("codex", 1, "cx", "", nil, limitWindow(10.0, ""), nil),
	}
	rows[1]["isActive"] = true
	rows[4]["disabled"] = true
	st := panelState(rows...)
	st.Fable = map[string]any{"state": "on", "storeDir": "/tmp/backups/fable/store", "number": number, "email": "first@example.test"}
	st.Auto.Fable = &web.FableAutoView{Quarantine: map[string]any{"6": map[string]any{"reason": "invalid_grant", "at": "2026-11-18T09:00:00Z"}}, Ranking: []string{"2"}}
	return st
}

func fableShell(t *testing.T) (*appShell, *fakeTray, *[]string) {
	t.Helper()
	sh, ft, calls := newTestShell(t)
	sh.act.SwitchFable = func(key string) error { *calls = append(*calls, "fable:"+key); return nil }
	return sh, ft, calls
}

// The Fable store's row has the Fable label; Use for Fable is on every row
// that can take the Fable store and on no other; the action cell stays the
// default store's. Without SwitchFable there is no button.
func TestTrayFablePanelRows(t *testing.T) {
	sh, _, _ := fableShell(t)
	check := func(p tray.Panel, eligible map[string]bool) {
		t.Helper()
		for _, row := range p.Rows {
			if row.Fable != (row.ID == "claude:1") || row.FableSwitch != eligible[row.ID] {
				t.Errorf("row %s: Fable %v, Use for Fable %v", row.ID, row.Fable, row.FableSwitch)
			}
		}
	}
	p := sh.panelAt(fableTrayState(1), panelNow)
	check(p, map[string]bool{"claude:2": true})
	if live, def := rowByID(t, p, "claude:1"), rowByID(t, p, "claude:2"); live.Active || !def.Active || live.Plan != "Ultra" {
		t.Fatalf("the action cells follow the Fable store: %+v / %+v", live, def)
	}
	st := fableTrayState(1)
	st.Auto.Fable = nil
	check(sh.panelAt(st, panelNow), map[string]bool{"claude:2": true, "claude:6": true})
	sh.act.SwitchFable = nil
	check(sh.panelAt(fableTrayState(1), panelNow), nil)
}

// The tooltip names both stores' accounts while some account has a Fable
// login; the auto-switch state and the Codex account follow as before, and
// the title next to the icon does not change.
func TestTrayFableTooltip(t *testing.T) {
	sh, ft, _ := fableShell(t)
	plain := fableTrayState(1)
	plain.Fable = nil
	sh.update(plain)
	title := ft.title
	if ft.tooltip != "tycswap — #2 second · 5h 10% · 7d 2% · auto-switch off" {
		t.Fatalf("tooltip without the Fable store %q", ft.tooltip)
	}
	for _, check := range []struct {
		name string
		edit func(st *web.State)
		want string
	}{
		{"number", func(*web.State) {}, "tycswap — Default #2 · Fable #1 · auto-switch off"},
		{"after JSON", func(st *web.State) { st.Fable["number"] = 1.0 }, "tycswap — Default #2 · Fable #1 · auto-switch off"},
		{"off", func(st *web.State) { st.Fable["state"], st.Fable["number"] = "off", nil }, "tycswap — Default #2 · Fable off · auto-switch off"},
		{"unknown", func(st *web.State) { st.Fable["number"] = nil }, "tycswap — Default #2 · Fable account unknown · auto-switch off"},
		{"unusable", func(st *web.State) { st.Fable["state"] = "unusable" }, "tycswap — Default #2 · Fable #1 · auto-switch off"},
		{"no default", func(st *web.State) { st.Accounts[1]["isActive"] = false }, "tycswap — no active account · Fable #1 · auto-switch off"},
		{"codex", func(st *web.State) { st.Accounts[7]["isActive"] = true }, "tycswap — Default #2 · Fable #1 · auto-switch off · codex #1 cx · 7d 10%"},
	} {
		st := fableTrayState(1)
		check.edit(&st)
		sh.update(st)
		if ft.tooltip != check.want {
			t.Errorf("%s: tooltip %q, want %q", check.name, ft.tooltip, check.want)
		}
		if check.name != "no default" && ft.title != title {
			t.Errorf("%s: title %q, want %q", check.name, ft.title, title)
		}
	}
	sh.act.AutoRunning = func() bool { return true }
	sh.update(fableTrayState(1))
	if ft.tooltip != "tycswap — Default #2 · Fable #1 · auto-switch on" {
		t.Errorf("auto-switch on: %q", ft.tooltip)
	}
	sh.act.Offline = func() (bool, string) { return true, "engine unreachable at http://127.0.0.1:7337" }
	sh.update(fableTrayState(1))
	if ft.tooltip != "tycswap — engine unreachable at http://127.0.0.1:7337" {
		t.Errorf("offline: %q", ft.tooltip)
	}
}

// One Use for Fable item per account that can take the Fable store, after
// the Claude rows (and their submenu) and before Add account…; the Fable
// store's row's label ends in " · Fable" on every menu that renders it.
func TestTrayFableMenu(t *testing.T) {
	sh, ft, _ := fableShell(t)
	sh.update(fableTrayState(1))
	ids, _ := menuShape(ft)
	if want := "brand,open,switch:claude:1,switch:claude:2,switch:claude:3,switch:claude:4,switch:claude:5,switch:claude:6,switch:claude:7,fable:claude:2,sessions-open,switch:codex:1,auto,autostart,update,quit"; ids != want {
		t.Fatalf("menu order %s\nwant %s", ids, want)
	}
	it, _ := ft.item("fable:claude:2")
	if it.Title != "Use for Fable: #2  second" || it.Kind != tray.KindPlain || !it.Clickable() || it.Dismiss || it.Sub != "" {
		t.Fatalf("Use for Fable item %+v", it)
	}
	live, _ := ft.item("switch:claude:1")
	if !strings.HasSuffix(live.Sub, " · Fable") || !strings.HasSuffix(live.FallbackTitle(), " · Fable") {
		t.Fatalf("the Fable store's row %+v", live)
	}
	for _, id := range []string{"switch:claude:2", "switch:claude:7", "switch:codex:1"} {
		if row, _ := ft.item(id); strings.Contains(row.Sub, "Fable") {
			t.Errorf("%s says Fable: %+v", id, row)
		}
	}
	sh.act.AccountSetup = func(string) error { return nil }
	sh.update(fableTrayState(1))
	if ids, _ := menuShape(ft); !strings.Contains(ids, "switch:claude:7,fable:claude:2,sessions-open,account-setup,menu:main,switch:codex:1") {
		t.Fatalf("with account setup %s", ids)
	}
	st := fableTrayState(1)
	st.Accounts = append(st.Accounts, manyAccounts(inlineAccounts+12, 0)[inlineAccounts:]...)
	sh.update(st)
	if ids, _ := menuShape(ft); !strings.Contains(ids, "switch:claude:2,accounts,fable:claude:2,sessions-open,account-setup") {
		t.Fatalf("with a submenu %s", ids)
	}
	sh.act.SwitchFable = nil
	sh.update(fableTrayState(1))
	if ids, _ := menuShape(ft); strings.Contains(ids, "fable:") {
		t.Fatalf("Use for Fable without SwitchFable: %s", ids)
	}
	if live, _ := ft.item("switch:claude:1"); !strings.HasSuffix(live.Sub, " · Fable") {
		t.Fatalf("the label went with the switch: %+v", live)
	}
}

// Without any Fable login nothing changes: the same title, tooltip, menu
// and panel whether the tray can switch the Fable store or not, and none of
// them names it.
func TestTrayWithoutFableLoginsNothingChanges(t *testing.T) {
	noFable := fableTrayState(1)
	noFable.Fable, noFable.Auto.Fable = nil, nil
	for _, row := range noFable.Accounts {
		delete(row, "fableLogin")
	}
	for name, st := range map[string]web.State{"sample": sampleState(), "codex": codexState(), "rows": noFable} {
		wired, wft, _ := fableShell(t)
		plain, pft, _ := newTestShell(t)
		wired.update(st)
		plain.update(st)
		if wft.title != pft.title || wft.tooltip != pft.tooltip || !reflect.DeepEqual(wft.menu, pft.menu) ||
			!reflect.DeepEqual(wired.panelAt(st, panelNow), plain.panelAt(st, panelNow)) {
			t.Fatalf("%s: SwitchFable changed the tray without a Fable login", name)
		}
		if strings.Contains(wft.tooltip, "Default #") {
			t.Errorf("%s: tooltip %q", name, wft.tooltip)
		}
		for _, it := range wft.menu {
			if strings.HasPrefix(it.ID, "fable:") || strings.HasSuffix(it.Sub, " · Fable") {
				t.Errorf("%s: menu item %+v", name, it)
			}
		}
		for _, row := range wired.panelAt(st, panelNow).Rows {
			if row.Fable || row.FableSwitch {
				t.Errorf("%s: panel row %+v", name, row)
			}
		}
	}
	sh, ft, _ := fableShell(t)
	sh.update(sampleState())
	if ft.tooltip != "tycswap — #1 work · 5h 45% · 7d 30% · Fable 12% (not counted) · auto-switch off" {
		t.Fatalf("tooltip %q", ft.tooltip)
	}
}

// Use for Fable switches the Fable store, tells the dashboard, and says so
// in the note and a notification; a failure says why. The panel's button
// sends the same command.
func TestTrayFableClick(t *testing.T) {
	sh, ft, calls := fableShell(t)
	pt := &panelCaptureTray{fakeTray: ft}
	sh.tray = pt
	pushed := 0
	sh.act.UpdatesChanged = func() { pushed++ }
	sh.update(fableTrayState(1))
	*calls = nil
	sh.click("fable:claude:2")
	if got := strings.Join(*calls, ","); got != "fable:claude:2" || pushed != 1 {
		t.Fatalf("calls %s, dashboard told %d times", got, pushed)
	}
	if pt.panel.Note != "Switched the Fable store to #2." || pt.panel.Alert {
		t.Fatalf("note %q (alert %v)", pt.panel.Note, pt.panel.Alert)
	}
	if note := lastNote(ft); note != "Switched the Fable store to account #2 | Fable sessions use account #2's Fable login." {
		t.Fatalf("notification %q", note)
	}
	sh.panelAction(tray.PanelAction{Kind: "command", Key: "fable:claude:2"})
	if got := strings.Join(*calls, ","); got != "fable:claude:2,fable:claude:2" {
		t.Fatalf("the panel's Use for Fable: %s", got)
	}
	sh.act.SwitchFable = func(string) error { return errors.New("Account-2's Fable login is gone") }
	sh.click("fable:claude:2")
	if pt.panel.Note != "Fable switch failed: Account-2's Fable login is gone" || !pt.panel.Alert || pushed != 2 {
		t.Fatalf("failure note %q (alert %v), dashboard told %d times", pt.panel.Note, pt.panel.Alert, pushed)
	}
	if note := lastNote(ft); note != "Fable switch failed | Account-2's Fable login is gone" {
		t.Fatalf("failure notification %q", note)
	}
	sh.act.SwitchFable = nil
	sh.click("fable:claude:2")
	if note := lastNote(ft); note != "Fable switch failed | Account-2's Fable login is gone" {
		t.Fatalf("a click without SwitchFable notified %q", note)
	}
}

// A Use for Fable click while one is in flight is dropped, not queued.
func TestTrayFableClicksDropWhileOneIsInFlight(t *testing.T) {
	sh, _, _ := fableShell(t)
	var count atomic.Int32
	inFirst, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	sh.act.SwitchFable = func(string) error {
		count.Add(1)
		once.Do(func() { close(inFirst); <-release })
		return nil
	}
	sh.update(fableTrayState(1))
	done := make(chan struct{}, 2)
	go func() { sh.click("fable:claude:2"); done <- struct{}{} }()
	select {
	case <-inFirst:
	case <-time.After(5 * time.Second):
		t.Fatal("a click never switched the Fable store")
	}
	go func() { sh.click("fable:claude:6"); done <- struct{}{} }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the second click waited for the first")
	}
	close(release)
	<-done
	if count.Load() != 1 {
		t.Fatalf("%d Fable switches, want the first only", count.Load())
	}
	sh.click("fable:claude:2")
	if count.Load() != 2 {
		t.Fatal("a click after the first had finished was dropped")
	}
}

// The app's own tray switches the Fable store through the switcher and logs
// a move as the tray's; a Codex key has no Fable login.
func TestAppTraySwitchesTheFableStore(t *testing.T) {
	sw := fableCLISwitcher(t)
	d := &dashboard{sw: sw}
	if err := d.switchFable("claude:2"); err != nil {
		t.Fatal(err)
	}
	live, _ := os.ReadFile(filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"))
	if string(live) != fableLogin("f2") {
		t.Fatalf("the Fable store holds %s", live)
	}
	if err := d.switchFable("claude:2"); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(filepath.Join(sw.BackupDir(), "tycswap.log"))
	if n := strings.Count(string(log), "Switched the Fable store to account 2"); n != 1 || !strings.Contains(string(log), "Switched the Fable store to account 2 by tray") {
		t.Fatalf("%d lines for the one move, want one, the tray's:\n%s", n, log)
	}
	err := d.switchFable("codex:1")
	var e *cerr.Error
	if !errors.As(err, &e) || e.Kind != cerr.KindAccountNotFound {
		t.Fatalf("codex key: %v", err)
	}
}

// -- the remote tray ---------------------------------------------------------

// remoteFakeFable is the dashboard's FableOps: recorded switches, failing
// with err when set.
type remoteFakeFable struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *remoteFakeFable) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *remoteFakeFable) SwitchFable(id string, jsonOut bool) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "SwitchFable("+id+")")
	if f.err != nil {
		return nil, f.err
	}
	n := 2
	return map[string]any{"switched": true, "to": map[string]any{"number": &n}, "reason": "switched", "store": "fable"}, nil
}

func (f *remoteFakeFable) EmptyFable(bool) (map[string]any, error) {
	return nil, errors.New("not in this test")
}

type fableFacade struct {
	*fakeFacade
	fable map[string]any
}

func (f *fableFacade) AccountsSnapshot(hidden map[string]bool) *reporting.AccountsSnapshot {
	snap := f.fakeFacade.AccountsSnapshot(hidden)
	snap.Fable = f.fable
	return snap
}

type fableAuto struct{ *remoteFakeAuto }

func (a fableAuto) View() web.AutoView {
	v := a.remoteFakeAuto.View()
	v.Fable = &web.FableAutoView{Quarantine: map[string]any{"3": map[string]any{"reason": "invalid_grant"}}, Ranking: []string{"2"}}
	return v
}

// withRemoteFable serves the remote fixture with the Fable store on account
// 1 and a usable Fable login on account 2.
func withRemoteFable(f *remoteFakeFable) func(*web.Deps) {
	return func(d *web.Deps) {
		fa := d.Facade.(*fakeFacade)
		fa.accounts[0].FableLogin = &reporting.FableLogin{Live: true, Status: reporting.FableLoginOK}
		fa.accounts[1].FableLogin = &reporting.FableLogin{Status: reporting.FableLoginOK}
		d.Facade = &fableFacade{fakeFacade: fa, fable: map[string]any{"state": "on", "storeDir": "/tmp/backups/fable/store", "number": 1, "email": "alice@example.com"}}
		d.Auto = fableAuto{d.Auto.(*remoteFakeAuto)}
		d.Fable = f
	}
}

// The remote tray decodes the Fable fields as the app's own tray reads them
// and shows the same; an older server sends none and the tray shows nothing
// new.
func TestRemoteTrayDecodesTheFableFields(t *testing.T) {
	fx := startRemoteServer(t, testRemoteToken, "127.0.0.1:0", withRemoteFable(&remoteFakeFable{}))
	st, err := newTestRemoteClient(t, fx, testRemoteToken).State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Fable["state"] != "on" || st.Fable["number"] != 1.0 {
		t.Fatalf("fable %v", st.Fable)
	}
	if got := st.Accounts[0]["fableLogin"]; !reflect.DeepEqual(got, map[string]any{"live": true, "status": "ok"}) {
		t.Fatalf("row 1 fableLogin %#v", got)
	}
	if st.Auto == nil || st.Auto.Fable == nil || st.Auto.Fable.Quarantine["3"] == nil {
		t.Fatalf("auto.fable %+v", st.Auto)
	}
	sh, ft, _ := fableShell(t)
	sh.update(st)
	if ft.tooltip != "tycswap — Default #1 · Fable #1 · auto-switch off" {
		t.Fatalf("tooltip %q", ft.tooltip)
	}
	p := sh.panelAt(st, panelNow)
	if !rowByID(t, p, "claude:1").Fable || !rowByID(t, p, "claude:2").FableSwitch {
		t.Fatalf("panel rows %+v", p.Rows)
	}

	old := startRemoteServer(t, testRemoteToken, "127.0.0.1:0")
	st, err = newTestRemoteClient(t, old, testRemoteToken).State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Fable != nil || (st.Auto != nil && st.Auto.Fable != nil) || st.Accounts[0]["fableLogin"] != nil {
		t.Fatalf("an older server's state carries Fable fields: %+v", st)
	}
	sh, ft, _ = fableShell(t)
	sh.update(st)
	plain, pft, _ := newTestShell(t)
	plain.update(st)
	if ft.tooltip != pft.tooltip || !reflect.DeepEqual(ft.menu, pft.menu) || !reflect.DeepEqual(sh.panelAt(st, panelNow), plain.panelAt(st, panelNow)) {
		t.Fatalf("the remote tray shows something new for an older server: %q", ft.tooltip)
	}
}

// SwitchFable posts the row key with store=fable, reaches the Fable store and
// not the default one, refreshes the cached state, and carries the server's
// refusal.
func TestRemoteSwitchFablePostsTheStore(t *testing.T) {
	fable := &remoteFakeFable{}
	fx := startRemoteServer(t, testRemoteToken, "127.0.0.1:0", withRemoteFable(fable))
	rc := newTestRemoteClient(t, fx, testRemoteToken)
	if err := rc.SwitchFable("claude:2"); err != nil {
		t.Fatal(err)
	}
	if got := fable.Calls(); !reflect.DeepEqual(got, []string{"SwitchFable(2)"}) || len(fx.fa.Calls()) != 0 {
		t.Fatalf("fable calls %v, default calls %v", got, fx.fa.Calls())
	}
	rc.mu.Lock()
	cached := rc.hasLast && rc.last.Fable != nil
	rc.mu.Unlock()
	if !cached {
		t.Fatal("the switch did not refresh the cached state")
	}
	fable.mu.Lock()
	fable.err = cerr.Switch("Account-2's Fable login is gone")
	fable.mu.Unlock()
	if err := rc.SwitchFable("claude:2"); err == nil || err.Error() != "Account-2's Fable login is gone" {
		t.Fatalf("refused switch: %v", err)
	}
	if err := rc.SwitchFable("codex:1"); err == nil {
		t.Fatal("a Codex key reached the Fable store")
	}
	old := startRemoteServer(t, testRemoteToken, "127.0.0.1:0")
	if err := newTestRemoteClient(t, old, testRemoteToken).SwitchFable("claude:2"); err == nil {
		t.Fatal("a server without the Fable store took the switch")
	}
}

// The whole remote command: Use for Fable in the menu reaches the engine's
// Fable store.
func TestRemoteModeUsesForFable(t *testing.T) {
	appLockHome(t)
	fable := &remoteFakeFable{}
	fx := startRemoteServer(t, testRemoteToken, "127.0.0.1:0", withRemoteFable(fable))
	ft := &blockingTray{done: make(chan struct{})}
	var opts atomic.Pointer[tray.Options]
	prev := newTray
	newTray = func(_ tray.Icon, o tray.Options) (tray.Tray, error) { opts.Store(&o); return ft, nil }
	t.Cleanup(func() { newTray = prev })
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run("tycswap", []string{"app", "--remote", fx.base, "--token-file", tokenFile(t, testRemoteToken), "--no-update-check"},
			ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
	}()
	waitUntil(t, "Use for Fable in the menu", func() bool {
		_, ok := ft.item("fable:claude:2")
		return ok
	})
	opts.Load().OnClick("fable:claude:2")
	if got := fable.Calls(); !reflect.DeepEqual(got, []string{"SwitchFable(2)"}) {
		t.Errorf("fable calls %v", got)
	}
	opts.Load().OnClick("quit")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d, stderr %q", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("app --remote did not exit on Quit")
	}
}
