package web

import (
	"fmt"
	"net/http"
	"testing"
)

func TestAccountSetupUsesAuthenticatedFixedAction(t *testing.T) {
	calls := 0
	h := newHarness(t, func(_ *harness, d *Deps) { d.AccountSetup = func() error { calls++; return nil } })
	resp := h.postJSON("/api/accounts/setup", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
	if calls != 1 {
		t.Fatal(calls)
	}
	resp = h.postJSON("/api/accounts/setup", map[string]any{"command": "arbitrary command"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.StatusCode)
	}
	resp.Body.Close()
	if calls != 1 {
		t.Fatal("unknown input launched a process")
	}
}

func TestAccountSetupReportsUnavailableDesktop(t *testing.T) {
	h := newHarness(t, func(_ *harness, d *Deps) { d.AccountSetup = func() error { return fmt.Errorf("no desktop terminal") } })
	resp := h.postJSON("/api/accounts/setup", map[string]any{})
	if resp.StatusCode != http.StatusServiceUnavailable || decodeError(t, resp) != "no desktop terminal" {
		t.Fatal("missing terminal was not reported")
	}
}

func TestAccountSetupTargetsOneDesktopAndDoesNotReplay(t *testing.T) {
	h := newHarness(t, func(_ *harness, d *Deps) {
		d.AccountSetup = func() error { t.Error("launched on headless server"); return nil }
	})
	browser, stopBrowser := h.s.hub.subscribe(false)
	defer stopBrowser()
	first, stopFirst := h.s.hub.subscribe(false, true)
	second, stopSecond := h.s.hub.subscribe(false, true)
	resp := h.postJSON("/api/accounts/setup", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
	resp.Body.Close()
	count := 0
	for _, ch := range []<-chan event{first, second} {
		select {
		case ev := <-ch:
			if ev.name != "account-setup" {
				t.Fatal(ev.name)
			}
			count++
		default:
		}
	}
	if count != 1 {
		t.Fatalf("targeted %d desktops", count)
	}
	select {
	case <-browser:
		t.Fatal("sent setup to browser")
	default:
	}
	stopFirst()
	stopSecond()
	newDesktop, stopNew := h.s.hub.subscribe(false, true)
	defer stopNew()
	select {
	case <-newDesktop:
		t.Fatal("replayed setup after reconnect")
	default:
	}
}
