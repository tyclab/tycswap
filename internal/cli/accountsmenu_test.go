package cli

import (
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/web"
)

// Tests for DESIGN A37: the accounts submenu, "Add current login" in the
// menu, and which rows close the menu.

func manyAccounts(n, active int) []map[string]any {
	var rows []map[string]any
	for i := 1; i <= n; i++ {
		rows = append(rows, acct(i, "u"+strconv.Itoa(i)+"@example.com", "", i == active, 10.0, 5.0, "", nil))
	}
	return rows
}

func TestShellAccountsMoveIntoASubmenu(t *testing.T) {
	sh, ft, calls := newTestShell(t)
	st := sampleState()
	st.Accounts = manyAccounts(inlineAccounts, 3)
	sh.update(st)
	if _, ok := ft.item("accounts"); ok {
		t.Errorf("%d accounts fit the menu", inlineAccounts)
	}
	if _, ok := ft.item("switch:claude:" + strconv.Itoa(inlineAccounts)); !ok {
		t.Error("the last of ten accounts is missing")
	}

	st.Accounts = manyAccounts(inlineAccounts+2, 3)
	sh.update(st)
	sub, ok := ft.item("accounts")
	if !ok || sub.Title != "All 12 accounts" || len(sub.Children) != 12 || sub.Clickable() {
		t.Fatalf("submenu = %+v (present %v)", sub, ok)
	}
	if sub.Children[0].ID != "switch:claude:1" || sub.Children[11].ID != "switch:claude:12" || sub.Children[11].Dismiss {
		t.Errorf("submenu rows = %+v … %+v", sub.Children[0], sub.Children[11])
	}
	// The active account stays in sight; the others are only in the submenu.
	if a, ok := ft.item("switch:claude:3"); !ok || !a.Checked {
		t.Errorf("active row = %+v (present %v)", a, ok)
	}
	if _, ok := ft.item("switch:claude:4"); ok {
		t.Error("an inactive account is listed outside the submenu")
	}
	ids, _ := menuShape(ft)
	if !strings.Contains(ids, "switch:claude:3,accounts") {
		t.Errorf("menu order = %s", ids)
	}
	sh.click("switch:claude:12")
	if got := strings.Join(*calls, ","); got != "switch:claude:12" {
		t.Errorf("calls = %s", got)
	}
}

func TestShellAddCurrentLogin(t *testing.T) {
	sh, ft, calls := newTestShell(t)
	type answer struct {
		res web.AddLoginResult
		err error
	}
	var next answer
	pushed := 0
	sh.act.AddCurrent = func() (web.AddLoginResult, error) {
		*calls = append(*calls, "add")
		return next.res, next.err
	}
	sh.act.UpdatesChanged = func() { pushed++ }

	sub := func(st web.State) string {
		sh.update(st)
		it, ok := ft.item("add-current")
		if !ok || it.Title != "Add current login" || it.Dismiss || !it.Clickable() {
			t.Fatalf("row = %+v (present %v)", it, ok)
		}
		return it.Sub
	}
	st := sampleState()
	st.CurrentLogin = &web.CurrentLoginView{Email: "dave@example.com"}
	if got := sub(st); got != "dave@example.com · not stored yet" {
		t.Errorf("unsaved: %q", got)
	}
	st.CurrentLogin.Saved = true
	if got := sub(st); got != "for another account: /login with it, never /logout" {
		t.Errorf("saved: %q", got)
	}
	if got := sub(web.State{}); got != "sign in to Claude Code first (/login)" {
		t.Errorf("no accounts, no login: %q", got)
	}
	if _, ok := ft.item("none"); ok {
		t.Error(`"Add current login" replaces the "No accounts yet" hint`)
	}

	next = answer{res: web.AddLoginResult{Number: "3", Email: "dave@example.com"}}
	sh.click("add-current")
	next = answer{res: web.AddLoginResult{Number: "1", Email: "alice@example.com", Refreshed: true}}
	sh.click("add-current")
	next = answer{err: web.ErrNoLogin}
	sh.click("add-current")
	if got := strings.Join(*calls, ","); got != "add,add,add" {
		t.Errorf("calls = %s", got)
	}
	notes := strings.Join(ft.notesNow(), "\n")
	for _, want := range []string{
		"Added account #3 | dave@example.com is account #3 now.",
		"Account #1 refreshed | alice@example.com was account #1 already",
		"Login not added | Claude Code has no subscription login on this computer.",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("notifications lack %q:\n%s", want, notes)
		}
	}
	if pushed != 2 {
		t.Errorf("the dashboard was told %d times, want once per added login", pushed)
	}
}

// A click leaves the menu open, except on the rows that hand over to another
// window or end the app.
func TestShellRowsThatCloseTheMenu(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.act.AddCurrent = func() (web.AddLoginResult, error) { return web.AddLoginResult{}, nil }
	sh.update(sampleState())
	for id, want := range map[string]bool{
		"open": true, "quit": true,
		"switch:claude:2": false, "auto": false, "autostart": false, "update": false, "add-current": false,
	} {
		if it, ok := ft.item(id); !ok || it.Dismiss != want {
			t.Errorf("%s: Dismiss %v (present %v), want %v", id, it.Dismiss, ok, want)
		}
	}
}
