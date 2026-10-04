package tray

import "testing"

func TestMenuModelIDs(t *testing.T) {
	var m menuModel
	m.set([]Item{{ID: "open", Title: "Open"}, Separator(), {ID: "quit", Title: "Quit"}})
	if id, ok := m.idAt(0); !ok || id != "open" {
		t.Errorf("idAt(0) = %q,%v", id, ok)
	}
	if _, ok := m.idAt(1); ok {
		t.Error("a separator has no ID")
	}
	if id, ok := m.idAt(2); !ok || id != "quit" {
		t.Errorf("idAt(2) = %q,%v", id, ok)
	}
	for _, i := range []int{-1, 3, 99} {
		if _, ok := m.idAt(i); ok {
			t.Errorf("idAt(%d) should be out of range", i)
		}
	}
	// set copies: mutating the caller's slice afterwards changes nothing.
	items := []Item{{ID: "a"}}
	m.set(items)
	items[0].ID = "b"
	if id, _ := m.idAt(0); id != "a" {
		t.Errorf("model aliased the caller's slice: %q", id)
	}
}

func TestFallbackTitleAndClickable(t *testing.T) {
	on := Item{Kind: KindToggle, Title: "Auto-switch", Checked: true, ID: "auto"}
	if got := on.FallbackTitle(); got != "Auto-switch: On" {
		t.Errorf("toggle on = %q", got)
	}
	g := Item{Kind: KindGauge, Title: "#1  work", Sub: "5h 45% · 7d 30%", Pct: 45, ID: "switch:1"}
	if got := g.FallbackTitle(); got != "#1  work  ▰▰▱▱▱ 45%  —  5h 45% · 7d 30%" {
		t.Errorf("gauge = %q", got)
	}
	if got := (Item{Kind: KindGauge, Title: "x", Pct: -1}).FallbackTitle(); got != "x" {
		t.Errorf("unknown pct = %q", got)
	}
	if got := (Item{Kind: KindGauge, Title: "x", Pct: 100}).FallbackTitle(); got != "x  ▰▰▰▰▰ 100%" {
		t.Errorf("full = %q", got)
	}
	// A text menu has no second line: the brand's lines join into one.
	if got := Brand("brand", "tycswap", "0.7.0 · auto-switch on\nClaude Code 2.1.281 · latest").FallbackTitle(); got != "tycswap — 0.7.0 · auto-switch on · Claude Code 2.1.281 · latest" {
		t.Errorf("brand = %q", got)
	}
	h := Header("Accounts")
	if h.Clickable() || h.FallbackTitle() != "Accounts" {
		t.Errorf("header = %+v", h)
	}
	var m menuModel
	m.set([]Item{h, g, {ID: "d", Disabled: true}})
	if _, ok := m.idAt(0); ok {
		t.Error("header must not resolve to an ID")
	}
	if id, ok := m.idAt(1); !ok || id != "switch:1" {
		t.Errorf("gauge idAt = %q,%v", id, ok)
	}
	if _, ok := m.idAt(2); ok {
		t.Error("disabled row must not resolve to an ID")
	}
}
