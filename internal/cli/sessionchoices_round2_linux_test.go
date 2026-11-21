package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

func TestSessionChoicesExcludeCurrentTarget(t *testing.T) {
	sw, from, _ := mythosTickFixture(t, 20, 30, true)
	bindTickSession(t, sw, tickSessionA, "mythos", from)
	bindTickSession(t, sw, tickSessionB, "opus", sw.DefaultProfileDir())
	for _, tc := range []struct{ id, here, other string }{{tickSessionA, "mythos", "default"}, {tickSessionB, "default", "mythos"}} {
		got, err := sessionMoveChoices(sw, tc.id)
		if err != nil || slices.Contains(got, tc.here) || !slices.Contains(got, tc.other) {
			t.Errorf("session on %s offers %v, want only different logins including %s (%v)", tc.here, got, tc.other, err)
		}
	}
}

func TestExplicitUseConfirmsCurrentLogin(t *testing.T) {
	sw, _, _ := mythosTickFixture(t, 20, 30, true)
	link := bindTickSession(t, sw, tickSessionB, "opus", sw.DefaultProfileDir())
	code, out, msg := runSub(t, "use", "default", "--session", tickSessionB)
	if code != 0 || !strings.Contains(strings.ToLower(out), "already") || !strings.Contains(out, "default login") {
		t.Fatalf("use on the current login returned %d, stdout %q, stderr %q", code, out, msg)
	}
	requireLinkTarget(t, link, sw.DefaultProfileDir())
}

func TestRefreshingSessionHasOnePathFreeReason(t *testing.T) {
	sw, _ := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true}, "2")
	setRecord(t, sw, "3", "mythos", true)
	dir := store.MythosLoginDir(sw.BackupDir(), fableEmail("3"), "")
	writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("third", "refresh-third"))
	bindTickSession(t, sw, tickSessionB, "opus", sw.DefaultProfileDir())
	if err := os.Mkdir(filepath.Join(sw.DefaultProfileDir(), ".oauth_refresh.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := sessionMoveChoices(sw, tickSessionB)
	const want = "The session login is refreshing."
	if len(got) != 0 || err == nil || err.Error() != want {
		t.Fatalf("refreshing session offers %v with explanation %q, want no actions and one path-free reason", got, err)
	}
}

func TestSessionLoginUsesBoundStoreAndVerifiedRosterOwner(t *testing.T) {
	sw, _ := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	root := sw.BackupDir()
	bindTickSession(t, sw, tickSessionA, "opus", sw.DefaultProfileDir())
	fable := paths.FableStoreDir(root)
	link, err := reporting.CreateSessionLink(root, fable)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporting.BindSessionLink(root, tickSessionB, link, "fable", "fable", sw.Clk.Now()); err != nil {
		t.Fatal(err)
	}
	target := store.MythosLoginDir(root, fableEmail("3"), "")
	writeFile(t, filepath.Join(target, ".credentials.json"), dryCreds("third", "refresh-third"))
	bindTickSession(t, sw, tickSessionC, "mythos", target)
	other := store.MythosLoginDir(root, fableEmail("4"), "")
	if err := store.SelectMythosLogin(root, other); err != nil {
		t.Fatal(err)
	}
	api, ok := any(&dashboard{sw: sw}).(interface{ SessionLogin(string) (string, string) })
	if !ok {
		t.Fatal("session state has no bound login and account")
	}
	check := func(id, wantLogin, wantNumber string) {
		t.Helper()
		login, number := api.SessionLogin(id)
		if login != wantLogin || number != wantNumber {
			t.Errorf("%s = %q %q, want %q %q", id, login, number, wantLogin, wantNumber)
		}
	}
	check(tickSessionA, "default login", "1")
	check(tickSessionB, "Fable store", "2")
	check(tickSessionC, "Mythos login", "3")
	check("11111111-2222-4333-8444-000000000004", "", "")
	writeFile(t, filepath.Join(sw.DefaultProfileDir(), ".credentials.json"), dryCreds("unmatched", "refresh-unmatched"))
	check(tickSessionA, "default login", "unknown")
	writeFile(t, filepath.Join(fable, ".credentials.json"), dryCreds("other", "refresh-other"))
	check(tickSessionB, "Fable store", "unknown")
	setRecord(t, sw, "3", "organizationUuid", nil)
	check(tickSessionC, "Mythos login", "unknown")
}
