// Package tray is the system-tray / menu-bar shell of `tycswap app`
// (DESIGN A35): one icon, a title next to it where the platform allows text
// (macOS), a tooltip, a flat menu and native notifications. No third-party
// code: macOS uses Cocoa through cgo, Windows the Win32 shell API through
// syscalls, Linux the StatusNotifierItem D-Bus protocol spoken directly.
//
// Run blocks and owns the platform's UI thread; call it from the main
// goroutine and everything else from wherever you like — the implementations
// marshal onto the UI thread themselves.
package tray

import (
	"errors"
	"reflect"
	"strings"
)

// ErrUnsupported is returned by New when this build has no tray: a darwin
// binary compiled without cgo, or a platform without an implementation.
var ErrUnsupported = errors.New("tray: no system tray in this build")

// ItemKind selects the row component. Platforms with custom menu views (macOS)
// render switches, gauges and section headers natively; the others fall back
// to text (see Item.FallbackTitle) plus a checkmark.
type ItemKind int

const (
	// KindPlain is an ordinary command row.
	KindPlain ItemKind = iota
	// KindToggle is an on/off switch; Checked is its state, a click flips it.
	KindToggle
	// KindGauge is a row with a title, a Sub line and a 0–100 % bar (Pct;
	// negative = unknown). Clickable unless Disabled.
	KindGauge
	// KindHeader is a non-clickable section label.
	KindHeader
	// KindBrand is the identity row at the top of the menu: the app icon,
	// Title as the product name and Sub as the version/state lines, "\n"
	// between them. Never clickable; platforms without custom views show it
	// as one disabled line.
	KindBrand
	// KindUpdate is a row that offers an update (DESIGN A44): Title and Sub
	// as on a plain row, drawn to stand out on macOS (an accent arrow disc on a
	// tinted row); elsewhere it is a plain row showing its Title.
	KindUpdate
)

// Item is one menu entry. A Separator ignores the other fields.
type Item struct {
	ID        string
	Title     string
	Sub       string // KindGauge: the second line (windows); KindToggle and KindPlain: helper text
	Kind      ItemKind
	Pct       float64 // KindGauge: 0–100, or negative when unknown
	Checked   bool
	Disabled  bool
	Separator bool
	// Children make the row a submenu with Title as its label (DESIGN A37):
	// the row opens them and is not clickable itself.
	Children []Item
	// Dismiss closes the menu on a click. Every other row leaves it open on
	// macOS, so a switch or a toggle shows what it did in place (A37); the
	// rows that hand over to another window (the browser, a dialog) or end
	// the app set it. Windows and Linux close the menu on every click: the
	// menu there is the system's.
	Dismiss bool
}

// Separator is the menu divider.
func Separator() Item { return Item{Separator: true} }

// Header is a section label.
func Header(title string) Item { return Item{Kind: KindHeader, Title: title, Disabled: true} }

// Brand is the identity row (product name and a version/state line).
func Brand(id, title, sub string) Item {
	return Item{ID: id, Kind: KindBrand, Title: title, Sub: sub, Disabled: true}
}

// Clickable reports whether a click on the row means anything.
func (it Item) Clickable() bool {
	return !it.Separator && it.Kind != KindHeader && !it.Disabled && it.ID != "" && len(it.Children) == 0
}

// FallbackTitle renders the row as one line for menus that only do text: a
// toggle shows its state, a gauge shows a five-cell bar and the percentage.
func (it Item) FallbackTitle() string {
	switch it.Kind {
	case KindToggle:
		state := "Off"
		if it.Checked {
			state = "On"
		}
		return it.Title + ": " + state
	case KindGauge:
		t := it.Title
		if it.Pct >= 0 {
			cells := int(it.Pct/20 + 0.5)
			if cells > 5 {
				cells = 5
			}
			bar := ""
			for i := 0; i < 5; i++ {
				if i < cells {
					bar += "▰"
				} else {
					bar += "▱"
				}
			}
			t += "  " + bar + " " + itoa(int(it.Pct+0.5)) + "%"
		}
		if it.Sub != "" {
			t += "  —  " + it.Sub
		}
		return t
	case KindHeader:
		return it.Title
	case KindBrand:
		if it.Sub != "" {
			return it.Title + " — " + strings.ReplaceAll(it.Sub, "\n", " · ")
		}
		return it.Title
	case KindUpdate:
		return it.Title // the title names the update; a text menu has no room for more
	}
	if it.Sub != "" {
		return it.Title + "  —  " + it.Sub
	}
	return it.Title
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// Icon carries the mark in the forms the platforms want.
type Icon struct {
	// PNG is the coloured icon (Windows, and the fallback everywhere).
	PNG []byte
	// LargePNG is the coloured mark at ≥ 64 px: the macOS menu bar (scaled
	// to 18 pt, crisp on Retina) and the menu's brand row.
	LargePNG []byte
	// ARGB32 is the coloured pixmap for StatusNotifierItem (Linux), Size²·4 bytes.
	ARGB32 []byte
	Size   int
}

// Options wires the caller's reactions.
type Options struct {
	// Tooltip is the initial hover text (also the accessibility label).
	Tooltip string
	// OnClick receives the ID of the menu item the user chose.
	OnClick func(id string)
	// OnActivate runs on a primary click where the platform has one that does
	// not open the menu (Linux StatusNotifierItem); macOS and Windows open the
	// menu on every click and never call it.
	OnActivate func()
}

// Tray is the live icon.
type Tray interface {
	// SetTitle puts text next to the icon (macOS); elsewhere it becomes the tooltip.
	SetTitle(string)
	SetTooltip(string)
	// SetIcon replaces the status-bar / notification-area icon, for the
	// update badge (DESIGN A44). Only the icon changes: the macOS menu's brand
	// row keeps the mark New was given. An icon without the form the platform
	// draws (LargePNG/PNG on macOS, PNG on Windows, ARGB32 on
	// Linux) is ignored. Before Run it sets the icon Run shows.
	SetIcon(Icon)
	// SetMenu replaces the whole menu.
	SetMenu([]Item)
	// Notify shows a native notification. Errors are advisory.
	Notify(title, body string) error
	// Run shows the icon and blocks until Quit. Main goroutine only.
	Run() error
	// Quit ends Run from any goroutine.
	Quit()
}

// Asker is the optional yes/no dialog a tray can show (update approval).
// Implementations return ErrUnsupported when no dialog is available.
type Asker interface {
	Ask(title, body, okLabel, cancelLabel string) (bool, error)
}

// New builds the platform tray or returns ErrUnsupported.
func New(icon Icon, opts Options) (Tray, error) {
	return newTray(icon, opts)
}

// menuModel is the platform-independent part: the current items, and the
// mapping from a platform's integer tag back to an item ID. A row's tag is
// its index in flat, the menu depth-first with a submenu's row before its
// children: the order walkMenu visits them in.
type menuModel struct {
	items []Item
	flat  []Item
}

// set takes a new menu and reports whether it differs from the one before,
// so a platform can leave an unchanged menu alone: rebuilding an open menu
// every poll tick would flicker under the pointer.
func (m *menuModel) set(items []Item) (changed bool) {
	if m.flat != nil && reflect.DeepEqual(m.items, items) {
		return false
	}
	m.items = append([]Item(nil), items...)
	m.flat = []Item{}
	walkMenu(items, func(_ int, it Item) { m.flat = append(m.flat, it) },
		func(_ int, it Item) { m.flat = append(m.flat, it) }, func() {})
	return true
}

func (m *menuModel) idAt(i int) (string, bool) {
	if i < 0 || i >= len(m.flat) || !m.flat[i].Clickable() {
		return "", false
	}
	return m.flat[i].ID, true
}

// walkMenu visits items depth-first and numbers them with the tags idAt
// takes: row for a row without children, enter for a submenu's row before
// its children, leave after them.
func walkMenu(items []Item, row func(tag int, it Item), enter func(tag int, it Item), leave func()) {
	tag := 0
	var walk func([]Item)
	walk = func(items []Item) {
		for _, it := range items {
			if len(it.Children) > 0 && !it.Separator {
				enter(tag, it)
				tag++
				walk(it.Children)
				leave()
				continue
			}
			row(tag, it)
			tag++
		}
	}
	walk(items)
}
