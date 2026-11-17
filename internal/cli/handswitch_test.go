package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/settings"
)

// handHold is the slot a switch by hand holds the engine on, from the store's
// engine state ("" for no hold, DESIGN A94).
func handHold(t *testing.T, sw *core.Switcher) string {
	t.Helper()
	raw, err := os.ReadFile(sw.StatePath())
	if err != nil {
		return ""
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if by, _ := state["lastSwitchBy"].(string); by != "hand" {
		return ""
	}
	to, _ := state["lastSwitchTo"].(string)
	return to
}

// servedDashboard is the app's dashboard over sw, served on loopback with a
// remote token; it returns the dashboard and its base URL.
func servedDashboard(t *testing.T, sw *core.Switcher) (*dashboard, string) {
	t.Helper()
	useSwitcher(t, sw)
	ctx, cancel := context.WithCancel(context.Background())
	d, code := newDashboard(ctx, 3600, false, ioStreams{in: strings.NewReader(""), out: os.Stdout, err: os.Stderr}, dashboardOptions{remoteToken: "remote-test-token", noUpdateSchedule: true})
	if code != 0 {
		t.Fatalf("newDashboard exit %d", code)
	}
	url, err := d.srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- d.srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("the dashboard did not stop")
		}
		d.auto.stopKeepingChoice()
	})
	return d, url[:strings.Index(url, "/?token=")]
}

// Every switch by hand holds the engine for its cooldown, through the one
// call they share (DESIGN A94): `tycswap switch <n>` and bare `tycswap
// switch`, the dashboard, the tray (the app's own and, through the dashboard,
// the remote one) and the TUI (core.Switcher.SwitchTo, its facade; its
// best-pick is core.Switcher.Switch, which bare `tycswap switch` runs).
// Auto-switch stays as it was.
func TestHandSwitchesHoldTheEngine(t *testing.T) {
	for name, switchByHand := range map[string]func(t *testing.T, sw *core.Switcher){
		"cli switch 2": func(t *testing.T, sw *core.Switcher) {
			useSwitcher(t, sw)
			if code, _, errb := runSub(t, "switch", "2"); code != 0 {
				t.Fatalf("switch 2: exit %d, stderr %q", code, errb)
			}
		},
		"cli switch": func(t *testing.T, sw *core.Switcher) {
			useSwitcher(t, sw)
			if code, _, errb := runSub(t, "switch"); code != 0 {
				t.Fatalf("switch: exit %d, stderr %q", code, errb)
			}
		},
		"dashboard": func(t *testing.T, sw *core.Switcher) {
			_, base := servedDashboard(t, sw)
			req, _ := http.NewRequest(http.MethodPost, base+"/api/switch/claude:2", nil)
			req.Header.Set("Authorization", "Bearer remote-test-token")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("switch over HTTP: status %d", resp.StatusCode)
			}
		},
		"tray": func(t *testing.T, sw *core.Switcher) {
			d, _ := servedDashboard(t, sw)
			if _, err := d.switchTo("claude:2"); err != nil {
				t.Fatal(err)
			}
		},
		"tui": func(t *testing.T, sw *core.Switcher) {
			if _, err := sw.SwitchTo("2", true); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			sw := fixtureSwitcher(t)
			if _, err := settings.SetSetting(sw.BackupDir(), "autoswitch.enabled", "true"); err != nil {
				t.Fatal(err)
			}
			switchByHand(t, sw)
			cur := sw.CurrentAccountNumber()
			if cur == nil || *cur == "1" {
				t.Fatalf("live account %v after the switch, want another than 1", cur)
			}
			if got := handHold(t, sw); got != *cur {
				t.Fatalf("hold on %q, want %s", got, *cur)
			}
			if !settings.Load(sw.BackupDir()).Enabled {
				t.Fatal("a switch by hand turned auto-switch off")
			}
		})
	}
}

// The engine's own switch and `tycswap start`'s go through the adapter: they
// stamp themselves under the state lock they hold, never as by hand.
func TestEngineSwitchIsNotByHand(t *testing.T) {
	sw := fixtureSwitcher(t)
	res, err := autoswitchAdapter{sw}.SwitchTo("2", true)
	if err != nil || res["switched"] != true {
		t.Fatalf("SwitchTo = %v, %v", res, err)
	}
	if got := handHold(t, sw); got != "" {
		t.Fatalf("the engine's switch left a hand hold on %q", got)
	}
}

// A switch onto the account already live switches nothing and holds nothing.
func TestHandSwitchOntoTheLiveAccountHoldsNothing(t *testing.T) {
	sw := fixtureSwitcher(t)
	if _, err := sw.SwitchTo("1", true); err != nil {
		t.Fatal(err)
	}
	if got := handHold(t, sw); got != "" {
		t.Fatalf("hold on %q after a switch that moved nothing", got)
	}
}

// After `tycswap switch 2` onto an account at its limit, a real tick over the
// store holds and says why (no-switch cooldown, naming the hand switch);
// once the cooldown has passed, the same tick switches.
func TestHandSwitchHoldsARealTick(t *testing.T) {
	sw := countingStore(t, "claude-opus-5-5", map[string][3]float64{"1": {10, 10, 10}, "2": {10, 100, 10}})
	useSwitcher(t, sw)
	if code, _, errb := runSub(t, "switch", "2"); code != 0 {
		t.Fatalf("switch 2: exit %d, stderr %q", code, errb)
	}
	tick := func() []autoswitch.Event {
		var events []autoswitch.Event
		s := settings.Load(sw.BackupDir())
		e := newAutoEngine(sw, s, func(ev autoswitch.Event) { events = append(events, ev) }, true, sw.OAuth)
		e.Tick()
		return events
	}
	var held *autoswitch.NoSwitchEvent
	for _, ev := range tick() {
		if ns, ok := ev.(autoswitch.NoSwitchEvent); ok && ns.Reason == "cooldown" {
			held = &ns
		}
		if _, ok := ev.(autoswitch.SwitchEvent); ok {
			t.Fatalf("the tick switched right after the hand switch: %+v", ev)
		}
	}
	if held == nil || !strings.HasPrefix(held.Detail, "switched by hand to account 2; held for another ") {
		t.Fatalf("no cooldown naming the hand switch: %+v", held)
	}
	raw, _ := os.ReadFile(sw.StatePath())
	var state map[string]any
	_ = json.Unmarshal(raw, &state)
	state["lastSwitchAt"] = float64(time.Now().Unix()) - settings.Load(sw.BackupDir()).CooldownSeconds - 1
	raw, _ = json.Marshal(state)
	if err := os.WriteFile(sw.StatePath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	switched := false
	for _, ev := range tick() {
		if s, ok := ev.(autoswitch.SwitchEvent); ok && s.Trigger == "at-limit" {
			switched = true
		}
	}
	if !switched {
		t.Fatal("after the cooldown the tick did not switch")
	}
}
