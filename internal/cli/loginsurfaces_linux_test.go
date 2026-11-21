package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/web"
)

func TestTrayProjectsMythosSessionActions(t *testing.T) {
	sh, _, _ := newTestShell(t)
	var state web.State
	err := json.Unmarshal([]byte(`{"accounts":[{"provider":"claude","number":2,"alias":"a-long-account-name-for-the-tray","mythosLogin":{"status":"ok","live":false,"selectable":true}},{"provider":"claude","number":1,"mythosLogin":{"status":"ok","live":true,"selectable":false}},{"provider":"claude","number":3,"mythosLogin":{"status":"sign in again","selectable":false}}],"mythos":{"number":1,"countedWindows":["5h","7d","Mythos"],"linkedSessions":2},"sessions":{"claude":[{"sessionId":"9421a2ec-67ef-4ff6-9e22-dad93a8d0b87","title":"Review","login":"Mythos login","accountNumber":"1","moves":["fable"]},{"sessionId":"7421a2ec-67ef-4ff6-9e22-dad93a8d0b87","title":"Refreshing","login":"default login","accountNumber":"unknown","movesError":"The session login is refreshing."}]},"nextStartModel":"mythos"}`), &state)
	if err != nil {
		t.Fatal(err)
	}
	panel := sh.panel(state)
	raw, _ := json.Marshal(panel)
	for _, want := range []string{"Use for Mythos", "Mythos login: sign in again", "5h", "2 linked sessions", "Move to Fable store", "Mythos login: in use", "Next start: Mythos", "Mythos login · #1", "default login · unknown"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("tray panel missing %q", want)
		}
	}
	var checked bool
	for _, group := range loginControls(state, false) {
		for _, child := range group.Children {
			if strings.Contains(child.Title, "Refreshing") {
				checked = true
				if len(child.Children) != 1 || !child.Children[0].Disabled || child.Children[0].Title != "The session login is refreshing." {
					t.Errorf("refresh submenu %#v", child.Children)
				}
			}
		}
	}
	if !checked {
		t.Error("refreshing session missing from the tray")
	}

	if strings.Contains(string(raw), "Move to default login") {
		t.Error("tray offered a refused destination")
	}
}
func TestLinkedSessionActionsUseCurrentBinding(t *testing.T) {
	sw, from, _ := mythosTickFixture(t, 20, 30, false)
	link := bindTickSession(t, sw, tickSessionA, "mythos", from)
	d := &dashboard{sw: sw}
	api, ok := any(d).(interface {
		SessionMoves(string) ([]string, error)
		UseSession(string, string, string) error
	})
	if !ok {
		t.Fatal("dashboard has no linked-session placement actions")
	}
	choices, err := api.SessionMoves(tickSessionA)
	if err != nil || !slices.Contains(choices, "default") {
		t.Fatalf("linked session options %v: %v", choices, err)
	}
	if opts, _ := api.SessionMoves(tickSessionB); len(opts) != 0 {
		t.Fatalf("unlinked session offered %v", opts)
	}
	lock := filepath.Join(from, ".oauth_refresh.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	choices, _ = api.SessionMoves(tickSessionA)
	if slices.Contains(choices, "default") {
		t.Fatalf("busy source offers move: %v", choices)
	}
	requireLinkTarget(t, link, from)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := api.UseSession(tickSessionA, "default", "tray"); err != nil {
		t.Fatal(err)
	}
	requireLinkTarget(t, link, sw.DefaultProfileDir())
}
func TestMythosSwitchRequiresAccount(t *testing.T) {
	for _, args := range [][]string{{"switch", "--store", "mythos"}, {"switch", "--store", "mythos", "--strategy", "rank"}, {"switch", "--store", "mythos", "--empty"}} {
		code, _, msg := runSub(t, args...)
		if code != 2 || strings.Contains(msg, "unknown store") || msg == "" {
			t.Errorf("%v = %d %q", args, code, msg)
		}
	}
}
