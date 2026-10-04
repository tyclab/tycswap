//go:build darwin && cgo

package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -fmodules
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "tray_darwin.h"
*/
import "C"

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/tyclab/tycswap/internal/brand"
)

// The Cocoa run loop must live on the process's main thread; locking the
// main goroutine to it in init is the standard Go idiom for that.
func init() { runtime.LockOSThread() }

var (
	darwinMu   sync.Mutex
	darwinTray *darwinTrayImpl
)

type darwinTrayImpl struct {
	icon Icon // as New got it: the brand row's mark
	opts Options
	mu   sync.Mutex
	menu menuModel
	done chan struct{}

	// iconMu orders SetIcon calls, so the image the main queue applies last
	// is the one stored last; bar is the status-bar icon, New's until SetIcon.
	iconMu sync.Mutex
	bar    Icon
}

func newTray(icon Icon, opts Options) (Tray, error) {
	if png, _ := barImage(icon); len(png) == 0 {
		return nil, ErrUnsupported
	}
	t := &darwinTrayImpl{icon: icon, bar: icon, opts: opts, done: make(chan struct{})}
	r, g, b := accentRGB(brand.Sanitized().AccentColor)
	C.tray_set_accent(C.double(r), C.double(g), C.double(b))
	darwinMu.Lock()
	darwinTray = t
	darwinMu.Unlock()
	return t, nil
}

// accentRGB is a validated #rrggbb as sRGB components in 0–1.
func accentRGB(hex string) (r, g, b float64) {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil || len(hex) != 7 {
		v = 0x5aa2ff
	}
	return float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255
}

// barImage picks the status-bar rendition of the coloured mark: the large
// one when there is one, so Retina bars get a crisp 18 pt image.
func barImage(icon Icon) []byte {
	if len(icon.LargePNG) > 0 {
		return icon.LargePNG
	}
	return icon.PNG
}

func (t *darwinTrayImpl) Run() error {
	t.iconMu.Lock()
	png := barImage(t.bar)
	t.iconMu.Unlock()
	mark := t.icon.LargePNG
	if len(mark) == 0 {
		mark = t.icon.PNG
	}
	var markPtr unsafe.Pointer
	if len(mark) > 0 {
		markPtr = unsafe.Pointer(&mark[0])
	}
	tip := C.CString(t.opts.Tooltip)
	defer C.free(unsafe.Pointer(tip))
	C.tray_run(unsafe.Pointer(&png[0]), C.int(len(png)), markPtr, C.int(len(mark)), tip)
	return nil
}

func (t *darwinTrayImpl) Quit() { C.tray_quit() }

func (t *darwinTrayImpl) SetTitle(s string) {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	C.tray_set_title(cs)
}

func (t *darwinTrayImpl) SetTooltip(s string) {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	C.tray_set_tooltip(cs)
}

// SetIcon swaps the status item's image on the main queue; the brand row
// keeps t.icon. Before Run the status item does not exist yet and the C side
// does nothing: Run then shows t.bar.
func (t *darwinTrayImpl) SetIcon(icon Icon) {
	png := barImage(icon)
	if len(png) == 0 {
		return
	}
	t.iconMu.Lock()
	defer t.iconMu.Unlock()
	t.bar = icon
	C.tray_set_icon(unsafe.Pointer(&png[0]), C.int(len(png)))
}

// SetMenu writes the menu, the open one included (A37); an unchanged menu is
// left alone. The lock is held to the commit, so two calls never interleave
// their rows on the main queue.
func (t *darwinTrayImpl) SetMenu(items []Item) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commit(items, false)
}

// redraw writes the menu again although it did not change: after a click
// whose outcome changed nothing, it ends the row's spinner and takes back a
// switch the row flipped ahead of the outcome (A44).
func (t *darwinTrayImpl) redraw() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commit(t.menu.items, true)
}

// commit sends items to the main queue; t.mu is held.
func (t *darwinTrayImpl) commit(items []Item, force bool) {
	if !t.menu.set(items) && !force {
		return
	}
	C.tray_menu_begin()
	walkMenu(items, func(tag int, it Item) {
		title := C.CString(it.Title)
		sub := C.CString(it.Sub)
		C.tray_menu_add(C.int(tag), title, sub, C.int(it.Kind), C.double(it.Pct), boolInt(it.Checked), boolInt(it.Disabled), boolInt(it.Separator), boolInt(it.Dismiss))
		C.free(unsafe.Pointer(title))
		C.free(unsafe.Pointer(sub))
	}, func(_ int, it Item) {
		title := C.CString(it.Title)
		C.tray_menu_push(title)
		C.free(unsafe.Pointer(title))
	}, func() { C.tray_menu_pop() })
	C.tray_menu_commit()
}

// Notify uses the system notification centre through osascript: a bare
// binary has no bundle identifier, which NSUserNotificationCenter requires,
// and the AppleScript route needs neither a bundle nor a framework.
func (t *darwinTrayImpl) Notify(title, body string) error {
	script := "display notification " + appleQuote(body) + " with title " + appleQuote(title)
	return exec.Command("osascript", "-e", script).Run()
}

// Ask shows a modal yes/no dialog through osascript (the same route as the
// notifications; no bundle identifier needed). The right button is the default.
// The menu closes first: a row that asks leaves it open (A37), and over an
// open menu the first click on the dialog would only close the menu.
func (t *darwinTrayImpl) Ask(title, body, okLabel, cancelLabel string) (bool, error) {
	C.tray_close_menu()
	script := "display dialog " + appleQuote(body) + " with title " + appleQuote(title) +
		" buttons {" + appleQuote(cancelLabel) + ", " + appleQuote(okLabel) + "} default button " + appleQuote(okLabel) +
		" cancel button " + appleQuote(cancelLabel) + " with icon caution"
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		// osascript exits 1 when the cancel button is chosen (user canceled -128)
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return strings.Contains(string(out), "button returned:"+okLabel), nil
}

// appleQuote renders s as an AppleScript string literal. A literal cannot span
// lines, so newlines become the escape AppleScript understands rather than a
// syntax error that silently swallows the notification.
func appleQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(s) + `"`
}

func boolInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

//export tycTrayClicked
func tycTrayClicked(tag C.int) {
	darwinMu.Lock()
	t := darwinTray
	darwinMu.Unlock()
	if t == nil {
		return
	}
	t.mu.Lock()
	id, ok := t.menu.idAt(int(tag))
	t.mu.Unlock()
	if ok && t.opts.OnClick != nil {
		go func() {
			t.opts.OnClick(id)
			// What the click did is in the menu by now, or comes with the next
			// SetMenu; one that changed nothing must not leave its row busy.
			t.redraw()
		}()
	}
}
